package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/retention"
	"seelynx/internal/storage"
	"seelynx/internal/store"
)

type locationView struct {
	ID            int64               `json:"id"`
	Name          string              `json:"name"`
	Path          string              `json:"path"`
	MinFreeGB     uint64              `json:"min_free_gb"`
	Recording     bool                `json:"recording"`
	OK            bool                `json:"ok"`
	Reason        string              `json:"reason"`
	Free          uint64              `json:"free"`
	Total         uint64              `json:"total"`
	RecordedBytes uint64              `json:"recorded_bytes"`
	Oldest        *time.Time          `json:"oldest"`
	Cameras       []string            `json:"cameras"`
	MovingTo      int64               `json:"moving_to"`
	Move          *storage.MoveStatus `json:"move"`
	Username      string              `json:"username"`
	HasPassword   bool                `json:"has_password"`
	Standby       bool                `json:"standby"`
	Return        *storage.MoveStatus `json:"return"`
}

// placement says where a camera records. Without a storage manager, as in tests and
// the probe tools, everything goes to the one recordings folder.
func (s *Server) placement(ctx context.Context, c store.Camera) media.Placement {
	if s.Storage == nil {
		return media.Placement{Record: s.RecordingDir}
	}
	return s.Storage.Placement(ctx, c)
}

