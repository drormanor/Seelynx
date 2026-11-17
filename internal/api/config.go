package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/schedule"
	"seelynx/internal/store"
)

type viewData struct {
	Layout int      `json:"layout"`
	Slots  []*int64 `json:"slots"`
}

type tourData struct {
	Dwell int     `json:"dwell"`
	Views []int64 `json:"views"`
}

type groupData struct {
	Parent int64 `json:"parent"`
}

type scheduleData struct {
	Periods []schedule.Period `json:"periods"`
}

// kinds lists the generic configuration collections. Writing needs the settings
// permission.
var kinds = map[string]struct {
	read     []string // any one of these permissions may list it
	validate func(*store.Item) string
}{
	"groups":    {[]string{""}, validateGroup},
	"views":     {[]string{""}, validateView},
	"tours":     {[]string{""}, validateTour},
	"schedules": {[]string{store.PermCameras, store.PermSettings}, validateSchedule},
	"access":    {[]string{store.PermSettings}, validateAccess},
	"rules":     {[]string{store.PermSettings}, validateRule},
	"drivers":   {[]string{store.PermSettings}, validateDriver},
}

func parse[T any](it *store.Item) (T, bool) {
	var v T
	if len(it.Data) == 0 {
		return v, false
	}
	return v, json.Unmarshal(it.Data, &v) == nil
}

func validateGroup(it *store.Item) string {
	g, ok := parse[groupData](it)
	if !ok {
		return "Invalid group."
	}
	if g.Parent != 0 && g.Parent == it.ID {
		return "A group can't be inside itself."
	}
	return ""
}

func validateView(it *store.Item) string {
	v, ok := parse[viewData](it)
	if !ok || (v.Layout != 1 && v.Layout != 4 && v.Layout != 9 && v.Layout != 16) || len(v.Slots) > v.Layout {
		return "Invalid view layout."
	}
	return ""
}

func validateTour(it *store.Item) string {
	t, ok := parse[tourData](it)
	if !ok || len(t.Views) == 0 {
		return "Choose at least one view."
	}
	if t.Dwell < 5 || t.Dwell > 3600 {
		return "Time per view must be 5 to 3600 seconds."
	}
	return ""
}

func validateSchedule(it *store.Item) string {
	v, ok := parse[scheduleData](it)
	if !ok {
		return "Invalid schedule."
	}
	if err := schedule.Validate(v.Periods); err != nil {
		return err.Error()
	}
	return ""
}

func parsePrefix(s string) (netip.Prefix, bool) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), true
	}
	if a, err := netip.ParseAddr(s); err == nil {
		return netip.PrefixFrom(a.Unmap(), a.Unmap().BitLen()), true
	}
	return netip.Prefix{}, false
}

// The entry's name is the address, so the shared Name field carries it.
func validateAccess(it *store.Item) string {
	if _, ok := parsePrefix(it.Name); !ok {
		return "Enter an IP address or network, such as 192.168.1.20 or 192.168.1.0/24."
	}
	return ""
}

func (s *Server) listItems(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	k, ok := kinds[kind]
	if !ok {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	u := currentUser(r)
	if !slices.ContainsFunc(k.read, u.Can) {
		fail(w, http.StatusForbidden, "Your account doesn't have permission to do this.")
		return
	}
	items, err := s.Store.Items(r.Context(), kind)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *Server) saveItem(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	k, ok := kinds[kind]
	if !ok {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	var it store.Item
	if !decode(w, r, &it) {
		return
	}
	it.ID = 0
	if raw := r.PathValue("id"); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			fail(w, http.StatusNotFound, "Not found.")
			return
		}
		it.ID = id
	}
	it.Name = strings.TrimSpace(it.Name)
	if it.Name == "" || len(it.Name) > 100 {
		fail(w, http.StatusBadRequest, "Enter a name of up to 100 characters.")
		return
	}
	if len(it.Data) == 0 {
		it.Data = json.RawMessage("{}")
	}
	if msg := k.validate(&it); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	code := http.StatusOK
	if it.ID == 0 {
		code = http.StatusCreated
	}
	saved, err := s.Store.PutItem(r.Context(), kind, it)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.afterItemChange(r.Context(), kind)
	writeJSON(w, code, saved)
}

