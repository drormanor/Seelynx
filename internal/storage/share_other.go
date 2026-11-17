//go:build !windows

package storage

import "errors"

// On Linux a share is mounted by the system, with its login in the mount settings.
func connectShare(string, string, string) error {
	return errors.New("Seelynx signs in to network shares itself only on Windows. On Linux, mount the share, with its login in the mount settings, and enter the folder it's mounted at. See docs/DOCKER.md.")
}
