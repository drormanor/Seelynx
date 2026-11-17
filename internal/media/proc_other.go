//go:build !windows && !linux

package media

import "os/exec"

func prepareChild(*exec.Cmd) {}

func bindChild(*exec.Cmd) error { return nil }
