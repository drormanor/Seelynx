package api

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"seelynx/internal/store"
)

func TestClientAllowed(t *testing.T) {
	list := []netip.Prefix{netip.MustParsePrefix("192.168.1.0/24"), netip.MustParsePrefix("192.0.2.5/32")}
	cases := []struct {
		list   []netip.Prefix
		remote string
		want   bool
	}{
		{nil, "8.8.8.8:1000", true},
		{list, "192.168.1.77:1000", true},
		{list, "192.0.2.5:1000", true},
		{list, "192.0.2.6:1000", false},
		{list, "127.0.0.1:1000", true},
		{list, "[::1]:1000", true},
		{list, "[::ffff:192.168.1.9]:1000", true},
		{list, "[::ffff:8.8.8.8]:1000", false},
		{list, "garbage", false},
	}
	for _, c := range cases {
		if got := clientAllowed(c.list, c.remote); got != c.want {
			t.Errorf("%s: got %v", c.remote, got)
		}
	}
}

func TestConfigValidation(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("a", "admin")
	bad := []struct {
		kind string
		body string
	}{
		{"views", `{"name":"v","data":{"layout":5,"slots":[]}}`},
		{"views", `{"name":"v","data":{"layout":1,"slots":[1,2]}}`},
		{"views", `{"name":"","data":{"layout":4,"slots":[]}}`},
		{"tours", `{"name":"t","data":{"dwell":1,"views":[1]}}`},
		{"tours", `{"name":"t","data":{"dwell":30,"views":[]}}`},
		{"schedules", `{"name":"s","data":{"periods":[]}}`},
		{"schedules", `{"name":"s","data":{"periods":[{"days":[9],"start":"08:00","end":"09:00"}]}}`},
		{"access", `{"name":"not an address"}`},
	}
	for _, b := range bad {
		res, err := c.Post(e.srv.URL+"/api/config/"+b.kind, "application/json", strings.NewReader(b.body))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("%s %s: got %d", b.kind, b.body, res.StatusCode)
		}
	}
}

func TestConfigPermissions(t *testing.T) {
	e := newEnv(t)
	v := e.addUser("v", "viewer")
	if code, _ := e.do(v, "GET", "/api/config/views", nil); code != 200 {
		t.Errorf("viewer should read views: %d", code)
	}
	for _, k := range []string{"schedules", "access"} {
		if code, _ := e.do(v, "GET", "/api/config/"+k, nil); code != 403 {
			t.Errorf("viewer read %s: %d", k, code)
		}
	}
	if code, _ := e.do(v, "POST", "/api/config/views", map[string]any{"name": "x"}); code != 403 {
		t.Errorf("viewer wrote a view: %d", code)
	}
	for _, route := range []string{"/api/backups", "/api/logs"} {
		if code, _ := e.do(v, "GET", route, nil); code != 403 {
			t.Errorf("viewer read %s: %d", route, code)
		}
		if code, _ := e.do(e.client(), "GET", route, nil); code != 401 {
			t.Errorf("anonymous read %s: %d", route, code)
		}
	}
	if code, _ := e.do(v, "POST", "/api/restore", nil); code != 403 {
		t.Errorf("viewer restored: %d", code)
	}
	if code, _ := e.do(e.addUser("a", "admin"), "GET", "/api/config/nonsense", nil); code != 404 {
		t.Errorf("unknown kind: %d", code)
	}
}

func TestItemsInUseCannotBeDeleted(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.addUser("a", "admin")
	sch, _ := e.st.PutItem(ctx, "schedules", store.Item{Name: "s", Data: []byte(`{"periods":[]}`)})
	grp, _ := e.st.PutItem(ctx, "groups", store.Item{Name: "g", Data: []byte(`{"parent":0}`)})
	child, _ := e.st.PutItem(ctx, "groups", store.Item{Name: "h", Data: []byte(fmt.Sprintf(`{"parent":%d}`, grp.ID))})
	view, _ := e.st.PutItem(ctx, "views", store.Item{Name: "v", Data: []byte(`{"layout":4,"slots":[]}`)})
	tour, _ := e.st.PutItem(ctx, "tours", store.Item{Name: "t", Data: []byte(fmt.Sprintf(`{"dwell":30,"views":[%d]}`, view.ID))})
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "c", MainURL: "rtsp://a", ScheduleID: sch.ID, GroupID: child.ID})

	del := func(kind string, id int64) int {
		code, _ := e.do(c, "DELETE", fmt.Sprintf("/api/config/%s/%d", kind, id), nil)
		return code
	}
	if del("schedules", sch.ID) != 409 || del("groups", child.ID) != 409 || del("groups", grp.ID) != 409 || del("views", view.ID) != 409 {
		t.Fatal("in-use items must be protected")
	}
	if del("tours", tour.ID) != 204 || del("views", view.ID) != 204 {
		t.Fatal("tour then view should delete")
	}
	if err := e.st.DeleteCamera(ctx, cam.ID); err != nil {
		t.Fatal(err)
	}
	if del("schedules", sch.ID) != 204 || del("groups", child.ID) != 204 || del("groups", grp.ID) != 204 {
		t.Fatal("unused items should delete")
	}
}

