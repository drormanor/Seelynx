package retention

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/store"
)

const (
	// A file stays long enough for motion that starts just after it to claim it as
	// pre-event video, and for that motion's event to arrive.
	pruneMargin = time.Minute
	// Cameras need a moment to connect after a restart, which isn't worth a warning.
	warnAfter = 2 * time.Minute
)

// SegmentStart reads a recording's local start time from its name, which looks like
// 2026-11-05_09-31-06-844688.mp4.
func SegmentStart(name string) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, ".mp4")
	if !ok || len(base) != len("2006-01-02_15-04-05-000000") {
		return time.Time{}, false
	}
	t, err := time.ParseInLocation("2006-01-02_15-04-05", base[:19], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	micros, err := strconv.Atoi(base[20:])
	if err != nil {
		return time.Time{}, false
	}
	return t.Add(time.Duration(micros) * time.Microsecond), true
}

type segment struct {
	path       string
	start, end time.Time
}

func segments(dir string) ([]segment, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []segment
	for _, e := range entries {
		if start, ok := SegmentStart(e.Name()); ok {
			out = append(out, segment{path: filepath.Join(dir, e.Name()), start: start})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	for i := range out {
		// If recording stopped between files this runs long, which only keeps more.
		if i+1 < len(out) {
			out[i].end = out[i+1].start
		}
	}
	return out, nil
}

// warnKeeping says once why a camera set to record on motion is keeping everything.
func (w *Worker) warnKeeping(ctx context.Context, c store.Camera, now time.Time, reason string) {
	if now.Sub(w.firstPass()) < warnAfter {
		return
	}
	if _, seen := w.keeping.LoadOrStore(c.ID, true); seen {
		return
	}
	msg := fmt.Sprintf("%s is set to record on motion, but %s, so all its video is being kept.", c.Name, reason)
	w.Log.Warn(msg)
	if err := w.Store.AddEvent(ctx, "warning", msg); err != nil {
		w.Log.Error("event", "err", err)
	}
}

func (w *Worker) firstPass() time.Time {
	w.startOnce.Do(func() { w.started = time.Now() })
	return w.started
}

// pruneMotion deletes a camera's video that has no motion nearby. Losing video is much
// worse than using disk, so anything uncertain is kept: a camera whose motion events
// aren't arriving, one that has never reported motion (its detection may be off), the
// time before events were last confirmed, video from before this mode was turned on, and
// the file still being written.
func (w *Worker) pruneMotion(ctx context.Context, c store.Camera, dir string, now time.Time) (int, error) {
	if w.MotionWatched == nil {
		return 0, nil
	}
	since, ok := w.MotionWatched(c.ID)
	if !ok {
		w.warnKeeping(ctx, c, now, "Seelynx isn't receiving its motion events")
		return 0, nil
	}
	seen, err := w.Store.HasMotion(ctx, c.ID)
	if err != nil {
		return 0, err
	}
	if !seen {
		w.warnKeeping(ctx, c, now, "it hasn't reported any motion yet")
		return 0, nil
	}
	w.keeping.Delete(c.ID)

	files, err := segments(dir)
	if err != nil || len(files) < 2 {
		return 0, err
	}
	pre := time.Duration(c.PreSeconds) * time.Second
	post := time.Duration(c.PostSeconds) * time.Second
	files = files[:len(files)-1] // the newest is still being written
	if c.MotionSince == 0 {
		return 0, nil
	}
	if on := time.Unix(c.MotionSince, 0); on.After(since) {
		since = on
	}

	motionAt := func(time.Time) bool { return c.MotionOnly }
	if w.MotionTimes != nil {
		motionAt = w.MotionTimes(ctx, c)
	}
	motion, err := w.Store.MotionBetween(ctx, c.ID, files[0].start.Add(-post), now.Add(time.Minute))
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, f := range files {
		if f.start.Before(since) || now.Sub(f.end) < pre+pruneMargin || !motionThroughout(motionAt, f) {
			continue
		}
		keep := false
		for _, m := range motion {
			end := now
			if m.End != nil {
				end = *m.End
			}
			if f.end.After(m.Start.Add(-pre)) && f.start.Before(end.Add(post)) {
				keep = true
				break
			}
		}
		if !keep && os.Remove(f.path) == nil {
			removed++
		}
	}
	return removed, nil
}

// motionThroughout reports whether only motion was to be kept for all of a file. A file
// that reaches into a time when everything is kept is kept whole.
func motionThroughout(motionAt func(time.Time) bool, f segment) bool {
	for t := f.start; t.Before(f.end); t = t.Add(time.Minute) {
		if !motionAt(t) {
			return false
		}
	}
	return motionAt(f.end.Add(-time.Millisecond))
}
