package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"seelynx/internal/store"
)

// roleRank orders the built-in roles for the route table in TestRoles.
var roleRank = map[string]int{"viewer": 1, "operator": 2, "admin": 3}

func (e *env) userWith(u store.User) *http.Client {
	e.t.Helper()
	hash, _ := bcrypt.GenerateFromPassword([]byte(testPass), bcrypt.MinCost)
	u.PasswordHash = string(hash)
	if _, err := e.st.AddUser(context.Background(), u); err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	if code, body := e.do(c, "POST", "/api/login", credentials{Username: u.Username, Password: testPass}); code != 200 {
		e.t.Fatalf("login: %d %s", code, body)
	}
	return c
}

func TestCheckPassword(t *testing.T) {
	cases := []struct {
		pw      string
		complex bool
		ok      bool
	}{
		{"short", false, false},
		{"aaaaaaaaaa", false, true},
		{"aaaaaaaaaa", true, false},
		{"Aaaaaaaaa1", true, true},
		{"aaaaaaaa1!", true, true},
		{"my-dror-Pass1", true, false}, // contains the username
		{strings.Repeat("a", 73), false, false},
	}
	for _, c := range cases {
		if got := checkPassword(c.pw, "dror", c.complex) == ""; got != c.ok {
			t.Errorf("%q complex=%v: got %v", c.pw, c.complex, got)
		}
	}
}

func TestCustomRoleLimitsFeaturesAndCameras(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	lobby, _ := e.st.CreateCamera(ctx, store.Camera{Name: "lobby", MainURL: "rtsp://a"})
	vault, _ := e.st.CreateCamera(ctx, store.Camera{Name: "vault", MainURL: "rtsp://b"})
	admin := e.addUser("root", "admin")

	code, body := e.do(admin, "POST", "/api/roles", store.Role{Name: "Reception", Permissions: []string{store.PermPlayback}, CameraIDs: []int64{lobby.ID}})
	if code != 201 {
		t.Fatalf("create role: %d %s", code, body)
	}
	var role store.Role
	json.Unmarshal([]byte(body), &role)
	c := e.userWith(store.User{Username: "r", RoleID: role.ID})

	_, list := e.do(c, "GET", "/api/cameras", nil)
	if !strings.Contains(list, "lobby") || strings.Contains(list, "vault") {
		t.Errorf("camera list: %s", list)
	}
	span := "?start=2026-01-01T00:00:00Z&end=2026-01-02T00:00:00Z"
	if code, _ := e.do(c, "GET", fmt.Sprintf("/api/cameras/%d/motion%s", lobby.ID, span), nil); code != 200 {
		t.Errorf("allowed camera: %d", code)
	}
	for _, route := range []string{"motion" + span, "recordings" + span, "playback?start=2026-01-01T00:00:00Z&duration=5"} {
		if code, _ := e.do(c, "GET", fmt.Sprintf("/api/cameras/%d/%s", vault.ID, route), nil); code != 404 {
			t.Errorf("hidden camera %s: %d", route, code)
		}
	}
	if code, _ := e.do(c, "POST", fmt.Sprintf("/api/cameras/%d/whep", lobby.ID), nil); code != 403 {
		t.Errorf("no live permission: %d", code)
	}
	if code, _ := e.do(c, "GET", "/api/system", nil); code != 403 {
		t.Errorf("no system permission: %d", code)
	}

	// Permission changes apply straight away, with no new sign-in.
	role.CameraIDs = []int64{vault.ID}
	if code, body := e.do(admin, "PUT", fmt.Sprintf("/api/roles/%d", role.ID), role); code != 200 {
		t.Fatalf("update role: %d %s", code, body)
	}
	if _, list := e.do(c, "GET", "/api/cameras", nil); strings.Contains(list, "lobby") || !strings.Contains(list, "vault") {
		t.Errorf("after change: %s", list)
	}
}

