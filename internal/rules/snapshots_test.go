package rules

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image/jpeg"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"seelynx/internal/store"
)

type upload struct {
	method, path, filename, fileType string
	fields                           map[string]string
	file                             []byte
	headers                          http.Header
	body                             []byte
}

// uploads records requests, reading multipart bodies into fields and a file.
func uploads(t *testing.T, status int) (*httptest.Server, func() upload) {
	var mu sync.Mutex
	var last upload
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		u := upload{method: r.Method, path: r.URL.Path, fields: map[string]string{}, headers: r.Header, body: body}
		if mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err == nil && mt == "multipart/form-data" {
			mr := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			for {
				p, err := mr.NextPart()
				if err != nil {
					break
				}
				data, _ := io.ReadAll(p)
				if p.FileName() != "" {
					u.filename, u.fileType, u.file = p.FormName(), p.Header.Get("Content-Type"), data
				} else {
					u.fields[p.FormName()] = string(data)
				}
			}
		}
		mu.Lock()
		last = u
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() upload { mu.Lock(); defer mu.Unlock(); return last }
}

var picture = []byte("\xFF\xD8\xFFpicture-bytes")

func withPicture() Message {
	m := sample
	m.Snapshot = picture
	return m
}

func TestPicturesReachEachService(t *testing.T) {
	srv, got := uploads(t, 200)
	saved := Endpoints
	defer func() { Endpoints = saved }()
	Endpoints.Telegram, Endpoints.Pushover = srv.URL, srv.URL
	ctx := context.Background()

	if err := Send(ctx, store.Connection{Type: "telegram", Config: map[string]string{"token": "1:a", "chat_id": "42"}}, withPicture()); err != nil {
		t.Fatal(err)
	}
	g := got()
	if g.path != "/bot1:a/sendPhoto" || g.filename != "photo" || g.fileType != "image/jpeg" || !bytes.Equal(g.file, picture) ||
		g.fields["chat_id"] != "42" || !strings.Contains(g.fields["caption"], "Motion at the door") {
		t.Errorf("telegram %+v", g)
	}

	if err := Send(ctx, store.Connection{Type: "discord", Config: map[string]string{"url": srv.URL + "/d"}}, withPicture()); err != nil {
		t.Fatal(err)
	}
	g = got()
	if g.filename != "files[0]" || !bytes.Equal(g.file, picture) || !strings.Contains(g.fields["payload_json"], "Motion at the door") {
		t.Errorf("discord %+v", g)
	}

	if err := Send(ctx, store.Connection{Type: "pushover", Config: map[string]string{"token": "tok", "user": "usr"}}, withPicture()); err != nil {
		t.Fatal(err)
	}
	g = got()
	if g.filename != "attachment" || !bytes.Equal(g.file, picture) || g.fields["token"] != "tok" || g.fields["user"] != "usr" || g.fields["priority"] != "1" {
		t.Errorf("pushover %+v", g)
	}

	if err := Send(ctx, store.Connection{Type: "ntfy", Config: map[string]string{"url": srv.URL + "/topic"}}, withPicture()); err != nil {
		t.Fatal(err)
	}
	g = got()
	if g.method != http.MethodPut || !bytes.Equal(g.body, picture) || g.headers.Get("Filename") != "snapshot.jpg" || g.headers.Get("Message") != "Motion at the door" {
		t.Errorf("ntfy %+v", g)
	}

	if err := Send(ctx, store.Connection{Type: "webhook", Config: map[string]string{"url": srv.URL + "/w"}}, withPicture()); err != nil {
		t.Fatal(err)
	}
	var hook struct {
		Text, Snapshot string
	}
	if err := json.Unmarshal(got().body, &hook); err != nil {
		t.Fatal(err)
	}
	if hook.Text != "Motion at the door" || hook.Snapshot != base64.StdEncoding.EncodeToString(picture) {
		t.Errorf("webhook %+v", hook)
	}
}

