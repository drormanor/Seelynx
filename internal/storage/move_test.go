package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"seelynx/internal/media"
)

func (e *env) waitFor(what string, done func() bool) {
	e.t.Helper()
	for range 500 {
		if done() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	e.t.Fatalf("timed out waiting for %s; messages: %v", what, e.messages)
}

func writeVideo(t *testing.T, path string, age time.Duration) time.Time {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o750)
	if err := os.WriteFile(path, []byte("video "+path), 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age).Truncate(time.Second)
	os.Chtimes(path, when, when)
	return when
}

func TestVideoIsMovedAndTheEmptyLocationRemoved(t *testing.T) {
	for _, copying := range []bool{false, true} {
		t.Run(map[bool]string{false: "same disk", true: "another disk"}[copying], func(t *testing.T) {
			if copying {
				saved := quickRename
				quickRename = func(string, string) error { return errors.New("not the same disk") }
				t.Cleanup(func() { quickRename = saved })
			}
			savedSettle := settleFile
			settleFile = 200 * time.Millisecond
			t.Cleanup(func() { settleFile = savedSettle })

			e, locs := newEnv(t, 1000, 1000)
			a, b := locs[0], locs[1]
			e.m.MovePause = 20 * time.Millisecond
			cams := e.cameras(2)
			e.refresh()

			live := filepath.Join(a.Path, media.PathName(cams[0].ID, "main"), "2026-11-01_10-00-00-000000.mp4")
			older := filepath.Join(a.Path, media.ArchiveName(cams[1].ID, a.ID), "2026-10-30_09-00-00-000000.mp4")
			fresh := filepath.Join(a.Path, media.PathName(cams[1].ID, "main"), "2026-11-09_10-00-00-000000.mp4")
			when := writeVideo(t, live, 48*time.Hour)
			writeVideo(t, older, 72*time.Hour)
			writeVideo(t, fresh, 0) // still being written: waits, then goes too
			notOurs := filepath.Join(a.Path, "notes.txt")
			os.WriteFile(notOurs, []byte("x"), 0o600)

			if err := e.st.SetMoving(e.ctx, a.ID, b.ID); err != nil {
				t.Fatal(err)
			}
			e.refresh()
			if got := e.where(); got[b.ID] != 2 {
				t.Errorf("cameras should leave a location being emptied: %v", got)
			}
			if e.said("again") || !e.said("1 camera is recording to other storage now") {
				t.Errorf("leaving a location being emptied isn't a return: %v", e.messages)
			}
			e.waitFor("the location to be removed", func() bool {
				_, err := e.st.Location(e.ctx, a.ID)
				return err != nil
			})

			for _, p := range []string{
				filepath.Join(b.Path, media.ArchiveName(cams[0].ID, b.ID), filepath.Base(live)),
				filepath.Join(b.Path, media.ArchiveName(cams[1].ID, b.ID), filepath.Base(older)),
				filepath.Join(b.Path, media.ArchiveName(cams[1].ID, b.ID), filepath.Base(fresh)),
			} {
				if _, err := os.Stat(p); err != nil {
					t.Errorf("not moved: %v", err)
				}
			}
			info, err := os.Stat(filepath.Join(b.Path, media.ArchiveName(cams[0].ID, b.ID), filepath.Base(live)))
			if err == nil && !info.ModTime().Equal(when) {
				t.Errorf("the file's time changed to %v, and retention goes by it", info.ModTime())
			}
			for _, p := range []string{live, older, fresh} {
				if _, err := os.Stat(p); err == nil {
					t.Errorf("left behind: %s", p)
				}
			}
			if _, err := os.Stat(notOurs); err != nil {
				t.Error("a file that isn't a recording was touched")
			}
			if _, err := os.Stat(filepath.Join(a.Path, MarkerFile)); err != nil {
				t.Error("the marker should stay, so the folder is recognised if added again")
			}
			if !e.said("Moved 0.0 GB of video from A to B, and removed A.") {
				t.Errorf("messages: %v", e.messages)
			}
			parts, _ := filepath.Glob(filepath.Join(b.Path, "*", "*.part"))
			if len(parts) != 0 {
				t.Errorf("half-copied files left: %v", parts)
			}
		})
	}
}

func TestAMoveCanBeStoppedAndWaitsForItsLocations(t *testing.T) {
	e, locs := newEnv(t, 1000, 1000)
	a, b := locs[0], locs[1]
	e.m.MovePause = 20 * time.Millisecond
	cams := e.cameras(1)
	e.refresh()
	file := filepath.Join(a.Path, media.PathName(cams[0].ID, "main"), "2026-11-01_10-00-00-000000.mp4")
	writeVideo(t, file, time.Hour)

	// The destination is offline, so the move waits.
	os.Remove(filepath.Join(b.Path, MarkerFile))
	e.st.SetMoving(e.ctx, a.ID, b.ID)
	e.refresh()
	e.waitFor("the move to wait", func() bool {
		mv, ok := e.m.Move(a.ID)
		return ok && mv.Waiting == "Waiting for B to be available."
	})
	if _, err := os.Stat(file); err != nil {
		t.Fatal("moved into a location that isn't available")
	}

	// Stopped before it could go on: the location and its video stay.
	e.st.SetMoving(e.ctx, a.ID, 0)
	Mark(b.Path, b.ID)
	e.refresh()
	time.Sleep(100 * time.Millisecond)
	if _, ok := e.m.Move(a.ID); ok {
		t.Error("a stopped move is still reported")
	}
	l, err := e.st.Location(e.ctx, a.ID)
	if err != nil || l.Recording || l.MovingTo != 0 {
		t.Errorf("after stopping: %+v %v", l, err)
	}
	if files, _ := videoFiles(a.Path); len(files) != 1 {
		t.Error("a stopped move went on")
	}
}

func TestAMoveThatCantFinishStopsAndSaysWhy(t *testing.T) {
	e, locs := newEnv(t, 1000, 1000)
	a, b := locs[0], locs[1]
	e.m.MovePause = 20 * time.Millisecond
	cams := e.cameras(1)
	e.refresh()
	name := "2026-11-01_10-00-00-000000.mp4"
	file := filepath.Join(a.Path, media.PathName(cams[0].ID, "main"), name)
	writeVideo(t, file, time.Hour)
	clash := filepath.Join(b.Path, media.ArchiveName(cams[0].ID, b.ID), name)
	os.MkdirAll(filepath.Dir(clash), 0o750)
	os.WriteFile(clash, []byte("something else entirely"), 0o600)

	e.st.SetMoving(e.ctx, a.ID, b.ID)
	e.refresh()
	e.waitFor("the move to give up", func() bool {
		mv, ok := e.m.Move(a.ID)
		return ok && mv.Failed != ""
	})
	if l, err := e.st.Location(e.ctx, a.ID); err != nil || l.MovingTo != 0 {
		t.Errorf("the location should stay, no longer moving: %+v %v", l, err)
	}
	if files, _ := videoFiles(a.Path); len(files) != 1 {
		t.Error("the file that couldn't be moved was lost")
	}
	if b, _ := os.ReadFile(clash); string(b) != "something else entirely" {
		t.Error("a different file in the destination was overwritten")
	}
	if !e.said("Moving video out of A stopped. 1 files couldn't be moved") {
		t.Errorf("messages: %v", e.messages)
	}
}