func (s *Server) afterItemChange(ctx context.Context, kind string) {
	switch kind {
	case "access":
		if err := s.reloadAccess(ctx); err != nil {
			s.Log.Error("reload access list", "err", err)
		}
	case "schedules":
		s.protectMotionVideo(ctx)
		s.syncSchedules(ctx)
	}
}

// inUse explains why an item can't be deleted, or returns "".
func (s *Server) inUse(ctx context.Context, kind string, id int64) (string, error) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return "", err
	}
	for _, c := range cams {
		if kind == "groups" && c.GroupID == id {
			return "This group still has cameras in it.", nil
		}
		if kind == "schedules" && c.ScheduleID == id {
			return "A camera is still using this schedule.", nil
		}
	}
	switch kind {
	case "groups":
		items, err := s.Store.Items(ctx, "groups")
		if err != nil {
			return "", err
		}
		for i := range items {
			if g, ok := parse[groupData](&items[i]); ok && g.Parent == id {
				return "This group still has groups inside it.", nil
			}
		}
	case "views":
		items, err := s.Store.Items(ctx, "tours")
		if err != nil {
			return "", err
		}
		for i := range items {
			if t, ok := parse[tourData](&items[i]); ok {
				for _, v := range t.Views {
					if v == id {
						return "A tour is still using this view.", nil
					}
				}
			}
		}
	}
	return "", nil
}

func (s *Server) deleteItem(w http.ResponseWriter, r *http.Request) {
	kind := r.PathValue("kind")
	if _, ok := kinds[kind]; !ok {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	reason, err := s.inUse(r.Context(), kind, id)
	if err != nil {
		s.internal(w, err)
		return
	}
	if reason != "" {
		fail(w, http.StatusConflict, reason)
		return
	}
	err = s.Store.DeleteItem(r.Context(), kind, id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	s.afterItemChange(r.Context(), kind)
	w.WriteHeader(http.StatusNoContent)
}

// --- recording schedules ---

// plan returns how a camera records at any moment: whether it records, and whether
// only video near motion is kept. The schedule is read once, so the result can be
// asked about many moments.
func (s *Server) plan(ctx context.Context, c store.Camera) func(time.Time) (record, motion bool) {
	if !c.Record {
		return func(time.Time) (bool, bool) { return false, false }
	}
	if c.ScheduleID == 0 {
		return func(time.Time) (bool, bool) { return true, c.MotionOnly }
	}
	// Failing open: losing footage is worse than recording extra, so a schedule that
	// can't be read records everything.
	all := func(time.Time) (bool, bool) { return true, false }
	it, err := s.Store.Item(ctx, "schedules", c.ScheduleID)
	if err != nil {
		if _, seen := s.scheduleWarned.LoadOrStore(c.ID, true); !seen {
			s.Log.Warn("schedule unavailable, recording anyway", "camera", c.ID, "err", err)
		}
		return all
	}
	v, ok := parse[scheduleData](&it)
	if !ok {
		return all
	}
	return func(t time.Time) (bool, bool) {
		if schedule.Active(v.Periods, t) {
			return true, c.MotionOnly
		}
		switch c.OutsideMode {
		case store.OutsideAll:
			return true, false
		case store.OutsideMotion:
			return true, true
		}
		return false, false
	}
}

// Recording reports whether a camera should be recording now.
func (s *Server) Recording(ctx context.Context, c store.Camera) bool {
	record, _ := s.plan(ctx, c)(time.Now())
	return record
}

// MotionTimes says, for any moment, whether a camera keeps only video near motion then.
func (s *Server) MotionTimes(ctx context.Context, c store.Camera) func(time.Time) bool {
	p := s.plan(ctx, c)
	return func(t time.Time) bool {
		_, motion := p(t)
		return motion
	}
}

// protectMotionVideo stops video recorded so far from being judged by changed rules:
// video kept in full under the old ones stays.
func (s *Server) protectMotionVideo(ctx context.Context) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		s.Log.Error("cameras", "err", err)
		return
	}
	now := time.Now().Unix()
	for _, c := range cams {
		if c.ScheduleID != 0 && c.UsesMotion() {
			if err := s.Store.SetMotionSince(ctx, c.ID, now); err != nil {
				s.Log.Error("motion since", "camera", c.ID, "err", err)
			}
		}
	}
}

