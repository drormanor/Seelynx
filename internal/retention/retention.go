// Package retention deletes old recordings by age and under disk pressure.
package retention

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"seelynx/internal/store"
)

type file struct {
	path string
	size int64
	mod  time.Time
}

// Location is one folder of recordings, with the free space to keep on its disk.
type Location struct {
	ID      int64
	Name    string
	Dir     string
	MinFree uint64 // bytes
}

// Usage is what a pass found in one location.
type Usage struct {
	Bytes  uint64    // size of the recordings kept
	Oldest time.Time // the oldest recording kept; zero when there's none
}

type Worker struct {
	Store *store.Store
	// Locations lists the folders that can be read now. Nil means the one in Dir.
	Locations func() []Location
	Dir       string
	MinFree   uint64 // bytes of free disk to keep, for Dir
	Log       *slog.Logger
	OnLowDisk func(message string) // may be nil
	// MotionWatched says since when a camera's motion events have been arriving, and
	// false if they aren't. Nil keeps all video.
	MotionWatched func(cameraID int64) (time.Time, bool)
	// MotionTimes says when a camera keeps only video near motion, for a camera with a
	// schedule. Nil means whenever it is set to record on motion.
	MotionTimes func(ctx context.Context, c store.Camera) func(time.Time) bool

	recorded  atomic.Uint64
	usage     sync.Map // location ID -> Usage
	busy      sync.Map // locations still being cleaned by an earlier pass
	warned    sync.Map // locations warned about an impossible target
	keeping   sync.Map // cameras already warned about
	startOnce sync.Once
	started   time.Time
}

// diskUsage is replaced in tests.
var diskUsage = DiskUsage

// stuckAfter is how long a pass waits for a location. A network share that stops
// answering can hang file calls for a long time, and shouldn't hold up the other locations.
const stuckAfter = 2 * time.Minute

func (w *Worker) locations() []Location {
	if w.Locations == nil {
		return []Location{{Dir: w.Dir, MinFree: w.MinFree}}
	}
	return w.Locations()
}

// minFree is the free space to keep. A target as large as the disk can never be met, so
// every recording would be deleted as soon as it was allowed. That would look like
// recording worked while keeping only minutes of video, so it's capped and reported.
func (w *Worker) minFree(total uint64) uint64 {
	return w.target(Location{Dir: w.Dir, MinFree: w.MinFree}, total)
}

func (w *Worker) target(l Location, total uint64) uint64 {
	if total == 0 || l.MinFree < total {
		return l.MinFree
	}
	if _, seen := w.warned.LoadOrStore(l.ID, true); !seen {
		where := "this disk"
		if l.Name != "" {
			where = "the disk of " + l.Name
		}
		msg := fmt.Sprintf("The free space to keep (%d GB) is more than %s holds (%d GB), so Seelynx is keeping 10%% free instead. Lower Keep free on the Storage screen.", l.MinFree>>30, where, total>>30)
		w.Log.Warn(msg)
		if w.OnLowDisk != nil {
			w.OnLowDisk(msg)
		}
	}
	return total / 10
}

// RecordedBytes is the size of all recordings at the end of the last pass.
func (w *Worker) RecordedBytes() uint64 { return w.recorded.Load() }

// Usage reports a location as of the last pass that finished it.
func (w *Worker) Usage(id int64) (Usage, bool) {
	u, ok := w.usage.Load(id)
	if !ok {
		return Usage{}, false
	}
	return u.(Usage), true
}

