// Package storage decides where each camera records when there's more than one storage
// location. Cameras are spread over the locations that take new video, in proportion to
// their size. When a location stops working its cameras record somewhere else, and they
// go back once it has worked for a while. Video in every working location stays
// available for playback.
package storage

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/retention"
	"seelynx/internal/store"
)

// MarkerFile marks a folder as a storage location. A drive or network share that isn't
// connected often leaves an empty folder behind, and recording into that would fill the
// wrong disk, so a location without its marker isn't used.
const MarkerFile = ".seelynx-storage"

const (
	defaultSettle  = 2 * time.Minute
	defaultTimeout = 10 * time.Second
	// fullBelow is free space too small to record into, whatever the location's target.
	fullBelow = 1 << 30
)

// Status is what the last check found.
type Status struct {
	OK          bool
	Reason      string // why it isn't OK
	Free, Total uint64
	Since       time.Time // when OK last changed
	checked     bool
}

type Manager struct {
	Store *store.Store
	Log   *slog.Logger
	// Apply sets up a camera again after it moves. It calls Placement.
	Apply func(context.Context, store.Camera) error
	// Notify reports a change to people, in the event log and to alarm rules. May be nil.
	Notify func(level, message string)
	// Settle is how long a location must work before its cameras return to it.
	Settle time.Duration
	// Timeout is how long a check may take. A network share that's gone can hang.
	Timeout time.Duration

	refresh  sync.Mutex // one refresh at a time
	mu       sync.Mutex
	locs     []store.Location
	status   map[int64]*Status
	checking map[int64]bool
	current  map[int64]int64 // camera -> the location it records to now
	stranded bool            // no location can take video; warned once
	bg       context.Context // what moves run in; set by Run
	moves    map[int64]*move // by the location being emptied
	returns  map[int64]*move // by standby location, moving its video back
	// MovePause is how long a move waits before trying again. Tests shorten it.
	MovePause time.Duration
}

// diskUsage is replaced in tests.
var diskUsage = retention.DiskUsage

func (m *Manager) settle() time.Duration {
	if m.Settle > 0 {
		return m.Settle
	}
	return defaultSettle
}

func (m *Manager) timeout() time.Duration {
	if m.Timeout > 0 {
		return m.Timeout
	}
	return defaultTimeout
}

func (m *Manager) notify(level, msg string) {
	if level == "warning" {
		m.Log.Warn(msg)
	} else {
		m.Log.Info(msg)
	}
	if m.Notify != nil {
		m.Notify(level, msg)
	}
}

func marker(id int64) string { return fmt.Sprintf("Seelynx storage location %d\n", id) }

// markerOwner reads which location a folder was set up for. It's 0 when there's no marker.
func markerOwner(dir string) (int64, error) {
	b, err := os.ReadFile(filepath.Join(dir, MarkerFile))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	s, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "Seelynx storage location ")
	if !ok {
		return 0, nil
	}
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, nil
	}
	return id, nil
}

func samePath(a, b string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}

