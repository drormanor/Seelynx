package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

// standbyEnv has two network locations, A and B, and a standby location S on a local disk.
func standbyEnv(t *testing.T) (*env, store.Location, store.Location, store.Location) {
	t.Helper()
	savedSettle := settleFile
	settleFile = 200 * time.Millisecond
	t.Cleanup(func() { settleFile = savedSettle })
	e, locs := newEnv(t, 1000, 1000, 5000)
	a, b, s := locs[0], locs[1], locs[2]
	s.Standby = true
	if _, err := e.st.PutLocation(e.ctx, s); err != nil {
		t.Fatal(err)
	}
	e.m.MovePause = 20 * time.Millisecond
	return e, a, b, s
}

func TestStandbyIsOnlyUsedWhenNothingElseWorks(t *testing.T) {
	e, a, b, s := standbyEnv(t)
	e.cameras(4)
	e.refresh()
	// The standby location is the biggest, but no camera is placed there.
	if got := e.where(); got[a.ID] != 2 || got[b.ID] != 2 || got[s.ID] != 0 {
		t.Fatalf("spread: %v", got)
	}
	cams, _ := e.st.Cameras(e.ctx)
	for _, c := range cams {
		if c.StorageID == s.ID {
			t.Fatalf("standby became a camera's own location: %+v", c)
		}
	}
	// One network location going down sends its cameras to the other, not to standby.
	os.Remove(filepath.Join(a.Path, MarkerFile))
	e.refresh()
	if got := e.where(); got[b.ID] != 4 || got[s.ID] != 0 {
		t.Fatalf("with A down: %v", got)
	}
	// With both down, standby takes them all.
	os.Remove(filepath.Join(b.Path, MarkerFile))
	e.refresh()
	if got := e.where(); got[s.ID] != 4 {
		t.Fatalf("with A and B down: %v", got)
	}
	if e.said("No storage location can take video") {
		t.Error("warned that nothing records while standby does")
	}
	// B coming back takes them off standby once it has worked for a while, so a share
	// that comes and goes doesn't move them back and forth. The cameras whose own
	// location is A go to B for now.
	Mark(b.Path, b.ID)
	e.refresh()
	if got := e.where(); got[s.ID] != 4 {
		t.Fatalf("left standby before B settled: %v", got)
	}
	e.m.Settle = time.Nanosecond
	time.Sleep(2 * time.Millisecond) // Windows' clock can read the same across two quick refreshes
	e.refresh()
	if got := e.where(); got[b.ID] != 4 || got[s.ID] != 0 {
		t.Fatalf("with B back: %v", got)
	}
	e.m.Settle = time.Hour
	// A new camera added during an outage gets a network location as its own.
	os.Remove(filepath.Join(b.Path, MarkerFile))
	e.refresh()
	extra, _ := e.st.CreateCamera(e.ctx, store.Camera{Name: "late", MainURL: "rtsp://x", Enabled: true})
	if p := e.m.Placement(e.ctx, extra); p.Record != s.Path {
		t.Errorf("a new camera during the outage records to %q", p.Record)
	}
	if c, _ := e.st.Camera(e.ctx, extra.ID); c.StorageID == s.ID {
		t.Error("a new camera was given standby as its own location")
	}
}

func TestVideoOnStandbyGoesHomeAndStandbyStays(t *testing.T) {
	e, a, b, s := standbyEnv(t)
	cams := e.cameras(2)
	e.refresh()
	cams, _ = e.st.Cameras(e.ctx)
	home := map[int64]store.Location{a.ID: a, b.ID: b}

	// Both network locations go down, and the cameras record to standby.
	os.Remove(filepath.Join(a.Path, MarkerFile))
	os.Remove(filepath.Join(b.Path, MarkerFile))
	e.refresh()
	if got := e.where(); got[s.ID] != 2 {
		t.Fatalf("not on standby: %v", got)
	}
	var during []string
	for _, c := range cams {
		p := filepath.Join(s.Path, media.PathName(c.ID, "main"), "2026-11-10_09-00-00-000000.mp4")
		writeVideo(t, p, time.Hour)
		during = append(during, p)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(during[0]); err != nil {
		t.Fatal("video was moved while cameras still recorded to standby")
	}

	// The network comes back and the cameras return home.
	Mark(a.Path, a.ID)
	Mark(b.Path, b.ID)
	e.m.Settle = time.Nanosecond
	time.Sleep(2 * time.Millisecond)
	e.refresh()
	e.refresh()
	if got := e.where(); got[s.ID] != 0 {
		t.Fatalf("still on standby: %v", got)
	}
	for i, c := range cams {
		dst := home[c.StorageID]
		want := filepath.Join(dst.Path, media.ArchiveName(c.ID, dst.ID), filepath.Base(during[i]))
		e.waitFor("video to go home", func() bool { _, err := os.Stat(want); return err == nil })
	}
	e.waitFor("the move back to be reported", func() bool { return e.said("is ready for next time") })
	if l, err := e.st.Location(e.ctx, s.ID); err != nil || !l.Standby {
		t.Fatalf("standby location after the move: %+v %v", l, err)
	}
	if _, err := os.Stat(filepath.Join(s.Path, MarkerFile)); err != nil {
		t.Error("standby lost its marker")
	}
	files, _ := videoFiles(s.Path)
	if len(files) != 0 {
		t.Errorf("left on standby: %v", files)
	}
}

func TestVideoOnStandbyWaitsForAWorkingNetworkLocation(t *testing.T) {
	e, a, b, s := standbyEnv(t)
	cams := e.cameras(1)
	e.refresh()
	os.Remove(filepath.Join(a.Path, MarkerFile))
	os.Remove(filepath.Join(b.Path, MarkerFile))
	e.refresh()
	stuck := filepath.Join(s.Path, media.ArchiveName(cams[0].ID, s.ID), "2026-11-10_09-00-00-000000.mp4")
	writeVideo(t, stuck, time.Hour)
	// The camera is moved off standby by hand (as if removed), but nowhere is working.
	if err := e.st.DeleteCamera(e.ctx, cams[0].ID); err != nil {
		t.Fatal(err)
	}
	e.refresh()
	time.Sleep(150 * time.Millisecond)
	if _, err := os.Stat(stuck); err != nil {
		t.Fatal("video left standby with nowhere to go")
	}
	// Once B works, it goes there, even though its camera is gone.
	Mark(b.Path, b.ID)
	e.refresh()
	want := filepath.Join(b.Path, media.ArchiveName(cams[0].ID, b.ID), filepath.Base(stuck))
	e.waitFor("video to reach B", func() bool { _, err := os.Stat(want); return err == nil })
}