func TestRecentMotionHidesRestrictedCameras(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	a, _ := e.st.CreateCamera(ctx, store.Camera{Name: "seen", MainURL: "rtsp://a"})
	b, _ := e.st.CreateCamera(ctx, store.Camera{Name: "hidden", MainURL: "rtsp://b"})
	e.st.StartMotion(ctx, a.ID, time.Now())
	e.st.StartMotion(ctx, b.ID, time.Now())
	role, err := e.st.SaveRole(ctx, store.Role{Name: "x", Permissions: []string{store.PermSystem}, CameraIDs: []int64{a.ID}})
	if err != nil {
		t.Fatal(err)
	}
	c := e.userWith(store.User{Username: "r", RoleID: role.ID})
	if _, body := e.do(c, "GET", "/api/motion", nil); !strings.Contains(body, "seen") || strings.Contains(body, "hidden") {
		t.Fatalf("%s", body)
	}
}

func TestBuiltinRolesAreProtected(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	r := store.Role{Name: "Hacked", Permissions: []string{store.PermLive}, AllCameras: true}
	if code, _ := e.do(c, "PUT", fmt.Sprintf("/api/roles/%d", store.RoleViewer), r); code != 409 {
		t.Errorf("edit built-in: %d", code)
	}
	if code, _ := e.do(c, "DELETE", fmt.Sprintf("/api/roles/%d", store.RoleViewer), nil); code != 409 {
		t.Errorf("delete built-in: %d", code)
	}
	bad := []store.Role{
		{Name: "", Permissions: []string{store.PermLive}, AllCameras: true},
		{Name: "a"},
		{Name: "a", Permissions: []string{"root"}, AllCameras: true},
		{Name: "a", Permissions: []string{store.PermLive}},
	}
	for _, b := range bad {
		if code, _ := e.do(c, "POST", "/api/roles", b); code != 400 {
			t.Errorf("%+v: %d", b, code)
		}
	}
	if code, _ := e.do(c, "POST", "/api/roles", store.Role{Name: "viewer", Permissions: []string{store.PermLive}, AllCameras: true}); code != 409 {
		t.Errorf("duplicate name: %d", code)
	}
}

func TestRoleInUseCannotBeRemoved(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.addUser("root", "admin")
	role, _ := e.st.SaveRole(ctx, store.Role{Name: "x", Permissions: []string{store.PermLive}, AllCameras: true})
	e.userWith(store.User{Username: "r", RoleID: role.ID})
	if code, _ := e.do(admin, "DELETE", fmt.Sprintf("/api/roles/%d", role.ID), nil); code != 409 {
		t.Errorf("role in use: %d", code)
	}
	u, _ := e.st.UserByName(ctx, "r")
	e.st.DeleteUser(ctx, u.ID)
	if code, _ := e.do(admin, "DELETE", fmt.Sprintf("/api/roles/%d", role.ID), nil); code != 204 {
		t.Errorf("unused role: %d", code)
	}
}

func TestForcedPasswordChangeBlocksEverythingElse(t *testing.T) {
	e := newEnv(t)
	c := e.userWith(store.User{Username: "new", RoleID: store.RoleViewer, MustChange: true})
	if _, body := e.do(c, "GET", "/api/session", nil); !strings.Contains(body, `"must_change_password":true`) {
		t.Fatalf("session: %s", body)
	}
	if code, _ := e.do(c, "GET", "/api/cameras", nil); code != 403 {
		t.Fatalf("should be blocked: %d", code)
	}
	change := func(cur, next string) int {
		code, _ := e.do(c, "POST", "/api/password", map[string]string{"current": cur, "new": next})
		return code
	}
	if change("wrong-password", "a-new-password-1") != 400 {
		t.Error("wrong current password accepted")
	}
	if change(testPass, testPass) != 400 {
		t.Error("same password accepted")
	}
	if change(testPass, "short") != 400 {
		t.Error("weak password accepted")
	}
	if change(testPass, "a-new-password-1") != 204 {
		t.Fatal("change failed")
	}
	if code, _ := e.do(c, "GET", "/api/cameras", nil); code != 200 {
		t.Fatalf("should work now: %d", code)
	}
}

