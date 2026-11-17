package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

func TestExportNameIsSafe(t *testing.T) {
	at := time.Date(2026, 11, 5, 9, 31, 6, 0, time.Local)
	for name, want := range map[string]string{
		"Front door":        "Front_door_2026-11-05_09-31-06.mp4",
		`../../etc"\passwd`: "etc_passwd_2026-11-05_09-31-06.mp4",
		"דלת":               "camera_2026-11-05_09-31-06.mp4",
	} {
		if got := exportName(name, at); got != want {
			t.Errorf("%q: %q", name, got)
		}
	}
}

func TestClipsCoverEveryRecordedPartOfTheRange(t *testing.T) {
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	spans := []media.Segment{
		{Start: base, Duration: 30},                        // cut at the start
		{Start: base.Add(time.Minute), Duration: 20},       // whole
		{Start: base.Add(2 * time.Minute), Duration: 3600}, // cut at the end
		{Start: base.Add(3 * time.Hour), Duration: 60},     // outside
	}
	got := clipsIn(spans, base.Add(10*time.Second), base.Add(130*time.Second))
	want := []clip{{base.Add(10 * time.Second), 20}, {base.Add(time.Minute), 20}, {base.Add(2 * time.Minute), 10}}
	if len(got) != len(want) {
		t.Fatalf("%+v", got)
	}
	for i := range want {
		if !got[i].start.Equal(want[i].start) || got[i].seconds != want[i].seconds {
			t.Errorf("clip %d: %+v", i, got[i])
		}
	}
	if clipsLength(got) != 50*time.Second {
		t.Errorf("length %s", clipsLength(got))
	}
	if len(clipsIn(spans, base.Add(2*time.Hour+time.Hour), base.Add(2*time.Hour+time.Hour))) != 0 {
		t.Error("an empty range has clips")
	}
}

func TestExportRequests(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cam, _ := e.st.CreateCamera(ctx, store.Camera{Name: "a", MainURL: "rtsp://a"})
	other, _ := e.st.CreateCamera(ctx, store.Camera{Name: "b", MainURL: "rtsp://b"})
	path := func(id int64, q string) string {
		return "/api/cameras/" + strings.TrimSpace(itoa(id)) + "/export" + q
	}
	good := "?start=2026-11-05T10:00:00Z&duration=10"
	if code, _ := e.do(e.client(), "GET", path(cam.ID, good), nil); code != 401 {
		t.Errorf("anonymous: %d", code)
	}
	if code, _ := e.do(e.addUser("v", "viewer"), "GET", path(cam.ID, good), nil); code != 403 {
		t.Errorf("viewer without playback: %d", code)
	}
	op := e.addUser("o", "operator")
	for _, q := range []string{"", "?start=nope&duration=10", "?start=2026-11-05T10:00:00Z", "?start=2026-11-05T10:00:00Z&duration=0", "?start=2026-11-05T10:00:00Z&duration=99999"} {
		if code, _ := e.do(op, "GET", path(cam.ID, q), nil); code != 400 {
			t.Errorf("%q: %d", q, code)
		}
	}
	role, _ := e.st.SaveRole(ctx, store.Role{Name: "x", Permissions: []string{store.PermPlayback}, CameraIDs: []int64{other.ID}})
	if code, _ := e.do(e.userWith(store.User{Username: "x", RoleID: role.ID}), "GET", path(cam.ID, good), nil); code != 404 {
		t.Errorf("another camera's user: %d", code)
	}
}

func itoa(n int64) string {
	if n < 10 {
		return string(rune('0' + n))
	}
	return itoa(n/10) + string(rune('0'+n%10))
}
