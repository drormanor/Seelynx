// Package media supervises the MediaMTX child process and talks to its
// localhost-only control, WebRTC and playback endpoints.
package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"seelynx/internal/store"
)

const internalUser = "seelynx"

type Config struct {
	Binary       string // path to the mediamtx executable
	DataDir      string
	RecordingDir string
	WebRTCUDP    string // host:port browsers send media to
	PortOffset   int    // added to the engine's local ports, to run a second copy
	Log          *slog.Logger
}

type Manager struct {
	cfg                                         Config
	pass                                        string
	apiAddr, playbackAddr, webrtcAddr, rtspAddr string
	client                                      *http.Client // bounded calls to the control API
	stream                                      *http.Client // unbounded: playback can run for a long time

	mu         sync.Mutex
	applied    map[string]pathConf // as last sent, so an unchanged path isn't restarted
	placements map[int64]Placement
}

// Placement says where a camera's video goes, and where its older video is.
type Placement struct {
	Record string // folder of the location it records to now
	// Archives holds the folders of the locations that can be read now, by location ID.
	// Video a camera left in a location it no longer records to is in a folder named
	// by ArchiveName, so each location's video can be played back on its own.
	Archives map[int64]string
}

// ArchiveName is the folder, and engine path, of a camera's video in a location it
// doesn't record to now.
func ArchiveName(id, location int64) string {
	return fmt.Sprintf("%s_at%d", PathName(id, "main"), location)
}

// belongs reports whether an engine path is one of the camera's.
func belongs(name string, id int64) bool {
	if name == PathName(id, "main") || name == PathName(id, "sub") {
		return true
	}
	at, ok := strings.CutPrefix(name, PathName(id, "main")+"_at")
	if !ok {
		return false
	}
	_, err := strconv.ParseInt(at, 10, 64)
	return err == nil
}

func recordPath(dir string) string {
	return filepath.ToSlash(dir) + "/%path/%Y-%m-%d_%H-%M-%S-%f"
}

func New(cfg Config) (*Manager, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	local := func(port int) string { return fmt.Sprintf("127.0.0.1:%d", port+cfg.PortOffset) }
	return &Manager{
		cfg:     cfg,
		apiAddr: local(19997), playbackAddr: local(19996), webrtcAddr: local(18889), rtspAddr: local(18654),
		pass:   hex.EncodeToString(b),
		client: &http.Client{Timeout: 10 * time.Second},
		stream: &http.Client{},
	}, nil
}

// ReadURL is where Seelynx itself reads a camera's live stream. It holds the engine's
// password, so it must never be logged or sent to a browser.
func (m *Manager) ReadURL(id int64, stream string) string {
	return fmt.Sprintf("rtsp://%s:%s@%s/%s", internalUser, m.pass, m.rtspAddr, PathName(id, stream))
}

func PathName(id int64, stream string) string { return fmt.Sprintf("cam_%d_%s", id, stream) }

// configYAML is written fresh at each start. The password is random per run,
// and every listener except the WebRTC media port is bound to localhost.
func (m *Manager) configYAML() string {
	rec := filepath.ToSlash(m.cfg.RecordingDir)
	return fmt.Sprintf(`logLevel: info
logDestinations: [stdout]
authMethod: internal
authInternalUsers:
  - user: %s
    pass: %s
    ips: ['127.0.0.1', '::1']
    permissions:
      - action: api
      - action: read
      - action: playback
api: true
apiAddress: %s
playback: true
playbackAddress: %s
rtsp: true
rtspTransports: [tcp]
rtspAddress: %s
rtmp: false
hls: false
srt: false
moq: false
webrtc: true
webrtcAddress: %s
webrtcLocalUDPAddress: %s
pathDefaults:
  recordPath: %s/%%path/%%Y-%%m-%%d_%%H-%%M-%%S-%%f
  recordFormat: fmp4
  recordSegmentDuration: 5m
  recordDeleteAfter: 0s
`, internalUser, m.pass, m.apiAddr, m.playbackAddr, m.rtspAddr, m.webrtcAddr, m.cfg.WebRTCUDP, rec)
}

// Run keeps MediaMTX alive until ctx ends. onStart runs after every successful
// start, because a fresh process has no camera paths.
func (m *Manager) Run(ctx context.Context, onStart func(context.Context)) error {
	cfgPath := filepath.Join(m.cfg.DataDir, "mediamtx.yml")
	if err := os.WriteFile(cfgPath, []byte(m.configYAML()), 0o600); err != nil {
		return err
	}
	logPath := filepath.Join(m.cfg.DataDir, "mediamtx.log")
	for ctx.Err() == nil {
		logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		cmd := exec.CommandContext(ctx, m.cfg.Binary, cfgPath)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		cmd.WaitDelay = 5 * time.Second
		cmd.Dir = m.cfg.DataDir
		prepareChild(cmd)
		started := time.Now()
		if err := cmd.Start(); err != nil {
			logFile.Close()
			return fmt.Errorf("start mediamtx: %w", err)
		}
		if err := bindChild(cmd); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			logFile.Close()
			return err
		}
		m.mu.Lock()
		m.applied = map[string]pathConf{} // a fresh process has no paths
		m.mu.Unlock()
		go func() {
			if m.waitReady(ctx) {
				onStart(ctx)
			}
		}()
		err = cmd.Wait()
		logFile.Close()
		if ctx.Err() != nil {
			return nil
		}
		m.cfg.Log.Error("mediamtx exited, restarting", "err", err, "uptime", time.Since(started).Round(time.Second))
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
	return nil
}