// ApplyCamera configures the media engine with the camera's current recording state.
func (s *Server) ApplyCamera(ctx context.Context, c store.Camera) error {
	want := s.Recording(ctx, c)
	c.Record = want
	apply := s.applyMedia
	if apply == nil {
		apply = func(ctx context.Context, c store.Camera) error { return s.Media.Apply(ctx, c, s.placement(ctx, c)) }
	}
	if err := apply(ctx, c); err != nil {
		return err
	}
	s.schedMu.Lock()
	defer s.schedMu.Unlock()
	if s.applied == nil {
		s.applied = map[int64]bool{}
	}
	s.applied[c.ID] = want
	return nil
}

func (s *Server) syncSchedules(ctx context.Context) {
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		s.Log.Error("schedule sync", "err", err)
		return
	}
	for _, c := range cams {
		if !c.Record || c.ScheduleID == 0 {
			continue
		}
		want := s.Recording(ctx, c)
		s.schedMu.Lock()
		have, known := s.applied[c.ID]
		s.schedMu.Unlock()
		if known && have == want {
			continue
		}
		if err := s.ApplyCamera(ctx, c); err != nil {
			s.Log.Error("apply schedule", "camera", c.ID, "err", err)
			continue
		}
		state := "stopped"
		if want {
			state = "started"
		}
		if err := s.Store.AddEvent(ctx, "info", c.Name+": scheduled recording "+state); err != nil {
			s.Log.Error("event", "err", err)
		}
	}
}

// RunSchedules checks recording schedules until ctx ends.
func (s *Server) RunSchedules(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.syncSchedules(ctx)
		}
	}
}

// --- client access list ---

func (s *Server) reloadAccess(ctx context.Context) error {
	items, err := s.Store.Items(ctx, "access")
	if err != nil {
		return err
	}
	list := make([]netip.Prefix, 0, len(items))
	for _, it := range items {
		if p, ok := parsePrefix(it.Name); ok {
			list = append(list, p)
		}
	}
	s.access.Store(&list)
	return nil
}

// clientAllowed applies the access list. An empty list allows everyone, and the
// server's own machine is always allowed so a bad entry can't lock out every admin.
func clientAllowed(list []netip.Prefix, remote string) bool {
	if len(list) == 0 {
		return true
	}
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	a, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	a = a.Unmap()
	if a.IsLoopback() {
		return true
	}
	for _, p := range list {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

func (s *Server) enforceAccess(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var list []netip.Prefix
		if p := s.access.Load(); p != nil {
			list = *p
		}
		if !clientAllowed(list, r.RemoteAddr) {
			fail(w, http.StatusForbidden, "This computer isn't on the access list.")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// numericTaken reports whether another camera already uses c's numeric ID.
func (s *Server) numericTaken(ctx context.Context, c store.Camera) (bool, error) {
	if c.NumericID == 0 {
		return false, nil
	}
	cams, err := s.Store.Cameras(ctx)
	if err != nil {
		return false, err
	}
	for _, o := range cams {
		if o.ID != c.ID && o.NumericID == c.NumericID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) numericFree(w http.ResponseWriter, r *http.Request, c store.Camera) bool {
	for _, ref := range []struct {
		kind string
		id   int64
		msg  string
	}{
		{"schedules", c.ScheduleID, "That recording schedule no longer exists. Choose another."},
		{"groups", c.GroupID, "That group no longer exists. Choose another."},
	} {
		if ref.id == 0 {
			continue
		}
		_, err := s.Store.Item(r.Context(), ref.kind, ref.id)
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusBadRequest, ref.msg)
			return false
		}
		if err != nil {
			s.internal(w, err)
			return false
		}
	}
	taken, err := s.numericTaken(r.Context(), c)
	if err != nil {
		s.internal(w, err)
		return false
	}
	if taken {
		fail(w, http.StatusConflict, "Another camera already uses this numeric ID.")
		return false
	}
	return true
}
