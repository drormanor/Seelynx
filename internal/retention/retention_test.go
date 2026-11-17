package retention

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seelynx/internal/store"
)

func TestCameraID(t *testing.T) {
	for dir, want := range map[string]int64{"cam_12_main": 12, "cam_12_main_at3": 12, "cam_12_main_atx": 0, "cam_12_mainx": 0, "cam_1_sub": 0, "other": 0, "cam_x_main": 0} {
		id, _ := cameraID(dir)
		if id != want {
			t.Errorf("%s: got %d", dir, id)
		}
	}
}

func TestPass(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	short, _ := st.CreateCamera(ctx, store.Camera{Name: "short", MainURL: "rtsp://a", RetentionDays: 7})
	forever, _ := st.CreateCamera(ctx, store.Camera{Name: "forever", MainURL: "rtsp://b", RetentionDays: 0})

	rec := filepath.Join(tmp, "rec")
	now := time.Now()
	write := func(rel string, age time.Duration) string {
		p := filepath.Join(rec, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	day := 24 * time.Hour
	dirA := "cam_" + itoa(short.ID) + "_main"
	dirB := "cam_" + itoa(forever.ID) + "_main"
	expired := write(dirA+"/old.mp4", 8*day)
	fresh := write(dirA+"/new.mp4", 6*day)
	kept := write(dirB+"/ancient.mp4", 400*day)
	foreign := write("not-ours/old.mp4", 400*day)
	orphanOld := write("cam_999_main/old.mp4", 31*day)
	orphanNew := write("cam_999_main/new.mp4", 29*day)

	w := &Worker{Store: st, Dir: rec, Log: slog.New(slog.DiscardHandler)}
	if err := w.Pass(ctx, now); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]bool{expired: false, fresh: true, kept: true, foreign: true, orphanOld: false, orphanNew: true} {
		_, err := os.Stat(p)
		if (err == nil) != want {
			t.Errorf("%s: exists=%v want %v", p, err == nil, want)
		}
	}
	if got := w.RecordedBytes(); got != 12 {
		t.Errorf("recorded bytes %d", got)
	}
}

func TestDiskPressureKeepsRecentFiles(t *testing.T) {
	saved := diskUsage
	defer func() { diskUsage = saved }()
	diskUsage = func(string) (uint64, uint64, error) { return 1 << 30, 1 << 40, nil }
	ctx := context.Background()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cam, _ := st.CreateCamera(ctx, store.Camera{Name: "c", MainURL: "rtsp://a", RetentionDays: 0})
	dir := filepath.Join(tmp, "rec", "cam_"+itoa(cam.ID)+"_main")
	os.MkdirAll(dir, 0o755)
	now := time.Now()
	old, recent := filepath.Join(dir, "old"), filepath.Join(dir, "recent")
	os.WriteFile(old, []byte("x"), 0o644)
	os.WriteFile(recent, []byte("x"), 0o644)
	os.Chtimes(old, now.Add(-time.Hour), now.Add(-time.Hour))

	// An impossible floor forces maximum pressure.
	w := &Worker{Store: st, Dir: filepath.Join(tmp, "rec"), MinFree: 1 << 39, Log: slog.New(slog.DiscardHandler)}
	if err := w.Pass(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); err == nil {
		t.Error("old segment should be deleted under pressure")
	}
	if _, err := os.Stat(recent); err != nil {
		t.Error("segment being written must survive")
	}
	events, _ := st.Events(ctx, 10)
	if len(events) != 1 || events[0].Level != "warning" {
		t.Errorf("expected a warning event, got %+v", events)
	}
}

func itoa(n int64) string {
	return string(rune('0' + n)) // test ids are single digits
}

func TestTargetLargerThanTheDiskIsCappedAndReported(t *testing.T) {
	saved := diskUsage
	defer func() { diskUsage = saved }()
	var messages []string
	w := &Worker{MinFree: 50 << 30, Log: slog.New(slog.DiscardHandler), OnLowDisk: func(m string) { messages = append(messages, m) }}
	if got := w.minFree(19 << 30); got != (19<<30)/10 {
		t.Fatalf("%d", got)
	}
	w.minFree(19 << 30)
	if len(messages) != 1 || !strings.Contains(messages[0], "Keep free") {
		t.Fatalf("%v", messages)
	}
	// A reachable target is left alone, and an unknown disk size changes nothing.
	ok := &Worker{MinFree: 200 << 30, Log: slog.New(slog.DiscardHandler)}
	if ok.minFree(1000<<30) != 200<<30 || ok.minFree(0) != 200<<30 {
		t.Fatal("changed a reachable target")
	}
}

func TestRecordingsSurviveATargetLargerThanTheDisk(t *testing.T) {
	saved := diskUsage
	defer func() { diskUsage = saved }()
	// 6 GB free of 19 GB, with a 50 GB target: the case that deleted everything.
	diskUsage = func(string) (uint64, uint64, error) { return 6 << 30, 19 << 30, nil }
	dir := t.TempDir()
	cam := filepath.Join(dir, "cam_1_main")
	if err := os.MkdirAll(cam, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cam, "old.mp4")
	if err := os.WriteFile(path, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	os.Chtimes(path, old, old)
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w := &Worker{Store: st, Dir: dir, MinFree: 50 << 30, Log: slog.New(slog.DiscardHandler)}
	if err := w.Pass(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("a recording was deleted to chase an impossible target")
	}
}

func TestEachLocationKeepsItsOwnFreeSpace(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	st, err := store.Open(filepath.Join(tmp, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cam, _ := st.CreateCamera(ctx, store.Camera{Name: "c", MainURL: "rtsp://a", RetentionDays: 0})
	full, roomy := filepath.Join(tmp, "full"), filepath.Join(tmp, "roomy")
	saved := diskUsage
	defer func() { diskUsage = saved }()
	diskUsage = func(p string) (uint64, uint64, error) {
		if p == full {
			return 1 << 30, 100 << 30, nil
		}
		return 90 << 30, 100 << 30, nil
	}
	now := time.Now()
	write := func(dir, folder string, age time.Duration) string {
		p := filepath.Join(dir, folder, "seg.mp4")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("video"), 0o644)
		os.Chtimes(p, now.Add(-age), now.Add(-age))
		return p
	}
	// The camera records into full now, and left older video in roomy.
	live := "cam_" + itoa(cam.ID) + "_main"
	inFull := write(full, live, time.Hour)
	inRoomy := write(roomy, live+"_at2", 48*time.Hour)

	w := &Worker{Store: st, Log: slog.New(slog.DiscardHandler), Locations: func() []Location {
		return []Location{{ID: 1, Name: "Full", Dir: full, MinFree: 10 << 30}, {ID: 2, Name: "Roomy", Dir: roomy, MinFree: 10 << 30}}
	}}
	if err := w.Pass(ctx, now); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(inFull); err == nil {
		t.Error("the full location wasn't cleaned")
	}
	if _, err := os.Stat(inRoomy); err != nil {
		t.Error("older video in another location was deleted for the full one")
	}
	if u, ok := w.Usage(2); !ok || u.Bytes != 5 || !u.Oldest.Equal(now.Add(-48*time.Hour).Truncate(time.Second)) && u.Oldest.IsZero() {
		t.Errorf("roomy usage: %+v %v", u, ok)
	}
	if u, _ := w.Usage(1); u.Bytes != 0 || !u.Oldest.IsZero() {
		t.Errorf("full usage: %+v", u)
	}
	if w.RecordedBytes() != 5 {
		t.Errorf("total %d", w.RecordedBytes())
	}
	events, _ := st.Events(ctx, 10)
	if len(events) != 1 || !strings.Contains(events[0].Message, "Low disk space on Full") {
		t.Errorf("%+v", events)
	}
}
