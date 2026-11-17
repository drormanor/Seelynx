package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"seelynx/internal/store"
)

const secretWord = "kept-on-the-server"

func TestDefaultLoginNeverReachesTheBrowser(t *testing.T) {
	e := newEnv(t)
	admin := e.addUser("a", "admin")
	save := map[string]any{"username": "installer", "password": secretWord}
	if code, body := e.do(admin, "PUT", "/api/camera-login", save); code != 200 || strings.Contains(body, secretWord) {
		t.Fatalf("save: %d %s", code, body)
	}
	code, body := e.do(admin, "GET", "/api/camera-login", nil)
	if code != 200 || !strings.Contains(body, `"username":"installer"`) || !strings.Contains(body, `"has_password":true`) || strings.Contains(body, secretWord) {
		t.Fatalf("read: %d %s", code, body)
	}
	code, body = e.do(admin, "POST", "/api/cameras", map[string]any{"name": "a", "main_url": "rtsp://a", "use_default_login": true})
	if code != 201 || strings.Contains(body, secretWord) {
		t.Fatalf("add: %d %s", code, body)
	}
	if _, list := e.do(admin, "GET", "/api/cameras", nil); strings.Contains(list, secretWord) {
		t.Fatal("password in the camera list")
	}
	events, err := e.st.Events(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	logged := false
	for _, ev := range events {
		logged = logged || ev.Message == "Default camera login changed by a"
		if strings.Contains(ev.Message, secretWord) {
			t.Errorf("password in the event log: %s", ev.Message)
		}
	}
	if !logged {
		t.Errorf("not logged: %+v", events)
	}
}

func TestDefaultLoginIsEncryptedOnDisk(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := st.UseKey(ctx, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveDefaultLogin(ctx, store.Login{Username: "installer", Password: secretWord}); err != nil {
		t.Fatal(err)
	}
	got, err := st.DefaultLogin(ctx)
	if err != nil || got.Password != secretWord {
		t.Fatalf("%+v %v", got, err)
	}
	st.Close()
	for _, f := range []string{path, path + "-wal"} {
		raw, err := os.ReadFile(f)
		if err == nil && strings.Contains(string(raw), secretWord) {
			t.Errorf("%s holds the password in plain text", f)
		}
	}
}

func TestNewCamerasCanUseTheDefaultLogin(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.addUser("a", "admin")
	add := func(extra map[string]any) store.Camera {
		t.Helper()
		in := map[string]any{"name": "cam", "main_url": "rtsp://a"}
		for k, v := range extra {
			in[k] = v
		}
		if code, body := e.do(admin, "POST", "/api/cameras", in); code != 201 {
			t.Fatalf("%d %s", code, body)
		}
		cams, _ := e.st.Cameras(ctx)
		return cams[len(cams)-1]
	}

	if code, _ := e.do(admin, "POST", "/api/cameras", map[string]any{"name": "x", "main_url": "rtsp://a", "use_default_login": true}); code != 400 {
		t.Errorf("no default set: %d", code)
	}
	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "installer", "password": secretWord})

	first, err := e.st.Camera(ctx, add(map[string]any{"use_default_login": true}).ID)
	if err != nil || first.Username != "installer" || first.Password != secretWord {
		t.Errorf("default not used: %q %q %v", first.Username, first.Password, err)
	}
	own, _ := e.st.Camera(ctx, add(map[string]any{"username": "own", "password": "own-password"}).ID)
	if own.Username != "own" || own.Password != "own-password" {
		t.Errorf("own login replaced: %q", own.Username)
	}
	none, _ := e.st.Camera(ctx, add(nil).ID)
	if none.Username != "" || none.Password != "" {
		t.Errorf("a camera with no login was given one: %q", none.Username)
	}

	// Overriding one camera, or changing the default, leaves the others alone.
	e.do(admin, "PUT", "/api/cameras/"+itoa(first.ID), map[string]any{"name": "cam", "main_url": "rtsp://a", "username": "changed", "password": "changed-password"})
	changed, _ := e.st.Camera(ctx, first.ID)
	if changed.Username != "changed" || changed.Password != "changed-password" {
		t.Errorf("override: %q", changed.Username)
	}
	second, _ := e.st.Camera(ctx, add(map[string]any{"use_default_login": true}).ID)
	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "other", "password": "other-password"})
	kept, _ := e.st.Camera(ctx, second.ID)
	if kept.Username != "installer" || kept.Password != secretWord {
		t.Errorf("an added camera changed with the default: %q", kept.Username)
	}
}

func TestDefaultLoginRules(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.addUser("a", "admin")
	if code, _ := e.do(e.client(), "GET", "/api/camera-login", nil); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	op := e.addUser("o", "operator")
	if code, _ := e.do(op, "GET", "/api/camera-login", nil); code != 403 {
		t.Errorf("operator read: %d", code)
	}
	if code, _ := e.do(op, "PUT", "/api/camera-login", map[string]any{"username": "x"}); code != 403 {
		t.Errorf("operator save: %d", code)
	}
	if code, _ := e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": " ", "password": "p"}); code != 400 {
		t.Errorf("password alone: %d", code)
	}

	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "installer", "password": secretWord})
	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "renamed"}) // no password keeps it
	if l, _ := e.st.DefaultLogin(ctx); l.Username != "renamed" || l.Password != secretWord {
		t.Errorf("keep: %+v", l)
	}
	if code, _ := e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": ""}); code != 400 {
		t.Errorf("removing the name but keeping the password: %d", code)
	}
	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "", "password": ""})
	if l, _ := e.st.DefaultLogin(ctx); l.Set() || l.Password != "" {
		t.Errorf("remove: %+v", l)
	}
}

func TestReadingStreamsCanUseTheDefaultLogin(t *testing.T) {
	var mu sync.Mutex
	var asked []string
	cam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		asked = append(asked, string(b))
		mu.Unlock()
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer cam.Close()

	e := newEnv(t)
	admin := e.addUser("a", "admin")
	in := map[string]any{"xaddr": cam.URL + "/onvif/device_service", "use_default_login": true}
	if code, _ := e.do(admin, "POST", "/api/onvif/profiles", in); code != 400 {
		t.Errorf("no default set: %d", code)
	}
	e.do(admin, "PUT", "/api/camera-login", map[string]any{"username": "installer", "password": secretWord})
	e.do(admin, "POST", "/api/onvif/profiles", in)

	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(asked, "\n")
	if !strings.Contains(all, "installer") {
		t.Errorf("the camera wasn't sent the default login: %s", all)
	}
	if strings.Contains(all, secretWord) {
		t.Error("the password went to the camera in plain text")
	}
}