func TestMQTTNeverCarriesThePicture(t *testing.T) {
	raw, _ := json.Marshal(withPicture())
	if strings.Contains(string(raw), "napshot") {
		t.Fatal(string(raw))
	}
}

func TestEmailAttachesThePicture(t *testing.T) {
	f, host, port := mailServer(t, false, false)
	job := smtpJob{host: host, port: port, from: "a@example.com", to: []string{"b@example.com"}}
	m := withPicture()
	m.Snapshot = bytes.Repeat(picture, 40) // long enough to need several lines
	if err := deliverSMTP(context.Background(), job, m); err != nil {
		t.Fatal(err)
	}
	data := f.snapshot().data
	head, body, _ := strings.Cut(data, "\r\n\r\n")
	mt, params, err := mime.ParseMediaType(strings.TrimSpace(after(head, "Content-Type:")))
	if err != nil || mt != "multipart/mixed" {
		t.Fatalf("%v %q", err, head)
	}
	r := multipart.NewReader(strings.NewReader(body), params["boundary"])
	text, err := r.NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(text); !strings.Contains(string(b), "Motion at the door") {
		t.Errorf("text %q", b)
	}
	part, err := r.NextRawPart()
	if err != nil {
		t.Fatal(err)
	}
	if part.FileName() != "snapshot.jpg" || part.Header.Get("Content-Type") != `image/jpeg; name="snapshot.jpg"` {
		t.Errorf("%v", part.Header)
	}
	raw, _ := io.ReadAll(part)
	for _, line := range strings.Split(string(raw), "\r\n") {
		if len(line) > 76 {
			t.Fatalf("line of %d characters", len(line))
		}
	}
	got, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if err != nil || !bytes.Equal(got, m.Snapshot) {
		t.Fatalf("%v %d", err, len(got))
	}
}

func after(s, prefix string) string {
	for _, line := range strings.Split(s, "\r\n") {
		if v, ok := strings.CutPrefix(line, prefix); ok {
			return v
		}
	}
	return ""
}

func TestMicrosoftAttachesThePicture(t *testing.T) {
	_, sent := microsoftServer(t, 202)
	if err := Send(context.Background(), store.Connection{Type: "microsoft365", Config: microsoftConfig("good-secret").Config}, withPicture()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(*sent, `"contentBytes":"`+base64.StdEncoding.EncodeToString(picture)+`"`) || !strings.Contains(*sent, "#microsoft.graph.fileAttachment") {
		t.Fatal(*sent)
	}
	// A picture Graph would refuse is left out, so the alert still gets through.
	m := withPicture()
	m.Snapshot = make([]byte, graphMaxAttachment+1)
	if err := Send(context.Background(), microsoftConfig("good-secret"), m); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(*sent, "attachments") {
		t.Fatal("oversized picture was attached")
	}
}

func snapshotEngine(t *testing.T, shot func(context.Context, int64, time.Time) ([]byte, error)) (*Engine, *store.Store) {
	e, st := engine(t)
	e.Snapshot, e.SnapshotDir = shot, filepath.Join(t.TempDir(), SnapshotFolder)
	return e, st
}

func TestAlarmGetsItsPictureAndServicesReceiveIt(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { calls.Add(1); return picture, nil })
	srv, got := uploads(t, 200)
	c, _ := st.SaveConnection(ctx, store.Connection{Name: "d", Type: "discord", Config: map[string]string{"url": srv.URL + "/d"}})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "low", Snapshot: true, Connections: []int64{c.ID}})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "low", Snapshot: true})

	if err := e.Process(ctx, Event{Type: TriggerMotion, CameraID: 1, CameraName: "Door", Message: "Motion", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if !bytes.Equal(got().file, picture) {
		t.Fatalf("%+v", got())
	}
	if calls.Load() != 1 {
		t.Errorf("%d pictures taken for one event", calls.Load())
	}
	list, _ := st.Alarms(ctx, 10)
	if len(list) != 2 || !list[0].HasSnapshot || list[0].Snapshot != list[1].Snapshot {
		t.Fatalf("%+v", list)
	}
	saved, err := os.ReadFile(filepath.Join(e.SnapshotDir, list[0].Snapshot))
	if err != nil || !bytes.Equal(saved, picture) {
		t.Fatalf("%v %q", err, saved)
	}
}

func TestSlowCameraDoesNotHoldUpOtherEvents(t *testing.T) {
	ctx := context.Background()
	release := make(chan struct{})
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { <-release; return picture, nil })
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "low", Snapshot: true})
	done := make(chan error, 1)
	go func() {
		done <- e.Process(ctx, Event{Type: TriggerMotion, CameraID: 1, Message: "Motion", At: time.Now()})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("processing waited for the camera")
	}
	// The alarm is on screen before the camera answers.
	if list, _ := st.Alarms(ctx, 10); len(list) != 1 || list[0].HasSnapshot {
		t.Fatalf("%+v", list)
	}
	close(release)
	e.Wait()
	if list, _ := st.Alarms(ctx, 10); !list[0].HasSnapshot {
		t.Fatal("picture never joined the alarm")
	}
}

