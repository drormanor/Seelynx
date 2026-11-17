package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"golang.org/x/crypto/bcrypt"

	"seelynx/internal/store"
)

const testPass = "correct-horse-battery"

type env struct {
	t      *testing.T
	st     *store.Store
	srv    *httptest.Server
	server *Server
}

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s := &Server{
		Store: st, Monitor: &Monitor{}, Motion: &MotionWatcher{}, Log: slog.New(slog.DiscardHandler),
		Web:        fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}},
		applyMedia: func(context.Context, store.Camera) error { return nil },
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t, st, srv, s}
}

func (e *env) client() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar}
}

func (e *env) do(c *http.Client, method, path string, body any) (int, string) {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func (e *env) addUser(name, role string) *http.Client {
	e.t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPass), bcrypt.MinCost)
	if _, err := e.st.CreateUser(context.Background(), name, string(hash), role); err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	if code, body := e.do(c, "POST", "/api/login", credentials{Username: name, Password: testPass}); code != 200 {
		e.t.Fatalf("login: %d %s", code, body)
	}
	return c
}

func TestSetupOnlyOnce(t *testing.T) {
	e := newEnv(t)
	c := e.client()
	if code, _ := e.do(c, "POST", "/api/setup", credentials{Username: "root", Password: "short"}); code != 400 {
		t.Fatalf("weak password accepted: %d", code)
	}
	if code, body := e.do(c, "POST", "/api/setup", credentials{Username: "root", Password: testPass}); code != 200 {
		t.Fatalf("setup: %d %s", code, body)
	}
	if code, _ := e.do(e.client(), "POST", "/api/setup", credentials{Username: "evil", Password: testPass}); code != 409 {
		t.Fatalf("second setup not refused: %d", code)
	}
	if code, _ := e.do(c, "GET", "/api/users", nil); code != 200 {
		t.Fatalf("setup should sign the admin in: %d", code)
	}
}

func TestRoles(t *testing.T) {
	e := newEnv(t)
	cam, err := e.st.CreateCamera(context.Background(), store.Camera{Name: "c", MainURL: "rtsp://192.0.2.9/a", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	clients := map[string]*http.Client{
		"anon": e.client(), "viewer": e.addUser("v", "viewer"),
		"operator": e.addUser("o", "operator"), "admin": e.addUser("a", "admin"),
	}
	range1 := "?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z"
	routes := []struct {
		method, path string
		min          string
	}{
		{"GET", "/api/cameras", "viewer"},
		{"GET", fmt.Sprintf("/api/cameras/%d/recordings%s", cam.ID, range1), "operator"},
		{"GET", fmt.Sprintf("/api/cameras/%d/playback", cam.ID), "operator"},
		{"GET", "/api/system", "operator"},
		{"POST", "/api/cameras", "admin"},
		{"PUT", fmt.Sprintf("/api/cameras/%d", cam.ID), "admin"},
		{"DELETE", fmt.Sprintf("/api/cameras/%d", cam.ID), "admin"},
		{"GET", "/api/users", "admin"},
		{"POST", "/api/users", "admin"},
		{"POST", "/api/onvif/discover", "admin"},
		{"POST", "/api/onvif/profiles", "admin"},
	}
	for _, r := range routes {
		for who, c := range clients {
			if roleRank[who] >= roleRank[r.min] {
				continue // only the refusals are asserted; these handlers need a media engine
			}
			want := 403
			if who == "anon" {
				want = 401
			}
			if code, _ := e.do(c, r.method, r.path, nil); code != want {
				t.Errorf("%s %s as %s: got %d want %d", r.method, r.path, who, code, want)
			}
		}
	}
}

func TestCameraSecretsAreNotExposed(t *testing.T) {
	e := newEnv(t)
	_, err := e.st.CreateCamera(context.Background(), store.Camera{Name: "c", MainURL: "rtsp://192.0.2.9/a", SubURL: "rtsp://192.0.2.9/b", Username: "camuser", Password: "camsecret", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	_, admin := e.do(e.addUser("a", "admin"), "GET", "/api/cameras", nil)
	if strings.Contains(admin, "camsecret") {
		t.Error("camera password sent to admin")
	}
	if !strings.Contains(admin, `"has_password":true`) || !strings.Contains(admin, "192.0.2.9") {
		t.Errorf("admin view incomplete: %s", admin)
	}
	_, viewer := e.do(e.addUser("v", "viewer"), "GET", "/api/cameras", nil)
	for _, leak := range []string{"camsecret", "192.0.2.9", "camuser"} {
		if strings.Contains(viewer, leak) {
			t.Errorf("viewer can see %q: %s", leak, viewer)
		}
	}
	if _, users := e.do(e.addUser("b", "admin"), "GET", "/api/users", nil); strings.Contains(users, "$2a$") {
		t.Error("password hash exposed")
	}
}

func TestSignInLockout(t *testing.T) {
	e := newEnv(t)
	e.addUser("a", "viewer")
	for i := 0; i < maxFailures; i++ {
		if code, _ := e.do(e.client(), "POST", "/api/login", credentials{Username: "a", Password: "wrong"}); code != 401 {
			t.Fatalf("attempt %d: %d", i, code)
		}
	}
	if code, _ := e.do(e.client(), "POST", "/api/login", credentials{Username: "a", Password: testPass}); code != 429 {
		t.Fatalf("correct password should be blocked during lockout: %d", code)
	}
}

func TestUnknownUserLooksLikeWrongPassword(t *testing.T) {
	e := newEnv(t)
	e.addUser("a", "viewer")
	c1, b1 := e.do(e.client(), "POST", "/api/login", credentials{Username: "nobody", Password: "x"})
	c2, b2 := e.do(e.client(), "POST", "/api/login", credentials{Username: "a", Password: "x"})
	if c1 != 401 || c1 != c2 || b1 != b2 {
		t.Fatalf("responses differ: %d %s / %d %s", c1, b1, c2, b2)
	}
}

func TestLastAdminCannotBeRemoved(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("a", "admin")
	u, _ := e.st.UserByName(context.Background(), "a")
	if code, _ := e.do(c, "DELETE", fmt.Sprintf("/api/users/%d", u.ID), nil); code != 409 {
		t.Fatalf("last admin removed: %d", code)
	}
}

func TestLogoutEndsSession(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("a", "viewer")
	var saved []*http.Cookie
	u := e.srv.URL
	req, _ := http.NewRequest("GET", u, nil)
	saved = c.Jar.Cookies(req.URL)
	if code, _ := e.do(c, "POST", "/api/logout", nil); code != 204 {
		t.Fatalf("logout: %d", code)
	}
	req, _ = http.NewRequest("GET", u+"/api/cameras", nil)
	for _, k := range saved {
		req.AddCookie(k) // replay the old token
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("old token still works: %d", res.StatusCode)
	}
}

func TestClientRoutesServeApp(t *testing.T) {
	e := newEnv(t)
	if code, body := e.do(e.client(), "GET", "/some/route", nil); code != 200 || !strings.Contains(body, "app") {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := e.do(e.client(), "GET", "/api/nope", nil); code != 404 {
		t.Fatalf("unknown API route: %d", code)
	}
}
