package rules

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"seelynx/internal/store"
)

type captured struct {
	path, body, contentType, header, auth string
}

func capture(t *testing.T) (*httptest.Server, func() captured) {
	var mu sync.Mutex
	var last captured
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		user, pass, _ := r.BasicAuth()
		mu.Lock()
		last = captured{r.URL.Path, string(b), r.Header.Get("Content-Type"), r.Header.Get("X-Test") + r.Header.Get("Title"), user + ":" + pass + r.Header.Get("Authorization")}
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	return srv, func() captured { mu.Lock(); defer mu.Unlock(); return last }
}

var sample = Message{Title: "Seelynx: Door", Text: "Motion at the door", Priority: "high", Event: "motion", Camera: "Door", CameraID: 7, At: time.Unix(1700000000, 0).UTC()}

func TestConnectorFormats(t *testing.T) {
	srv, got := capture(t)
	saved := Endpoints
	defer func() { Endpoints = saved }()
	Endpoints.Telegram, Endpoints.Twilio, Endpoints.PagerDuty, Endpoints.Pushover = srv.URL, srv.URL, srv.URL, srv.URL

	cases := []struct {
		kind string
		cfg  map[string]string
		path string
		want []string
	}{
		{"webhook", map[string]string{"url": srv.URL + "/hook", "header": "X-Test: abc"}, "/hook", []string{`"camera":"Door"`, `"priority":"high"`, `"event":"motion"`}},
		{"slack", map[string]string{"url": srv.URL + "/s"}, "/s", []string{`{"text":"Seelynx: Door: Motion at the door"}`}},
		{"discord", map[string]string{"url": srv.URL + "/d"}, "/d", []string{`{"content":"Seelynx: Door: Motion at the door"}`}},
		{"teams", map[string]string{"url": srv.URL + "/t"}, "/t", []string{`"AdaptiveCard"`, `"Motion at the door"`, `application/vnd.microsoft.card.adaptive`}},
		{"telegram", map[string]string{"token": "123:abc", "chat_id": "42"}, "/bot123:abc/sendMessage", []string{`"chat_id":"42"`}},
		{"pushover", map[string]string{"token": "tok", "user": "usr"}, "/1/messages.json", []string{"token=tok", "user=usr", "priority=1"}},
		{"ntfy", map[string]string{"url": srv.URL + "/topic"}, "/topic", []string{"Motion at the door"}},
		{"pagerduty", map[string]string{"routing_key": "rk"}, "/v2/enqueue", []string{`"routing_key":"rk"`, `"event_action":"trigger"`, `"severity":"critical"`}},
		{"twilio", map[string]string{"account_sid": "AC1", "auth_token": "tk", "from": "+1", "to": "+2"}, "/2010-04-01/Accounts/AC1/Messages.json", []string{"Body=", "To=%2B2"}},
	}
	for _, c := range cases {
		if err := Send(context.Background(), store.Connection{Type: c.kind, Config: c.cfg}, sample); err != nil {
			t.Errorf("%s: %v", c.kind, err)
			continue
		}
		g := got()
		if g.path != c.path {
			t.Errorf("%s: path %q", c.kind, g.path)
		}
		for _, w := range c.want {
			if !strings.Contains(g.body, w) {
				t.Errorf("%s: %q not in %s", c.kind, w, g.body)
			}
		}
	}
	Send(context.Background(), store.Connection{Type: "webhook", Config: map[string]string{"url": srv.URL, "header": "X-Test: abc"}}, sample)
	if got().header != "abc" {
		t.Error("webhook header not sent")
	}
	Send(context.Background(), store.Connection{Type: "twilio", Config: map[string]string{"account_sid": "AC1", "auth_token": "tk"}}, sample)
	if !strings.HasPrefix(got().auth, "AC1:tk") {
		t.Errorf("twilio auth: %q", got().auth)
	}
}

