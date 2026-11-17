package api

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/rules"
	"seelynx/internal/store"
)

const (
	motionMergeGap   = 5 * time.Second // detectors flicker off mid-movement
	motionSubTTL     = 10 * time.Minute
	motionRenewEvery = 5 * time.Minute
	motionRetryMin   = 30 * time.Second
	motionRetryMax   = 5 * time.Minute
	motionKeepDays   = 90
)

// motionTracker turns raw on and off signals into motion periods, merging short gaps.
type motionTracker struct {
	gap     time.Duration
	active  bool
	lastOff time.Time
}

// observe records a signal and reports whether a new period started.
func (m *motionTracker) observe(on bool, at time.Time) (started bool) {
	if on {
		m.lastOff = time.Time{}
		if !m.active {
			m.active = true
			return true
		}
		return false
	}
	if m.active && m.lastOff.IsZero() {
		m.lastOff = at
	}
	return false
}

// expire ends the period once the gap has passed, returning when it ended.
func (m *motionTracker) expire(now time.Time) (time.Time, bool) {
	if m.active && !m.lastOff.IsZero() && now.Sub(m.lastOff) >= m.gap {
		end := m.lastOff
		m.active, m.lastOff = false, time.Time{}
		return end, true
	}
	return time.Time{}, false
}

// close ends any open period because the connection is going away.
func (m *motionTracker) close(now time.Time) (time.Time, bool) {
	if !m.active {
		return time.Time{}, false
	}
	end := now
	if !m.lastOff.IsZero() {
		end = m.lastOff
	}
	m.active, m.lastOff = false, time.Time{}
	return end, true
}

// motionSignal reads a notification as motion on or off. MotionAlarm is the ONVIF
// video source topic, and CellMotionDetector and MotionRegionDetector are analytics rule
// topics that real cameras also send for plain motion.
func motionSignal(e onvif.Event) (on, ok bool) {
	var raw string
	switch {
	case strings.HasSuffix(e.Topic, "MotionAlarm"), strings.Contains(e.Topic, "MotionRegionDetector"):
		raw, ok = e.Items["State"]
	case strings.Contains(e.Topic, "CellMotionDetector"):
		raw, ok = e.Items["IsMotion"]
	}
	if !ok {
		return false, false
	}
	switch strings.ToLower(raw) {
	case "true", "1":
		return true, true
	case "false", "0":
		return false, true
	}
	return false, false
}

// MotionWatcher keeps an ONVIF event subscription open for each camera with a login.
type MotionWatcher struct {
	Store *store.Store
	Log   *slog.Logger

	Address func(store.Camera) string // device service URL; nil derives it
	Gap     time.Duration             // zero uses motionMergeGap
	Events  func(rules.Event)         // receives motion and analytics events; may be nil
	// Names returns a driver's friendly name for an analytics event; may be nil.
	Names func(manufacturer, model, topic string) (string, bool)

	mu      sync.RWMutex
	active  map[int64]bool
	running map[int64]*motionWorker
	models  map[int64]onvif.DeviceInfo
	watched map[int64]time.Time // since when each camera's events have been arriving
}

type motionWorker struct {
	key    string
	cancel context.CancelFunc
}

// Active reports whether a camera has motion right now.
func (w *MotionWatcher) Active(id int64) bool {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.active[id]
}

func (w *MotionWatcher) setActive(id int64, on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.active == nil {
		w.active = map[int64]bool{}
	}
	w.active[id] = on
}

// Watched says since when a camera's motion events have been arriving.
func (w *MotionWatcher) Watched(id int64) (time.Time, bool) {
	w.mu.RLock()
	defer w.mu.RUnlock()
	since, ok := w.watched[id]
	return since, ok
}

func (w *MotionWatcher) setWatched(id int64, on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.watched == nil {
		w.watched = map[int64]time.Time{}
	}
	if on {
		w.watched[id] = time.Now()
	} else {
		delete(w.watched, id)
	}
}

