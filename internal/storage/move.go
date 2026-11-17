package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

// Moving empties a location into another before it's removed. The location stops taking
// video first, so its cameras record elsewhere. Then its files are moved one at a time:
// copied, synced to disk, renamed into place and only then deleted, so every recording can
// be played back from one location or the other throughout. Files are renamed into the
// destination's folder for the camera's older video, where playback already looks.

// MoveStatus is how a move is going, for the Storage page.
type MoveStatus struct {
	To      int64  `json:"to"`
	Done    uint64 `json:"done"`    // bytes moved so far
	Total   uint64 `json:"total"`   // bytes there were to move when it started
	Waiting string `json:"waiting"` // why it's paused, if it is
	Failed  string `json:"failed"`  // why it stopped, if it did
}

type move struct {
	status MoveStatus
	cancel context.CancelFunc
}

const movePause = 30 * time.Second

// A file written in the last minute may still be open, so it waits for the next pass.
// Replaced in tests, as is quickRename, which moves a file within one disk.
var (
	settleFile  = time.Minute
	quickRename = os.Rename
)

var errDifferentFile = errors.New("a different file with the same name is already there")

// folderCamera reads the camera from a recordings folder name, as retention does.
func folderCamera(name string) (int64, bool) {
	s, ok := strings.CutPrefix(name, "cam_")
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

type videoFile struct {
	path   string
	camera int64
	size   int64
	mod    time.Time
}

// videoFiles lists the recordings in a location. Nothing outside camera folders is touched.
func videoFiles(dir string) ([]videoFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []videoFile
	for _, e := range entries {
		id, ok := folderCamera(e.Name())
		if !ok || !e.IsDir() {
			continue
		}
		filepath.WalkDir(filepath.Join(dir, e.Name()), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				out = append(out, videoFile{p, id, info.Size(), info.ModTime()})
			}
			return nil
		})
	}
	return out, nil
}

// VideoBytes is the size of the recordings in a folder.
func VideoBytes(dir string) (uint64, error) {
	files, err := videoFiles(dir)
	var total uint64
	for _, f := range files {
		total += uint64(f.size)
	}
	return total, err
}

// moveFile moves one recording into a folder. It keeps the file's time, which retention
// goes by. A copy already there, from a move that stopped part way, finishes the job.
func moveFile(f videoFile, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	dst := filepath.Join(dir, filepath.Base(f.path))
	if info, err := os.Stat(dst); err == nil {
		if info.Size() != f.size {
			return errDifferentFile
		}
		return os.Remove(f.path)
	}
	if quickRename(f.path, dst) == nil {
		return nil // the same disk
	}
	in, err := os.Open(f.path)
	if err != nil {
		return err
	}
	defer in.Close()
	part := dst + ".part"
	out, err := os.Create(part)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chtimes(part, f.mod, f.mod)
	}
	if err == nil {
		err = os.Rename(part, dst)
	}
	if err != nil {
		os.Remove(part)
		return err
	}
	in.Close()
	return os.Remove(f.path)
}

// syncMoves starts the moves the database asks for, and stops the ones it no longer does.
// The caller holds no lock.
func (m *Manager) syncMoves(locs []store.Location) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.moves == nil {
		m.moves = map[int64]*move{}
	}
	want := map[int64]int64{}
	for _, l := range locs {
		if l.MovingTo != 0 {
			want[l.ID] = l.MovingTo
		}
	}
	for id, mv := range m.moves {
		if want[id] != mv.status.To && mv.cancel != nil {
			mv.cancel()
			mv.cancel = nil
			if mv.status.Failed == "" {
				delete(m.moves, id)
			}
		}
	}
	bg := m.bg
	if bg == nil {
		bg = context.Background()
	}
	for id, to := range want {
		if mv, ok := m.moves[id]; ok && mv.cancel != nil {
			continue
		}
		ctx, cancel := context.WithCancel(bg)
		mv := &move{status: MoveStatus{To: to}, cancel: cancel}
		m.moves[id] = mv
		go m.runMove(ctx, id, to, mv)
	}
}

func (m *Manager) setMove(mv *move, change func(*MoveStatus)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	change(&mv.status)
}

func (m *Manager) pause(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(m.movePause()):
		return true
	}
}

func (m *Manager) movePause() time.Duration {
	if m.MovePause > 0 {
		return m.MovePause
	}
	return movePause
}

