package storage

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

type env struct {
	t        *testing.T
	ctx      context.Context
	st       *store.Store
	m        *Manager
	mu       sync.Mutex
	messages []string
	applied  []int64
}

const gb = 1 << 30

// newEnv sets up locations of the given sizes in GB, each keeping 10 GB free.
func newEnv(t *testing.T, sizes ...uint64) (*env, []store.Location) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{t: t, ctx: context.Background(), st: st}
	totals := map[string]uint64{}
	var locs []store.Location
	for i, size := range sizes {
		path := filepath.Join(dir, string(rune('A'+i)))
		os.MkdirAll(path, 0o750)
		l, err := st.PutLocation(e.ctx, store.Location{Name: string(rune('A' + i)), Path: path, MinFreeGB: 10, Recording: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := Mark(path, l.ID); err != nil {
			t.Fatal(err)
		}
		totals[path] = size * gb
		locs = append(locs, l)
	}
	saved := diskUsage
	diskUsage = func(p string) (uint64, uint64, error) { return totals[p] / 2, totals[p], nil }
	t.Cleanup(func() { diskUsage = saved })
	e.m = &Manager{Store: st, Log: slog.New(slog.DiscardHandler), Settle: time.Hour, Timeout: 200 * time.Millisecond}
	e.m.Notify = func(level, msg string) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.messages = append(e.messages, msg)
	}
	e.m.Apply = func(ctx context.Context, c store.Camera) error {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.applied = append(e.applied, c.ID)
		return nil
	}
	return e, locs
}

func (e *env) cameras(n int) []store.Camera {
	var out []store.Camera
	for i := range n {
		c, err := e.st.CreateCamera(e.ctx, store.Camera{Name: "cam" + string(rune('a'+i)), MainURL: "rtsp://x", Enabled: true})
		if err != nil {
			e.t.Fatal(err)
		}
		out = append(out, c)
	}
	return out
}

func (e *env) refresh() {
	e.t.Helper()
	if err := e.m.Refresh(e.ctx); err != nil {
		e.t.Fatal(err)
	}
}

// where counts the cameras recording in each location now.
func (e *env) where() map[int64]int {
	e.m.mu.Lock()
	defer e.m.mu.Unlock()
	out := map[int64]int{}
	for _, l := range e.m.current {
		out[l]++
	}
	return out
}

func (e *env) said(part string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, m := range e.messages {
		if strings.Contains(m, part) {
			return true
		}
	}
	return false
}

func TestCamerasAreSpreadBySize(t *testing.T) {
	e, locs := newEnv(t, 1000, 3000)
	e.cameras(8)
	e.refresh()
	got := e.where()
	if got[locs[0].ID] != 2 || got[locs[1].ID] != 6 {
		t.Fatalf("spread: %v", got)
	}
	cams, _ := e.st.Cameras(e.ctx)
	for _, c := range cams {
		if c.StorageID == 0 {
			t.Errorf("%s has no home saved", c.Name)
		}
	}
	// A camera added later goes where there's most room for it.
	late, _ := e.st.CreateCamera(e.ctx, store.Camera{Name: "late", MainURL: "rtsp://x", Enabled: true})
	p := e.m.Placement(e.ctx, late)
	if p.Record != locs[1].Path || len(p.Archives) != 2 {
		t.Fatalf("new camera: %+v", p)
	}
	if c, _ := e.st.Camera(e.ctx, late.ID); c.StorageID != locs[1].ID {
		t.Errorf("new camera's home wasn't saved: %d", c.StorageID)
	}
}