func (m *Manager) waitReady(ctx context.Context) bool {
	for i := 0; i < 100 && ctx.Err() == nil; i++ {
		if _, err := m.configuredPaths(ctx); err == nil {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

func (m *Manager) api(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://"+m.apiAddr+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(internalUser, m.pass)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return fmt.Errorf("mediamtx %s %s: %d %s", method, strings.SplitN(path, "?", 2)[0], res.StatusCode, msg)
	}
	if out != nil {
		return json.NewDecoder(res.Body).Decode(out)
	}
	return nil
}

func (m *Manager) configuredPaths(ctx context.Context) (map[string]bool, error) {
	var list struct {
		Items []struct {
			Name string `json:"name"`
		} `json:"items"`
	}
	if err := m.api(ctx, http.MethodGet, "/v3/config/paths/list?itemsPerPage=10000", nil, &list); err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(list.Items))
	for _, it := range list.Items {
		out[it.Name] = true
	}
	return out, nil
}

func sourceURL(raw, user, pass string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid stream URL")
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	return u.String(), nil
}

type pathConf struct {
	Source         string `json:"source"`
	SourceOnDemand bool   `json:"sourceOnDemand"`
	Record         bool   `json:"record"`
	RTSPTransport  string `json:"rtspTransport,omitempty"`
	RecordPath     string `json:"recordPath,omitempty"`
	// SegmentLength overrides the file length, and is empty for the default.
	SegmentLength string `json:"recordSegmentDuration,omitempty"`
}

// motionSegment is short so that what's kept stays close to the camera's settings.
const motionSegment = "10s"

// desired returns the paths a camera should have. The main stream stays
// connected only while recording; everything else connects when someone watches.
// Each location's older video gets a path with no source, used only for playback.
func desired(c store.Camera, p Placement) (map[string]pathConf, error) {
	out := map[string]pathConf{}
	if !c.Enabled {
		return out, nil
	}
	main, err := sourceURL(c.MainURL, c.Username, c.Password)
	if err != nil {
		return nil, err
	}
	mainPath := pathConf{Source: main, SourceOnDemand: !c.Record, Record: c.Record, RTSPTransport: "tcp"}
	if p.Record != "" {
		mainPath.RecordPath = recordPath(p.Record)
	}
	for loc, dir := range p.Archives {
		out[ArchiveName(c.ID, loc)] = pathConf{Source: "publisher", RecordPath: recordPath(dir)}
	}
	if c.UsesMotion() {
		mainPath.SegmentLength = motionSegment
	}
	out[PathName(c.ID, "main")] = mainPath
	if c.SubURL != "" {
		sub, err := sourceURL(c.SubURL, c.Username, c.Password)
		if err != nil {
			return nil, err
		}
		out[PathName(c.ID, "sub")] = pathConf{Source: sub, SourceOnDemand: true, RTSPTransport: "tcp"}
	}
	return out, nil
}

// Apply makes MediaMTX match one camera's configuration.
func (m *Manager) Apply(ctx context.Context, c store.Camera, p Placement) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, err := m.configuredPaths(ctx)
	if err != nil {
		return err
	}
	want, err := desired(c, p)
	if err != nil {
		return err
	}
	names := []string{}
	for name := range existing {
		if belongs(name, c.ID) {
			names = append(names, name)
		}
	}
	for name := range want {
		if !existing[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if m.applied == nil {
		m.applied = map[string]pathConf{}
	}
	for _, name := range names {
		conf, wanted := want[name]
		switch {
		case wanted && existing[name]:
			if last, ok := m.applied[name]; ok && last == conf {
				continue // replacing would reconnect the camera for nothing
			}
			err = m.api(ctx, http.MethodPost, "/v3/config/paths/replace/"+name, conf, nil)
		case wanted:
			err = m.api(ctx, http.MethodPost, "/v3/config/paths/add/"+name, conf, nil)
		default:
			err = m.api(ctx, http.MethodDelete, "/v3/config/paths/delete/"+name, nil, nil)
		}
		if err != nil {
			delete(m.applied, name)
			return err
		}
		if wanted {
			m.applied[name] = conf
		} else {
			delete(m.applied, name)
		}
	}
	if m.placements == nil {
		m.placements = map[int64]Placement{}
	}
	if c.Enabled {
		m.placements[c.ID] = p
	} else {
		delete(m.placements, c.ID)
	}
	return nil
}

// Remove drops all paths for a deleted camera. Recordings stay on disk until
// retention removes them.
func (m *Manager) Remove(ctx context.Context, id int64) error {
	return m.Apply(ctx, store.Camera{ID: id}, Placement{})
}

// Folder is where a camera is recording now, or "" if it isn't set up.
func (m *Manager) Folder(id int64) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.placements[id]
	if !ok || p.Record == "" {
		return ""
	}
	return filepath.Join(p.Record, PathName(id, "main"))
}

// sources lists the camera's engine paths that hold video, with their folders.
func (m *Manager) sources(id int64) map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.placements[id]
	if !ok {
		return nil
	}
	out := map[string]string{}
	if p.Record != "" {
		out[PathName(id, "main")] = filepath.Join(p.Record, PathName(id, "main"))
	}
	for loc, dir := range p.Archives {
		out[ArchiveName(id, loc)] = filepath.Join(dir, ArchiveName(id, loc))
	}
	return out
}

