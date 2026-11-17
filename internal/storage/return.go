package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

// A standby location takes video only while no other location can, such as a local disk
// for when the network storage is down. Once nothing records to it, its video is moved
// to each camera's own location, the same careful way as a move before removing a
// location, and it stays, empty and ready for next time.

// syncReturns keeps one worker for each standby location. The caller holds no lock.
func (m *Manager) syncReturns(locs []store.Location) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.returns == nil {
		m.returns = map[int64]*move{}
	}
	want := map[int64]bool{}
	for _, l := range locs {
		if l.Standby && l.MovingTo == 0 {
			want[l.ID] = true
		}
	}
	for id, rt := range m.returns {
		if !want[id] {
			if rt.cancel != nil {
				rt.cancel()
			}
			delete(m.returns, id)
		}
	}
	bg := m.bg
	if bg == nil {
		bg = context.Background()
	}
	for id := range want {
		if _, ok := m.returns[id]; ok {
			continue
		}
		ctx, cancel := context.WithCancel(bg)
		rt := &move{cancel: cancel}
		m.returns[id] = rt
		go m.runReturn(ctx, id, rt)
	}
}

// returnTarget is where a camera's video on standby goes: its own location if that works,
// or else the regular location with the most room. The caller holds mu.
func (m *Manager) returnTarget(camera int64, homes map[int64]int64) (store.Location, bool) {
	if home, ok := m.location(homes[camera]); ok && m.canBeHome(home.ID) && m.usable(home.ID) {
		return home, true
	}
	var pick store.Location
	room := -1.0
	for _, l := range m.locs {
		if m.canBeHome(l.ID) && m.usable(l.ID) {
			if c := capacity(l, m.status[l.ID]); c > room {
				pick, room = l, c
			}
		}
	}
	return pick, room >= 0
}

func (m *Manager) runReturn(ctx context.Context, srcID int64, rt *move) {
	for ctx.Err() == nil {
		cams, err := m.Store.Cameras(ctx)
		if err != nil {
			if !m.pause(ctx) {
				return
			}
			continue
		}
		homes := map[int64]int64{}
		for _, c := range cams {
			homes[c.ID] = c.StorageID
		}
		m.mu.Lock()
		src, ok := m.location(srcID)
		up := ok && m.status[srcID] != nil && m.status[srcID].OK
		busy := false
		for _, loc := range m.current {
			busy = busy || loc == srcID
		}
		started := rt.status.Total > 0
		m.mu.Unlock()
		if !ok {
			return
		}
		if !up || busy {
			// Video is still arriving, or the folder can't be read: try again later. A
			// return part way through says why it's paused.
			if busy && started {
				m.setMove(rt, func(s *MoveStatus) { s.Waiting = "Cameras are recording here again, so moving the video back waits." })
			}
			if !m.pause(ctx) {
				return
			}
			continue
		}
		files, err := videoFiles(src.Path)
		if err != nil || len(files) == 0 {
			m.finishReturn(src, rt)
			if !m.pause(ctx) {
				return
			}
			continue
		}
		if !started {
			var total uint64
			for _, f := range files {
				total += uint64(f.size)
			}
			m.setMove(rt, func(s *MoveStatus) { s.Total = total })
		}
		moved, recent, waiting := 0, 0, 0
		var lastErr error
		for _, f := range files {
			if ctx.Err() != nil {
				return
			}
			if fileIsRecent(f) {
				recent++
				continue
			}
			m.mu.Lock()
			dst, ok := m.returnTarget(f.camera, homes)
			m.mu.Unlock()
			if !ok {
				waiting++
				continue
			}
			err := moveFile(f, filepath.Join(dst.Path, media.ArchiveName(f.camera, dst.ID)))
			switch {
			case err == nil:
				moved++
				m.setMove(rt, func(s *MoveStatus) { s.Done += uint64(f.size); s.To = dst.ID })
			case errors.Is(err, os.ErrNotExist):
				// Deleted by retention meanwhile.
			default:
				lastErr = fmt.Errorf("%s: %s", filepath.Base(f.path), plainError(err))
			}
		}
		switch {
		case waiting > 0 && moved == 0:
			m.setMove(rt, func(s *MoveStatus) { s.Waiting = "Waiting for the cameras' own storage to be available." })
		case lastErr != nil && moved == 0 && recent == 0:
			// Kept here, still playable, and tried again later.
			m.setMove(rt, func(s *MoveStatus) { s.Waiting = fmt.Sprintf("Some files can't be moved yet, such as %v.", lastErr) })
		default:
			m.setMove(rt, func(s *MoveStatus) { s.Waiting = "" })
		}
		if moved == 0 && !m.pause(ctx) {
			return
		}
	}
}

// fileIsRecent reports whether a file may still be open for writing.
func fileIsRecent(f videoFile) bool {
	return time.Since(f.mod) < settleFile
}

// finishReturn reports a finished return once, and makes the location ready again.
func (m *Manager) finishReturn(src store.Location, rt *move) {
	m.mu.Lock()
	done, to := rt.status.Done, rt.status.To
	rt.status = MoveStatus{}
	dst, _ := m.location(to)
	m.mu.Unlock()
	if done == 0 {
		return
	}
	if entries, err := os.ReadDir(src.Path); err == nil {
		for _, e := range entries {
			if _, ok := folderCamera(e.Name()); ok && e.IsDir() {
				os.Remove(filepath.Join(src.Path, e.Name())) // only if empty
			}
		}
	}
	where := "the cameras' own storage"
	if dst.Name != "" {
		where = dst.Name
	}
	m.notify("info", fmt.Sprintf("Moved %.1f GB of video recorded on %s back to %s. %s is ready for next time.", float64(done)/1e9, src.Name, where, src.Name))
}
