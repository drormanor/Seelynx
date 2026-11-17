package api

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/rules"
	"seelynx/internal/store"
)

type CameraState struct {
	Online  bool
	Idle    bool // connects only when someone watches, and nobody is
	Mbps    float64
	Viewers int
}

// Monitor polls MediaMTX and turns connection changes into events.
type Monitor struct {
	Store *store.Store
	Media *media.Manager
	Log   *slog.Logger
	// Recording reports whether a camera should be recording now.
	Recording func(context.Context, store.Camera) bool
	// Events receives connection changes; may be nil.
	Events func(rules.Event)

	mu     sync.RWMutex
	state  map[int64]CameraState
	bytes  map[int64]uint64
	polled time.Time
}

func (m *Monitor) Get(id int64) CameraState {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.state[id]
}

func (m *Monitor) Forget(id int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.state, id)
	delete(m.bytes, id)
}

func (m *Monitor) Totals() (online int, mbps float64) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, s := range m.state {
		if s.Online {
			online++
		}
		mbps += s.Mbps
	}
	return online, mbps
}

func (m *Monitor) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := m.poll(ctx); err != nil && ctx.Err() == nil {
				m.Log.Warn("status poll failed", "err", err)
			}
		}
	}
}

func (m *Monitor) poll(ctx context.Context) error {
	paths, err := m.Media.Status(ctx)
	if err != nil {
		return err
	}
	cams, err := m.Store.Cameras(ctx)
	if err != nil {
		return err
	}
	now := time.Now()

	m.mu.Lock()
	if m.state == nil {
		m.state, m.bytes = map[int64]CameraState{}, map[int64]uint64{}
	}
	elapsed := now.Sub(m.polled).Seconds()
	first := m.polled.IsZero()
	m.polled = now
	var events []string
	for _, c := range cams {
		main := paths[media.PathName(c.ID, "main")]
		sub := paths[media.PathName(c.ID, "sub")]
		next := CameraState{Online: main.Ready || sub.Ready, Viewers: main.Readers + sub.Readers}
		// A main stream that connects only on demand is idle, not offline.
		recording := c.Record
		if m.Recording != nil {
			recording = m.Recording(ctx, c)
		}
		idle := !recording && !main.Ready
		next.Idle = idle && !next.Online

		received := main.BytesReceived + sub.BytesReceived
		if prev, seen := m.bytes[c.ID]; seen && !first && received >= prev && elapsed > 0 {
			next.Mbps = float64(received-prev) * 8 / 1e6 / elapsed
		}
		m.bytes[c.ID] = received

		prev, known := m.state[c.ID]
		if c.Enabled && !idle {
			switch {
			case next.Online && !prev.Online:
				events = append(events, fmt.Sprintf("%s connected", c.Name))
				if m.Events != nil && known {
					m.Events(rules.Event{Type: rules.TriggerOnline, CameraID: c.ID, CameraName: c.Name, Message: "Camera back online"})
				}
			case !next.Online && prev.Online && known:
				events = append(events, fmt.Sprintf("%s disconnected", c.Name))
				if m.Events != nil {
					m.Events(rules.Event{Type: rules.TriggerOffline, CameraID: c.ID, CameraName: c.Name, Message: "Camera offline"})
				}
			}
		}
		m.state[c.ID] = next
	}
	m.mu.Unlock()

	for _, e := range events {
		if err := m.Store.AddEvent(ctx, "info", e); err != nil {
			return err
		}
	}
	return nil
}
