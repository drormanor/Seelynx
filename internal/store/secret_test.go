package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testKey(b byte) []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = b
	}
	return k
}

func rawPassword(t *testing.T, s *Store, id int64) string {
	t.Helper()
	var p string
	if err := s.db.QueryRow(`SELECT password FROM cameras WHERE id = ?`, id).Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestKeyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret.key")
	first, err := LoadOrCreateKey(path)
	if err != nil || len(first) != 32 {
		t.Fatalf("%v %d", err, len(first))
	}
	again, err := LoadOrCreateKey(path)
	if err != nil || string(again) != string(first) {
		t.Fatal("key must stay the same between starts")
	}
	os.WriteFile(path, []byte("not a key"), 0o600)
	if _, err := LoadOrCreateKey(path); err == nil {
		t.Fatal("a damaged key file must not be replaced silently")
	}
}

func TestPasswordsAreEncryptedAtRest(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "t.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UseKey(ctx, testKey(1)); err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateCamera(ctx, Camera{Name: "c", MainURL: "rtsp://a", Password: "plainsecret"})
	if err != nil {
		t.Fatal(err)
	}
	if c.Password != "plainsecret" || !c.HasPassword {
		t.Fatalf("read back: %+v", c)
	}
	if raw := rawPassword(t, s, c.ID); !strings.HasPrefix(raw, secretPrefix) || strings.Contains(raw, "plainsecret") {
		t.Fatalf("stored as %q", raw)
	}
	first := rawPassword(t, s, c.ID)
	c.Name = "renamed"
	if _, err := s.UpdateCamera(ctx, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Camera(ctx, c.ID); got.Password != "plainsecret" {
		t.Fatalf("after update: %q", got.Password)
	}
	if rawPassword(t, s, c.ID) == first {
		t.Error("each save should use a fresh nonce")
	}
	s.Close()
}

func TestExistingPasswordsAreUpgradedAndScrubbed(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "t.db")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := s.CreateCamera(ctx, Camera{Name: "c", MainURL: "rtsp://a", Password: "oldplaintextsecret"})
	empty, _ := s.CreateCamera(ctx, Camera{Name: "e", MainURL: "rtsp://b"})
	if _, err := s.UseKey(ctx, testKey(1)); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Camera(ctx, c.ID); got.Password != "oldplaintextsecret" {
		t.Fatalf("got %q", got.Password)
	}
	if rawPassword(t, s, empty.ID) != "" {
		t.Error("an empty password stays empty")
	}
	s.Close()
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(dbPath + suffix)
		if err != nil {
			continue
		}
		if strings.Contains(string(data), "oldplaintextsecret") {
			t.Errorf("plain password still in the database file%s", suffix)
		}
	}
}

func TestWrongKeyIsReportedNotFatal(t *testing.T) {
	ctx := context.Background()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.UseKey(ctx, testKey(1))
	c, _ := s.CreateCamera(ctx, Camera{Name: "c", MainURL: "rtsp://a", Password: "secret"})
	other, _ := s.CreateCamera(ctx, Camera{Name: "open", MainURL: "rtsp://b"})

	n, err := s.UseKey(ctx, testKey(2))
	if err != nil || n != 1 {
		t.Fatalf("unreadable %d, err %v", n, err)
	}
	cams, err := s.Cameras(ctx)
	if err != nil || len(cams) != 2 {
		t.Fatalf("one bad password must not hide cameras: %d %v", len(cams), err)
	}
	got, _ := s.Camera(ctx, c.ID)
	if got.Password != "" || !got.HasPassword {
		t.Fatalf("%+v", got)
	}
	if got, _ := s.Camera(ctx, other.ID); got.Name != "open" {
		t.Fatal("other camera")
	}
	// Saving without a new password must not destroy the stored value.
	raw := rawPassword(t, s, c.ID)
	got.Name = "renamed"
	if _, err := s.UpdateCamera(ctx, got); err != nil {
		t.Fatal(err)
	}
	if rawPassword(t, s, c.ID) != raw {
		t.Error("the unreadable password was overwritten")
	}
}