func TestCamerasLeaveAndReturnToAFailedLocation(t *testing.T) {
	e, locs := newEnv(t, 1000, 1000)
	a, b := locs[0], locs[1]
	e.cameras(4)
	e.refresh()
	if got := e.where(); got[a.ID] != 2 || got[b.ID] != 2 {
		t.Fatalf("spread: %v", got)
	}
	cams, _ := e.st.Cameras(e.ctx)
	var onA store.Camera
	for _, c := range cams {
		if c.StorageID == a.ID {
			onA = c
		}
	}
	old := filepath.Join(a.Path, media.PathName(onA.ID, "main"), "before.mp4")
	os.MkdirAll(filepath.Dir(old), 0o750)
	os.WriteFile(old, []byte("video"), 0o600)

	// The share drops, leaving an empty folder behind: its marker is gone.
	os.Remove(filepath.Join(a.Path, MarkerFile))
	e.applied = nil
	e.refresh()
	if got := e.where(); got[b.ID] != 4 {
		t.Fatalf("cameras didn't leave A: %v", got)
	}
	if !e.said("Storage A is unavailable") || !e.said("2 cameras are recording to other storage while A is unavailable") {
		t.Errorf("messages: %v", e.messages)
	}
	if len(e.applied) != 4 {
		t.Errorf("every camera's video in A went out of reach, so all are set up again: %v", e.applied)
	}
	if p := e.m.Placement(e.ctx, onA); p.Record != b.Path || p.Archives[a.ID] != "" || p.Archives[b.ID] != b.Path {
		t.Errorf("during the outage: %+v", p)
	}
	if _, err := os.Stat(old); err != nil {
		t.Error("video in a location that can't be reached was touched")
	}

	// It comes back. Cameras wait to see that it stays.
	Mark(a.Path, a.ID)
	e.refresh()
	if got := e.where(); got[b.ID] != 4 {
		t.Fatalf("cameras returned before A settled: %v", got)
	}
	if !e.said("Storage A is available again") {
		t.Errorf("messages: %v", e.messages)
	}
	if p := e.m.Placement(e.ctx, onA); p.Archives[a.ID] != a.Path {
		t.Errorf("A's video should be back in playback: %+v", p)
	}
	moved := filepath.Join(a.Path, media.ArchiveName(onA.ID, a.ID), "before.mp4")
	if _, err := os.Stat(moved); err != nil {
		t.Errorf("video left in A wasn't put where playback finds it: %v", err)
	}
	during := filepath.Join(b.Path, media.PathName(onA.ID, "main"), "during.mp4")
	os.MkdirAll(filepath.Dir(during), 0o750)
	os.WriteFile(during, []byte("video"), 0o600)

	e.m.Settle = time.Nanosecond
	time.Sleep(2 * time.Millisecond) // Windows' clock can read the same across two quick refreshes
	e.refresh()
	if got := e.where(); got[a.ID] != 2 || got[b.ID] != 2 {
		t.Fatalf("cameras didn't return: %v", got)
	}
	if !e.said("2 cameras are recording to A again") {
		t.Errorf("messages: %v", e.messages)
	}
	if _, err := os.Stat(filepath.Join(b.Path, media.ArchiveName(onA.ID, b.ID), "during.mp4")); err != nil {
		t.Errorf("video recorded during the outage wasn't kept for playback: %v", err)
	}
}

func TestALocationThatStopsAnsweringIsLeft(t *testing.T) {
	e, locs := newEnv(t, 1000, 1000)
	e.cameras(2)
	release := make(chan struct{})
	defer close(release)
	saved := inspect
	defer func() { inspect = saved }()
	hung := false
	inspect = func(l store.Location) Status {
		if hung && l.ID == locs[0].ID {
			<-release
		}
		return inspectFolder(l)
	}
	e.refresh()
	hung = true
	for range 2 { // the second refresh finds the first check still waiting
		start := time.Now()
		e.refresh()
		if time.Since(start) > 2*time.Second {
			t.Fatal("a hung share held up the refresh")
		}
		if got := e.where(); got[locs[1].ID] != 2 {
			t.Fatalf("cameras stayed on a share that isn't answering: %v", got)
		}
	}
	if !e.said("isn't answering") {
		t.Errorf("messages: %v", e.messages)
	}
}

