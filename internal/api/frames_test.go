package api

import (
	"bytes"
	"context"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"seelynx/internal/store"
)

func testFFmpeg(t *testing.T) string {
	t.Helper()
	for _, p := range []string{"../../.tools/ffmpeg/ffmpeg.exe", "ffmpeg"} {
		if path, err := exec.LookPath(p); err == nil {
			abs, _ := filepath.Abs(path)
			return abs
		}
	}
	t.Skip("ffmpeg isn't installed")
	return ""
}

// recording writes a fragmented MP4, like the recorder's, that is red for two seconds
// and then blue, named so it starts at start.
func recording(t *testing.T, ffmpeg, dir string, start time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, start.Format("2006-01-02_15-04-05")+"-000000.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-y",
		"-f", "lavfi", "-i", "color=red:s=64x64:r=10:d=2",
		"-f", "lavfi", "-i", "color=blue:s=64x64:r=10:d=2",
		"-filter_complex", "[0:v][1:v]concat=n=2:v=1[v]", "-map", "[v]",
		"-c:v", "libx264", "-g", "5", "-pix_fmt", "yuv420p", "-movflags", "frag_keyframe+empty_moov", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	return path
}

func frameColor(t *testing.T, data []byte) string {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, _, b, _ := img.At(32, 32).RGBA()
	if r > b {
		return "red"
	}
	return "blue"
}

// movingBox writes a recording of a dark scene where a bright box shows for a moment.
func movingBox(t *testing.T, ffmpeg, dir string, start time.Time, drawbox string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, start.Format("2006-01-02_15-04-05")+"-000000.mp4")
	filter := "color=0x202020:s=320x240:r=30:d=10"
	if drawbox != "" {
		filter += ",drawbox=" + drawbox
	}
	cmd := exec.Command(ffmpeg, "-v", "error", "-y", "-f", "lavfi", "-i", filter,
		"-c:v", "libx264", "-g", "15", "-pix_fmt", "yuv420p", "-movflags", "frag_keyframe+empty_moov", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
}

func boxVisible(t *testing.T, data []byte) bool {
	t.Helper()
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := img.At(160, 120).RGBA()
	return r > 0x8000
}

func TestBriefMovementBeforeTheEventIsFound(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	// The box shows for half a second, and is gone a second before the event arrives.
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='between(t,3.5,4)'")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture missed the movement")
	}
}

func TestSomeoneLeavingIsNotMistakenForTheChange(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	// In view when the search window opens, then gone for the rest of it.
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='lt(t,3.5)'")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture showed the empty scene")
	}
}

func TestExposureChangeIsNotMistakenForMovement(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	// The whole picture brightens near the end, as a camera does after a movement.
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='between(t,3.5,4)',drawbox=x=0:y=0:w=320:h=240:color=white@0.3:t=fill:enable='gt(t,8)'")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture showed the exposure change")
	}
}

func TestSomeoneAppearingAfterTheEventIsFound(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='between(t,8,8.6)'")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture missed someone arriving just after the event")
	}
}

func TestStillSceneUsesTheEventTime(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	movingBox(t, e.server.FFmpeg, dir, start, "")
	path, offset, err := findSegment(dir, start.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	_, moved, err := bestMoment(context.Background(), e.server.FFmpeg, []string{"-i", path}, offset-searchBefore, offset+searchAfter)
	if err != nil || moved {
		t.Fatalf("%v %v", moved, err)
	}
	if _, err := e.server.recordedFrame(context.Background(), 7, start.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestSearchCrossesShortMotionFiles(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	// Two 10-second files, as motion recording makes. The movement is in the first, and
	// the event arrives during the second.
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='between(t,8.5,9.5)'")
	movingBox(t, e.server.FFmpeg, dir, start.Add(10*time.Second), "")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(11*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture missed movement in the previous file")
	}
}

func TestEventAtTheStartOfARecordingStillWorks(t *testing.T) {
	e, dir := frameEnv(t)
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	movingBox(t, e.server.FFmpeg, dir, start, "x=100:y=80:w=120:h=80:color=white:t=fill:enable='between(t,0.4,0.9)'")
	data, err := e.server.recordedFrame(context.Background(), 7, start.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !boxVisible(t, data) {
		t.Fatal("the picture missed the movement")
	}
}

func frameEnv(t *testing.T) (*env, string) {
	e := newEnv(t)
	e.server.FFmpeg = testFFmpeg(t)
	e.server.RecordingDir = t.TempDir()
	saved, savedRetry := frameSettle, frameRetry
	frameSettle, frameRetry = 0, 10*time.Millisecond
	t.Cleanup(func() { frameSettle, frameRetry = saved, savedRetry })
	return e, filepath.Join(e.server.RecordingDir, "cam_7_main")
}

func TestPicksTheSegmentCoveringTheEvent(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 5, 9, 0, 0, 0, time.Local)
	for _, name := range []string{"2026-11-05_09-00-00-000000.mp4", "2026-11-05_09-05-00-250000.mp4", "2026-11-05_09-10-00-000000.mp4", "notes.txt", "bad-name.mp4"} {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, base.Add(time.Hour), base.Add(time.Hour))
	}
	path, offset, err := findSegment(dir, base.Add(6*time.Minute))
	if err != nil || filepath.Base(path) != "2026-11-05_09-05-00-250000.mp4" || offset != 59750*time.Millisecond {
		t.Fatalf("%s %s %v", path, offset, err)
	}
	if _, _, err := findSegment(dir, base.Add(-time.Minute)); err == nil {
		t.Fatal("a moment before any recording was accepted")
	}
}

func TestStoppedRecordingIsNotUsedForALaterEvent(t *testing.T) {
	dir := t.TempDir()
	start := time.Now().Add(-time.Hour)
	p := filepath.Join(dir, start.Format("2006-01-02_15-04-05")+"-000000.mp4")
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, start.Add(time.Minute), start.Add(time.Minute))
	if _, _, err := findSegment(dir, time.Now()); err == nil {
		t.Fatal("a recording that stopped an hour ago was used")
	}
}

func TestWithoutARecordingTheCameraIsAsked(t *testing.T) {
	e, dir := frameEnv(t)
	ctx := context.Background()
	var asked atomic.Int32
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Add(1); w.WriteHeader(500) }))
	defer cam.Close()
	e.server.Motion = &MotionWatcher{Address: func(store.Camera) string { return cam.URL }}
	c, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})
	e.server.CaptureSnapshot(ctx, c.ID, time.Now())
	if asked.Load() == 0 {
		t.Fatal("the camera wasn't asked")
	}

	// With a recording, the camera isn't troubled at all.
	asked.Store(0)
	dir = filepath.Join(filepath.Dir(dir), "cam_"+strconv.FormatInt(c.ID, 10)+"_main")
	start := time.Now().Add(-time.Minute).Truncate(time.Second)
	recording(t, e.server.FFmpeg, dir, start)
	data, err := e.server.CaptureSnapshot(ctx, c.ID, start.Add(time.Second))
	if err != nil || frameColor(t, data) == "" || asked.Load() != 0 {
		t.Fatalf("%v, camera asked %d times", err, asked.Load())
	}
}

func TestNoFFmpegFallsBackToTheCamera(t *testing.T) {
	e := newEnv(t)
	if _, err := e.server.recordedFrame(context.Background(), 1, time.Now()); err == nil {
		t.Fatal("expected an error")
	}
}