func TestRecordingFollowsSchedule(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := &Server{Store: e.st, Log: discard()}
	now := time.Now()
	day := int(now.Weekday())
	on, _ := e.st.PutItem(ctx, "schedules", store.Item{Name: "on", Data: []byte(fmt.Sprintf(`{"periods":[{"days":[%d],"start":"00:00","end":"00:00"}]}`, day))})
	off, _ := e.st.PutItem(ctx, "schedules", store.Item{Name: "off", Data: []byte(fmt.Sprintf(`{"periods":[{"days":[%d],"start":"00:00","end":"00:00"}]}`, (day+1)%7))})
	cases := []struct {
		name string
		cam  store.Camera
		want bool
	}{
		{"no schedule", store.Camera{Record: true}, true},
		{"recording off beats schedule", store.Camera{Record: false, ScheduleID: on.ID}, false},
		{"inside schedule", store.Camera{Record: true, ScheduleID: on.ID}, true},
		{"outside schedule", store.Camera{Record: true, ScheduleID: off.ID}, false},
		{"missing schedule keeps recording", store.Camera{Record: true, ScheduleID: 9999}, true},
	}
	for _, c := range cases {
		if got := s.Recording(ctx, c.cam); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
}

func maintenanceEnv(t *testing.T) (*env, *http.Client, string) {
	e := newEnv(t)
	dir := t.TempDir()
	e.server.DataDir = dir
	return e, e.addUser("a", "admin"), dir
}

func TestBackupRestore(t *testing.T) {
	e, c, dir := maintenanceEnv(t)
	ctx := context.Background()
	if _, err := e.st.CreateCamera(ctx, store.Camera{Name: "keep me", MainURL: "rtsp://a"}); err != nil {
		t.Fatal(err)
	}
	code, body := e.do(c, "POST", "/api/backups", nil)
	if code != 201 {
		t.Fatalf("backup: %d %s", code, body)
	}
	name := strings.Split(body, `"`)[3]

	if code, _ := e.do(c, "GET", "/api/backups/..%2f..%2fseelynx.db", nil); code != 404 {
		t.Errorf("traversal: %d", code)
	}
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/restore", strings.NewReader("this is not a database at all"))
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 400 {
		t.Errorf("garbage restore: %d", res.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(dir, restoreName)); err == nil {
		t.Fatal("garbage must not be staged")
	}

	if code, body := e.do(c, "POST", "/api/restore?name="+name, nil); code != 200 {
		t.Fatalf("restore: %d %s", code, body)
	}
	os.WriteFile(filepath.Join(dir, databaseName), []byte("old"), 0o600)
	os.WriteFile(filepath.Join(dir, databaseName+"-wal"), []byte("oldwal"), 0o600)
	done, err := ApplyPendingRestore(dir)
	if err != nil || !done {
		t.Fatalf("apply: %v %v", done, err)
	}
	if _, err := os.Stat(filepath.Join(dir, databaseName+"-wal")); err == nil {
		t.Error("old WAL must not stay beside the restored database")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, databaseName+".pre-restore")); string(b) != "old" {
		t.Error("previous database should be kept")
	}
	st, err := store.Open(filepath.Join(dir, databaseName))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cams, err := st.Cameras(ctx)
	if err != nil || len(cams) != 1 || cams[0].Name != "keep me" {
		t.Fatalf("restored data wrong: %v %+v", err, cams)
	}
	if done, _ := ApplyPendingRestore(dir); done {
		t.Error("restore must only apply once")
	}
}

func TestBackupRejectsDatabaseWithNoUsers(t *testing.T) {
	_, _, dir := maintenanceEnv(t)
	p := filepath.Join(dir, "empty.db")
	st, err := store.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	if err := checkDatabase(context.Background(), p); err == nil {
		t.Fatal("empty database would lock everyone out")
	}
}

func TestLogsAreRedacted(t *testing.T) {
	e, c, dir := maintenanceEnv(t)
	os.WriteFile(filepath.Join(dir, "mediamtx.log"), []byte("source rtsp://admin:hunter2@192.0.2.9/live ok\n"), 0o600)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/logs", nil)
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(z.File) != 1 {
		t.Fatalf("files: %d", len(z.File))
	}
	f, _ := z.File[0].Open()
	text, _ := io.ReadAll(f)
	if strings.Contains(string(text), "hunter2") || !strings.Contains(string(text), "rtsp://***@192.0.2.9/live") {
		t.Fatalf("not redacted: %s", text)
	}
}

func TestMigrateLegacyNames(t *testing.T) {
	dir := t.TempDir()
	old := "night" + "jar"
	os.MkdirAll(filepath.Join(dir, "backups"), 0o750)
	os.WriteFile(filepath.Join(dir, old+".db"), []byte("db"), 0o600)
	os.WriteFile(filepath.Join(dir, old+".db-wal"), []byte("wal"), 0o600)
	os.WriteFile(filepath.Join(dir, old+".log"), []byte("old log"), 0o600)
	os.WriteFile(filepath.Join(dir, "seelynx.log"), []byte("new log"), 0o600)
	os.WriteFile(filepath.Join(dir, "backups", old+"-20260101-000000.db"), []byte("b"), 0o600)
	for i := 0; i < 2; i++ { // running it twice must be harmless
		if err := MigrateLegacyNames(dir); err != nil {
			t.Fatal(err)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "seelynx.db")); string(b) != "db" {
		t.Error("database not moved")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "seelynx.db-wal")); string(b) != "wal" {
		t.Error("WAL must move with the database")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "seelynx.log")); string(b) != "new log" {
		t.Error("an existing file must not be overwritten")
	}
	if !backupName.MatchString("seelynx-20260101-000000.db") {
		t.Error("backup pattern")
	}
	if _, err := os.Stat(filepath.Join(dir, "backups", "seelynx-20260101-000000.db")); err != nil {
		t.Error("backup not moved")
	}
}

