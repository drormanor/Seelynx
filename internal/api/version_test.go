package api

import (
	"strings"
	"testing"
)

func TestVersionIsShownOnlyToSignedInUsers(t *testing.T) {
	e := newEnv(t)
	old := build
	t.Cleanup(func() { build = old })
	build = buildInfo{Version: "abc1234", Built: "2026-11-10T16:00:00Z"}
	if _, body := e.do(e.client(), "GET", "/api/session", nil); strings.Contains(body, "abc1234") {
		t.Errorf("the version was shown before signing in: %s", body)
	}
	if _, body := e.do(e.addUser("root", "admin"), "GET", "/api/session", nil); !strings.Contains(body, `"build":{"version":"abc1234","built":"2026-11-10T16:00:00Z"}`) {
		t.Errorf("no version after signing in: %s", body)
	}
}

func TestVersionSetWhenBuildingWins(t *testing.T) {
	old := Version
	t.Cleanup(func() { Version = old })
	Version = "1.2.3"
	if b := readBuild(); b.Version != "1.2.3" {
		t.Errorf("got %+v", b)
	}
	Version = ""
	if b := readBuild(); b.Version == "" {
		t.Error("no version at all")
	}
}
