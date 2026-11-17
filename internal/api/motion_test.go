package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/store"
)

func TestTrackerMergesShortGaps(t *testing.T) {
	t0 := time.Unix(1000, 0)
	at := func(s int) time.Time { return t0.Add(time.Duration(s) * time.Second) }
	m := motionTracker{gap: 5 * time.Second}

	if m.observe(false, at(0)) {
		t.Fatal("off at start is not a period")
	}
	if !m.observe(true, at(1)) {
		t.Fatal("first on starts a period")
	}
	if m.observe(true, at(2)) {
		t.Fatal("repeated on must not start another")
	}
	m.observe(false, at(3))
	if _, ok := m.expire(at(6)); ok {
		t.Fatal("gap not over yet")
	}
	if m.observe(true, at(6)) {
		t.Fatal("on within the gap continues the period")
	}
	m.observe(false, at(10))
	m.observe(false, at(12)) // must not push the end later
	end, ok := m.expire(at(15))
	if !ok || !end.Equal(at(10)) {
		t.Fatalf("end %v %v", end, ok)
	}
	if !m.observe(true, at(20)) {
		t.Fatal("new period after the gap")
	}
	if end, ok := m.close(at(25)); !ok || !end.Equal(at(25)) {
		t.Fatalf("close %v %v", end, ok)
	}
	if _, ok := m.close(at(26)); ok {
		t.Fatal("nothing left to close")
	}
}

func TestMotionSignal(t *testing.T) {
	cases := []struct {
		topic, key, value string
		on, ok            bool
	}{
		{"tns1:VideoSource/MotionAlarm", "State", "true", true, true},
		{"tns1:VideoSource/MotionAlarm", "State", "false", false, true},
		{"tns1:RuleEngine/CellMotionDetector/Motion", "IsMotion", "1", true, true},
		{"tns1:VideoSource/GlobalSceneChange/ImagingService", "State", "1", false, false},
		{"tns1:Monitoring/ProcessorUsage", "Value", "6", false, false},
		{"tns1:VideoSource/MotionAlarm", "State", "maybe", false, false},
		{"tns1:VideoSource/MotionAlarm", "Other", "true", false, false},
	}
	for _, c := range cases {
		on, ok := motionSignal(onvif.Event{Topic: c.topic, Items: map[string]string{c.key: c.value}})
		if on != c.on || ok != c.ok {
			t.Errorf("%s %s=%s: %v %v", c.topic, c.key, c.value, on, ok)
		}
	}
}

// fakeEventCamera scripts a sequence of MotionAlarm states, one per pull.
type fakeEventCamera struct {
	srv          *httptest.Server
	mu           sync.Mutex
	script       []string
	unsubscribed atomic.Int32
	failSub      atomic.Bool
}

