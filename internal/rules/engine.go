// Package rules turns events into alarms and messages to outside services.
package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"time"

	"seelynx/internal/schedule"
	"seelynx/internal/store"
)

// Triggers a rule can react to.
const (
	TriggerMotion    = "motion"
	TriggerAnalytics = "analytics"
	TriggerOffline   = "offline"
	TriggerOnline    = "online"
	TriggerDisk      = "disk"
	TriggerExternal  = "external"
)

// Event is something that happened.
type Event struct {
	Type       string
	Topic      string // the ONVIF topic for analytics events
	Message    string
	CameraID   int64
	CameraName string
	RuleID     int64 // set for inbound hooks, which belong to one rule
	At         time.Time
}

// Rule is the Data of a "rules" configuration item.
type Rule struct {
	Enabled     bool    `json:"enabled"`
	Trigger     string  `json:"trigger"`
	Topic       string  `json:"topic"` // analytics only: part of the topic to match
	CameraIDs   []int64 `json:"camera_ids"`
	ScheduleID  int64   `json:"schedule_id"`
	Cooldown    int     `json:"cooldown"` // seconds between firings, per camera
	Priority    string  `json:"priority"`
	Alarm       bool    `json:"alarm"`
	Message     string  `json:"message"`
	Connections []int64 `json:"connections"`
	Token       string  `json:"token,omitempty"` // external only: the secret in the hook address
	Snapshot    bool    `json:"snapshot"`        // attach a picture from the camera
	CameraID    int64   `json:"camera_id"`       // external only: the camera the outside event is about
}

// Matches reports whether a rule applies to an event, apart from schedule and cooldown.
func (r Rule) Matches(ruleID int64, e Event) bool {
	if !r.Enabled || r.Trigger != e.Type {
		return false
	}
	if e.Type == TriggerExternal {
		return ruleID == e.RuleID
	}
	if len(r.CameraIDs) > 0 && !slices.Contains(r.CameraIDs, e.CameraID) {
		return false
	}
	if e.Type == TriggerAnalytics && r.Topic != "" {
		return strings.Contains(strings.ToLower(e.Topic), strings.ToLower(r.Topic))
	}
	return true
}

// Render fills in a message template.
func Render(template string, e Event) string {
	if template == "" {
		template = "{event}"
	}
	return strings.NewReplacer("{camera}", e.CameraName, "{event}", e.Message, "{topic}", e.Topic,
		"{time}", e.At.Local().Format("2006-01-02 15:04:05")).Replace(template)
}

// Engine processes events one at a time, so a slow service can't reorder them.
type Engine struct {
	Store *store.Store
	Log   *slog.Logger

	// Snapshot returns a picture from a camera, as close to at as it can. Nil means no pictures.
	Snapshot    func(ctx context.Context, cameraID int64, at time.Time) ([]byte, error)
	SnapshotDir string // where alarm pictures are kept

	once    sync.Once
	events  chan Event
	slots   chan struct{}
	pending sync.WaitGroup
	mu      sync.Mutex
	last    map[string]time.Time
}

func (e *Engine) init() {
	e.once.Do(func() {
		e.events = make(chan Event, 1024)
		e.slots = make(chan struct{}, maxCaptures)
		e.last = map[string]time.Time{}
	})
}

// Emit queues an event without blocking, because callers include the video monitor.
func (e *Engine) Emit(ev Event) {
	e.init()
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	select {
	case e.events <- ev:
	default:
		e.Log.Warn("event queue full, dropping event", "type", ev.Type)
	}
}

// Run processes events until ctx ends.
func (e *Engine) Run(ctx context.Context) {
	e.init()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-e.events:
			if err := e.Process(ctx, ev); err != nil && ctx.Err() == nil {
				e.Log.Error("rule processing failed", "err", err)
			}
		}
	}
}

func (e *Engine) inSchedule(ctx context.Context, id int64, at time.Time) bool {
	if id == 0 {
		return true
	}
	it, err := e.Store.Item(ctx, "schedules", id)
	if err != nil {
		return true // a removed schedule shouldn't silence alarms
	}
	var data struct {
		Periods []schedule.Period `json:"periods"`
	}
	if json.Unmarshal(it.Data, &data) != nil {
		return true
	}
	return schedule.Active(data.Periods, at)
}