func TestFailedSnapshotStillSendsTheAlert(t *testing.T) {
	ctx := context.Background()
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { return nil, errors.New("camera busy") })
	srv, got := capture(t)
	c, _ := st.SaveConnection(ctx, store.Connection{Name: "s", Type: "slack", Config: map[string]string{"url": srv.URL + "/s"}})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "low", Snapshot: true, Message: "Seen", Connections: []int64{c.ID}})
	e.Process(ctx, Event{Type: TriggerMotion, CameraID: 1, CameraName: "Door", Message: "Motion", At: time.Now()})
	e.Wait()
	if !strings.Contains(got().body, "Seen") {
		t.Fatal("alert lost")
	}
	if list, _ := st.Alarms(ctx, 10); len(list) != 1 || list[0].HasSnapshot {
		t.Fatalf("%+v", list)
	}
	events, _ := st.Events(ctx, 10)
	if len(events) != 1 || !strings.Contains(events[0].Message, "camera busy") {
		t.Fatalf("%+v", events)
	}
}

func TestRefusedPictureFallsBackToText(t *testing.T) {
	ctx := context.Background()
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { return picture, nil })
	var mu sync.Mutex
	var texts int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
			w.WriteHeader(413)
			return
		}
		mu.Lock()
		texts++
		mu.Unlock()
	}))
	defer srv.Close()
	c, _ := st.SaveConnection(ctx, store.Connection{Name: "d", Type: "discord", Config: map[string]string{"url": srv.URL}})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Priority: "low", Snapshot: true, Connections: []int64{c.ID}})
	e.Process(ctx, Event{Type: TriggerMotion, CameraID: 1, CameraName: "Door", Message: "Motion", At: time.Now()})
	e.Wait()
	mu.Lock()
	defer mu.Unlock()
	if texts != 1 {
		t.Fatalf("%d text messages", texts)
	}
	events, _ := st.Events(ctx, 10)
	if len(events) != 1 || !strings.Contains(events[0].Message, "without the picture") {
		t.Fatalf("%+v", events)
	}
}

func TestNoPictureWhereItCantWork(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { calls.Add(1); return picture, nil })
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerOffline, Alarm: true, Priority: "low", Snapshot: true})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerDisk, Alarm: true, Priority: "low", Snapshot: true})
	addRule(t, st, Rule{Enabled: true, Trigger: TriggerMotion, Alarm: true, Priority: "low"})
	e.Process(ctx, Event{Type: TriggerOffline, CameraID: 1, Message: "Offline", At: time.Now()})
	e.Process(ctx, Event{Type: TriggerDisk, Message: "Low", At: time.Now()})
	e.Process(ctx, Event{Type: TriggerMotion, CameraID: 1, Message: "Motion", At: time.Now()})
	e.Wait()
	if calls.Load() != 0 {
		t.Fatalf("%d pictures", calls.Load())
	}
}