func (m *Manager) runMove(ctx context.Context, srcID, dstID int64, mv *move) {
	first := true
	for ctx.Err() == nil {
		m.mu.Lock()
		src, srcOK := m.location(srcID)
		dst, dstOK := m.location(dstID)
		srcUp := srcOK && m.status[srcID] != nil && m.status[srcID].OK
		dstUp := dstOK && m.status[dstID] != nil && m.status[dstID].OK
		m.mu.Unlock()
		if !srcOK {
			return
		}
		if !dstOK {
			m.stopMove(ctx, src, mv, "The location it was moving to was removed.")
			return
		}
		if !srcUp || !dstUp {
			name := src.Name
			if srcUp {
				name = dst.Name
			}
			m.setMove(mv, func(s *MoveStatus) { s.Waiting = "Waiting for " + name + " to be available." })
			if !m.pause(ctx) {
				return
			}
			continue
		}
		m.setMove(mv, func(s *MoveStatus) { s.Waiting = "" })

		files, err := videoFiles(src.Path)
		if err != nil {
			m.setMove(mv, func(s *MoveStatus) { s.Waiting = "Seelynx can't read " + src.Name + ": " + plainError(err) })
			if !m.pause(ctx) {
				return
			}
			continue
		}
		if first {
			var total uint64
			for _, f := range files {
				total += uint64(f.size)
			}
			m.setMove(mv, func(s *MoveStatus) { s.Total = total })
			first = false
		}
		if len(files) == 0 {
			m.finishMove(ctx, src, dst, mv)
			return
		}
		moved, recent, failed := 0, 0, 0
		var lastErr error
		for _, f := range files {
			if ctx.Err() != nil {
				return
			}
			if time.Since(f.mod) < settleFile {
				recent++
				continue
			}
			err := moveFile(f, filepath.Join(dst.Path, media.ArchiveName(f.camera, dst.ID)))
			switch {
			case err == nil:
				moved++
				m.setMove(mv, func(s *MoveStatus) { s.Done += uint64(f.size) })
			case errors.Is(err, os.ErrNotExist):
				// Deleted by retention while it waited: nothing left to move.
			default:
				failed++
				lastErr = fmt.Errorf("%s: %s", filepath.Base(f.path), plainError(err))
			}
		}
		if moved == 0 && recent == 0 && failed > 0 {
			m.stopMove(ctx, src, mv, fmt.Sprintf("%d files couldn't be moved, such as %v. The rest were moved, and %s is kept for playback.", failed, lastErr, src.Name))
			return
		}
		if moved == 0 && !m.pause(ctx) {
			return
		}
	}
}

// stopMove gives up, leaving the location in place, taking no new video.
func (m *Manager) stopMove(ctx context.Context, src store.Location, mv *move, why string) {
	m.setMove(mv, func(s *MoveStatus) { s.Failed, s.Waiting = why, "" })
	m.mu.Lock()
	mv.cancel = nil
	m.mu.Unlock()
	if err := m.Store.SetMoving(ctx, src.ID, 0); err != nil {
		m.Log.Error("stop move", "err", err)
	}
	m.notify("warning", fmt.Sprintf("Moving video out of %s stopped. %s", src.Name, why))
}

// finishMove removes the emptied location. Empty camera folders go, and the marker file
// stays, so adding the folder again is still recognised.
func (m *Manager) finishMove(ctx context.Context, src, dst store.Location, mv *move) {
	if entries, err := os.ReadDir(src.Path); err == nil {
		for _, e := range entries {
			if _, ok := folderCamera(e.Name()); ok && e.IsDir() {
				os.Remove(filepath.Join(src.Path, e.Name())) // only if empty
			}
		}
	}
	m.mu.Lock()
	done := mv.status.Done
	mv.cancel = nil
	delete(m.moves, src.ID)
	m.mu.Unlock()
	if err := m.Store.DeleteLocation(ctx, src.ID); err != nil {
		m.Log.Error("remove moved location", "err", err)
		return
	}
	m.notify("info", fmt.Sprintf("Moved %.1f GB of video from %s to %s, and removed %s.", float64(done)/1e9, src.Name, dst.Name, src.Name))
	if err := m.Refresh(ctx); err != nil && ctx.Err() == nil {
		m.Log.Error("storage refresh", "err", err)
	}
}

// Move reports a location's move, if it has one.
func (m *Manager) Move(id int64) (MoveStatus, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	mv, ok := m.moves[id]
	if !ok {
		return MoveStatus{}, false
	}
	return mv.status, true
}

// ClearMove forgets a failed move once someone starts another or removes the location.
func (m *Manager) ClearMove(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if mv, ok := m.moves[id]; ok && mv.cancel == nil {
		delete(m.moves, id)
	}
}
