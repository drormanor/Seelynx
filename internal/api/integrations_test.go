package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"seelynx/internal/rules"
	"seelynx/internal/store"
)

func TestConnectionSecretsNeverReturn(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	in := store.Connection{Name: "Ops", Type: "telegram", Config: map[string]string{"token": "123:topsecret", "chat_id": "42"}}
	code, body := e.do(c, "POST", "/api/connections", in)
	if code != 200 || strings.Contains(body, "topsecret") || !strings.Contains(body, `"chat_id":"42"`) {
		t.Fatalf("%d %s", code, body)
	}
	var saved store.Connection
	json.Unmarshal([]byte(body), &saved)
	if _, list := e.do(c, "GET", "/api/connections", nil); strings.Contains(list, "topsecret") {
		t.Fatal("secret in list")
	}
	// Saving the masked value keeps the stored secret.
	saved.Config["chat_id"] = "43"
	if code, body := e.do(c, "PUT", fmt.Sprintf("/api/connections/%d", saved.ID), saved); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	got, _ := e.st.Connection(context.Background(), saved.ID)
	if got.Config["token"] != "123:topsecret" || got.Config["chat_id"] != "43" {
		t.Fatalf("%+v", got.Config)
	}
}

func TestConnectionValidation(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	bad := []store.Connection{
		{Name: "", Type: "slack", Config: map[string]string{"url": "https://a.example/x"}},
		{Name: "x", Type: "carrier-pigeon"},
		{Name: "x", Type: "slack"},
		{Name: "x", Type: "slack", Config: map[string]string{"url": "ftp://a.example"}},
		{Name: "x", Type: "telegram", Config: map[string]string{"token": "t"}},
	}
	for _, b := range bad {
		if code, _ := e.do(c, "POST", "/api/connections", b); code != 400 {
			t.Errorf("%+v: %d", b, code)
		}
	}
}

func TestIntegrationPermissions(t *testing.T) {
	e := newEnv(t)
	o := e.addUser("o", "operator")
	v := e.addUser("v", "viewer")
	for _, route := range []string{"/api/connections", "/api/connection-types", "/api/config/rules"} {
		if code, _ := e.do(o, "GET", route, nil); code != 403 {
			t.Errorf("operator read %s: %d", route, code)
		}
	}
	if code, _ := e.do(o, "GET", "/api/alarms", nil); code != 200 {
		t.Errorf("operator alarms: %d", code)
	}
	if code, _ := e.do(v, "GET", "/api/alarms", nil); code != 403 {
		t.Errorf("viewer alarms: %d", code)
	}
	if code, _ := e.do(e.client(), "GET", "/api/alarms", nil); code != 401 {
		t.Errorf("anonymous alarms: %d", code)
	}
}