func TestALocationCanStopTakingNewVideo(t *testing.T) {
	e, locs := newEnv(t, 1000, 1000)
	e.cameras(4)
	e.refresh()
	a := locs[0]
	a.Recording = false
	if _, err := e.st.PutLocation(e.ctx, a); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	if got := e.where(); got[locs[1].ID] != 4 {
		t.Fatalf("%v", got)
	}
	cams, _ := e.st.Cameras(e.ctx)
	for _, c := range cams {
		if c.StorageID != locs[1].ID {
			t.Errorf("%s still has A as its home", c.Name)
		}
		if p := e.m.Placement(e.ctx, c); p.Archives[a.ID] != a.Path {
			t.Errorf("A's video should still play: %+v", p)
		}
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	used := filepath.Join(dir, "used")
	os.MkdirAll(used, 0o750)
	Mark(used, 1)
	all := []store.Location{{ID: 1, Name: "Disk", Path: used}}
	cases := map[string]string{
		"video":                           "full folder path",
		filepath.Join(used, "inner"):      "overlaps Disk",
		dir:                               "overlaps Disk",
		filepath.Join(dir, "new", "deep"): "",
	}
	for path, want := range cases {
		err := Check(path, 0, all)
		if (want == "") != (err == nil) || (err != nil && !strings.Contains(err.Error(), want)) {
			t.Errorf("%s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "new", "deep")); err != nil {
		t.Error("the folder wasn't made")
	}
	// The same folder by another path, here a copy of its marker.
	alias := filepath.Join(dir, "alias")
	os.MkdirAll(alias, 0o750)
	Mark(alias, 1)
	if err := Check(alias, 0, all); err == nil || !strings.Contains(err.Error(), "already Disk") {
		t.Errorf("alias: %v", err)
	}
	if err := Check(used, 1, all); err != nil {
		t.Errorf("a location checking itself: %v", err)
	}
}

func TestBootstrapKeepsAnExistingInstallationRecordingWhereItDid(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := filepath.Join(dir, "recordings")
	for range 2 {
		if err := Bootstrap(context.Background(), st, rec, 200); err != nil {
			t.Fatal(err)
		}
	}
	locs, _ := st.Locations(context.Background())
	if len(locs) != 1 || locs[0].Path != rec || locs[0].MinFreeGB != 200 || !locs[0].Recording {
		t.Fatalf("%+v", locs)
	}
	if owner, _ := markerOwner(rec); owner != locs[0].ID {
		t.Errorf("marker: %d", owner)
	}
}

func TestASignedOutShareSignsInAgain(t *testing.T) {
	old := Connect
	t.Cleanup(func() { Connect = old })
	var asked string
	Connect = func(path, user, password string) error {
		asked = path + " " + user
		return errors.New("The share refused that username or password.")
	}
	st := inspectFolder(store.Location{ID: 1, Path: `\\nas-that-isnt-there\video\seelynx`, Username: "nas-user", Password: "test-value"})
	if asked != `\\nas-that-isnt-there\video\seelynx nas-user` || st.OK || st.Reason != "The share refused that username or password." {
		t.Errorf("asked %q, status %+v", asked, st)
	}
	// A folder with no login isn't signed in to.
	asked = ""
	inspectFolder(store.Location{ID: 1, Path: `\\nas-that-isnt-there\video`})
	if asked != "" {
		t.Error("signed in without a login")
	}
}

func TestShareRoot(t *testing.T) {
	for in, want := range map[string]string{`\\nas\video`: `\\nas\video`, `\\nas\video\a\b`: `\\nas\video`, `\\nas`: ``, `D:\video`: ``} {
		if got := shareRoot(in); got != want {
			t.Errorf("%s: %q", in, got)
		}
	}
	if IsShare(`\\?\C:\video`) || !IsShare(`\\nas\video`) || IsShare(`D:\video`) {
		t.Error("IsShare")
	}
}
