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

	"seelynx/internal/drivers"
	"seelynx/internal/onvif"
	"seelynx/internal/rules"
	"seelynx/internal/store"
)

const crossedTopic = "tns1:RuleEngine/LineDetector/Crossed"

func fakeAI(t *testing.T, name string) (*httptest.Server, *string) {
	sent := new(string)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		*sent = string(raw)
		answer := `{"ok":true}`
		if strings.Contains(*sent, "LineDetector") {
			answer = `{"analytics":[{"topic":"` + crossedTopic + `","name":"` + name + `","description":"Someone crosses a line."}],"notes":""}`
		}
		text, _ := json.Marshal(answer)
		fmt.Fprintf(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":%s}]}`, text)
	}))
	t.Cleanup(srv.Close)
	return srv, sent
}

func TestAIKeyIsStoredButNeverReturned(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	save := func(in map[string]string) (int, string) { return e.do(c, "PUT", "/api/ai", in) }
	if code, body := save(map[string]string{"provider": "anthropic", "api_key": "sk-secret"}); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	_, view := e.do(c, "GET", "/api/ai", nil)
	if strings.Contains(view, "sk-secret") || !strings.Contains(view, `"has_key":true`) {
		t.Fatal(view)
	}
	// Saving without a key keeps it.
	if code, _ := save(map[string]string{"provider": "anthropic", "model": "claude-haiku-5-5"}); code != 204 {
		t.Fatal(code)
	}
	cfg, _ := e.st.AIService(context.Background())
	if cfg["api_key"] != "sk-secret" || cfg["model"] != "claude-haiku-5-5" {
		t.Fatalf("%v", cfg)
	}
}

func TestAIKeyNeverFollowsToAnotherAddress(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	e.do(c, "PUT", "/api/ai", map[string]string{"provider": "anthropic", "api_key": "sk-secret"})
	if code, _ := e.do(c, "PUT", "/api/ai", map[string]string{"provider": "anthropic", "base_url": "https://evil.example"}); code != 400 {
		t.Fatalf("moved the key to a new address: %d", code)
	}
	if code, _ := e.do(c, "PUT", "/api/ai", map[string]string{"provider": "openai", "model": "m", "base_url": "http://127.0.0.1:11434/v1"}); code != 204 {
		t.Fatal(code)
	}
	cfg, _ := e.st.AIService(context.Background())
	if cfg["api_key"] != "" {
		t.Fatalf("%v", cfg)
	}
}

func TestAISettingsValidation(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	bad := []map[string]string{
		{"provider": "skynet", "api_key": "k"},
		{"provider": "anthropic"},
		{"provider": "openai", "api_key": "k"},
		{"provider": "openai", "model": "m"},
		{"provider": "openai", "model": "m", "base_url": "ftp://x", "api_key": "k"},
	}
	for _, b := range bad {
		if code, _ := e.do(c, "PUT", "/api/ai", b); code != 400 {
			t.Errorf("%v: %d", b, code)
		}
	}
}

func TestAIServiceCanBeRemoved(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	e.do(c, "PUT", "/api/ai", map[string]string{"provider": "anthropic", "api_key": "k"})
	if code, _ := e.do(c, "PUT", "/api/ai", map[string]string{}); code != 204 {
		t.Fatal(code)
	}
	if cfg, _ := e.st.AIService(context.Background()); cfg != nil {
		t.Fatalf("%v", cfg)
	}
}

func TestDriverRoutesNeedSettings(t *testing.T) {
	e := newEnv(t)
	o := e.addUser("o", "operator")
	for _, route := range [][2]string{{"GET", "/api/ai"}, {"PUT", "/api/ai"}, {"POST", "/api/ai/test"}, {"POST", "/api/drivers/probe"}, {"POST", "/api/drivers/build"}} {
		if code, _ := e.do(o, route[0], route[1], map[string]any{}); code != 403 {
			t.Errorf("%s: %d", route[1], code)
		}
		if code, _ := e.do(e.client(), route[0], route[1], map[string]any{}); code != 401 {
			t.Errorf("anonymous %s: %d", route[1], code)
		}
	}
}

func TestBuildNeedsAnAIService(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	report := drivers.Report{Manufacturer: "Vicon", Model: "V1", Topics: []drivers.Topic{{Topic: crossedTopic}}}
	if code, body := e.do(c, "POST", "/api/drivers/build", report); code != 409 || !strings.Contains(body, "Connect an AI service") {
		t.Fatalf("%d %s", code, body)
	}
}

