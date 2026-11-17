package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/store"
)

func TestScanShowsCamerasAlreadyAdded(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	old := discoverDevices
	t.Cleanup(func() { discoverDevices = old })
	discoverDevices = func(context.Context, time.Duration) ([]onvif.Device, error) {
		return []onvif.Device{
			{XAddr: "http://192.0.2.5/onvif/device_service", Host: "192.0.2.5", Name: "Dome"},
			{XAddr: "http://192.0.2.6/onvif/device_service", Host: "192.0.2.6", Name: "Bullet"},
		}, nil
	}
	e.st.CreateCamera(ctx, store.Camera{Name: "Front door", MainURL: "rtsp://192.0.2.5:554/main"})
	e.st.CreateCamera(ctx, store.Camera{Name: "Front door wide", MainURL: "rtsp://user@192.0.2.5/second"})
	e.st.CreateCamera(ctx, store.Camera{Name: "Elsewhere", MainURL: "rtsp://192.0.2.50/main"})

	code, body := e.do(e.addUser("root", "admin"), "POST", "/api/onvif/discover", nil)
	if code != 200 {
		t.Fatal(code, body)
	}
	if !strings.Contains(body, `"name":"Dome","cameras":["Front door","Front door wide"]`) {
		t.Errorf("added camera not marked: %s", body)
	}
	if strings.Contains(body, `"name":"Bullet","cameras"`) || strings.Contains(body, "Elsewhere") {
		t.Errorf("new camera marked as added: %s", body)
	}
}

func TestCameraPictureIsKeptAndReused(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.server.DataDir = t.TempDir()
	var fetches atomic.Int32
	var down atomic.Bool
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if down.Load() {
			w.WriteHeader(503)
			return
		}
		if r.Method == http.MethodGet {
			n := fetches.Add(1)
			fmt.Fprintf(w, "\xFF\xD8\xFFpicture %d", n)
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
			wrap(`<GetSnapshotUriResponse><MediaUri><Uri>` + srv.URL + `/snap.jpg</Uri></MediaUri></GetSnapshotUriResponse>`)
		default:
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	e.server.Motion = &MotionWatcher{Address: func(store.Camera) string { return srv.URL + "/device" }}
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a", Enabled: true})
	path := fmt.Sprintf("/api/cameras/%d/picture", cam.ID)
	admin := e.addUser("root", "admin")

	code, body := e.do(admin, "GET", path, nil)
	if code != 200 || !strings.HasSuffix(body, "picture 1") {
		t.Fatalf("first: %d %q", code, body)
	}
	if _, err := os.Stat(e.server.picturePath(cam.ID)); err != nil {
		t.Fatal("picture not kept:", err)
	}
	// Under a minute old, it's reused; asking for a fresh one takes a new one.
	if _, body := e.do(admin, "GET", path, nil); !strings.HasSuffix(body, "picture 1") {
		t.Errorf("not reused: %q", body)
	}
	if _, body := e.do(admin, "GET", path+"?fresh=1", nil); !strings.HasSuffix(body, "picture 2") {
		t.Errorf("not fresh: %q", body)
	}
	// A camera that stops answering still shows its last picture.
	down.Store(true)
	if code, body := e.do(admin, "GET", path+"?fresh=1", nil); code != 200 || !strings.HasSuffix(body, "picture 2") {
		t.Errorf("offline: %d %q", code, body)
	}
	// A role that only sees system health doesn't get pictures.
	role, _ := e.st.SaveRole(ctx, store.Role{Name: "health", Permissions: []string{store.PermSystem}})
	if code, _ := e.do(e.userWith(store.User{Username: "health", RoleID: role.ID}), "GET", path, nil); code != 403 && code != 404 {
		t.Errorf("health role: %d", code)
	}
	// Without a kept picture and with the camera down, there's nothing to show.
	e.server.forgetPicture(cam.ID)
	if code, _ := e.do(admin, "GET", path, nil); code != 404 {
		t.Errorf("forgotten: %d", code)
	}
}

func TestDisabledCameraIsNotAskedForAPicture(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.server.DataDir = t.TempDir()
	var asked atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { asked.Store(true) }))
	defer srv.Close()
	e.server.Motion = &MotionWatcher{Address: func(store.Camera) string { return srv.URL }}
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})
	if code, _ := e.do(e.addUser("root", "admin"), "GET", fmt.Sprintf("/api/cameras/%d/picture", cam.ID), nil); code != 404 || asked.Load() {
		t.Errorf("code %d, asked %v", code, asked.Load())
	}
}