func TestPasswordExpiryForcesChange(t *testing.T) {
	now := time.Unix(1_000_000_000, 0)
	u := store.User{PasswordMaxDays: 30, PasswordChanged: now.Unix() - 29*86400}
	if u.NeedsNewPassword(now) {
		t.Error("29 days old")
	}
	u.PasswordChanged = now.Unix() - 30*86400
	if !u.NeedsNewPassword(now) {
		t.Error("30 days old")
	}
	if (store.User{PasswordChanged: 1}).NeedsNewPassword(now) {
		t.Error("no limit set")
	}
}

func TestExpiredAccountIsLockedOut(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.userWith(store.User{Username: "temp", RoleID: store.RoleViewer})
	u, _ := e.st.UserByName(ctx, "temp")
	u.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	if _, err := e.st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.do(c, "GET", "/api/cameras", nil); code != 401 {
		t.Errorf("active session: %d", code)
	}
	if code, body := e.do(e.client(), "POST", "/api/login", credentials{Username: "temp", Password: testPass}); code != 401 || !strings.Contains(body, "expired") {
		t.Errorf("login: %d %s", code, body)
	}
}

func TestLastAdminCannotBeDemotedOrExpired(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	c := e.addUser("root", "admin")
	u, _ := e.st.UserByName(ctx, "root")
	path := fmt.Sprintf("/api/users/%d", u.ID)
	if code, _ := e.do(c, "PUT", path, userInput{RoleID: store.RoleViewer}); code != 409 {
		t.Errorf("demote: %d", code)
	}
	if code, _ := e.do(c, "PUT", path, userInput{RoleID: store.RoleAdmin, ExpiresAt: time.Now().Unix()}); code != 409 {
		t.Errorf("expire: %d", code)
	}
	e.addUser("second", "admin")
	if code, body := e.do(c, "PUT", path, userInput{RoleID: store.RoleViewer}); code != 200 {
		t.Errorf("demote with a second admin: %d %s", code, body)
	}
}

func TestAdminPasswordResetEndsSessions(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	admin := e.addUser("root", "admin")
	c := e.addUser("v", "viewer")
	u, _ := e.st.UserByName(ctx, "v")
	pw := "brand-new-password"
	if code, body := e.do(admin, "PUT", fmt.Sprintf("/api/users/%d", u.ID), userInput{RoleID: store.RoleViewer, Password: &pw, MustChange: true}); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if code, _ := e.do(c, "GET", "/api/cameras", nil); code != 401 {
		t.Errorf("old session survived: %d", code)
	}
	if code, _ := e.do(e.client(), "POST", "/api/login", credentials{Username: "v", Password: pw}); code != 200 {
		t.Errorf("new password: %d", code)
	}
}

func TestProfileFieldsRoundTrip(t *testing.T) {
	e := newEnv(t)
	c := e.addUser("root", "admin")
	pw := "good-password-1"
	in := userInput{Username: "pat", Password: &pw, RoleID: store.RoleOperator, FirstName: "Pat", LastName: "Lee", Remarks: "Nights", PasswordMaxDays: 90}
	code, body := e.do(c, "POST", "/api/users", in)
	if code != 201 || !strings.Contains(body, `"first_name":"Pat"`) || !strings.Contains(body, `"role":"Operator"`) || !strings.Contains(body, `"password_max_days":90`) {
		t.Fatalf("%d %s", code, body)
	}
	if strings.Contains(body, "$2a$") {
		t.Fatal("hash exposed")
	}
	in.RoleID = 999
	in.Username = "x"
	if code, _ := e.do(c, "POST", "/api/users", in); code != 400 {
		t.Errorf("unknown role: %d", code)
	}
}

func TestUpgradeKeepsExistingRoles(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.addUser("a", "admin")
	e.addUser("o", "operator")
	e.addUser("v", "viewer")
	for name, want := range map[string]string{"a": "Administrator", "o": "Operator", "v": "Viewer"} {
		if u, err := e.st.UserByName(ctx, name); err != nil || u.Role != want {
			t.Errorf("%s: %q %v", name, u.Role, err)
		}
	}
}
