package api

import (
	"runtime/debug"
	"time"
)

// Version can be set when building, with -ldflags "-X seelynx/internal/api.Version=...".
// Docker builds have no Git history, so they pass it in. Otherwise it comes from the
// commit Go records in the program.
var Version = ""

type buildInfo struct {
	Version string `json:"version"`
	// Built is the commit's time, or empty when it isn't known.
	Built string `json:"built,omitempty"`
}

func readBuild() buildInfo {
	if Version != "" {
		return buildInfo{Version: Version}
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return buildInfo{Version: "dev"}
	}
	var rev, at string
	modified := false
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			rev = s.Value
		case "vcs.time":
			at = s.Value
		case "vcs.modified":
			modified = s.Value == "true"
		}
	}
	if rev == "" {
		return buildInfo{Version: "dev"}
	}
	out := buildInfo{Version: rev[:min(7, len(rev))]}
	if modified {
		// Built with changes not yet committed, so the commit alone doesn't say what it is.
		out.Version += "+"
	}
	if t, err := time.Parse(time.RFC3339, at); err == nil {
		out.Built = t.UTC().Format(time.RFC3339)
	}
	return out
}

var build = readBuild()
