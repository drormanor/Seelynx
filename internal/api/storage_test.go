package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"seelynx/internal/storage"
)

func withStorage(t *testing.T) (*env, string) {
	t.Helper()
	e := newEnv(t)
	dir := t.TempDir()
	if err := storage.Bootstrap(context.Background(), e.st, filepath.Join(dir, "main"), 50); err != nil {
		t.Fatal(err)
	}
	e.server.Storage = &storage.Manager{Store: e.st, Log: discard()}
	if err := e.server.Storage.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e, dir
}

func storageList(t *testing.T, e *env, c *http.Client) []locationView {
	t.Helper()
	code, body := e.do(c, "GET", "/api/storage", nil)
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var out []locationView
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestStorageLocationsCanBeAddedChangedAndRemoved(t *testing.T) {
	e, dir := withStorage(t)
	admin := e.addUser("a", "admin")
	e.do(admin, "POST", "/api/cameras", map[string]any{"name": "Door", "main_url": "rtsp://a", "enabled": true, "record": true})

	nas := filepath.Join(dir, "nas")
	code, body := e.do(admin, "POST", "/api/storage", map[string]any{"name": "NAS", "path": nas, "min_free_gb": 20, "recording": true})
	if code != 201 {
		t.Fatalf("add: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(nas, storage.MarkerFile)); err != nil {
		t.Error("the new folder wasn't marked")
	}
	list := storageList(t, e, admin)
	if len(list) != 2 || !list[0].OK || !list[1].OK || list[1].Name != "NAS" || list[1].MinFreeGB != 20 {
		t.Fatalf("%+v", list)
	}
	if n := len(list[0].Cameras) + len(list[1].Cameras); n != 1 {
		t.Errorf("the camera should be recording in one place: %+v", list)
	}

	for _, bad := range []struct {
		in   map[string]any
		code int
		want string
	}{
		{map[string]any{"name": "X", "path": "relative", "min_free_gb": 5, "recording": true}, 400, "full folder path"},
		{map[string]any{"name": "X", "path": filepath.Join(nas, "inner"), "min_free_gb": 5, "recording": true}, 400, "overlaps NAS"},
		{map[string]any{"name": "nas", "path": filepath.Join(dir, "other"), "min_free_gb": 5, "recording": true}, 409, "already has this name"},
		{map[string]any{"name": "X", "path": filepath.Join(dir, "other"), "min_free_gb": 0, "recording": true}, 400, "Keep free"},
		{map[string]any{"name": " ", "path": filepath.Join(dir, "other"), "min_free_gb": 5, "recording": true}, 400, "name"},
	} {
		if code, body := e.do(admin, "POST", "/api/storage", bad.in); code != bad.code || !strings.Contains(body, bad.want) {
			t.Errorf("%v: %d %s", bad.in, code, body)
		}
	}

	// Moving a location marks the new folder.
	moved := filepath.Join(dir, "nas2")
	id := itoa(list[1].ID)
	if code, body := e.do(admin, "PUT", "/api/storage/"+id, map[string]any{"name": "NAS", "path": moved, "min_free_gb": 30, "recording": true}); code != 200 {
		t.Fatalf("move: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(moved, storage.MarkerFile)); err != nil {
		t.Error("the moved folder wasn't marked")
	}

	// One location has to take new video.
	mainID := itoa(list[0].ID)
	e.do(admin, "PUT", "/api/storage/"+id, map[string]any{"name": "NAS", "path": moved, "min_free_gb": 30, "recording": false})
	if code, body := e.do(admin, "PUT", "/api/storage/"+mainID, map[string]any{"name": "Main storage", "path": list[0].Path, "min_free_gb": 50, "recording": false}); code != 400 {
		t.Errorf("turned off the last recording location: %d %s", code, body)
	}
	if code, _ := e.do(admin, "DELETE", "/api/storage/"+mainID, nil); code != 400 {
		t.Errorf("removed the last recording location: %d", code)
	}
	if list := storageList(t, e, admin); len(list[0].Cameras) != 1 || len(list[1].Cameras) != 0 {
		t.Errorf("the camera should have left a location that stopped taking video: %+v", list)
	}

	if code, _ := e.do(admin, "DELETE", "/api/storage/"+id, nil); code != 204 {
		t.Fatalf("remove: %d", code)
	}
	if len(storageList(t, e, admin)) != 1 {
		t.Error("not removed")
	}
	if _, err := os.Stat(moved); err != nil {
		t.Error("removing a location deleted its folder")
	}
	events, _ := e.st.Events(context.Background(), 20)
	var all []string
	for _, ev := range events {
		all = append(all, ev.Message)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"Storage NAS (" + nas + ") added by a", "Storage NAS moved from", "Storage NAS (" + moved + ") removed by a. Its files were kept."} {
		if !strings.Contains(joined, want) {
			t.Errorf("event %q missing from:\n%s", want, joined)
		}
	}
}

func TestOnlyAdministratorsManageStorage(t *testing.T) {
	e, dir := withStorage(t)
	op := e.addUser("o", "operator")
	if code, _ := e.do(op, "GET", "/api/storage", nil); code != 403 {
		t.Errorf("operator read: %d", code)
	}
	if code, _ := e.do(op, "POST", "/api/storage", map[string]any{"name": "X", "path": filepath.Join(dir, "x"), "min_free_gb": 5, "recording": true}); code != 403 {
		t.Errorf("operator add: %d", code)
	}
	if code, _ := e.do(e.client(), "GET", "/api/storage", nil); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "x")); err == nil {
		t.Error("a refused request made a folder")
	}
}

func TestTheFolderDialogBrowsesTheServer(t *testing.T) {
	e, dir := withStorage(t)
	admin := e.addUser("a", "admin")
	os.MkdirAll(filepath.Join(dir, "Video", "cams"), 0o750)
	os.MkdirAll(filepath.Join(dir, ".hidden"), 0o750)
	os.WriteFile(filepath.Join(dir, "file.txt"), []byte("x"), 0o600)

	var got folderListing
	code, body := e.do(admin, "GET", "/api/storage/folders?path="+url.QueryEscape(dir), nil)
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil {
		t.Fatalf("%d %s", code, body)
	}
	var names []string
	for _, f := range got.Folders {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "main,Video" || got.Parent != filepath.Dir(dir) || !got.Up {
		t.Errorf("listing: %+v", got)
	}

	// The top: drives on Windows, / elsewhere.
	code, body = e.do(admin, "GET", "/api/storage/folders", nil)
	if code != 200 || json.Unmarshal([]byte(body), &got) != nil || len(got.Folders) == 0 {
		t.Errorf("top: %d %s", code, body)
	}

	for path, want := range map[string]int{"relative": 400, filepath.Join(dir, "missing"): 404} {
		if code, _ := e.do(admin, "GET", "/api/storage/folders?path="+url.QueryEscape(path), nil); code != want {
			t.Errorf("%s: %d", path, code)
		}
	}

	if code, body := e.do(admin, "POST", "/api/storage/folders", map[string]any{"parent": dir, "name": "New one"}); code != 201 || !strings.Contains(body, "New one") {
		t.Errorf("new folder: %d %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(dir, "New one")); err != nil {
		t.Error("not made")
	}
	for _, bad := range []map[string]any{{"parent": dir, "name": "New one"}, {"parent": dir, "name": "../out"}, {"parent": dir, "name": " "}, {"parent": "relative", "name": "x"}} {
		if code, _ := e.do(admin, "POST", "/api/storage/folders", bad); code < 400 {
			t.Errorf("%v: %d", bad, code)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "out")); err == nil {
		t.Error("a folder was made outside the one chosen")
	}
	op := e.addUser("o", "operator")
	if code, _ := e.do(op, "GET", "/api/storage/folders?path="+url.QueryEscape(dir), nil); code != 403 {
		t.Errorf("operator: %d", code)
	}
}

func TestMovingALocationsVideoBeforeRemovingIt(t *testing.T) {
	e, dir := withStorage(t)
	admin := e.addUser("a", "admin")
	e.do(admin, "POST", "/api/storage", map[string]any{"name": "NAS", "path": filepath.Join(dir, "nas"), "min_free_gb": 5, "recording": true})
	list := storageList(t, e, admin)
	mainID, nasID := itoa(list[0].ID), list[1].ID
	video := filepath.Join(list[0].Path, "cam_9_main", "2026-11-01_10-00-00-000000.mp4")
	os.MkdirAll(filepath.Dir(video), 0o750)
	os.WriteFile(video, []byte("video"), 0o600)

	for _, bad := range []struct {
		to   int64
		code int
		want string
	}{
		{list[0].ID, 400, "Choose another location"},
		{999, 400, "Choose another location"},
	} {
		if code, body := e.do(admin, "POST", "/api/storage/"+mainID+"/move", map[string]any{"to": bad.to}); code != bad.code || !strings.Contains(body, bad.want) {
			t.Errorf("to %d: %d %s", bad.to, code, body)
		}
	}
	// No room above the destination's Keep free.
	e.do(admin, "PUT", "/api/storage/"+itoa(nasID), map[string]any{"name": "NAS", "path": filepath.Join(dir, "nas"), "min_free_gb": 1000000, "recording": true})
	if code, body := e.do(admin, "POST", "/api/storage/"+mainID+"/move", map[string]any{"to": nasID}); code != 400 || !strings.Contains(body, "has room for 0.0 GB") {
		t.Errorf("no room: %d %s", code, body)
	}
	e.do(admin, "PUT", "/api/storage/"+itoa(nasID), map[string]any{"name": "NAS", "path": filepath.Join(dir, "nas"), "min_free_gb": 5, "recording": true})

	if code, body := e.do(admin, "POST", "/api/storage/"+mainID+"/move", map[string]any{"to": nasID}); code != 202 {
		t.Fatalf("start: %d %s", code, body)
	}
	list = storageList(t, e, admin)
	if list[0].MovingTo != nasID || list[0].Recording || list[0].Move == nil {
		t.Errorf("while moving: %+v", list[0])
	}
	if code, _ := e.do(admin, "POST", "/api/storage/"+mainID+"/move", map[string]any{"to": nasID}); code != 409 {
		t.Errorf("started twice: %d", code)
	}
	if code, _ := e.do(admin, "PUT", "/api/storage/"+mainID, map[string]any{"name": "Main storage", "path": list[0].Path, "min_free_gb": 50, "recording": true}); code != 409 {
		t.Errorf("edited while moving: %d", code)
	}
	if code, body := e.do(admin, "DELETE", "/api/storage/"+itoa(nasID), nil); code != 409 || !strings.Contains(body, "from Main storage") {
		t.Errorf("removed the destination: %d %s", code, body)
	}
	if code, _ := e.do(admin, "POST", "/api/storage/"+mainID+"/move/stop", nil); code != 204 {
		t.Errorf("stop: %d", code)
	}
	if code, _ := e.do(admin, "POST", "/api/storage/"+mainID+"/move/stop", nil); code != 409 {
		t.Errorf("stopped twice: %d", code)
	}
	if l := storageList(t, e, admin)[0]; l.MovingTo != 0 || l.Recording {
		t.Errorf("after stopping, it stays and takes no video: %+v", l)
	}
	events, _ := e.st.Events(context.Background(), 20)
	var all []string
	for _, ev := range events {
		all = append(all, ev.Message)
	}
	joined := strings.Join(all, "\n")
	for _, want := range []string{"a started moving 0.0 GB of video from Main storage to NAS. Main storage will be removed when it's empty.", "a stopped moving video out of Main storage."} {
		if !strings.Contains(joined, want) {
			t.Errorf("event %q missing:\n%s", want, joined)
		}
	}
}

func TestAStandbyLocation(t *testing.T) {
	e, dir := withStorage(t)
	admin := e.addUser("a", "admin")
	add := func(name string, standby, recording bool) (int, string) {
		return e.do(admin, "POST", "/api/storage", map[string]any{"name": name, "path": filepath.Join(dir, name), "min_free_gb": 5, "recording": recording, "standby": standby})
	}
	if code, body := add("Local", true, true); code != 201 {
		t.Fatalf("add standby: %d %s", code, body)
	}
	list := storageList(t, e, admin)
	if !list[1].Standby || list[0].Standby {
		t.Fatalf("%+v", list)
	}
	if code, body := add("Off", true, false); code != 400 || !strings.Contains(body, "has to be able to record") {
		t.Errorf("standby that doesn't record: %d %s", code, body)
	}
	// Main storage is the only regular location, so it can't become standby or be removed.
	main := list[0]
	if code, body := e.do(admin, "PUT", "/api/storage/"+itoa(main.ID), map[string]any{"name": main.Name, "path": main.Path, "min_free_gb": 5, "recording": true, "standby": true}); code != 400 || !strings.Contains(body, "isn't on standby") {
		t.Errorf("last regular location to standby: %d %s", code, body)
	}
	if code, _ := e.do(admin, "DELETE", "/api/storage/"+itoa(main.ID), nil); code != 400 {
		t.Errorf("removing the last regular location: %d", code)
	}
	// Video can't be moved into standby.
	if code, body := add("Other", false, true); code != 201 {
		t.Fatalf("%d %s", code, body)
	}
	list = storageList(t, e, admin)
	if code, body := e.do(admin, "POST", "/api/storage/"+itoa(list[2].ID)+"/move", map[string]any{"to": list[1].ID}); code != 400 || !strings.Contains(body, "on standby") {
		t.Errorf("move into standby: %d %s", code, body)
	}
}