func TestBuildDraftsAndSavesADriverThatNamesEvents(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	srv, sent := fakeAI(t, "Line Cross")
	e.do(c, "PUT", "/api/ai", map[string]string{"provider": "anthropic", "api_key": "sk-secret", "base_url": srv.URL})

	if code, body := e.do(c, "POST", "/api/ai/test", nil); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	report := drivers.Report{Manufacturer: "Vicon", Model: "V1", Topics: []drivers.Topic{{Topic: crossedTopic, Fields: []string{"Value"}}}}
	code, body := e.do(c, "POST", "/api/drivers/build", report)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(*sent, "sk-secret") {
		t.Fatal("key in the prompt")
	}
	var draft drivers.Driver
	json.Unmarshal([]byte(body), &draft)
	if draft.Analytics[0].Name != "Line Cross" {
		t.Fatal(body)
	}
	if code, body := e.do(c, "POST", "/api/config/drivers", map[string]any{"name": "Vicon V1", "data": draft}); code != 200 && code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	if name, ok := e.server.AnalyticsName("vicon", "v1", crossedTopic); !ok || name != "Line Cross" {
		t.Fatalf("%q %v", name, ok)
	}
	if _, ok := e.server.AnalyticsName("Other", "v1", crossedTopic); ok {
		t.Fatal("named another model's event")
	}
}

func TestInvalidDriverIsRefused(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	code, _ := e.do(c, "POST", "/api/config/drivers", map[string]any{"name": "x", "data": drivers.Driver{Manufacturer: "x"}})
	if code != 400 {
		t.Fatal(code)
	}
}

func TestAlertsUseTheDriversName(t *testing.T) {
	var got []rules.Event
	w := &MotionWatcher{
		Events: func(e rules.Event) { got = append(got, e) },
		Names: func(make, model, topic string) (string, bool) {
			return "Line Cross", make == "Vicon" && model == "V1" && topic == crossedTopic
		},
	}
	c := store.Camera{ID: 1, Name: "Door"}
	event := onvif_(crossedTopic, "Value", "true")
	w.emitAnalytics(c, event)
	w.setModel(1, onvif.DeviceInfo{Manufacturer: "Vicon", Model: "V1"})
	w.emitAnalytics(c, event)
	if len(got) != 2 || got[0].Message != "Analytics event: Crossed" || got[1].Message != "Line Cross" || got[1].Topic != crossedTopic {
		t.Fatalf("%+v", got)
	}
}