func (s *Server) storageReport(ctx context.Context) ([]locationView, error) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(cams))
	for _, c := range cams {
		names[c.ID] = c.Name
	}
	out := []locationView{}
	for _, r := range s.Storage.Report(names) {
		v := locationView{ID: r.ID, Name: r.Name, Path: r.Path, MinFreeGB: r.MinFreeGB, Recording: r.Recording,
			OK: r.OK, Reason: r.Reason, Free: r.Free, Total: r.Total, Cameras: r.Cameras, MovingTo: r.MovingTo, Move: r.Move,
			Username: r.Username, HasPassword: r.Password != "", Standby: r.Standby, Return: r.Return}
		if s.Retention != nil {
			if u, ok := s.Retention.Usage(r.ID); ok {
				v.RecordedBytes = u.Bytes
				if !u.Oldest.IsZero() {
					v.Oldest = &u.Oldest
				}
			}
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Server) listStorage(w http.ResponseWriter, r *http.Request) {
	out, err := s.storageReport(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type locationInput struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	MinFreeGB uint64 `json:"min_free_gb"`
	Recording bool   `json:"recording"`
	// Username and Password sign in to a network share. A missing password keeps the
	// stored one.
	Username string  `json:"username"`
	Password *string `json:"password"`
	// Standby takes video only while no other location can.
	Standby bool `json:"standby"`
}

// saveStorage adds a location, or changes one. The folder is checked and marked before
// anything records into it.
func (s *Server) saveStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in locationInput
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Path = strings.TrimSpace(in.Path)
	if in.Path != "" && filepath.IsAbs(in.Path) {
		in.Path = filepath.Clean(in.Path)
	}
	switch {
	case in.Name == "" || len(in.Name) > 60:
		fail(w, http.StatusBadRequest, "Enter a name of up to 60 characters.")
		return
	case in.Path == "":
		fail(w, http.StatusBadRequest, "Enter the folder to keep video in.")
		return
	case in.MinFreeGB < 1 || in.MinFreeGB > 1000000:
		fail(w, http.StatusBadRequest, "Keep free must be 1 to 1,000,000 GB.")
		return
	}
	all, err := s.Store.Locations(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	in.Username = strings.TrimSpace(in.Username)
	if in.Username != "" && !storage.IsShare(in.Path) {
		fail(w, http.StatusBadRequest, `A username is only for a network share, such as \\nas\video. Clear it for a folder on this server.`)
		return
	}
	l := store.Location{Name: in.Name, Path: in.Path, MinFreeGB: in.MinFreeGB, Recording: in.Recording, Username: in.Username, Standby: in.Standby}
	var before store.Location
	if r.PathValue("id") != "" {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		if before, err = s.Store.Location(ctx, id); errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, "Storage location not found.")
			return
		} else if err != nil {
			s.internal(w, err)
			return
		}
		l.ID = id
		if before.MovingTo != 0 {
			fail(w, http.StatusConflict, "Video is being moved out of this location. Stop the move first.")
			return
		}
	}
	switch {
	case in.Password != nil:
		l.Password = *in.Password
	case l.Username != "" && l.Username == before.Username:
		l.Password = before.Password
	}
	if l.Username == "" {
		l.Password = ""
	}
	// Sign in first, so the folder can be checked. A changed login is tried even when the
	// path stays the same.
	loginChanged := l.Username != before.Username || l.Password != before.Password
	if l.Username != "" && (l.ID == 0 || l.Path != before.Path || loginChanged) {
		if err := storage.Connect(l.Path, l.Username, l.Password); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		if l.ID != 0 && l.Path == before.Path {
			if err := storage.Check(l.Path, l.ID, all); err != nil {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
		}
	}
	if l.Standby && !l.Recording {
		fail(w, http.StatusBadRequest, "A standby location has to be able to record. Tick Record new video here.")
		return
	}
	if (!l.Recording || l.Standby) && !s.othersRecord(all, l.ID) {
		fail(w, http.StatusBadRequest, "At least one location that isn't on standby has to take new video.")
		return
	}
	if l.ID == 0 || l.Path != before.Path {
		if err := storage.Check(l.Path, l.ID, all); err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	saved, err := s.Store.PutLocation(ctx, l)
	if errors.Is(err, store.ErrDuplicate) {
		fail(w, http.StatusConflict, "Another location already has this name or folder.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	if l.ID == 0 || l.Path != before.Path {
		if err := storage.Mark(saved.Path, saved.ID); err != nil {
			if l.ID == 0 {
				s.Store.DeleteLocation(ctx, saved.ID)
			} else {
				s.Store.PutLocation(ctx, before)
			}
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	user := currentUser(r).Username
	msg := fmt.Sprintf("Storage %s (%s) added by %s", saved.Name, saved.Path, user)
	if l.ID != 0 {
		msg = fmt.Sprintf("Storage %s changed by %s", saved.Name, user)
		if l.Path != before.Path {
			msg = fmt.Sprintf("Storage %s moved from %s to %s by %s", saved.Name, before.Path, saved.Path, user)
		}
	}
	s.afterStorageChange(ctx, msg)
	code := http.StatusOK
	if l.ID == 0 {
		code = http.StatusCreated
	}
	writeJSON(w, code, saved)
}

// othersRecord reports whether a location other than id takes new video.
func (s *Server) othersRecord(all []store.Location, id int64) bool {
	for _, o := range all {
		if o.ID != id && o.Recording && !o.Standby {
			return true
		}
	}
	return false
}

// deleteStorage stops using a location. Its files stay, so adding the same folder again
// brings its video back.
func (s *Server) deleteStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	all, err := s.Store.Locations(ctx)
	if err != nil {
		s.internal(w, err)
		return
	}
	l, err := s.Store.Location(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Storage location not found.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	for _, o := range all {
		if o.MovingTo == id {
			fail(w, http.StatusConflict, fmt.Sprintf("Video is being moved into this location from %s. Stop that move first.", o.Name))
			return
		}
	}
	if !s.othersRecord(all, id) {
		fail(w, http.StatusBadRequest, "This is the only location that takes new video. Add another one first.")
		return
	}
	if s.Storage != nil {
		s.Storage.ClearMove(id)
	}
	if err := s.Store.DeleteLocation(ctx, id); err != nil {
		s.internal(w, err)
		return
	}
	s.afterStorageChange(ctx, fmt.Sprintf("Storage %s (%s) removed by %s. Its files were kept.", l.Name, l.Path, currentUser(r).Username))
	w.WriteHeader(http.StatusNoContent)
}

// moveStorage starts moving a location's video to another, after which it's removed.
// The destination must have room for all of it above its Keep free, or its own cleanup
// would delete the oldest video, which could be what was just moved in.
func (s *Server) moveStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		To int64 `json:"to"`
	}
	if !decode(w, r, &in) {
		return
	}
	if s.Storage == nil {
		fail(w, http.StatusConflict, "Storage isn't managed on this server.")
		return
	}
	src, err := s.Store.Location(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Storage location not found.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	if src.MovingTo != 0 {
		fail(w, http.StatusConflict, "Its video is already being moved.")
		return
	}
	dst, err := s.Store.Location(ctx, in.To)
	if errors.Is(err, store.ErrNotFound) || in.To == id {
		fail(w, http.StatusBadRequest, "Choose another location to move the video to.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	if dst.Standby {
		fail(w, http.StatusBadRequest, dst.Name+" is on standby, and its video is moved away again. Choose another location.")
		return
	}
	if !dst.Recording || dst.MovingTo != 0 {
		fail(w, http.StatusBadRequest, dst.Name+" isn't taking new video. Choose a location that is.")
		return
	}
	var srcUp bool
	var dstFree uint64
	dstUp := false
	for _, rep := range s.Storage.Report(nil) {
		if rep.ID == src.ID {
			srcUp = rep.OK
		}
		if rep.ID == dst.ID {
			dstUp, dstFree = rep.OK, rep.Free
		}
	}
	if !srcUp {
		fail(w, http.StatusConflict, src.Name+" is unavailable, so its video can't be read. Connect it first.")
		return
	}
	if !dstUp {
		fail(w, http.StatusConflict, dst.Name+" is unavailable. Connect it first.")
		return
	}
	need, err := storage.VideoBytes(src.Path)
	if err != nil {
		fail(w, http.StatusConflict, "Seelynx can't read "+src.Name+".")
		return
	}
	var room uint64
	if keep := dst.MinFreeGB << 30; dstFree > keep {
		room = dstFree - keep
	}
	if need > room {
		fail(w, http.StatusBadRequest, fmt.Sprintf("%s holds %.1f GB of video, but %s has room for %.1f GB before it reaches its Keep free. Choose another location, or lower %s's Keep free.",
			src.Name, float64(need)/1e9, dst.Name, float64(room)/1e9, dst.Name))
		return
	}
	s.Storage.ClearMove(id)
	if err := s.Store.SetMoving(ctx, id, dst.ID); err != nil {
		s.internal(w, err)
		return
	}
	s.afterStorageChange(ctx, fmt.Sprintf("%s started moving %.1f GB of video from %s to %s. %s will be removed when it's empty.",
		currentUser(r).Username, float64(need)/1e9, src.Name, dst.Name, src.Name))
	w.WriteHeader(http.StatusAccepted)
}

// stopMoveStorage stops a move part way. The location stays, keeping what wasn't moved
// for playback, and takes no new video until someone turns that back on.
func (s *Server) stopMoveStorage(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	l, err := s.Store.Location(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Storage location not found.")
		return
	} else if err != nil {
		s.internal(w, err)
		return
	}
	if l.MovingTo == 0 {
		fail(w, http.StatusConflict, "Its video isn't being moved.")
		return
	}
	if err := s.Store.SetMoving(ctx, id, 0); err != nil {
		s.internal(w, err)
		return
	}
	s.afterStorageChange(ctx, fmt.Sprintf("%s stopped moving video out of %s. What's left there is kept for playback.", currentUser(r).Username, l.Name))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) afterStorageChange(ctx context.Context, msg string) {
	if err := s.Store.AddEvent(ctx, "info", msg); err != nil {
		s.Log.Error("event", "err", err)
	}
	if s.Storage != nil {
		if err := s.Storage.Refresh(ctx); err != nil {
			s.Log.Error("storage refresh", "err", err)
		}
	}
}

// diskTotals adds up the space of the locations that can be read now.
func (s *Server) diskTotals() (free, total uint64, err error) {
	if s.Storage == nil {
		return retention.DiskUsage(s.RecordingDir)
	}
	for _, r := range s.Storage.Report(nil) {
		if r.OK {
			free += r.Free
			total += r.Total
		}
	}
	return free, total, nil
}
