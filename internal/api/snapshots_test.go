package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"seelynx/internal/rules"
	"seelynx/internal/store"
)

func TestAlarmPictureFollowsPermissions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.server.DataDir = t.TempDir()
	dir := filepath.Join(e.server.DataDir, rules.SnapshotFolder)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.jpg"), []byte("picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	a, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})
	b, _ := e.st.CreateCamera(ctx, store.Camera{Name: "b", MainURL: "rtsp://b"})
	id, _ := e.st.AddAlarm(ctx, store.Alarm{At: time.Now(), Rule: "r", CameraID: a.ID, Message: "m", Priority: "low"})
	if err := e.st.SetAlarmSnapshot(ctx, id, "a.jpg"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/alarms/%d/snapshot", id)

	operator := e.addUser("o", "operator")
	if code, body := e.do(operator, "GET", path, nil); code != 200 || body != "picture" {
		t.Fatalf("%d %s", code, body)
	}
	if _, list := e.do(operator, "GET", "/api/alarms", nil); !strings.Contains(list, `"has_snapshot":true`) {
		t.Fatal(list)
	}
	if code, _ := e.do(e.addUser("v", "viewer"), "GET", path, nil); code != 403 {
		t.Errorf("viewer: %d", code)
	}
	if code, _ := e.do(e.client(), "GET", path, nil); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	// System status alone doesn't include seeing video.
	healthRole, _ := e.st.SaveRole(ctx, store.Role{Name: "health", Permissions: []string{store.PermSystem}, AllCameras: true})
	health := e.userWith(store.User{Username: "health", RoleID: healthRole.ID})
	if code, _ := e.do(health, "GET", path, nil); code != 404 {
		t.Errorf("health role: %d", code)
	}
	if _, list := e.do(health, "GET", "/api/alarms", nil); strings.Contains(list, `"has_snapshot":true`) {
		t.Error(list)
	}
	// A role limited to other cameras can't reach it either.
	otherRole, _ := e.st.SaveRole(ctx, store.Role{Name: "other", Permissions: []string{store.PermSystem, store.PermLive}, CameraIDs: []int64{b.ID}})
	if code, _ := e.do(e.userWith(store.User{Username: "other", RoleID: otherRole.ID}), "GET", path, nil); code != 404 {
		t.Errorf("other camera: %d", code)
	}
	// A picture removed by the size limit is simply gone.
	os.Remove(filepath.Join(dir, "a.jpg"))
	if code, _ := e.do(operator, "GET", path, nil); code != 404 {
		t.Errorf("deleted: %d", code)
	}
}

func TestCaptureRemembersTheSnapshotAddress(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	var lookups, fetches atomic.Int32
	var broken atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			fetches.Add(1)
			if broken.Load() && r.URL.Path == "/old.jpg" {
				w.WriteHeader(404)
				return
			}
			w.Write([]byte("\xFF\xD8\xFFpicture"))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		body := string(raw)
		wrap := func(s string) {
			fmt.Fprint(w, `<Envelope xmlns="http://www.w3.org/2003/05/soap-envelope"><Body>`+s+`</Body></Envelope>`)
		}
		switch {
		case strings.Contains(body, "GetCapabilities"):
			wrap(`<GetCapabilitiesResponse><Capabilities><Media><XAddr>` + srv.URL + `/media</XAddr></Media></Capabilities></GetCapabilitiesResponse>`)
		case strings.Contains(body, "GetProfiles"):
			wrap(`<GetProfilesResponse><Profiles token="p"/></GetProfilesResponse>`)
		case strings.Contains(body, "GetSnapshotUri"):
			lookups.Add(1)
			name := "old"
			if broken.Load() {
				name = "new"
			}
			wrap(`<GetSnapshotUriResponse><MediaUri><Uri>` + srv.URL + "/" + name + `.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	e.server.Motion = &MotionWatcher{Address: func(store.Camera) string { return srv.URL + "/device" }}
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})

	for range 3 {
		if _, err := e.server.CaptureSnapshot(ctx, cam.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	if lookups.Load() != 1 || fetches.Load() != 3 {
		t.Fatalf("lookups %d, fetches %d", lookups.Load(), fetches.Load())
	}
	// When the remembered address stops working, it's looked up again.
	broken.Store(true)
	if _, err := e.server.CaptureSnapshot(ctx, cam.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 2 {
		t.Fatalf("lookups %d", lookups.Load())
	}
}

func TestTestButtonSendsASamplePicture(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	var snapshot atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		snapshot.Store(strings.Contains(string(b), `"snapshot":"`))
	}))
	defer srv.Close()
	conn, _ := e.st.SaveConnection(context.Background(), store.Connection{Name: "w", Type: "webhook", Config: map[string]string{"url": srv.URL}})
	if code, body := e.do(c, "POST", fmt.Sprintf("/api/connections/%d/test", conn.ID), nil); code != 204 {
		t.Fatalf("%d %s", code, body)
	}
	if !snapshot.Load() {
		t.Fatal("no picture in the test")
	}
}