func TestOutsideEventBringsItsRulesCameraPicture(t *testing.T) {
	ctx := context.Background()
	var asked atomic.Int64
	e, st := snapshotEngine(t, func(_ context.Context, id int64, _ time.Time) ([]byte, error) { asked.Store(id); return picture, nil })
	cam, err := st.CreateCamera(ctx, store.Camera{Name: "Back door", MainURL: "rtsp://a"})
	if err != nil {
		t.Fatal(err)
	}
	rule := addRule(t, st, Rule{Enabled: true, Trigger: TriggerExternal, Token: "x", Alarm: true, Priority: "high", Snapshot: true, CameraID: cam.ID, Message: "{event} at {camera}"})
	if err := e.Process(ctx, Event{Type: TriggerExternal, RuleID: rule, Message: "Door forced", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	if asked.Load() != cam.ID {
		t.Fatalf("asked camera %d", asked.Load())
	}
	list, _ := st.Alarms(ctx, 10)
	if len(list) != 1 || list[0].CameraID != cam.ID || list[0].CameraName != "Back door" || list[0].Message != "Door forced at Back door" || !list[0].HasSnapshot {
		t.Fatalf("%+v", list)
	}
}

func TestOutsideEventStillFiresIfItsCameraIsGone(t *testing.T) {
	ctx := context.Background()
	var calls atomic.Int32
	e, st := snapshotEngine(t, func(context.Context, int64, time.Time) ([]byte, error) { calls.Add(1); return picture, nil })
	rule := addRule(t, st, Rule{Enabled: true, Trigger: TriggerExternal, Token: "x", Alarm: true, Priority: "high", Snapshot: true, CameraID: 999, Message: "{event}"})
	if err := e.Process(ctx, Event{Type: TriggerExternal, RuleID: rule, Message: "Door forced", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.Wait()
	list, _ := st.Alarms(ctx, 10)
	if len(list) != 1 || list[0].CameraID != 0 || calls.Load() != 0 {
		t.Fatalf("%+v, %d pictures", list, calls.Load())
	}
}

func TestPurgeSnapshotsByAgeAndSize(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	add := func(name string, age time.Duration, size int) {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
		os.Chtimes(p, now.Add(-age), now.Add(-age))
	}
	add("expired.jpg", 40*24*time.Hour, 10)
	add("old.jpg", 3*time.Hour, 100)
	add("middle.jpg", 2*time.Hour, 100)
	add("new.jpg", time.Hour, 100)
	add("notes.txt", 40*24*time.Hour, 10)
	if err := PurgeSnapshots(dir, now.Add(-SnapshotKeep), 250); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{"expired.jpg": false, "old.jpg": false, "middle.jpg": true, "new.jpg": true, "notes.txt": true} {
		if _, err := os.Stat(filepath.Join(dir, name)); (err == nil) != want {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := PurgeSnapshots(filepath.Join(dir, "missing"), now, 1); err != nil {
		t.Fatal(err)
	}
}

func TestSamplePictureIsAValidJPEG(t *testing.T) {
	if _, err := jpeg.Decode(bytes.NewReader(SamplePicture())); err != nil {
		t.Fatal(err)
	}
}

func TestEverySenderOfPicturesHandlesOne(t *testing.T) {
	saved := Endpoints
	defer func() { Endpoints = saved }()
	dead := "http://127.0.0.1:1"
	Endpoints.Telegram, Endpoints.Pushover, Endpoints.Gmail, Endpoints.MicrosoftLogin = dead, dead, "127.0.0.1:1", dead
	for _, ty := range Types {
		if !SendsImages(ty.ID) {
			continue
		}
		cfg := map[string]string{"host": "127.0.0.1", "port": "1", "url": dead}
		if err := Send(context.Background(), store.Connection{Type: ty.ID, Config: cfg}, withPicture()); err != nil && strings.Contains(err.Error(), "unknown connection type") {
			t.Errorf("%s", ty.ID)
		}
	}
}