func TestRuleValidationAndHookToken(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	bad := []string{
		`{"name":"r","data":{"trigger":"nonsense","priority":"low","alarm":true}}`,
		`{"name":"r","data":{"trigger":"motion","priority":"urgent","alarm":true}}`,
		`{"name":"r","data":{"trigger":"motion","priority":"low"}}`,
		`{"name":"r","data":{"trigger":"motion","priority":"low","alarm":true,"cooldown":-1}}`,
	}
	for _, b := range bad {
		res, err := c.Post(e.srv.URL+"/api/config/rules", "application/json", strings.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("%s: %d", b, res.StatusCode)
		}
	}
	res, err := c.Post(e.srv.URL+"/api/config/rules", "application/json", strings.NewReader(`{"name":"r","data":{"trigger":"external","priority":"low","alarm":true,"enabled":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var item store.Item
	json.NewDecoder(res.Body).Decode(&item)
	var r rules.Rule
	json.Unmarshal(item.Data, &r)
	if len(r.Token) != 48 {
		t.Fatalf("token %q", r.Token)
	}
}

func TestInboundHookTriggersOnlyItsRule(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	eng := &rules.Engine{Store: e.st, Log: discard()}
	e.server.Rules = eng
	go eng.Run(ctx)
	raw, _ := json.Marshal(rules.Rule{Enabled: true, Trigger: rules.TriggerExternal, Priority: "high", Alarm: true, Token: "right-token", Message: "From outside: {event}"})
	if _, err := e.st.PutItem(ctx, "rules", store.Item{Name: "Hook", Data: raw}); err != nil {
		t.Fatal(err)
	}
	post := func(token, body string) int {
		res, err := http.Post(e.srv.URL+"/api/hooks/"+token, "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		io.Copy(io.Discard, res.Body)
		res.Body.Close()
		return res.StatusCode
	}
	if post("wrong-token", `{}`) != 404 {
		t.Error("wrong token accepted")
	}
	if post("right-token", `{"message":"door forced"}`) != 202 {
		t.Fatal("right token refused")
	}
	waitFor(t, "alarm", func() bool {
		a, _ := e.st.Alarms(ctx, 10)
		return len(a) == 1 && a[0].Message == "From outside: door forced"
	})
}

func TestAlarmAcknowledgeAndCameraLimits(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})
	b, _ := e.st.CreateCamera(ctx, store.Camera{Name: "b", MainURL: "rtsp://b"})
	e.st.AddAlarm(ctx, store.Alarm{At: time.Now(), Rule: "r", CameraID: a.ID, Message: "seen-alarm", Priority: "low"})
	e.st.AddAlarm(ctx, store.Alarm{At: time.Now(), Rule: "r", CameraID: b.ID, Message: "hidden-alarm", Priority: "low"})
	role, _ := e.st.SaveRole(ctx, store.Role{Name: "x", Permissions: []string{store.PermSystem}, CameraIDs: []int64{a.ID}})
	c := e.userWith(store.User{Username: "r", RoleID: role.ID})
	if _, body := e.do(c, "GET", "/api/alarms", nil); !strings.Contains(body, "seen-alarm") || strings.Contains(body, "hidden-alarm") {
		t.Fatalf("%s", body)
	}
	list, _ := e.st.Alarms(ctx, 10)
	id := list[len(list)-1].ID
	if code, body := e.do(c, "POST", fmt.Sprintf("/api/alarms/%d/ack", id), map[string]string{"note": "checked"}); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := e.do(c, "POST", fmt.Sprintf("/api/alarms/%d/ack", id), map[string]string{}); code != 409 {
		t.Errorf("second ack: %d", code)
	}
	list, _ = e.st.Alarms(ctx, 10)
	got := list[len(list)-1]
	if got.AckedBy != "r" || got.Note != "checked" || got.AckedAt == nil {
		t.Fatalf("%+v", got)
	}
}

func TestConnectionInUseCannotBeRemoved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.addUser("root", "admin")
	conn, _ := e.st.SaveConnection(ctx, store.Connection{Name: "s", Type: "slack", Config: map[string]string{"url": "https://a.example"}})
	raw, _ := json.Marshal(rules.Rule{Trigger: rules.TriggerMotion, Priority: "low", Connections: []int64{conn.ID}})
	rule, _ := e.st.PutItem(ctx, "rules", store.Item{Name: "r", Data: raw})
	if code, _ := e.do(c, "DELETE", fmt.Sprintf("/api/connections/%d", conn.ID), nil); code != 409 {
		t.Fatalf("in use: %d", code)
	}
	e.st.DeleteItem(ctx, "rules", rule.ID)
	if code, _ := e.do(c, "DELETE", fmt.Sprintf("/api/connections/%d", conn.ID), nil); code != 204 {
		t.Fatalf("unused: %d", code)
	}
}

func TestTestButtonReportsFailureWithoutTheAddress(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	good, _ := e.st.SaveConnection(context.Background(), store.Connection{Name: "g", Type: "slack", Config: map[string]string{"url": ok.URL}})
	if code, _ := e.do(c, "POST", fmt.Sprintf("/api/connections/%d/test", good.ID), nil); code != 204 {
		t.Errorf("good: %d", code)
	}
	bad, _ := e.st.SaveConnection(context.Background(), store.Connection{Name: "b", Type: "slack", Config: map[string]string{"url": "http://127.0.0.1:1/secret-token"}})
	code, body := e.do(c, "POST", fmt.Sprintf("/api/connections/%d/test", bad.ID), nil)
	if code != 502 || strings.Contains(body, "secret-token") {
		t.Errorf("%d %s", code, body)
	}
}

func TestRememberedAnalyticsStateIsNotAnEvent(t *testing.T) {
	var got []rules.Event
	w := &MotionWatcher{Events: func(e rules.Event) { got = append(got, e) }}
	c := store.Camera{ID: 1, Name: "Door"}
	old := onvif_("tns1:RuleEngine/LineDetector/Crossed", "Value", "true")
	old.Operation = "Initialized"
	w.emitAnalytics(c, old)
	if len(got) != 0 {
		t.Fatalf("a stale state raised %+v", got)
	}
	now := onvif_("tns1:RuleEngine/LineDetector/Crossed", "Value", "true")
	now.Operation = "Changed"
	w.emitAnalytics(c, now)
	if len(got) != 1 {
		t.Fatal("a real crossing was ignored")
	}
}

func TestWatcherEmitsMotionAndAnalytics(t *testing.T) {
	var got []rules.Event
	w := &MotionWatcher{Events: func(e rules.Event) { got = append(got, e) }}
	c := store.Camera{ID: 1, Name: "Door"}
	w.emitAnalytics(c, onvif_("tns1:RuleEngine/LineDetector/Crossed", "Value", "true"))
	w.emitAnalytics(c, onvif_("tns1:RuleEngine/LineDetector/Crossed", "Value", "false"))
	w.emitAnalytics(c, onvif_("tns1:Monitoring/ProcessorUsage", "Value", "1"))
	w.emitAnalytics(c, onvif_("tns1:VideoSource/MotionAlarm", "State", "true"))
	w.emitAnalytics(c, onvif_("tns1:AudioAnalytics/Audio/DetectedSound", "State", "true"))
	if len(got) != 2 || got[0].Topic != "tns1:RuleEngine/LineDetector/Crossed" || got[1].Topic != "tns1:AudioAnalytics/Audio/DetectedSound" {
		t.Fatalf("%+v", got)
	}
}