// within reports whether path is dir or inside it.
func within(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if samePath(path, dir) {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(dir, sep) {
		dir += sep
	}
	return len(path) > len(dir) && samePath(path[:len(dir)], dir)
}

// Check says whether a folder can become location id, or stay one after its path
// changes. It makes the folder if needed. The answer is meant for people.
func Check(path string, id int64, all []store.Location) error {
	if !filepath.IsAbs(path) {
		return errors.New("Enter a full folder path, such as D:\\Video or /mnt/nas/video.")
	}
	for _, o := range all {
		if o.ID == id {
			continue
		}
		if within(path, o.Path) || within(o.Path, path) {
			return fmt.Errorf("This folder overlaps %s (%s). Choose a folder that's separate from other locations.", o.Name, o.Path)
		}
	}
	if err := os.MkdirAll(path, 0o750); err != nil {
		return fmt.Errorf("Seelynx can't make this folder: %s", plainError(err))
	}
	owner, err := markerOwner(path)
	if err != nil {
		return fmt.Errorf("Seelynx can't read this folder: %s", plainError(err))
	}
	for _, o := range all {
		if owner != 0 && owner != id && o.ID == owner {
			return fmt.Errorf("This folder is already %s, under another path. A drive or share can have more than one path.", o.Name)
		}
	}
	return probe(path)
}

// Mark writes the marker that ties a folder to location id.
func Mark(path string, id int64) error {
	if err := os.WriteFile(filepath.Join(path, MarkerFile), []byte(marker(id)), 0o640); err != nil {
		return fmt.Errorf("Seelynx can't write to this folder: %s", plainError(err))
	}
	return nil
}

func probe(dir string) error {
	p := filepath.Join(dir, ".seelynx-probe")
	if err := os.WriteFile(p, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("Seelynx can't write to this folder: %s", plainError(err))
	}
	os.Remove(p)
	return nil
}

// plainError drops the path from a file error, since people already know which folder.
func plainError(err error) string {
	var pe *os.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	msg := err.Error()
	if errors.Is(err, os.ErrPermission) {
		msg = "access is denied. Give the account Seelynx runs as permission to change this folder"
	}
	return msg
}

// inspect checks one location. It can hang on a network share that's gone, so it's only
// called with a time limit.
var inspect = inspectFolder // replaced in tests

func inspectFolder(l store.Location) Status {
	info, err := os.Stat(l.Path)
	if err != nil && l.Username != "" && IsShare(l.Path) {
		// Signed out, such as after Seelynx restarted or the NAS did: sign in again.
		if cerr := connectIfShare(l.Path, l.Username, l.Password); cerr != nil {
			return Status{Reason: cerr.Error()}
		}
		info, err = os.Stat(l.Path)
	}
	if err != nil || !info.IsDir() {
		return Status{Reason: "The folder isn't there. If it's on another drive or a network share, check that it's connected."}
	}
	owner, err := markerOwner(l.Path)
	switch {
	case err != nil:
		return Status{Reason: "Seelynx can't read the folder: " + plainError(err)}
	case owner == 0:
		return Status{Reason: "The folder is there but it isn't the one that was set up: its " + MarkerFile + " file is missing. If it's on another drive or a network share, check that it's connected."}
	case owner != l.ID:
		return Status{Reason: "The folder belongs to another storage location."}
	}
	if err := probe(l.Path); err != nil {
		return Status{Reason: err.Error()}
	}
	free, total, err := diskUsage(l.Path)
	if err != nil {
		return Status{Reason: "Seelynx can't read the free space: " + plainError(err)}
	}
	if free < fullBelow {
		return Status{Reason: "The disk is full.", Free: free, Total: total}
	}
	return Status{OK: true, Free: free, Total: total}
}

// checkAll checks every location at once. One that doesn't answer in time counts as
// unavailable, and isn't checked again until its last check returns.
func (m *Manager) checkAll(locs []store.Location) map[int64]Status {
	type result struct {
		id int64
		st Status
	}
	done := make(chan result, len(locs))
	waiting := map[int64]bool{}
	m.mu.Lock()
	if m.checking == nil {
		m.checking = map[int64]bool{}
	}
	for _, l := range locs {
		if m.checking[l.ID] {
			continue
		}
		m.checking[l.ID] = true
		waiting[l.ID] = true
		go func() {
			st := inspect(l)
			m.mu.Lock()
			delete(m.checking, l.ID)
			m.mu.Unlock()
			done <- result{l.ID, st}
		}()
	}
	m.mu.Unlock()

	out := map[int64]Status{}
	deadline := time.After(m.timeout())
	for len(waiting) > 0 {
		select {
		case r := <-done:
			out[r.id] = r.st
			delete(waiting, r.id)
		case <-deadline:
			waiting = nil
		}
	}
	for _, l := range locs {
		if _, ok := out[l.ID]; !ok {
			out[l.ID] = Status{Reason: "The folder isn't answering. If it's on a network share, the share may be offline."}
		}
	}
	return out
}

// capacity is the room a location has for video.
func capacity(l store.Location, st *Status) float64 {
	room := float64(1 << 30)
	if st != nil && st.Total > l.MinFreeGB<<30 {
		room = max(room, float64(st.Total-l.MinFreeGB<<30))
	}
	return room
}

// best picks the location among candidates where one more camera adds the least load
// for its size. Ties go to the oldest location.
func (m *Manager) best(candidates []store.Location, count map[int64]int) int64 {
	var pick int64
	score := 0.0
	for _, l := range candidates {
		s := float64(count[l.ID]+1) / capacity(l, m.status[l.ID])
		if pick == 0 || s < score {
			pick, score = l.ID, s
		}
	}
	return pick
}

// usable reports whether a location can take video now. The caller holds mu.
func (m *Manager) usable(id int64) bool {
	for _, l := range m.locs {
		if l.ID == id {
			st := m.status[id]
			return l.Recording && st != nil && st.OK
		}
	}
	return false
}

func (m *Manager) location(id int64) (store.Location, bool) {
	for _, l := range m.locs {
		if l.ID == id {
			return l, true
		}
	}
	return store.Location{}, false
}

// Refresh checks every location, moves cameras that need to move, and tidies the video
// they left behind. It runs on a timer and after any change to the locations.
func (m *Manager) Refresh(ctx context.Context) error {
	m.refresh.Lock()
	defer m.refresh.Unlock()
	locs, err := m.Store.Locations(ctx)
	if err != nil {
		return err
	}
	checked := m.checkAll(locs)
	cams, err := m.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	now := time.Now()

	m.mu.Lock()
	if m.status == nil {
		m.status = map[int64]*Status{}
		m.current = map[int64]int64{}
	}
	m.locs = locs
	var messages [][2]string
	readableBefore := m.readableSet()
	for _, l := range locs {
		st := checked[l.ID]
		prev := m.status[l.ID]
		st.checked = true
		st.Since = now
		switch {
		case prev == nil || !prev.checked:
			if !st.OK {
				messages = append(messages, [2]string{"warning", fmt.Sprintf("Storage %s is unavailable. %s", l.Name, st.Reason)})
			}
		case prev.OK != st.OK:
			if st.OK {
				messages = append(messages, [2]string{"info", fmt.Sprintf("Storage %s is available again.", l.Name)})
			} else {
				messages = append(messages, [2]string{"warning", fmt.Sprintf("Storage %s is unavailable. %s", l.Name, st.Reason)})
			}
		default:
			st.Since = prev.Since
		}
		m.status[l.ID] = &st
	}
	for id := range m.status {
		if _, ok := m.location(id); !ok {
			delete(m.status, id)
		}
	}
	archivesChanged := readableBefore != m.readableSet()

	// Every camera has a home: the location it records to while that location works. A
	// standby location is never a home. It takes video only while no other location can.
	var recording, working, spare []store.Location
	for _, l := range locs {
		switch {
		case !l.Recording:
		case l.Standby:
			if m.status[l.ID].OK {
				spare = append(spare, l)
			}
		default:
			recording = append(recording, l)
			if m.status[l.ID].OK {
				working = append(working, l)
			}
		}
	}
	homes := map[int64]int{}
	for _, c := range cams {
		if m.canBeHome(c.StorageID) {
			homes[c.StorageID]++
		}
	}
	type rehome struct{ cam, loc int64 }
	var rehomed []rehome
	newHome := map[int64]bool{} // cameras given a new home now, which isn't a return
	for i, c := range cams {
		if m.canBeHome(c.StorageID) {
			continue
		}
		pick := m.best(working, homes)
		if pick == 0 {
			pick = m.best(recording, homes)
		}
		if pick == 0 || pick == c.StorageID {
			continue
		}
		homes[pick]++
		cams[i].StorageID = pick
		rehomed = append(rehomed, rehome{c.ID, pick})
		newHome[c.ID] = true
	}

	// Each camera records at home when it can. One that had to leave goes back once home
	// has worked for a while, so a share that comes and goes doesn't move it back and forth.
	load := map[int64]int{}
	for _, c := range cams {
		if cur, ok := m.current[c.ID]; ok && m.usable(cur) {
			load[cur]++
		}
	}
	var settled []store.Location // regular locations that have worked for a while
	for _, l := range working {
		if now.Sub(m.status[l.ID].Since) >= m.settle() {
			settled = append(settled, l)
		}
	}
	var moved []store.Camera
	left := map[int64][]string{} // location -> cameras that left it
	back := map[int64][]string{}
	known := map[int64]bool{}
	for _, c := range cams {
		known[c.ID] = true
		prev, had := m.current[c.ID]
		next := prev
		home := c.StorageID
		switch {
		case m.usable(home) && (!had || prev == home || !m.usable(prev) || now.Sub(m.status[home].Since) >= m.settle()):
			next = home
		case had && m.usable(prev) && !(m.standby(prev) && len(settled) > 0):
			// stay, unless it's on standby and a regular location has worked for a while,
			// so a share that comes and goes doesn't move cameras back and forth
		case had && m.usable(prev):
			if pick := m.best(settled, load); pick != 0 {
				next = pick
			}
		default:
			if pick := m.best(working, load); pick != 0 {
				next = pick
			} else if pick := m.best(spare, load); pick != 0 {
				next = pick
			} else if !had {
				next = home
			}
		}
		if had && m.usable(prev) {
			load[prev]--
		}
		if m.usable(next) {
			load[next]++
		}
		if had && next == prev {
			continue
		}
		m.current[c.ID] = next
		if had {
			moved = append(moved, c)
			if next == home && !newHome[c.ID] {
				back[home] = append(back[home], c.Name)
			} else {
				left[prev] = append(left[prev], c.Name)
			}
		}
	}
	for id := range m.current {
		if !known[id] {
			delete(m.current, id)
		}
	}
	for id, names := range left {
		l, ok := m.location(id)
		if st := m.status[id]; ok && st != nil && !st.OK {
			messages = append(messages, [2]string{"warning", fmt.Sprintf("%s recording to other storage while %s is unavailable: %s.", cameras(len(names)), l.Name, strings.Join(names, ", "))})
		} else {
			messages = append(messages, [2]string{"info", fmt.Sprintf("%s recording to other storage now: %s.", cameras(len(names)), strings.Join(names, ", "))})
		}
	}
	for id, names := range back {
		l, _ := m.location(id)
		messages = append(messages, [2]string{"info", fmt.Sprintf("%s recording to %s again: %s.", cameras(len(names)), l.Name, strings.Join(names, ", "))})
	}
	stranded := len(working) == 0 && len(spare) == 0 && len(cams) > 0
	if stranded && !m.stranded {
		messages = append(messages, [2]string{"warning", "No storage location can take video, so nothing is being recorded. Check the Storage screen."})
	}
	m.stranded = stranded
	m.mu.Unlock()

	for _, r := range rehomed {
		if err := m.Store.SetCameraStorage(ctx, r.cam, r.loc); err != nil {
			return err
		}
	}
	for _, msg := range messages {
		m.notify(msg[0], msg[1])
	}
	if m.Apply != nil {
		if archivesChanged {
			moved = cams // every camera's older video moved in or out of reach
		}
		for _, c := range moved {
			if err := m.Apply(ctx, c); err != nil && ctx.Err() == nil {
				m.Log.Error("move camera", "camera", c.ID, "err", err)
			}
		}
	}
	m.tidy(cams)
	m.syncMoves(locs)
	m.syncReturns(locs)
	return nil
}

// canBeHome reports whether a camera can record to a location as its own. The caller
// holds mu.
func (m *Manager) canBeHome(id int64) bool {
	l, ok := m.location(id)
	return ok && l.Recording && !l.Standby
}

// standby reports whether a location is a standby one. The caller holds mu.
func (m *Manager) standby(id int64) bool {
	l, ok := m.location(id)
	return ok && l.Standby
}

func cameras(n int) string {
	if n == 1 {
		return "1 camera is"
	}
	return fmt.Sprintf("%d cameras are", n)
}

// readableSet names the locations whose video can be read. The caller holds mu.
func (m *Manager) readableSet() string {
	var ids []string
	for _, l := range m.locs {
		if st := m.status[l.ID]; st != nil && st.OK {
			ids = append(ids, fmt.Sprintf("%d=%s", l.ID, l.Path))
		}
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// tidy renames the folders cameras left in locations they no longer record to, so each
// location's video can be played back on its own. A file that's still open can't be moved
// on Windows, so anything left is tried again next time.
func (m *Manager) tidy(cams []store.Camera) {
	m.mu.Lock()
	type job struct {
		from, to string
	}
	var jobs []job
	for _, l := range m.locs {
		if st := m.status[l.ID]; st == nil || !st.OK {
			continue
		}
		for _, c := range cams {
			if m.current[c.ID] == l.ID {
				continue
			}
			jobs = append(jobs, job{filepath.Join(l.Path, media.PathName(c.ID, "main")), filepath.Join(l.Path, media.ArchiveName(c.ID, l.ID))})
		}
	}
	m.mu.Unlock()
	for _, j := range jobs {
		entries, err := os.ReadDir(j.from)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(j.to, 0o750); err != nil {
			m.Log.Warn("tidy storage", "err", err)
			continue
		}
		for _, e := range entries {
			if err := os.Rename(filepath.Join(j.from, e.Name()), filepath.Join(j.to, e.Name())); err != nil {
				m.Log.Debug("tidy storage: will try again", "file", e.Name(), "err", err)
			}
		}
		os.Remove(j.from) // only succeeds once it's empty
	}
}

// Placement says where a camera records now and where its older video is.
func (m *Manager) Placement(ctx context.Context, c store.Camera) media.Placement {
	m.mu.Lock()
	if m.current == nil {
		m.current = map[int64]int64{}
	}
	cur, had := m.current[c.ID]
	var home int64
	if !had {
		// A camera added since the last refresh: give it a home now.
		cur = c.StorageID
		if !m.canBeHome(cur) || !m.usable(cur) {
			count := map[int64]int{}
			for _, id := range m.current {
				count[id]++
			}
			var working, spare []store.Location
			for _, l := range m.locs {
				if m.usable(l.ID) {
					if l.Standby {
						spare = append(spare, l)
					} else {
						working = append(working, l)
					}
				}
			}
			if pick := m.best(working, count); pick != 0 {
				cur = pick
			} else if pick := m.best(spare, count); pick != 0 {
				cur = pick
			}
		}
		if cur != 0 {
			m.current[c.ID] = cur
			if !m.canBeHome(c.StorageID) && m.canBeHome(cur) {
				home = cur
			}
		}
	}
	p := media.Placement{Archives: map[int64]string{}}
	if l, ok := m.location(cur); ok {
		p.Record = l.Path
	}
	for _, l := range m.locs {
		if st := m.status[l.ID]; st != nil && st.OK {
			p.Archives[l.ID] = l.Path
		}
	}
	m.mu.Unlock()
	if home != 0 {
		if err := m.Store.SetCameraStorage(ctx, c.ID, home); err != nil {
			m.Log.Error("place camera", "camera", c.ID, "err", err)
		}
	}
	return p
}

// Run refreshes until ctx ends.
func (m *Manager) Run(ctx context.Context, every time.Duration) {
	m.mu.Lock()
	m.bg = ctx
	m.mu.Unlock()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
		if err := m.Refresh(ctx); err != nil && ctx.Err() == nil {
			m.Log.Error("storage refresh", "err", err)
		}
	}
}

// Report describes one location for the Storage screen.
type Report struct {
	store.Location
	Status
	Cameras []string    // recording here now
	Move    *MoveStatus // video being moved out, before it's removed
	Return  *MoveStatus // video recorded on standby, going back to the cameras' own locations
}

func (m *Manager) Report(names map[int64]string) []Report {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Report, 0, len(m.locs))
	for _, l := range m.locs {
		r := Report{Location: l, Cameras: []string{}}
		if st := m.status[l.ID]; st != nil {
			r.Status = *st
		} else {
			r.Status = Status{Reason: "Not checked yet."}
		}
		for cam, loc := range m.current {
			if loc == l.ID {
				if n, ok := names[cam]; ok {
					r.Cameras = append(r.Cameras, n)
				}
			}
		}
		sort.Strings(r.Cameras)
		if mv, ok := m.moves[l.ID]; ok {
			st := mv.status
			r.Move = &st
		}
		if rt, ok := m.returns[l.ID]; ok && (rt.status.Total > 0 || rt.status.Waiting != "") {
			st := rt.status
			r.Return = &st
		}
		out = append(out, r)
	}
	return out
}

// Readable lists the locations whose video can be read now, for retention.
func (m *Manager) Readable() []retention.Location {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []retention.Location
	for _, l := range m.locs {
		if st := m.status[l.ID]; st != nil && st.OK {
			out = append(out, retention.Location{ID: l.ID, Name: l.Name, Dir: l.Path, MinFree: l.MinFreeGB << 30})
		}
	}
	return out
}

// Bootstrap creates the first location, from the folder given at startup, so an
// existing installation keeps recording where it did.
func Bootstrap(ctx context.Context, st *store.Store, dir string, minFreeGB uint64) error {
	locs, err := st.Locations(ctx)
	if err != nil || len(locs) > 0 {
		return err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	l, err := st.PutLocation(ctx, store.Location{Name: "Main storage", Path: dir, MinFreeGB: minFreeGB, Recording: true})
	if err != nil {
		return err
	}
	return Mark(dir, l.ID)
}
