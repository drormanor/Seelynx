//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"golang.org/x/sys/windows/svc"
)

type serviceRun struct {
	requests chan svc.ChangeRequest
	status   chan svc.Status
	result   chan [2]any
}

func startService(h *serviceHandler) *serviceRun {
	r := &serviceRun{make(chan svc.ChangeRequest), make(chan svc.Status, 8), make(chan [2]any, 1)}
	go func() {
		special, code := h.Execute(nil, r.requests, r.status)
		r.result <- [2]any{special, code}
	}()
	return r
}

func (r *serviceRun) waitFor(t *testing.T, state svc.State) svc.Status {
	t.Helper()
	for {
		select {
		case s := <-r.status:
			if s.State == state {
				return s
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("never reached state %d", state)
		}
	}
}

func (r *serviceRun) finish(t *testing.T) (bool, uint32) {
	t.Helper()
	select {
	case res := <-r.result:
		return res[0].(bool), res[1].(uint32)
	case <-time.After(3 * time.Second):
		t.Fatal("the service never finished")
		return false, 0
	}
}

func TestStopAndShutdownCloseTheServerCleanly(t *testing.T) {
	for _, cmd := range []svc.Cmd{svc.Stop, svc.Shutdown} {
		closed := make(chan struct{})
		h := &serviceHandler{work: func(ctx context.Context) error {
			<-ctx.Done()
			close(closed)
			return nil
		}}
		r := startService(h)
		running := r.waitFor(t, svc.Running)
		if running.Accepts&svc.AcceptStop == 0 || running.Accepts&svc.AcceptShutdown == 0 {
			t.Fatalf("accepts %v", running.Accepts)
		}
		r.requests <- svc.ChangeRequest{Cmd: cmd}
		if hint := r.waitFor(t, svc.StopPending).WaitHint; hint == 0 {
			t.Error("Windows wasn't told how long to wait")
		}
		if special, code := r.finish(t); special || code != 0 {
			t.Fatalf("%v %d", special, code)
		}
		select {
		case <-closed:
		default:
			t.Fatal("the server wasn't told to stop")
		}
		if h.failure != nil {
			t.Fatal(h.failure)
		}
	}
}

func TestInterrogateIsAnswered(t *testing.T) {
	h := &serviceHandler{work: func(ctx context.Context) error { <-ctx.Done(); return nil }}
	r := startService(h)
	r.waitFor(t, svc.Running)
	r.requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: svc.Status{State: svc.Running}}
	r.waitFor(t, svc.Running)
	r.requests <- svc.ChangeRequest{Cmd: svc.Stop}
	r.finish(t)
}

// A nonzero exit code is what makes Windows' recovery settings restart the service.
func TestServerFailureReportsAnError(t *testing.T) {
	for name, err := range map[string]error{"error": errors.New("disk gone"), "silent exit": nil} {
		h := &serviceHandler{work: func(context.Context) error { return err }}
		r := startService(h)
		if special, code := r.finish(t); !special || code == 0 {
			t.Fatalf("%s: %v %d", name, special, code)
		}
		if h.failure == nil {
			t.Fatalf("%s: no reason recorded", name)
		}
	}
}

func TestStuckServerDoesNotHoldUpWindows(t *testing.T) {
	saved := serviceStopWait
	serviceStopWait = 50 * time.Millisecond
	defer func() { serviceStopWait = saved }()
	block := make(chan struct{})
	defer close(block)
	h := &serviceHandler{work: func(context.Context) error { <-block; return nil }}
	r := startService(h)
	r.waitFor(t, svc.Running)
	r.requests <- svc.ChangeRequest{Cmd: svc.Stop}
	r.finish(t)
	if h.failure == nil {
		t.Fatal("no reason recorded")
	}
}

func TestATestRunIsNotAService(t *testing.T) {
	if isService() {
		t.Fatal("a normal process was taken for a service")
	}
}
