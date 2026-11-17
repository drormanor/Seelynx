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

type motionEnv struct {
	st  *store.Store
	w   *Worker
	cam store.Camera
	dir string
	now time.Time
}

func newMotionEnv(t *testing.T) *motionEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cam, err := st.CreateCamera(context.Background(), store.Camera{Name: "Door", MainURL: "rtsp://a", Record: true, MotionOnly: true, PreSeconds: 10, PostSeconds: 30, RetentionDays: 30, MotionSince: time.Now().Add(-24 * time.Hour).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	rec := t.TempDir()
	dir := filepath.Join(rec, "cam_"+itoa(cam.ID)+"_main")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Second)
	e := &motionEnv{st, &Worker{Store: st, Dir: rec, Log: slog.New(slog.DiscardHandler)}, cam, dir, now}
	// Watched since long ago, unless a test says otherwise.
	e.w.MotionWatched = func(int64) (time.Time, bool) { return now.Add(-24 * time.Hour), true }
	return e
}

// tenSecondFiles writes files covering the last span, ending at now, and returns them
// keyed by their age in seconds at the start of each file.
func (e *motionEnv) files(t *testing.T, span time.Duration) map[int]string {
	t.Helper()
	out := map[int]string{}
	for age := int(span / time.Second); age >= 0; age -= 10 {
		start := e.now.Add(-time.Duration(age) * time.Second)
		name := start.Format("2006-01-02_15-04-05") + "-000000.mp4"
		p := filepath.Join(e.dir, name)
		if err := os.WriteFile(p, []byte("video"), 0o600); err != nil {
			t.Fatal(err)
		}
		out[age] = p
	}
	return out
}