func TestMigrateFromEachEarlierName(t *testing.T) {
	for _, old := range []string{"night" + "jar", "still" + "watch", "fra" + "mix"} {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, old+".db"), []byte("db"), 0o600)
		if err := MigrateLegacyNames(dir); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, "seelynx.db")); string(b) != "db" {
			t.Errorf("%s: database not moved", old)
		}
	}
}

func TestCameraRejectsMissingScheduleAndGroup(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("a", "admin")
	base := map[string]any{"name": "c", "main_url": "rtsp://192.0.2.9/a", "record": true, "retention_days": 7, "enabled": true}
	for _, field := range []string{"schedule_id", "group_id"} {
		body := map[string]any{field: 999}
		for k, v := range base {
			body[k] = v
		}
		if code, resp := e.do(c, "POST", "/api/cameras", body); code != 400 {
			t.Errorf("%s: got %d %s", field, code, resp)
		}
	}
	if cams, _ := e.st.Cameras(context.Background()); len(cams) != 0 {
		t.Fatal("rejected camera was saved")
	}
}

func TestScheduleChoosesHowToRecord(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	s := &Server{Store: e.st, Log: discard()}
	now := time.Now()
	day := int(now.Weekday())
	on, _ := e.st.PutItem(ctx, "schedules", store.Item{Name: "on", Data: []byte(fmt.Sprintf(`{"periods":[{"days":[%d],"start":"00:00","end":"00:00"}]}`, day))})
	off, _ := e.st.PutItem(ctx, "schedules", store.Item{Name: "off", Data: []byte(fmt.Sprintf(`{"periods":[{"days":[%d],"start":"00:00","end":"00:00"}]}`, (day+1)%7))})
	cases := []struct {
		name           string
		cam            store.Camera
		record, motion bool
	}{
		{"inside, all video", store.Camera{Record: true, ScheduleID: on.ID, OutsideMode: store.OutsideMotion}, true, false},
		{"inside, on motion", store.Camera{Record: true, ScheduleID: on.ID, MotionOnly: true}, true, true},
		{"outside, off", store.Camera{Record: true, ScheduleID: off.ID, MotionOnly: true}, false, false},
		{"outside, all video", store.Camera{Record: true, ScheduleID: off.ID, MotionOnly: true, OutsideMode: store.OutsideAll}, true, false},
		{"outside, on motion", store.Camera{Record: true, ScheduleID: off.ID, OutsideMode: store.OutsideMotion}, true, true},
		{"no schedule ignores outside", store.Camera{Record: true, OutsideMode: store.OutsideMotion}, true, false},
		{"recording off beats everything", store.Camera{ScheduleID: off.ID, OutsideMode: store.OutsideAll}, false, false},
		{"missing schedule keeps everything", store.Camera{Record: true, ScheduleID: 9999, OutsideMode: store.OutsideMotion}, true, false},
	}
	for _, c := range cases {
		if got := s.Recording(ctx, c.cam); got != c.record {
			t.Errorf("%s: recording %v", c.name, got)
		}
		if got := s.MotionTimes(ctx, c.cam)(now); got != c.motion {
			t.Errorf("%s: motion only %v", c.name, got)
		}
	}
}
