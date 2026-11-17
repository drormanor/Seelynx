package api

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"seelynx/internal/storage"
	"seelynx/internal/store"
)

// fakeLogin stands in for Windows' sign-in to a share, and remembers what it was given.
type fakeLogin struct{ calls []string }

func (f *fakeLogin) connect(path, user, password string) error {
	f.calls = append(f.calls, user+":"+password)
	if password != "right-test-value" && password != "new-test-value" {
		return errors.New("The share refused that username or password.")
	}
	return nil
}

func TestShareLoginIsUsedAndKeptSecret(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Seelynx signs in to shares only on Windows")
	}
	ctx := context.Background()
	e, dir := withStorage(t)
	key, err := store.LoadOrCreateKey(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.UseKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	// This computer's own C$ share, reached the way a NAS would be.
	share := `\\localhost\` + dir[:1] + `$` + filepath.Join(dir[2:], "nas")
	if _, err := os.Stat(`\\localhost\` + dir[:1] + `$` + dir[2:]); err != nil {
		t.Skip("this computer's own share can't be reached:", err)
	}
	login := &fakeLogin{}
	old := storage.Connect
	t.Cleanup(func() { storage.Connect = old })
	storage.Connect = login.connect
	admin := e.addUser("a", "admin")
	add := func(password string) (int, string) {
		return e.do(admin, "POST", "/api/storage", map[string]any{"name": "NAS", "path": share, "min_free_gb": 20, "recording": true, "username": "nas-user", "password": password})
	}

	if code, body := add("wrong-test-value"); code != 400 || !strings.Contains(body, "refused") {
		t.Fatalf("a wrong password: %d %s", code, body)
	}
	if len(storageList(t, e, admin)) != 1 {
		t.Fatal("a location was added with a wrong password")
	}
	code, body := add("right-test-value")
	if code != 201 {
		t.Fatalf("add: %d %s", code, body)
	}
	list := storageList(t, e, admin)
	nas := list[1]
	if nas.Username != "nas-user" || !nas.HasPassword || !nas.OK {
		t.Fatalf("%+v", nas)
	}
	if _, all := e.do(admin, "GET", "/api/storage", nil); strings.Contains(all, "right-test-value") || strings.Contains(body, "right-test-value") {
		t.Error("the password was sent back")
	}
	if l, _ := e.st.Location(ctx, nas.ID); l.Password != "right-test-value" {
		t.Errorf("stored password %q", l.Password)
	}

	// Saving without a password keeps it, and doesn't sign in again.
	calls := len(login.calls)
	path := fmt.Sprintf("/api/storage/%d", nas.ID)
	if code, body := e.do(admin, "PUT", path, map[string]any{"name": "NAS 2", "path": share, "min_free_gb": 20, "recording": true, "username": "nas-user"}); code != 200 {
		t.Fatalf("rename: %d %s", code, body)
	}
	if l, _ := e.st.Location(ctx, nas.ID); l.Password != "right-test-value" || len(login.calls) != calls {
		t.Errorf("password %q after renaming, %d sign-ins", l.Password, len(login.calls)-calls)
	}
	// A new password is tried before it's kept.
	if code, _ := e.do(admin, "PUT", path, map[string]any{"name": "NAS 2", "path": share, "min_free_gb": 20, "recording": true, "username": "nas-user", "password": "wrong-test-value"}); code != 400 {
		t.Errorf("a wrong new password: %d", code)
	}
	if code, body := e.do(admin, "PUT", path, map[string]any{"name": "NAS 2", "path": share, "min_free_gb": 20, "recording": true, "username": "nas-user", "password": "new-test-value"}); code != 200 {
		t.Fatalf("new password: %d %s", code, body)
	}
	if l, _ := e.st.Location(ctx, nas.ID); l.Password != "new-test-value" {
		t.Errorf("stored password %q", l.Password)
	}

	// The folder dialog signs in with the stored password.
	calls = len(login.calls)
	if code, body := e.do(admin, "POST", "/api/storage/connect", map[string]any{"path": share, "username": "nas-user", "id": nas.ID}); code != 204 || login.calls[calls] != "nas-user:new-test-value" {
		t.Errorf("connect for the dialog: %d %s %v", code, body, login.calls[calls:])
	}

	// A login is only for a share.
	if code, body := e.do(admin, "POST", "/api/storage", map[string]any{"name": "Local", "path": filepath.Join(dir, "local"), "min_free_gb": 20, "recording": true, "username": "x", "password": "y"}); code != 400 || !strings.Contains(body, "only for a network share") {
		t.Errorf("a login for a local folder: %d %s", code, body)
	}
}
