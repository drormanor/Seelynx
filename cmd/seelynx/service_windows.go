//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/eventlog"
)

const serviceName = "Seelynx"

// serviceStopWait is how long a stop may take. Windows is told the same, so it waits
// for the database and video engine to close properly.
var serviceStopWait = 30 * time.Second

func isService() bool {
	ok, err := svc.IsWindowsService()
	return err == nil && ok
}

// runService hands control to Windows. Startup errors go to the Event Log, because they
// can happen before the log file is open and a service has no console.
func runService(work func(context.Context) error) error {
	h := &serviceHandler{work: work}
	err := svc.Run(serviceName, h)
	if err == nil {
		err = h.failure
	}
	if err != nil {
		if el, openErr := eventlog.Open(serviceName); openErr == nil {
			el.Error(1, "Seelynx stopped: "+err.Error())
			el.Close()
		}
	}
	return err
}

type serviceHandler struct {
	work    func(context.Context) error
	failure error
}

// Execute answers Windows' service requests. A nonzero exit code counts as a failure, so
// the recovery settings restart the service if the server stops by itself.
func (h *serviceHandler) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	const accepts = svc.AcceptStop | svc.AcceptShutdown
	status <- svc.Status{State: svc.StartPending}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- h.work(ctx) }()
	status <- svc.Status{State: svc.Running, Accepts: accepts}
	for {
		select {
		case err := <-done:
			if err == nil {
				err = errors.New("the server stopped unexpectedly")
			}
			h.failure = err
			return true, 1
		case r := <-requests:
			switch r.Cmd {
			case svc.Interrogate:
				status <- r.CurrentStatus
			case svc.Stop, svc.Shutdown:
				status <- svc.Status{State: svc.StopPending, WaitHint: uint32(serviceStopWait / time.Millisecond)}
				cancel()
				select {
				case err := <-done:
					h.failure = err
				case <-time.After(serviceStopWait):
					h.failure = fmt.Errorf("the server didn't stop within %s", serviceStopWait)
				}
				return false, 0
			}
		}
	}
}