type PathStatus struct {
	Ready         bool
	BytesReceived uint64
	Readers       int
}

func (m *Manager) Status(ctx context.Context) (map[string]PathStatus, error) {
	var list struct {
		Items []struct {
			Name          string            `json:"name"`
			Ready         bool              `json:"ready"`
			BytesReceived uint64            `json:"bytesReceived"`
			Readers       []json.RawMessage `json:"readers"`
		} `json:"items"`
	}
	if err := m.api(ctx, http.MethodGet, "/v3/paths/list?itemsPerPage=10000", nil, &list); err != nil {
		return nil, err
	}
	out := make(map[string]PathStatus, len(list.Items))
	for _, it := range list.Items {
		out[it.Name] = PathStatus{Ready: it.Ready, BytesReceived: it.BytesReceived, Readers: len(it.Readers)}
	}
	return out, nil
}

// WHEP forwards a browser's SDP offer and returns MediaMTX's answer.
func (m *Manager) WHEP(ctx context.Context, path string, offer []byte) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+m.webrtcAddr+"/"+path+"/whep", bytes.NewReader(offer))
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(internalUser, m.pass)
	req.Header.Set("Content-Type", "application/sdp")
	res, err := m.stream.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, body, err
}

type Segment struct {
	Start    time.Time `json:"start"`
	Duration float64   `json:"duration"`
	path     string    // the engine path holding it
}

func (s Segment) end() time.Time {
	return s.Start.Add(time.Duration(s.Duration * float64(time.Second)))
}

// Recordings lists a camera's recorded spans in every location.
func (m *Manager) Recordings(ctx context.Context, id int64, start, end time.Time) ([]Segment, error) {
	out := []Segment{}
	for path, dir := range m.sources(id) {
		// MediaMTX reports a missing folder as a server error, so check first.
		if _, err := os.Stat(dir); err != nil {
			continue
		}
		segs, err := m.list(ctx, path, start, end)
		if err != nil {
			return nil, err
		}
		for _, s := range segs {
			s.path = path
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start.Before(out[j].Start) })
	return out, nil
}

// list asks for one path's spans. MediaMTX answers 404 when a path has no recordings,
// which is an empty result here.
func (m *Manager) list(ctx context.Context, path string, start, end time.Time) ([]Segment, error) {
	q := url.Values{"path": {path}, "start": {start.Format(time.RFC3339)}, "end": {end.Format(time.RFC3339)}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.playbackAddr+"/list?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(internalUser, m.pass)
	res, err := m.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	out := []Segment{}
	if res.StatusCode == http.StatusNotFound {
		return out, nil
	}
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		return nil, fmt.Errorf("mediamtx list: %d %s", res.StatusCode, msg)
	}
	return out, json.NewDecoder(res.Body).Decode(&out)
}

// Playback opens a recorded span as a single stream. The caller closes it. The format is
// "fmp4" for watching and "mp4" for a file that plays anywhere. The video comes from the
// location holding the first span that reaches past start.
func (m *Manager) Playback(ctx context.Context, id int64, start time.Time, seconds float64, format string) (*http.Response, error) {
	path := PathName(id, "main")
	segs, err := m.Recordings(ctx, id, start, start.Add(time.Duration(seconds*float64(time.Second))))
	if err != nil {
		return nil, err
	}
	for _, s := range segs {
		if s.end().After(start) {
			path = s.path
			// A start in a gap, even one a few microseconds wide, finds nothing in the
			// engine. Browsers send whole milliseconds, which land just before a recording
			// that began part of one in, so the video starts where the recording does.
			if gap := s.Start.Sub(start); gap > 0 {
				start = s.Start
				seconds -= gap.Seconds()
			}
			break
		}
	}
	q := url.Values{"path": {path}, "start": {start.Format(time.RFC3339Nano)}, "duration": {fmt.Sprintf("%g", seconds)}, "format": {format}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+m.playbackAddr+"/get?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.SetBasicAuth(internalUser, m.pass)
	return m.stream.Do(req)
}