func TestMQTTPublish(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	got := make(chan []byte, 1)
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		r := bufio.NewReader(c)
		first, _ := r.ReadByte()
		size, _ := r.ReadByte()
		connect := make([]byte, size)
		io.ReadFull(r, connect)
		if first != 0x10 || string(connect[2:6]) != "MQTT" || connect[7]&0xC0 != 0xC0 {
			t.Errorf("bad CONNECT %x", connect)
		}
		c.Write([]byte{0x20, 2, 0, 0})
		rest, _ := io.ReadAll(r)
		got <- rest
	}()
	cfg := map[string]string{"host": l.Addr().String(), "topic": "seelynx/events", "username": "u", "password": "p"}
	if err := Send(context.Background(), store.Connection{Type: "mqtt", Config: cfg}, sample); err != nil {
		t.Fatal(err)
	}
	b := <-got
	if b[0] != 0x30 || !strings.Contains(string(b), "seelynx/events") || !strings.Contains(string(b), `"camera":"Door"`) {
		t.Fatalf("bad PUBLISH %q", b)
	}
}

func TestErrorsNeverContainTheAddress(t *testing.T) {
	err := Send(context.Background(), store.Connection{Type: "webhook", Config: map[string]string{"url": "http://127.0.0.1:1/secret-token"}}, sample)
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("%v", err)
	}
}

func TestEveryTypeIsImplemented(t *testing.T) {
	// A closed local port, so this never reaches the real services.
	saved := Endpoints
	defer func() { Endpoints = saved }()
	dead := "http://127.0.0.1:1"
	Endpoints.Telegram, Endpoints.Twilio, Endpoints.PagerDuty, Endpoints.Pushover = dead, dead, dead, dead
	Endpoints.Gmail, Endpoints.MicrosoftLogin, Endpoints.Graph = "127.0.0.1:1", dead, dead
	for _, ty := range Types {
		err := Send(context.Background(), store.Connection{Type: ty.ID, Config: map[string]string{"host": "127.0.0.1:1", "url": "http://127.0.0.1:1"}}, sample)
		if err != nil && strings.Contains(err.Error(), "unknown connection type") {
			t.Errorf("%s has no sender", ty.ID)
		}
	}
}

func TestRuleMatching(t *testing.T) {
	motion := Event{Type: TriggerMotion, CameraID: 1}
	cases := []struct {
		name string
		r    Rule
		e    Event
		want bool
	}{
		{"match", Rule{Enabled: true, Trigger: TriggerMotion}, motion, true},
		{"disabled", Rule{Trigger: TriggerMotion}, motion, false},
		{"other trigger", Rule{Enabled: true, Trigger: TriggerOffline}, motion, false},
		{"listed camera", Rule{Enabled: true, Trigger: TriggerMotion, CameraIDs: []int64{1}}, motion, true},
		{"other camera", Rule{Enabled: true, Trigger: TriggerMotion, CameraIDs: []int64{2}}, motion, false},
		{"topic", Rule{Enabled: true, Trigger: TriggerAnalytics, Topic: "linedetector"}, Event{Type: TriggerAnalytics, Topic: "tns1:RuleEngine/LineDetector/Crossed"}, true},
		{"other topic", Rule{Enabled: true, Trigger: TriggerAnalytics, Topic: "face"}, Event{Type: TriggerAnalytics, Topic: "tns1:RuleEngine/LineDetector/Crossed"}, false},
		{"hook for this rule", Rule{Enabled: true, Trigger: TriggerExternal}, Event{Type: TriggerExternal, RuleID: 5}, true},
		{"hook for another rule", Rule{Enabled: true, Trigger: TriggerExternal}, Event{Type: TriggerExternal, RuleID: 6}, false},
	}
	for _, c := range cases {
		if got := c.r.Matches(5, c.e); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func TestRender(t *testing.T) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.Local)
	got := Render("{camera} at {time}: {event}", Event{CameraName: "Door", Message: "Motion", At: at})
	if got != "Door at 2026-01-02 03:04:05: Motion" {
		t.Fatal(got)
	}
	if Render("", Event{Message: "Default"}) != "Default" {
		t.Fatal("empty template")
	}
}

