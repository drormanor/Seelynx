package storage

import (
	"strings"
)

// IsShare reports whether a path is a Windows network share, such as \\nas\video.
func IsShare(path string) bool {
	return strings.HasPrefix(path, `\\`) && !strings.HasPrefix(path, `\\?\`) && !strings.HasPrefix(path, `\\.\`)
}

// shareRoot is the \\server\share part of a share path, which is what a login is for.
func shareRoot(path string) string {
	if !IsShare(path) {
		return ""
	}
	parts := strings.SplitN(strings.TrimPrefix(path, `\\`), `\`, 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return ""
	}
	return `\\` + parts[0] + `\` + parts[1]
}

// Connect signs in to the network share that holds path, for Seelynx and the video
// engine it starts, which run as the same account. The answer is meant for people.
// It's replaced in tests.
var Connect = connectShare

// connectIfShare signs in when a location is on a share with a login. Being signed in
// already isn't an error.
func connectIfShare(path, user, password string) error {
	if user == "" || !IsShare(path) {
		return nil
	}
	return Connect(path, user, password)
}