func newFakeEventCamera(t *testing.T, script ...string) *fakeEventCamera {
	f := &fakeEventCamera{script: script}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := string(b)
		env := func(s string) {
			io.WriteString(w, `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:t="urn:t"><s:Body>`+s+`</s:Body></s:Envelope>`)
		}
		switch {
		case strings.Contains(body, "GetSystemDateAndTime"):
			now := time.Now().UTC()
			env(fmt.Sprintf(`<t:GetSystemDateAndTimeResponse><t:SystemDateAndTime><t:UTCDateTime><t:Time><t:Hour>%d</t:Hour><t:Minute>%d</t:Minute><t:Second>%d</t:Second></t:Time><t:Date><t:Year>%d</t:Year><t:Month>%d</t:Month><t:Day>%d</t:Day></t:Date></t:UTCDateTime></t:SystemDateAndTime></t:GetSystemDateAndTimeResponse>`,
				now.Hour(), now.Minute(), now.Second(), now.Year(), int(now.Month()), now.Day()))
		case strings.Contains(body, "GetCapabilities"):
			env(`<t:GetCapabilitiesResponse><t:Capabilities><t:Events><t:XAddr>` + f.srv.URL + `/events</t:XAddr></t:Events></t:Capabilities></t:GetCapabilitiesResponse>`)
		case strings.Contains(body, "CreatePullPointSubscription"):
			if f.failSub.Load() {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			env(`<t:CreatePullPointSubscriptionResponse><t:SubscriptionReference><t:Address>` + f.srv.URL + `/sub</t:Address></t:SubscriptionReference></t:CreatePullPointSubscriptionResponse>`)
		case strings.Contains(body, "PullMessages"):
			f.mu.Lock()
			var state string
			if len(f.script) > 0 {
				state, f.script = f.script[0], f.script[1:]
			}
			f.mu.Unlock()
			msg := ""
			if state != "" {
				msg = `<t:NotificationMessage><t:Topic>tns1:VideoSource/MotionAlarm</t:Topic><t:Message><t:Message UtcTime="x"><t:Data><t:SimpleItem Name="State" Value="` + state + `"/></t:Data></t:Message></t:Message></t:NotificationMessage>`
			} else {
				time.Sleep(10 * time.Millisecond)
			}
			env(`<t:PullMessagesResponse>` + msg + `</t:PullMessagesResponse>`)
		case strings.Contains(body, "Unsubscribe"):
			f.unsubscribed.Add(1)
			env(`<t:UnsubscribeResponse/>`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestWatcherRecordsMotionPeriod(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cam, err := e.st.CreateCamera(ctx, store.Camera{Name: "c", MainURL: "rtsp://192.0.2.9/a", Username: "u", Password: "p", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f := newFakeEventCamera(t, "false", "true", "true", "false")
	w := &MotionWatcher{Store: e.st, Log: discard(), Gap: 50 * time.Millisecond, Address: func(store.Camera) string { return f.srv.URL + "/device" }}
	w.reconcile(ctx)

	var rows []store.Motion
	waitFor(t, "a finished motion period", func() bool {
		rows, _ = e.st.RecentMotion(ctx, 10)
		return len(rows) == 1 && rows[0].End != nil
	})
	if rows[0].CameraID != cam.ID || rows[0].CameraName != "c" {
		t.Fatalf("%+v", rows[0])
	}
	if w.Active(cam.ID) {
		t.Error("motion should have ended")
	}

	// Removing the camera stops the worker and gives the subscription back.
	if err := e.st.DeleteCamera(ctx, cam.ID); err != nil {
		t.Fatal(err)
	}
	w.reconcile(ctx)
	waitFor(t, "unsubscribe", func() bool { return f.unsubscribed.Load() == 1 })
}

func TestWatcherClosesPeriodOnShutdown(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "c", MainURL: "rtsp://192.0.2.9/a", Username: "u", Enabled: true})
	f := newFakeEventCamera(t, "true")
	w := &MotionWatcher{Store: e.st, Log: discard(), Address: func(store.Camera) string { return f.srv.URL + "/device" }}
	w.reconcile(ctx)
	waitFor(t, "motion to start", func() bool { return w.Active(cam.ID) })
	cancel()
	waitFor(t, "the period to close", func() bool {
		rows, _ := e.st.RecentMotion(context.Background(), 10)
		return len(rows) == 1 && rows[0].End != nil
	})
}

func TestWatcherSkipsCamerasWithoutLogin(t *testing.T) {
	e := newEnv(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	e.st.CreateCamera(ctx, store.Camera{Name: "sim", MainURL: "rtsp://127.0.0.1/a", Enabled: true})
	e.st.CreateCamera(ctx, store.Camera{Name: "off", MainURL: "rtsp://192.0.2.9/a", Username: "u", Enabled: false})
	w := &MotionWatcher{Store: e.st, Log: discard()}
	w.reconcile(ctx)
	if len(w.running) != 0 {
		t.Fatalf("workers: %d", len(w.running))
	}
}

func TestStartupClosesOrphanedPeriods(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := e.st.StartMotion(ctx, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := e.st.CloseOpenMotion(ctx); err != nil {
		t.Fatal(err)
	}
	rows, _ := e.st.RecentMotion(ctx, 10)
	if len(rows) != 1 || rows[0].End == nil {
		t.Fatalf("%+v", rows)
	}
}

func TestMotionPermissions(t *testing.T) {
	e := newEnv(t)
	cam, _ := e.st.CreateCamera(context.Background(), store.Camera{Name: "c", MainURL: "rtsp://a"})
	v := e.addUser("v", "viewer")
	path := fmt.Sprintf("/api/cameras/%d/motion?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", cam.ID)
	if code, _ := e.do(v, "GET", path, nil); code != 403 {
		t.Errorf("viewer read camera motion: %d", code)
	}
	if code, _ := e.do(v, "GET", "/api/motion", nil); code != 403 {
		t.Errorf("viewer read recent motion: %d", code)
	}
	o := e.addUser("o", "operator")
	if code, body := e.do(o, "GET", path, nil); code != 200 || !strings.HasPrefix(body, "[]") {
		t.Errorf("operator: %d %s", code, body)
	}
	if code, _ := e.do(o, "GET", "/api/cameras/9999/motion?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z", nil); code != 404 {
		t.Errorf("missing camera: %d", code)
	}
}

func onvif_(topic, key, value string) onvif.Event {
	return onvif.Event{Topic: topic, Items: map[string]string{key: value}}
}