func (e *Engine) cooledDown(ruleID, cameraID int64, seconds int, at time.Time) bool {
	e.init()
	e.mu.Lock()
	defer e.mu.Unlock()
	key := fmt.Sprintf("%d/%d", ruleID, cameraID)
	if last, ok := e.last[key]; ok && at.Sub(last) < time.Duration(seconds)*time.Second {
		return false
	}
	e.last[key] = at
	return true
}

// withRuleCamera ties an outside event to the rule's camera. A camera removed since the
// rule was saved just leaves the event without one, because the alert still matters.
func (e *Engine) withRuleCamera(ctx context.Context, r Rule, ev Event) Event {
	if ev.Type != TriggerExternal || r.CameraID == 0 {
		return ev
	}
	c, err := e.Store.Camera(ctx, r.CameraID)
	if err != nil {
		e.Log.Warn("rule's camera not found", "camera", r.CameraID, "err", err)
		return ev
	}
	ev.CameraID, ev.CameraName = c.ID, c.Name
	return ev
}

// Process runs one event through every rule.
func (e *Engine) Process(ctx context.Context, ev Event) error {
	items, err := e.Store.Items(ctx, "rules")
	if err != nil {
		return err
	}
	var shot *eventShot
	for _, it := range items {
		var r Rule
		if err := json.Unmarshal(it.Data, &r); err != nil {
			continue
		}
		if !r.Matches(it.ID, ev) || !e.inSchedule(ctx, r.ScheduleID, ev.At) || !e.cooledDown(it.ID, ev.CameraID, r.Cooldown, ev.At) {
			continue
		}
		ev := e.withRuleCamera(ctx, r, ev)
		text := Render(r.Message, ev)
		var alarmID int64
		if r.Alarm {
			if alarmID, err = e.Store.AddAlarm(ctx, store.Alarm{At: ev.At, Rule: it.Name, CameraID: ev.CameraID, CameraName: ev.CameraName, Message: text, Priority: r.Priority}); err != nil {
				return err
			}
		}
		title := "Seelynx: " + it.Name
		if ev.CameraName != "" {
			title += " on " + ev.CameraName
		}
		m := Message{Title: title, Text: text, Priority: r.Priority, Event: ev.Type, Topic: ev.Topic, Camera: ev.CameraName, CameraID: ev.CameraID, At: ev.At}
		if !e.wantsSnapshot(r, ev) {
			e.deliver(ctx, it.Name, r.Connections, m)
			continue
		}
		if shot == nil {
			shot = &eventShot{}
		}
		e.init()
		e.pending.Add(1)
		go func() {
			defer e.pending.Done()
			e.withSnapshot(ctx, shot, ev, it.Name, alarmID, r.Connections, m)
		}()
	}
	return nil
}

// deliver sends to every connection at once, so one slow service doesn't hold up the others.
func (e *Engine) deliver(ctx context.Context, rule string, ids []int64, m Message) {
	var wg sync.WaitGroup
	for _, id := range ids {
		c, err := e.Store.Connection(ctx, id)
		if err != nil {
			e.failed(ctx, rule, fmt.Sprintf("connection %d", id), err)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.send(ctx, rule, c, m); err != nil {
				e.failed(ctx, rule, c.Name, err)
			}
		}()
	}
	wg.Wait()
}

// send falls back to text when a picture is refused, because an alert without a
// picture is far better than none.
func (e *Engine) send(ctx context.Context, rule string, c store.Connection, m Message) error {
	attempt := func(m Message) error {
		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return Send(sctx, c, m)
	}
	err := attempt(m)
	if err == nil || len(m.Snapshot) == 0 || !SendsImages(c.Type) {
		return err
	}
	m.Snapshot = nil
	if retry := attempt(m); retry != nil {
		return retry
	}
	if err := e.Store.AddEvent(ctx, "warning", fmt.Sprintf("Rule %q reached %s, but without the picture: %v", rule, c.Name, err)); err != nil {
		e.Log.Error("event", "err", err)
	}
	return nil
}

func (e *Engine) failed(ctx context.Context, rule, target string, err error) {
	e.Log.Warn("connection failed", "rule", rule, "connection", target, "err", err)
	if err := e.Store.AddEvent(ctx, "warning", fmt.Sprintf("Rule %q couldn't reach %s: %v", rule, target, err)); err != nil {
		e.Log.Error("event", "err", err)
	}
}