func engine(t *testing.T) (*Engine, *store.Store) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &Engine{Store: st, Log: slog.New(slog.DiscardHandler)}, st
}

func addRule(t *testing.T, st *store.Store, r Rule) int64 {
	raw, _ := json.Marshal(r)
	it, err := st.PutItem(context.Background(), "rules", store.Item{Name: "Rule", Data: raw})
	if err != nil {
		t.Fatal(err)
	}
	return it.ID
}

func TestEngineCreatesAlarmSendsAndCoolsDown(t *testing.T) {
	ctx := context.Background()
	e, st := engine(t)
	srv, got := capture(t)
	c, err := st.SaveConnection(ctx, store.Connection{Name: "hook", Type: "slack", Config: map[string]string{"url": srv.URL + "/s"}})
	if err != nil {
		t.Fatal(err)
	}
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "high", Cooldown: 60, Message: "Seen on {camera}", Connections: []int64{c.ID}})

	at := time.Now()
	ev := Event{Type: TriggerMotion, CameraID: 1, CameraName: "Door", Message: "Motion", At: at}
	if err := e.Process(ctx, ev); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got().body, "Seen on Door") {
		t.Fatalf("not delivered: %+v", got())
	}
	ev.At = at.Add(30 * time.Second)
	e.Process(ctx, ev)
	ev.CameraID = 2 // the cooldown is per camera
	e.Process(ctx, ev)
	ev.CameraID, ev.At = 1, at.Add(61*time.Second)
	e.Process(ctx, ev)
	alarms, _ := st.Alarms(ctx, 10)
	if len(alarms) != 3 || alarms[0].Priority != "high" || alarms[0].Rule != "Rule" {
		t.Fatalf("%d alarms: %+v", len(alarms), alarms)
	}
}

func TestScheduleLimitsRule(t *testing.T) {
	ctx := context.Background()
	e, st := engine(t)
	now := time.Now()
	other := (int(now.Weekday()) + 1) % 7
	raw, _ := json.Marshal(map[string]any{"periods": []map[string]any{{"days": []int{other}, "start": "00:00", "end": "00:00"}}})
	s, _ := st.PutItem(ctx, "schedules", store.Item{Name: "s", Data: raw})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, ScheduleID: s.ID})
	e.Process(ctx, Event{Type: TriggerMotion, At: now})
	if a, _ := st.Alarms(ctx, 10); len(a) != 0 {
		t.Fatal("rule fired outside its schedule")
	}
}

func TestFailedDeliveryIsReportedAndDoesNotStopTheAlarm(t *testing.T) {
	ctx := context.Background()
	e, st := engine(t)
	c, _ := st.SaveConnection(ctx, store.Connection{Name: "broken", Type: "webhook", Config: map[string]string{"url": "http://127.0.0.1:1/secret-token"}})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Connections: []int64{c.ID}})
	if err := e.Process(ctx, Event{Type: TriggerMotion, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if a, _ := st.Alarms(ctx, 10); len(a) != 1 {
		t.Fatal("the alarm must survive a failed delivery")
	}
	ev, _ := st.Events(ctx, 10)
	if len(ev) != 1 || !strings.Contains(ev[0].Message, "broken") || strings.Contains(ev[0].Message, "secret-token") {
		t.Fatalf("%+v", ev)
	}
}

func TestEmitNeverBlocks(t *testing.T) {
	e, _ := engine(t)
	done := make(chan bool)
	go func() {
		for i := 0; i < 5000; i++ {
			e.Emit(Event{Type: TriggerMotion})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Emit blocked with no worker running")
	}
}
