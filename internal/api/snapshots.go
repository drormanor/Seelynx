package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/retention"
	"seelynx/internal/rules"
	"seelynx/internal/store"
)

type snapshotAddress struct{ key, uri string }

var (
	// frameSettle lets the recording catch up with the end of the search window. It ran
	// about a second behind real time on real cameras.
	frameSettle = 1500 * time.Millisecond
	frameRetry  = 750 * time.Millisecond
)

// A fixed time can't show what caused an event. On a real camera the movement began 0.7
// to 1.4 seconds before the event arrived, and could be over by then, but the camera
// could also react to someone just out of view who appeared 3 to 5 seconds later. So
// the picture is chosen from the video on both sides of the event.
const (
	searchBefore = 3 * time.Second
	searchAfter  = 5 * time.Second

	searchFPS     = 10
	searchWidth   = 160
	searchHeight  = 120
	pixelChanged  = 24 // brightness difference that counts as a change
	changedPixels = 12 // fewer than this is noise, not movement
)

const (
	frameAttempts = 4
	// A segment last written this long before an event stopped before it happened.
	segmentStale = 10 * time.Second
)

// findSegment returns the recording that covers a moment, and how far into it that is.
func findSegment(dir string, at time.Time) (string, time.Duration, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", 0, err
	}
	var best string
	var bestStart time.Time
	for _, e := range entries {
		start, ok := retention.SegmentStart(e.Name())
		if ok && !start.After(at) && start.After(bestStart) {
			best, bestStart = e.Name(), start
		}
	}
	if best == "" {
		return "", 0, errors.New("no recording covers this moment")
	}
	path := filepath.Join(dir, best)
	// os.Stat on purpose: a directory listing lags by many seconds for a file being written.
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, err
	}
	if info.ModTime().Add(segmentStale).Before(at) {
		return "", 0, errors.New("the camera wasn't recording at this moment")
	}
	return path, at.Sub(bestStart), nil
}

// bestMoment finds the frame that differs most from the scene around it. The scene is
// each pixel's median over the window, so it shows the empty room as long as nobody stays
// for most of the window. Each frame's overall brightness shift is taken out first,
// because cameras re-adjust exposure after movement and that changes every pixel.
// With no real movement, it returns false.
func bestMoment(ctx context.Context, ffmpeg string, input []string, from, to time.Duration) (time.Duration, bool, error) {
	args := append([]string{"-nostdin", "-v", "fatal", "-ss", fmt.Sprintf("%.3f", from.Seconds()),
		"-t", fmt.Sprintf("%.3f", (to - from).Seconds())}, input...)
	args = append(args, "-vf", fmt.Sprintf("fps=%d,scale=%d:%d,format=gray", searchFPS, searchWidth, searchHeight), "-f", "rawvideo", "-")
	cmd := exec.CommandContext(ctx, ffmpeg, args...)
	data, err := cmd.Output()
	if err != nil {
		return 0, false, err
	}
	const size = searchWidth * searchHeight
	count := len(data) / size
	if count < 3 {
		return 0, false, errors.New("too little video to search")
	}
	scene := make([]byte, size)
	column := make([]byte, count)
	for p := range size {
		for f := range count {
			column[f] = data[f*size+p]
		}
		slices.Sort(column)
		scene[p] = column[count/2]
	}
	best, bestChanged := 0, 0
	for f := range count {
		frame := data[f*size : (f+1)*size]
		var shifts [511]int
		for p, v := range frame {
			shifts[int(v)-int(scene[p])+255]++
		}
		shift, seen := 0, 0
		for i, n := range shifts {
			if seen += n; seen > size/2 {
				shift = i - 255
				break
			}
		}
		changed := 0
		for p, v := range frame {
			if d := int(v) - int(scene[p]) - shift; d > pixelChanged || d < -pixelChanged {
				changed++
			}
		}
		if changed > bestChanged {
			best, bestChanged = f, changed
		}
	}
	if bestChanged < changedPixels {
		return 0, false, nil
	}
	return from + time.Duration(best)*time.Second/searchFPS, true, nil
}

// searchWindow looks for the best moment around an event. Motion recording uses files
// of about 10 seconds, and any recording can roll over mid-search, so the window can span
// several files.
func (s *Server) searchWindow(ctx context.Context, dir string, at time.Time) (time.Time, bool, error) {
	from, to := at.Add(-searchBefore), at.Add(searchAfter)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false, err
	}
	type part struct {
		path  string
		start time.Time
	}
	var all []part
	for _, e := range entries {
		if start, ok := retention.SegmentStart(e.Name()); ok {
			all = append(all, part{filepath.Join(dir, e.Name()), start})
		}
	}
	slices.SortFunc(all, func(a, b part) int { return a.start.Compare(b.start) })
	var parts []part
	for i, p := range all {
		if p.start.After(to) {
			break
		}
		if i+1 == len(all) || all[i+1].start.After(from) {
			parts = append(parts, p)
		}
	}
	if len(parts) == 0 {
		return time.Time{}, false, errors.New("no recording covers this moment")
	}
	first := parts[0].start
	input := []string{"-i", parts[0].path}
	if len(parts) > 1 {
		var list strings.Builder
		for _, p := range parts {
			fmt.Fprintf(&list, "file '%s'\n", strings.ReplaceAll(filepath.ToSlash(p.path), "'", `'\''`))
		}
		tmp, err := os.CreateTemp("", "seelynx-*.txt")
		if err != nil {
			return time.Time{}, false, err
		}
		defer os.Remove(tmp.Name())
		_, err = tmp.WriteString(list.String())
		tmp.Close()
		if err != nil {
			return time.Time{}, false, err
		}
		input = []string{"-f", "concat", "-safe", "0", "-i", tmp.Name()}
	}
	start := max(from.Sub(first), 0)
	found, moved, err := bestMoment(ctx, s.FFmpeg, input, start, to.Sub(first))
	return first.Add(found), moved, err
}