func (w *Worker) Run(ctx context.Context, every time.Duration) {
	for {
		if err := w.Pass(ctx, time.Now()); err != nil && ctx.Err() == nil {
			w.Log.Error("retention pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

// cameraID reads the camera from a folder name. A camera records into cam_<id>_main, and
// video it left in a location it no longer records to is in cam_<id>_main_at<location>.
func cameraID(dir string) (int64, bool) {
	s, ok := strings.CutPrefix(dir, "cam_")
	if !ok {
		return 0, false
	}
	s, rest, ok := strings.Cut(s, "_main")
	if !ok {
		return 0, false
	}
	if rest != "" {
		at, ok := strings.CutPrefix(rest, "_at")
		if !ok {
			return 0, false
		}
		if _, err := strconv.ParseInt(at, 10, 64); err != nil {
			return 0, false
		}
	}
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}

// Pass runs once over every location. Recordings of deleted cameras are kept for
// defaultDays, so removing a camera by mistake does not immediately destroy its footage.
func (w *Worker) Pass(ctx context.Context, now time.Time) error {
	cams, err := w.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	w.firstPass()
	type result struct {
		id  int64
		err error
	}
	locs := w.locations()
	done := make(chan result, len(locs))
	started := 0
	for _, l := range locs {
		if _, running := w.busy.LoadOrStore(l.ID, true); running {
			continue
		}
		started++
		go func() {
			defer w.busy.Delete(l.ID)
			done <- result{l.ID, w.clean(ctx, l, cams, now)}
		}()
	}
	var first error
	timeout := time.NewTimer(stuckAfter)
	defer timeout.Stop()
	for range started {
		select {
		case r := <-done:
			if r.err != nil && first == nil {
				first = r.err
			}
		case <-timeout.C:
			w.Log.Warn("a storage location is slow to clean, continuing without it")
			return first
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	var total uint64
	for _, l := range locs {
		if u, ok := w.Usage(l.ID); ok {
			total += u.Bytes
		}
	}
	w.recorded.Store(total)
	return first
}

// clean applies retention to one location.
func (w *Worker) clean(ctx context.Context, l Location, cams []store.Camera, now time.Time) error {
	const defaultDays = 30
	days := make(map[int64]int, len(cams))
	byID := make(map[int64]store.Camera, len(cams))
	for _, c := range cams {
		days[c.ID] = c.RetentionDays
		byID[c.ID] = c
	}
	dirs, err := os.ReadDir(l.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			w.usage.Store(l.ID, Usage{})
			return nil
		}
		return err
	}
	var files []file
	var total uint64
	var expired, pruned int
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		id, ok := cameraID(d.Name())
		if !ok {
			continue // never touch anything that is not ours
		}
		if c, ok := byID[id]; ok && c.UsesMotion() {
			n, err := w.pruneMotion(ctx, c, filepath.Join(l.Dir, d.Name()), now)
			if err != nil {
				w.Log.Error("motion recording", "camera", id, "err", err)
			}
			pruned += n
		}
		keep, known := days[id]
		if !known {
			keep = defaultDays
		}
		cutoff := now.AddDate(0, 0, -keep)
		err := filepath.WalkDir(filepath.Join(l.Dir, d.Name()), func(p string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() {
				return nil
			}
			info, err := e.Info()
			if err != nil {
				return nil
			}
			if keep > 0 && info.ModTime().Before(cutoff) && os.Remove(p) == nil {
				expired++
				return nil
			}
			files = append(files, file{p, info.Size(), info.ModTime()})
			total += uint64(info.Size())
			return nil
		})
		if err != nil {
			return err
		}
	}

	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	pressured, freed := 0, uint64(0)
	if free, size, err := diskUsage(l.Dir); err == nil && free < w.target(l, size) {
		need := w.target(l, size) - free
		for i, f := range files {
			if freed >= need {
				break
			}
			// Files still being written cannot be deleted on Windows, and
			// should not be deleted anywhere.
			if now.Sub(f.mod) < 10*time.Minute {
				continue
			}
			if os.Remove(f.path) == nil {
				freed += uint64(f.size)
				pressured++
				files[i].size = -1 // gone
			}
		}
	}
	u := Usage{Bytes: total - freed}
	for _, f := range files {
		if f.size >= 0 {
			u.Oldest = f.mod
			break
		}
	}
	w.usage.Store(l.ID, u)

	if expired > 0 {
		w.Log.Info("retention removed expired segments", "count", expired, "location", l.Name)
	}
	if pruned > 0 {
		w.Log.Debug("removed video without motion", "count", pruned)
	}
	if pressured > 0 {
		msg := fmt.Sprintf("Low disk space: deleted %d oldest segments (%.1f GB) before their retention period ended", pressured, float64(freed)/1e9)
		if l.Name != "" {
			msg = fmt.Sprintf("Low disk space on %s: deleted %d oldest segments (%.1f GB) before their retention period ended", l.Name, pressured, float64(freed)/1e9)
		}
		if err := w.Store.AddEvent(ctx, "warning", msg); err != nil {
			return err
		}
		if w.OnLowDisk != nil {
			w.OnLowDisk(msg)
		}
	}
	return nil
}