func (w *MotionWatcher) setModel(id int64, info onvif.DeviceInfo) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.models == nil {
		w.models = map[int64]onvif.DeviceInfo{}
	}
	w.models[id] = info
}

// analyticsMessage uses the camera's driver if it has one.
func (w *MotionWatcher) analyticsMessage(c store.Camera, topic string) string {
	if w.Names != nil {
		w.mu.RLock()
		info := w.models[c.ID]
		w.mu.RUnlock()
		if name, ok := w.Names(info.Manufacturer, info.Model, topic); ok {
			return name
		}
	}
	return "Analytics event: " + topic[strings.LastIndex(topic, "/")+1:]
}

func deviceAddress(c store.Camera) string {
	u, err := url.Parse(c.MainURL)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return "http://" + u.Hostname() + "/onvif/device_service"
}

func (w *MotionWatcher) address(c store.Camera) string {
	if w.Address != nil {
		return w.Address(c)
	}
	return deviceAddress(c)
}

// Run starts and stops per-camera workers as cameras change, until ctx ends.
func (w *MotionWatcher) Run(ctx context.Context, every time.Duration) {
	if err := w.Store.CloseOpenMotion(ctx); err != nil {
		w.Log.Error("close open motion", "err", err)
	}
	for {
		w.reconcile(ctx)
		if err := w.Store.PurgeMotion(ctx, time.Now().AddDate(0, 0, -motionKeepDays)); err != nil && ctx.Err() == nil {
			w.Log.Error("purge motion", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(every):
		}
	}
}

func (w *MotionWatcher) reconcile(ctx context.Context) {
	cams, err := w.Store.Cameras(ctx)
	if err != nil {
		if ctx.Err() == nil {
			w.Log.Error("motion reconcile", "err", err)
		}
		return
	}
	want := map[int64]store.Camera{}
	for _, c := range cams {
		// ONVIF events need a login, so cameras without one are skipped.
		if c.Enabled && c.Username != "" && w.address(c) != "" {
			want[c.ID] = c
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.running == nil {
		w.running = map[int64]*motionWorker{}
	}
	for id, r := range w.running {
		if c, ok := want[id]; !ok || motionKey(c) != r.key {
			r.cancel()
			delete(w.running, id)
		}
	}
	for id, c := range want {
		if _, ok := w.running[id]; ok {
			continue
		}
		wctx, cancel := context.WithCancel(ctx)
		w.running[id] = &motionWorker{motionKey(c), cancel}
		go w.watch(wctx, c)
	}
}

func motionKey(c store.Camera) string { return c.MainURL + "\x00" + c.Username + "\x00" + c.Password }

func (w *MotionWatcher) emit(e rules.Event) {
	if w.Events != nil {
		w.Events(e)
	}
}

// analyticsOn reads whether an event turned on. Models name this field differently: real
// cameras used Value, State, state and IsInside.
func analyticsOn(items map[string]string) bool {
	for name, value := range items {
		switch strings.ToLower(name) {
		case "value", "state", "isinside", "ismotion", "active":
			if v := strings.ToLower(value); v == "true" || v == "1" {
				return true
			}
		}
	}
	return false
}

// emitAnalytics passes on analytics and audio events when they turn on. Topics vary by
// camera, so anything under these branches counts, and rules can filter by topic text.
func (w *MotionWatcher) emitAnalytics(c store.Camera, e onvif.Event) {
	if !strings.Contains(e.Topic, "Analytics") && !strings.Contains(e.Topic, "RuleEngine") {
		return
	}
	// On subscribing, a real camera repeated its last line crossing as Value=true, so
	// this would otherwise raise an alarm on every restart and reconnect.
	if e.Operation == "Initialized" {
		return
	}
	if on, ok := motionSignal(e); ok {
		_ = on // already handled as motion
		return
	}
	if !analyticsOn(e.Items) {
		return
	}
	w.emit(rules.Event{Type: rules.TriggerAnalytics, Topic: e.Topic, CameraID: c.ID, CameraName: c.Name, Message: w.analyticsMessage(c, e.Topic), At: time.Now()})
}

// watch keeps one camera subscribed, retrying with a growing delay.
func (w *MotionWatcher) watch(ctx context.Context, c store.Camera) {
	delay := motionRetryMin
	warned := false
	for ctx.Err() == nil {
		connected, err := w.session(ctx, c)
		if ctx.Err() != nil {
			return
		}
		if connected {
			delay, warned = motionRetryMin, false
		}
		if err != nil && !warned {
			w.Log.Warn("motion events unavailable", "camera", c.ID, "err", err)
			warned = true
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay *= 2; delay > motionRetryMax {
			delay = motionRetryMax
		}
	}
}

func (w *MotionWatcher) session(ctx context.Context, c store.Camera) (connected bool, err error) {
	client := &onvif.Client{XAddr: w.address(c), User: c.Username, Pass: c.Password}
	// A camera with a wrong clock can refuse the login, so measure it first.
	if err := client.SyncClock(ctx); err != nil {
		w.Log.Debug("camera clock unavailable", "camera", c.ID, "err", err)
	}
	if info, err := client.DeviceInformation(ctx); err == nil {
		w.setModel(c.ID, info)
	}
	addr, err := client.EventsAddress(ctx)
	if err != nil {
		return false, err
	}
	sub, err := client.Subscribe(ctx, addr, motionSubTTL)
	if err != nil {
		return false, err
	}
	w.setWatched(c.ID, true)
	defer w.setWatched(c.ID, false)
	defer func() {
		// Cameras allow few subscriptions, so give this one back.
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client.Unsubscribe(dctx, sub)
	}()

	gap := w.Gap
	if gap == 0 {
		gap = motionMergeGap
	}
	tracker := motionTracker{gap: gap}
	var row int64
	finish := func(end time.Time) {
		w.setActive(c.ID, false)
		// Background context: the period must close even during shutdown.
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := w.Store.EndMotion(dctx, row, end); err != nil {
			w.Log.Error("end motion", "camera", c.ID, "err", err)
		}
	}
	defer func() {
		if end, ok := tracker.close(time.Now()); ok {
			finish(end)
		}
	}()

	renew := time.Now().Add(motionRenewEvery)
	for ctx.Err() == nil {
		events, err := client.Pull(ctx, sub, 2*time.Second)
		if err != nil {
			return true, err
		}
		now := time.Now()
		for _, e := range events {
			on, ok := motionSignal(e)
			if !ok {
				w.emitAnalytics(c, e)
				continue
			}
			if tracker.observe(on, now) {
				id, err := w.Store.StartMotion(ctx, c.ID, now)
				if err != nil {
					return true, err
				}
				row = id
				w.setActive(c.ID, true)
				w.emit(rules.Event{Type: rules.TriggerMotion, CameraID: c.ID, CameraName: c.Name, Message: "Motion detected", At: now})
			}
		}
		if end, ok := tracker.expire(now); ok {
			finish(end)
		}
		if now.After(renew) {
			if err := client.Renew(ctx, sub, motionSubTTL); err != nil {
				return true, err
			}
			renew = now.Add(motionRenewEvery)
		}
	}
	return true, nil
}

// --- API ---

func (s *Server) cameraMotion(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	start, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil || !end.After(start) {
		fail(w, http.StatusBadRequest, "Invalid time range.")
		return
	}
	list, err := s.Store.MotionBetween(r.Context(), c.ID, start, end)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) recentMotion(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 200 {
		limit = 20
	}
	list, err := s.Store.RecentMotion(r.Context(), 1000)
	if err != nil {
		s.internal(w, err)
		return
	}
	u := currentUser(r)
	out := make([]store.Motion, 0, limit)
	for _, m := range list {
		if len(out) == limit {
			break
		}
		if u.CanSeeCamera(m.CameraID) {
			out = append(out, m)
		}
	}
	writeJSON(w, http.StatusOK, out)
}
