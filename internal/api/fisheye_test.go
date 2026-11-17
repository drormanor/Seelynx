package api

import (
	"context"
	"strings"
	"testing"

	"seelynx/internal/store"
)

func TestFisheyeSettings(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "Yard", MainURL: "rtsp://192.0.2.5/main", Enabled: true})
	admin := e.addUser("a", "admin")
	viewer := e.addUser("v", "viewer")
	path := "/api/cameras/" + itoa(cam.ID) + "/fisheye"
	lens := map[string]any{"lens": true, "mount": "wall", "x": 0.5, "y": 0.49, "r": 0.48, "fov": 190, "tilt": 15}

	if code, _ := e.do(viewer, "PUT", path, lens); code != 403 {
		t.Errorf("a viewer changed the lens: %d", code)
	}
	for _, bad := range []map[string]any{
		{"lens": true, "mount": "roof", "x": 0.5, "y": 0.5, "r": 0.5, "fov": 180},
		{"lens": true, "mount": "wall", "x": 1.5, "y": 0.5, "r": 0.5, "fov": 180},
		{"lens": true, "mount": "wall", "x": 0.5, "y": 0.5, "r": 0.5, "fov": 90},
		{"lens": true, "mount": "wall", "x": 0.5, "y": 0.5, "r": 0.01, "fov": 180},
	} {
		if code, body := e.do(admin, "PUT", path, bad); code != 400 {
			t.Errorf("accepted %v: %d %s", bad, code, body)
		}
	}
	if code, body := e.do(admin, "PUT", path, lens); code != 200 {
		t.Fatal(code, body)
	}
	// Everyone who can watch the camera gets the lens, so their view can be dewarped.
	_, body := e.do(viewer, "GET", "/api/cameras", nil)
	if !strings.Contains(body, `"fisheye":{"mount":"wall","x":0.5,"y":0.49,"r":0.48,"fov":190,"tilt":15}`) {
		t.Errorf("lens not listed: %s", body)
	}
	// Editing the camera's other settings keeps the lens.
	if code, body := e.do(admin, "PUT", "/api/cameras/"+itoa(cam.ID), map[string]any{"name": "Yard 2", "main_url": "rtsp://192.0.2.5/main", "enabled": true}); code != 200 {
		t.Fatal(code, body)
	}
	if c, _ := e.st.Camera(ctx, cam.ID); c.Fisheye == nil || c.Fisheye.FOV != 190 || c.Name != "Yard 2" {
		t.Errorf("after an edit: %+v %+v", c, c.Fisheye)
	}
	if code, _ := e.do(admin, "PUT", path, map[string]any{"lens": false}); code != 200 {
		t.Fatal(code)
	}
	if c, _ := e.st.Camera(ctx, cam.ID); c.Fisheye != nil {
		t.Errorf("lens left after turning it off: %+v", c.Fisheye)
	}
	_, body = e.do(viewer, "GET", "/api/cameras", nil)
	if !strings.Contains(body, `"fisheye":null`) {
		t.Errorf("normal lens: %s", body)
	}
}

func TestRotation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "Stairs", MainURL: "rtsp://192.0.2.6/main", Enabled: true})
	admin := e.addUser("a", "admin")
	viewer := e.addUser("v", "viewer")
	path := "/api/cameras/" + itoa(cam.ID) + "/rotation"

	if code, _ := e.do(viewer, "PUT", path, map[string]any{"rotation": 90}); code != 403 {
		t.Errorf("a viewer turned the picture: %d", code)
	}
	for _, bad := range []int{45, -90, 360} {
		if code, _ := e.do(admin, "PUT", path, map[string]any{"rotation": bad}); code != 400 {
			t.Errorf("accepted %d degrees: %d", bad, code)
		}
	}
	if code, body := e.do(admin, "PUT", path, map[string]any{"rotation": 270}); code != 200 {
		t.Fatal(code, body)
	}
	if _, body := e.do(viewer, "GET", "/api/cameras", nil); !strings.Contains(body, `"rotation":270`) {
		t.Errorf("rotation not listed: %s", body)
	}
	// Editing the camera's other settings keeps the rotation.
	if code, body := e.do(admin, "PUT", "/api/cameras/"+itoa(cam.ID), map[string]any{"name": "Stairs 2", "main_url": "rtsp://192.0.2.6/main", "enabled": true}); code != 200 {
		t.Fatal(code, body)
	}
	if c, _ := e.st.Camera(ctx, cam.ID); c.Rotation != 270 || c.Name != "Stairs 2" {
		t.Errorf("after an edit: %+v", c)
	}
}