func TestEveryFieldStyleOfRealCamerasFires(t *testing.T) {
	var got []rules.Event
	w := &MotionWatcher{Events: func(e rules.Event) { got = append(got, e) }}
	c := store.Camera{ID: 1, Name: "Door"}
	for _, e := range []onvif.Event{
		onvif_("tns1:RuleEngine/LineDetector/Crossed", "Value", "true"),
		onvif_("tns1:AudioAnalytics/Audio/DetectedSound", "State", "true"),
		onvif_("tns1:VideoAnalytics/tnsht:ObjectClass/Human", "state", "true"),
		onvif_("tns1:RuleEngine/FieldDetector/ObjectsInside", "IsInside", "true"),
	} {
		w.emitAnalytics(c, e)
	}
	if len(got) != 4 {
		t.Fatalf("%d events: %+v", len(got), got)
	}
	got = nil
	w.emitAnalytics(c, onvif_("tns1:VideoAnalytics/tnsht:ObjectClass/Human", "state", "false"))
	w.emitAnalytics(c, onvif_("tns1:RuleEngine/FieldDetector/ObjectsInside", "Rule", "true"))
	if len(got) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestTurningOnMotionRecordingMarksTheTime(t *testing.T) {
	var c store.Camera
	in := cameraInput{Name: "a", MainURL: "rtsp://a/x", Record: true}
	if !in.apply(httptest.NewRecorder(), &c) || c.MotionSince != 0 {
		t.Fatalf("marked without the mode on: %+v", c)
	}
	in.MotionOnly, in.PreSeconds, in.PostSeconds = true, 10, 30
	if !in.apply(httptest.NewRecorder(), &c) || c.MotionSince == 0 {
		t.Fatalf("not marked: %+v", c)
	}
	// Saving again with the mode still on must not move the mark.
	c.MotionSince = 12345
	in.apply(httptest.NewRecorder(), &c)
	if c.MotionSince != 12345 {
		t.Fatal("the mark moved")
	}
	// Turning it off and on again starts a new mark.
	in.MotionOnly = false
	in.apply(httptest.NewRecorder(), &c)
	in.MotionOnly = true
	in.apply(httptest.NewRecorder(), &c)
	if c.MotionSince == 12345 {
		t.Fatal("the old mark stayed")
	}
	// Motion only outside a schedule is marked too, and so is a change of schedule.
	in.MotionOnly, in.ScheduleID, in.OutsideMode = false, 7, store.OutsideMotion
	c.MotionSince = 12345
	in.apply(httptest.NewRecorder(), &c)
	if c.MotionSince == 12345 {
		t.Fatal("motion outside the schedule wasn't marked")
	}
	c.MotionSince = 12345
	in.ScheduleID = 8
	in.apply(httptest.NewRecorder(), &c)
	if c.MotionSince == 12345 {
		t.Fatal("a new schedule wasn't marked")
	}
	in.OutsideMode = 3
	if in.apply(httptest.NewRecorder(), &c) {
		t.Fatal("an unknown mode outside the schedule was accepted")
	}
	in.OutsideMode = store.OutsideOff
	in.PreSeconds = 301
	if in.apply(httptest.NewRecorder(), &c) {
		t.Fatal("301 seconds accepted")
	}
}

func TestPlainMotionUnderAnotherNameIsNotAnAnalytic(t *testing.T) {
	var got []rules.Event
	w := &MotionWatcher{Events: func(e rules.Event) { got = append(got, e) }}
	motion := onvif_("tns1:RuleEngine/MotionRegionDetector/Motion", "State", "true")
	w.emitAnalytics(store.Camera{ID: 1}, motion)
	if len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	if on, ok := motionSignal(motion); !on || !ok {
		t.Fatal("not read as motion")
	}
	if isAnalyticsTopic(motion) {
		t.Fatal("would be offered in a driver")
	}
	w.emitAnalytics(store.Camera{ID: 1}, onvif_("tns1:VideoAnalytics/tnsht:ObjectClass/Car", "Value", "true"))
	if len(got) != 1 {
		t.Fatal("a vehicle was ignored")
	}
}

func TestReportHoldsNoAddressesOrLogins(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var srv *httptest.Server
	subscribed, unsubscribed := 0, 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		wrap := func(s string) {
			fmt.Fprint(w, `<Envelope xmlns="http://www.w3.org/2003/05/soap-envelope"><Body>`+s+`</Body></Envelope>`)
		}
		switch {
		case strings.Contains(body, "GetDeviceInformation"):
			wrap(`<GetDeviceInformationResponse><Manufacturer>Vicon</Manufacturer><Model>V1</Model><FirmwareVersion>1.2</FirmwareVersion><SerialNumber>SN-SECRET</SerialNumber></GetDeviceInformationResponse>`)
		case strings.Contains(body, "GetCapabilities"):
			if strings.Contains(body, "<Category>Events") {
				wrap(`<GetCapabilitiesResponse><Capabilities><Events><XAddr>` + srv.URL + `/events</XAddr></Events></Capabilities></GetCapabilitiesResponse>`)
				return
			}
			wrap(`<GetCapabilitiesResponse><Capabilities><Media><XAddr>` + srv.URL + `/media</XAddr></Media><Events><XAddr>` + srv.URL + `/events</XAddr></Events></Capabilities></GetCapabilitiesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			wrap(`<GetProfilesResponse><Profiles token="p"><Name>Main</Name><VideoEncoderConfiguration><Encoding>H264</Encoding><Resolution><Width>1920</Width><Height>1080</Height></Resolution></VideoEncoderConfiguration></Profiles></GetProfilesResponse>`)
		case strings.Contains(body, "GetStreamUri"):
			wrap(`<GetStreamUriResponse><MediaUri><Uri>rtsp://admin:pw@192.0.2.41/s1</Uri></MediaUri></GetStreamUriResponse>`)
		case strings.Contains(body, "GetSnapshotUri"):
			wrap(`<GetSnapshotUriResponse><MediaUri><Uri>http://192.0.2.41/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		case strings.Contains(body, "CreatePullPointSubscription"), strings.Contains(body, "Subscribe>"):
			subscribed++
			wrap(`<CreatePullPointSubscriptionResponse><SubscriptionReference><Address>` + srv.URL + `/sub</Address></SubscriptionReference></CreatePullPointSubscriptionResponse>`)
		case strings.Contains(body, "PullMessages"):
			wrap(`<PullMessagesResponse>
				<NotificationMessage><Topic>tns1:RuleEngine/LineDetector/Crossed</Topic><Message><Message UtcTime="t" PropertyOperation="Initialized"><Data><SimpleItem Name="Value" Value="false"/></Data></Message></Message></NotificationMessage>
				<NotificationMessage><Topic>tns1:VideoSource/MotionAlarm</Topic><Message><Message UtcTime="t"><Data><SimpleItem Name="State" Value="false"/></Data></Message></Message></NotificationMessage>
				<NotificationMessage><Topic>tns1:Monitoring/ProcessorUsage</Topic><Message><Message UtcTime="t"><Data><SimpleItem Name="Value" Value="1"/></Data></Message></Message></NotificationMessage>
			</PullMessagesResponse>`)
		case strings.Contains(body, "Unsubscribe"):
			unsubscribed++
			wrap(`<UnsubscribeResponse/>`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	e.server.Motion = &MotionWatcher{Address: func(store.Camera) string { return srv.URL + "/device" }}
	cam := store.Camera{Name: "a", MainURL: "rtsp://192.0.2.41/s1", Username: "admin", Password: "pw"}
	r, err := e.server.probe(ctx, cam)
	if err != nil {
		t.Fatal(err)
	}
	if r.Manufacturer != "Vicon" || r.Model != "V1" || r.Firmware != "1.2" || !r.Snapshot || len(r.Profiles) != 1 {
		t.Fatalf("%+v", r)
	}
	if len(r.Topics) != 1 || r.Topics[0].Topic != crossedTopic || r.Topics[0].Fields[0] != "Value" {
		t.Fatalf("%+v", r.Topics)
	}
	raw, _ := json.Marshal(r)
	for _, leak := range []string{"192.0.2.41", "admin", "pw@", "SN-SECRET", "rtsp"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("report contains %q: %s", leak, raw)
		}
	}
	if subscribed != 1 || unsubscribed != 1 {
		t.Errorf("subscribed %d, unsubscribed %d", subscribed, unsubscribed)
	}
}
