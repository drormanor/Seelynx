//go:build windows

package media

import (
	"fmt"
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	jobOnce sync.Once
	job     windows.Handle // held open for the life of this process
	jobErr  error
)

// prepareChild has nothing to set before start on Windows.
func prepareChild(*exec.Cmd) {}

// bindChild puts the child in a job that Windows kills when this process exits
// for any reason, including a crash. An orphaned engine would keep the ports
// and a password the restarted server no longer knows.
func bindChild(cmd *exec.Cmd) error {
	jobOnce.Do(func() {
		job, jobErr = windows.CreateJobObject(nil, nil)
		if jobErr != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		_, jobErr = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)))
	})
	if jobErr != nil {
		return fmt.Errorf("create job object: %w", jobErr)
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fmt.Errorf("open child: %w", err)
	}
	defer windows.CloseHandle(h)
	return windows.AssignProcessToJobObject(job, h)
}