func extractFrame(ctx context.Context, ffmpeg, path string, offset time.Duration) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "fatal", "-ss", fmt.Sprintf("%.3f", offset.Seconds()), "-i", path,
		"-frames:v", "1", "-q:v", "3", "-f", "image2pipe", "-c:v", "mjpeg", "-")
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errors.New("no frame at this moment yet")
	}
	return data, nil
}

// recordedFrame reads the picture from the recording, choosing the frame in the seconds
// around the event that shows the most movement.
func (s *Server) recordedFrame(ctx context.Context, id int64, at time.Time) ([]byte, error) {
	dir := ""
	if s.Media != nil {
		dir = s.Media.Folder(id)
	}
	if dir == "" && s.RecordingDir != "" {
		dir = filepath.Join(s.RecordingDir, fmt.Sprintf("cam_%d_main", id))
	}
	if s.FFmpeg == "" || dir == "" {
		return nil, errors.New("recordings can't be read without ffmpeg")
	}
	// A camera that isn't recording goes straight to being asked, without the wait.
	if _, _, err := findSegment(dir, at); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Until(at.Add(searchAfter + frameSettle))):
	}
	var last error
	for range frameAttempts {
		path, offset, err := findSegment(dir, at)
		if err != nil {
			return nil, err
		}
		found, moved, err := s.searchWindow(ctx, dir, at)
		if err != nil {
			s.Log.Debug("couldn't search the recording", "camera", id, "err", err)
		} else if moved {
			s.Log.Debug("chose a frame", "camera", id, "after_event", found.Sub(at))
			if p, o, err := findSegment(dir, found); err == nil {
				path, offset = p, o
			}
		}
		data, err := extractFrame(ctx, s.FFmpeg, path, offset)
		if err == nil {
			return data, nil
		}
		last = err
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(frameRetry):
		}
	}
	return nil, last
}

// CaptureSnapshot returns a picture of a moment: from the recording when there is one,
// otherwise a fresh one from the camera.
func (s *Server) CaptureSnapshot(ctx context.Context, id int64, at time.Time) ([]byte, error) {
	data, err := s.recordedFrame(ctx, id, at)
	if err == nil {
		return data, nil
	}
	s.Log.Debug("no frame from the recording", "camera", id, "err", err)
	return s.cameraPicture(ctx, id)
}

// cameraPicture asks the camera for a picture. Its snapshot address is remembered,
// because looking it up can take as long as a slow camera's picture.
func (s *Server) cameraPicture(ctx context.Context, id int64) ([]byte, error) {
	c, err := s.Store.Camera(ctx, id)
	if err != nil {
		return nil, err
	}
	addr := deviceAddress(c)
	if s.Motion != nil {
		addr = s.Motion.address(c)
	}
	if addr == "" {
		return nil, errors.New("the camera has no address")
	}
	client := &onvif.Client{XAddr: addr, User: c.Username, Pass: c.Password}
	key := addr + "\x00" + c.Username + "\x00" + c.Password

	s.snapshotMu.Lock()
	known := s.snapshotURIs[id]
	s.snapshotMu.Unlock()
	if known.key == key {
		if data, err := client.Fetch(ctx, known.uri); err == nil {
			return data, nil
		}
	}
	// A failed clock read only matters if the camera then refuses the login.
	if err := client.SyncClock(ctx); err != nil {
		s.Log.Debug("camera clock", "camera", c.Name, "err", err)
	}
	uri, err := client.SnapshotURI(ctx)
	if err != nil {
		return nil, err
	}
	s.snapshotMu.Lock()
	if s.snapshotURIs == nil {
		s.snapshotURIs = map[int64]snapshotAddress{}
	}
	s.snapshotURIs[id] = snapshotAddress{key, uri}
	s.snapshotMu.Unlock()
	return client.Fetch(ctx, uri)
}

// canSeePictures keeps pictures from a role that may only see system health.
func canSeePictures(u store.User, cameraID int64) bool {
	return (u.Can(store.PermLive) || u.Can(store.PermPlayback)) && u.CanSeeCamera(cameraID)
}

func (s *Server) alarmSnapshot(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	a, err := s.Store.Alarm(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Alarm not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if a.Snapshot == "" || !canSeePictures(currentUser(r), a.CameraID) {
		fail(w, http.StatusNotFound, "No picture.")
		return
	}
	data, err := os.ReadFile(filepath.Join(s.DataDir, rules.SnapshotFolder, filepath.Base(a.Snapshot)))
	if err != nil {
		fail(w, http.StatusNotFound, "This picture has been deleted.")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	w.Write(data)
}