func (e *motionEnv) motion(t *testing.T, startAgo, endAgo time.Duration) {
	t.Helper()
	ctx := context.Background()
	id, err := e.st.StartMotion(ctx, e.cam.ID, e.now.Add(-startAgo))
	if err != nil {
		t.Fatal(err)
	}
	if endAgo >= 0 {
		if err := e.st.EndMotion(ctx, id, e.now.Add(-endAgo)); err != nil {
			t.Fatal(err)
		}
	}
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

func (e *motionEnv) pass(t *testing.T) {
	t.Helper()
	if err := e.w.Pass(context.Background(), e.now); err != nil {
		t.Fatal(err)
	}
}

func TestOnlyVideoNearMotionIsKept(t *testing.T) {
	e := newMotionEnv(t)
	files := e.files(t, 20*time.Minute)
	// Motion from 600 to 590 seconds ago, with 10 seconds before and 30 after.
	e.motion(t, 600*time.Second, 590*time.Second)
	e.pass(t)

	for age, want := range map[int]bool{
		900: false, // long before
		620: false, // ended 610 seconds ago, exactly at the start of the pre-event time
		610: true,  // pre-event
		600: true,  // motion
		590: true,  // post-event
		570: true,  // post-event
		550: false, // after
		300: false,
		10:  true, // too recent to judge
		0:   true, // still being written
	} {
		if got := exists(files[age]); got != want {
			t.Errorf("file %d seconds old: exists %v, want %v", age, got, want)
		}
	}
}

func TestMotionInProgressKeepsItsVideo(t *testing.T) {
	e := newMotionEnv(t)
	files := e.files(t, 10*time.Minute)
	e.motion(t, 300*time.Second, -1) // still going
	e.pass(t)
	if !exists(files[300]) || !exists(files[120]) {
		t.Fatal("video during ongoing motion was deleted")
	}
	if exists(files[500]) {
		t.Fatal("video before the motion was kept")
	}
}

func TestRecentVideoWaitsForLateMotion(t *testing.T) {
	e := newMotionEnv(t)
	files := e.files(t, 5*time.Minute)
	e.motion(t, 290*time.Second, 285*time.Second) // so it has reported motion before
	e.pass(t)
	// Ended under pre-time plus the margin ago: motion may yet claim it.
	if !exists(files[60]) {
		t.Fatal("video was deleted before late motion could claim it")
	}
	if exists(files[200]) {
		t.Fatal("old video without motion was kept")
	}
}

func TestLostMotionEventsKeepEverything(t *testing.T) {
	e := newMotionEnv(t)
	e.w.MotionWatched = func(int64) (time.Time, bool) { return time.Time{}, false }
	files := e.files(t, 20*time.Minute)
	e.motion(t, 600*time.Second, 590*time.Second)
	e.pass(t)
	if !exists(files[900]) {
		t.Fatal("video was deleted while motion events were down")
	}
}

func TestVideoFromAnOutageIsKeptAfterEventsReturn(t *testing.T) {
	e := newMotionEnv(t)
	// Events came back 300 seconds ago, so nothing before then can be judged.
	e.w.MotionWatched = func(int64) (time.Time, bool) { return e.now.Add(-300 * time.Second), true }
	files := e.files(t, 20*time.Minute)
	e.motion(t, 1100*time.Second, 1090*time.Second)
	e.pass(t)
	if !exists(files[900]) {
		t.Fatal("video from the outage was deleted")
	}
	if exists(files[200]) {
		t.Fatal("video after the outage wasn't judged")
	}
}

func TestCameraThatNeverReportedMotionKeepsEverything(t *testing.T) {
	e := newMotionEnv(t)
	files := e.files(t, 20*time.Minute)
	e.w.firstPass()
	e.w.started = e.now.Add(-time.Hour)
	e.pass(t)
	e.pass(t)
	if !exists(files[900]) {
		t.Fatal("video was deleted from a camera whose detection may be off")
	}
	events, _ := e.st.Events(context.Background(), 10)
	if len(events) != 1 || !strings.Contains(events[0].Message, "hasn't reported any motion") {
		t.Fatalf("%+v", events)
	}
}

func TestVideoFromBeforeTheModeWasTurnedOnIsKept(t *testing.T) {
	e := newMotionEnv(t)
	c := e.cam
	c.MotionSince = e.now.Add(-300 * time.Second).Unix()
	if _, err := e.st.UpdateCamera(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	files := e.files(t, 20*time.Minute)
	e.motion(t, 1150*time.Second, 1140*time.Second)
	e.pass(t)
	if !exists(files[900]) {
		t.Fatal("video recorded before the mode was on was deleted")
	}
	if exists(files[200]) {
		t.Fatal("video since the mode was on wasn't judged")
	}
}

func TestAnOlderLongFileCountsItsWholeLength(t *testing.T) {
	e := newMotionEnv(t)
	// Five-minute files, with motion near the end of the first.
	first := e.now.Add(-20 * time.Minute)
	long := filepath.Join(e.dir, first.Format("2006-01-02_15-04-05")+"-000000.mp4")
	next := filepath.Join(e.dir, first.Add(5*time.Minute).Format("2006-01-02_15-04-05")+"-000000.mp4")
	last := filepath.Join(e.dir, e.now.Format("2006-01-02_15-04-05")+"-000000.mp4")
	for _, p := range []string{long, next, last} {
		if err := os.WriteFile(p, []byte("video"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	e.motion(t, 16*time.Minute, 16*time.Minute-time.Second)
	e.pass(t)
	if !exists(long) {
		t.Fatal("motion late in a long file didn't count")
	}
}

func TestContinuousCamerasAreNeverPruned(t *testing.T) {
	e := newMotionEnv(t)
	c := e.cam
	c.MotionOnly = false
	if _, err := e.st.UpdateCamera(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	files := e.files(t, 20*time.Minute)
	e.motion(t, 600*time.Second, 590*time.Second)
	e.pass(t)
	if !exists(files[900]) {
		t.Fatal("a continuous camera lost video")
	}
}

func TestSegmentStart(t *testing.T) {
	got, ok := SegmentStart("2026-11-05_09-31-06-844688.mp4")
	want := time.Date(2026, 11, 5, 9, 31, 6, 844688000, time.Local)
	if !ok || !got.Equal(want) {
		t.Fatalf("%v %v", got, ok)
	}
	for _, name := range []string{"notes.txt", "bad.mp4", "2026-11-05_09-31-06-abcdef.mp4"} {
		if _, ok := SegmentStart(name); ok {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestOnlyMotionTimesArePruned(t *testing.T) {
	e := newMotionEnv(t)
	files := e.files(t, 20*time.Minute)
	e.motion(t, 1150*time.Second, 1145*time.Second) // so it has reported motion before
	// Everything is kept from 605 to 300 seconds ago, as outside a motion-only schedule.
	keepFrom, keepTo := e.now.Add(-605*time.Second), e.now.Add(-5*time.Minute)
	e.w.MotionTimes = func(context.Context, store.Camera) func(time.Time) bool {
		return func(t time.Time) bool { return t.Before(keepFrom) || !t.Before(keepTo) }
	}
	e.pass(t)
	for age, want := range map[int]bool{
		900: false, // a motion-only time, without motion
		610: true,  // reaches into the time everything is kept
		600: true,
		400: true,
		310: true,
		300: false, // back to motion only
		200: false,
	} {
		if got := exists(files[age]); got != want {
			t.Errorf("file %d seconds old: exists %v, want %v", age, got, want)
		}
	}
}

func TestMotionOutsideTheScheduleIsPruned(t *testing.T) {
	e := newMotionEnv(t)
	c := e.cam
	c.MotionOnly, c.ScheduleID, c.OutsideMode = false, 1, store.OutsideMotion
	if _, err := e.st.UpdateCamera(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	files := e.files(t, 20*time.Minute)
	e.motion(t, 1150*time.Second, 1145*time.Second)
	e.w.MotionTimes = func(context.Context, store.Camera) func(time.Time) bool {
		return func(time.Time) bool { return true }
	}
	e.pass(t)
	if exists(files[600]) {
		t.Fatal("video outside the schedule, without motion, was kept")
	}
}
