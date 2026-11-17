//go:build linux

package media

import (
	"os/exec"
	"syscall"
)

// prepareChild asks the kernel to kill the child if this process dies.
func prepareChild(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}

func bindChild(*exec.Cmd) error { return nil }
