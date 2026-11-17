// Package api is the HTTP interface used by the web client.
package api

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/bcrypt"

	"seelynx/internal/media"
	"seelynx/internal/onvif"
	"seelynx/internal/retention"
	"seelynx/internal/rules"
	"seelynx/internal/storage"
	"seelynx/internal/store"
)

const (
	cookieName = "sl_session"
	sessionTTL = 12 * time.Hour
	minPass    = 10
)

type Server struct {
	Store        *store.Store
	Media        *media.Manager
	applyMedia   func(context.Context, store.Camera) error // replaces Media.Apply in tests
	Retention    *retention.Worker
	Storage      *storage.Manager // nil keeps all video in RecordingDir
	Monitor      *Monitor
	Motion       *MotionWatcher
	Rules        *rules.Engine
	Web          fs.FS
	RecordingDir string // recordings directory
	FFmpeg       string // path to ffmpeg; empty means pictures only come from cameras
	DataDir      string
	Secure       bool // set the Secure cookie flag (TLS enabled)
	Log          *slog.Logger

	limiter        limiter
	access         atomic.Pointer[[]netip.Prefix]
	schedMu        sync.Mutex
	applied        map[int64]bool
	scheduleWarned sync.Map
	snapshotMu     sync.Mutex
	snapshotURIs   map[int64]snapshotAddress
	pictureMu      sync.Mutex
	pictureLocks   map[int64]*sync.Mutex
	pictureSlots   chan struct{}
}

type ctxKey struct{}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) internal(w http.ResponseWriter, err error) {
	s.Log.Error("request failed", "err", err)
	fail(w, http.StatusInternalServerError, "Something went wrong on the server. Check the server log.")
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return false
	}
	return true
}

// need wraps a handler with a session check and a permission. An empty permission
// means any signed-in account.
func (s *Server) need(perm string, h http.HandlerFunc) http.HandlerFunc {
	return s.auth(perm, false, h)
}

func (s *Server) auth(perm string, allowStalePassword bool, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie(cookieName); err != nil {
			fail(w, http.StatusUnauthorized, "Please sign in.")
			return
		}
		u, _, err := s.sessionFor(r)
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusUnauthorized, "Your session has ended. Please sign in again.")
			return
		}
		if err != nil {
			s.internal(w, err)
			return
		}
		if !allowStalePassword && u.NeedsNewPassword(time.Now()) {
			fail(w, http.StatusForbidden, "Change your password to continue.")
			return
		}
		if !u.Can(perm) {
			fail(w, http.StatusForbidden, "Your account doesn't have permission to do this.")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	}
}

func (s *Server) Handler() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /api/session", s.session)
	m.HandleFunc("POST /api/setup", s.setup)
	m.HandleFunc("POST /api/login", s.login)
	m.HandleFunc("POST /api/logout", s.logout)

	m.HandleFunc("GET /api/cameras", s.need("", s.listCameras))
	m.HandleFunc("POST /api/cameras", s.need(store.PermCameras, s.createCamera))
	m.HandleFunc("PUT /api/cameras/{id}", s.need(store.PermCameras, s.updateCamera))
	m.HandleFunc("DELETE /api/cameras/{id}", s.need(store.PermCameras, s.deleteCamera))
	m.HandleFunc("PUT /api/cameras/{id}/fisheye", s.need(store.PermCameras, s.saveFisheye))
	m.HandleFunc("PUT /api/cameras/{id}/rotation", s.need(store.PermCameras, s.saveRotation))
	m.HandleFunc("POST /api/cameras/{id}/whep", s.need(store.PermLive, s.whep))
	m.HandleFunc("GET /api/cameras/{id}/recordings", s.need(store.PermPlayback, s.recordings))
	m.HandleFunc("GET /api/cameras/{id}/playback", s.need(store.PermPlayback, s.playback))
	m.HandleFunc("GET /api/cameras/{id}/export", s.need(store.PermPlayback, s.exportVideo))
	m.HandleFunc("GET /api/cameras/{id}/picture", s.need("", s.cameraPictureHandler))
	m.HandleFunc("GET /api/cameras/{id}/motion", s.need(store.PermPlayback, s.cameraMotion))
	m.HandleFunc("GET /api/motion", s.need(store.PermSystem, s.recentMotion))

	m.HandleFunc("POST /api/onvif/discover", s.need(store.PermCameras, s.discover))
	m.HandleFunc("POST /api/onvif/profiles", s.need(store.PermCameras, s.profiles))
	m.HandleFunc("GET /api/camera-login", s.need(store.PermCameras, s.getDefaultLogin))
	m.HandleFunc("PUT /api/camera-login", s.need(store.PermCameras, s.saveDefaultLogin))

	m.HandleFunc("GET /api/users", s.need(store.PermUsers, s.listUsers))
	m.HandleFunc("POST /api/users", s.need(store.PermUsers, s.createUser))
	m.HandleFunc("DELETE /api/users/{id}", s.need(store.PermUsers, s.deleteUser))
	m.HandleFunc("PUT /api/users/{id}", s.need(store.PermUsers, s.updateUser))
	m.HandleFunc("POST /api/password", s.auth("", true, s.changePassword))
	m.HandleFunc("GET /api/roles", s.need(store.PermUsers, s.listRoles))
	m.HandleFunc("POST /api/roles", s.need(store.PermUsers, s.saveRole))
	m.HandleFunc("PUT /api/roles/{id}", s.need(store.PermUsers, s.saveRole))
	m.HandleFunc("DELETE /api/roles/{id}", s.need(store.PermUsers, s.deleteRole))

	m.HandleFunc("GET /api/system", s.need(store.PermSystem, s.system))
	m.HandleFunc("GET /api/storage", s.need(store.PermSettings, s.listStorage))
	m.HandleFunc("POST /api/storage", s.need(store.PermSettings, s.saveStorage))
	m.HandleFunc("PUT /api/storage/{id}", s.need(store.PermSettings, s.saveStorage))
	m.HandleFunc("DELETE /api/storage/{id}", s.need(store.PermSettings, s.deleteStorage))
	m.HandleFunc("POST /api/storage/{id}/move", s.need(store.PermSettings, s.moveStorage))
	m.HandleFunc("POST /api/storage/{id}/move/stop", s.need(store.PermSettings, s.stopMoveStorage))
	m.HandleFunc("GET /api/storage/folders", s.need(store.PermSettings, s.listFolders))
	m.HandleFunc("POST /api/storage/folders", s.need(store.PermSettings, s.makeFolder))
	m.HandleFunc("POST /api/storage/connect", s.need(store.PermSettings, s.connectShare))

	m.HandleFunc("GET /api/config/{kind}", s.need("", s.listItems))
	m.HandleFunc("POST /api/config/{kind}", s.need(store.PermSettings, s.saveItem))
	m.HandleFunc("PUT /api/config/{kind}/{id}", s.need(store.PermSettings, s.saveItem))
	m.HandleFunc("DELETE /api/config/{kind}/{id}", s.need(store.PermSettings, s.deleteItem))

	m.HandleFunc("GET /api/backups", s.need(store.PermSettings, s.listBackups))
	m.HandleFunc("POST /api/backups", s.need(store.PermSettings, s.createBackup))
	m.HandleFunc("GET /api/backups/{name}", s.need(store.PermSettings, s.downloadBackup))
	m.HandleFunc("POST /api/restore", s.need(store.PermSettings, s.stageRestore))
	m.HandleFunc("GET /api/logs", s.need(store.PermSettings, s.downloadLogs))

	m.HandleFunc("GET /api/connection-types", s.need(store.PermSettings, s.connectionTypes))
	m.HandleFunc("GET /api/connections", s.need(store.PermSettings, s.listConnections))
	m.HandleFunc("POST /api/connections", s.need(store.PermSettings, s.saveConnection))
	m.HandleFunc("PUT /api/connections/{id}", s.need(store.PermSettings, s.saveConnection))
	m.HandleFunc("DELETE /api/connections/{id}", s.need(store.PermSettings, s.deleteConnection))
	m.HandleFunc("POST /api/connections/{id}/test", s.need(store.PermSettings, s.testConnection))
	m.HandleFunc("GET /api/alarms", s.need(store.PermSystem, s.listAlarms))
	m.HandleFunc("POST /api/alarms/{id}/ack", s.need(store.PermSystem, s.ackAlarm))
	m.HandleFunc("GET /api/alarms/{id}/snapshot", s.need(store.PermSystem, s.alarmSnapshot))
	m.HandleFunc("POST /api/hooks/{token}", s.inboundHook)

	m.HandleFunc("GET /api/ai", s.need(store.PermSettings, s.getAI))
	m.HandleFunc("PUT /api/ai", s.need(store.PermSettings, s.saveAI))
	m.HandleFunc("POST /api/ai/test", s.need(store.PermSettings, s.testAI))
	m.HandleFunc("POST /api/drivers/probe", s.need(store.PermSettings, s.probeDriver))
	m.HandleFunc("POST /api/drivers/build", s.need(store.PermSettings, s.buildDriver))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "Not found.") })
	m.Handle("/", s.spa())
	if err := s.reloadAccess(context.Background()); err != nil {
		s.Log.Error("load access list", "err", err)
	}
	return securityHeaders(s.enforceAccess(m))
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; media-src 'self' blob:; connect-src 'self'; img-src 'self' data: blob:")
		next.ServeHTTP(w, r)
	})
}

// spa serves built assets and falls back to index.html for client-side routes.
func (s *Server) spa() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" {
			if _, err := fs.Stat(s.Web, p); err == nil {
				files.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFileFS(w, r, s.Web, "index.html")
	})
}

// --- sessions ---

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	n, err := s.Store.UserCount(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	out := map[string]any{"needs_setup": n == 0, "user": nil, "must_change_password": false}
	if u, _, err := s.sessionFor(r); err == nil {
		out["user"] = u
		out["must_change_password"] = u.NeedsNewPassword(time.Now())
		// Only signed-in users learn the version, so a stranger can't look up its weaknesses.
		out["build"] = build
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, u store.User) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		s.internal(w, err)
		return
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	exp := time.Now().Add(sessionTTL)
	if err := s.Store.CreateSession(r.Context(), token, u.ID, exp); err != nil {
		s.internal(w, err)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: token, Path: "/", Expires: exp, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode})
	writeJSON(w, http.StatusOK, u)
}

type credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

func validNewUser(w http.ResponseWriter, c *credentials) bool {
	c.Username = strings.TrimSpace(c.Username)
	if c.Username == "" || len(c.Username) > 64 {
		fail(w, http.StatusBadRequest, "Enter a username of up to 64 characters.")
		return false
	}
	if msg := checkPassword(c.Password, c.Username, false); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return false
	}
	return true
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if !decode(w, r, &c) || !validNewUser(w, &c) {
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(c.Password), bcrypt.DefaultCost)
	if err != nil {
		s.internal(w, err)
		return
	}
	created, err := s.Store.CreateFirstUser(r.Context(), c.Username, string(hash))
	if err != nil {
		s.internal(w, err)
		return
	}
	if !created {
		fail(w, http.StatusConflict, "Setup is already complete. Sign in instead.")
		return
	}
	u, err := s.Store.UserByName(r.Context(), c.Username)
	if err != nil {
		s.internal(w, err)
		return
	}
	s.startSession(w, r, u)
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		fail(w, http.StatusTooManyRequests, "Too many failed sign-ins. Wait 15 minutes and try again.")
		return
	}
	var c credentials
	if !decode(w, r, &c) {
		return
	}
	u, err := s.Store.UserByName(r.Context(), strings.TrimSpace(c.Username))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internal(w, err)
		return
	}
	hash := []byte(u.PasswordHash)
	if err != nil {
		hash = dummyHash // same work for unknown users, so timing doesn't reveal them
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(c.Password)) != nil || u.ID == 0 {
		s.limiter.failed(ip)
		fail(w, http.StatusUnauthorized, "Incorrect username or password.")
		return
	}
	if u.Expired(time.Now()) {
		fail(w, http.StatusUnauthorized, "This account has expired. Contact an administrator.")
		return
	}
	s.limiter.reset(ip)
	s.startSession(w, r, u)
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("seelynx-unknown-user"), bcrypt.DefaultCost)

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			s.internal(w, err)
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteStrictMode})
	w.WriteHeader(http.StatusNoContent)
}

// limiter blocks an address after repeated sign-in failures.
type limiter struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	n     int
	first time.Time
}

const (
	maxFailures   = 10
	failureWindow = 15 * time.Minute
)

func (l *limiter) allow(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	a := l.m[ip]
	if a == nil {
		return true
	}
	if time.Since(a.first) > failureWindow {
		delete(l.m, ip)
		return true
	}
	return a.n < maxFailures
}

func (l *limiter) failed(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.m == nil {
		l.m = map[string]*attempts{}
	}
	if a := l.m[ip]; a != nil {
		a.n++
		return
	}
	l.m[ip] = &attempts{1, time.Now()}
}

func (l *limiter) reset(ip string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.m, ip)
}

// --- cameras ---

type cameraView struct {
	store.Camera
	Online  bool    `json:"online"`
	Idle    bool    `json:"idle"`
	Motion  bool    `json:"motion"`
	Mbps    float64 `json:"mbps"`
	Viewers int     `json:"viewers"`
}

func (s *Server) listCameras(w http.ResponseWriter, r *http.Request) {
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	u := r.Context().Value(ctxKey{}).(store.User)
	out := make([]cameraView, 0, len(cams))
	for _, c := range cams {
		if !u.CanSeeCamera(c.ID) {
			continue
		}
		st := s.Monitor.Get(c.ID)
		if !u.Can(store.PermCameras) {
			// Stream addresses reveal the camera network; only admins need them.
			c.MainURL, c.SubURL, c.Username = "", "", ""
		}
		out = append(out, cameraView{c, st.Online, st.Idle, s.Motion.Active(c.ID), st.Mbps, st.Viewers})
	}
	writeJSON(w, http.StatusOK, out)
}

type cameraInput struct {
	Name          string  `json:"name"`
	MainURL       string  `json:"main_url"`
	SubURL        string  `json:"sub_url"`
	Username      string  `json:"username"`
	Password      *string `json:"password"` // nil keeps the stored password
	Record        bool    `json:"record"`
	RetentionDays int     `json:"retention_days"`
	Enabled       bool    `json:"enabled"`
	GroupID       int64   `json:"group_id"`
	NumericID     int64   `json:"numeric_id"`
	ScheduleID    int64   `json:"schedule_id"`
	MotionOnly    bool    `json:"motion_only"`
	OutsideMode   int     `json:"outside_mode"`
	PreSeconds    int     `json:"pre_seconds"`
	PostSeconds   int     `json:"post_seconds"`
	// UseDefaultLogin gives a new camera the default login, in place of its own.
	UseDefaultLogin bool `json:"use_default_login"`
}

func validStreamURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && (u.Scheme == "rtsp" || u.Scheme == "rtsps")
}

func (in *cameraInput) apply(w http.ResponseWriter, c *store.Camera) bool {
	in.Name = strings.TrimSpace(in.Name)
	in.MainURL = strings.TrimSpace(in.MainURL)
	in.SubURL = strings.TrimSpace(in.SubURL)
	switch {
	case in.Name == "":
		fail(w, http.StatusBadRequest, "Enter a camera name.")
	case !validStreamURL(in.MainURL):
		fail(w, http.StatusBadRequest, "Main stream must be an rtsp:// or rtsps:// URL.")
	case in.SubURL != "" && !validStreamURL(in.SubURL):
		fail(w, http.StatusBadRequest, "Sub stream must be an rtsp:// or rtsps:// URL.")
	case in.NumericID < 0 || in.NumericID > 999999:
		fail(w, http.StatusBadRequest, "Numeric ID must be 0 to 999999.")
	case in.RetentionDays < 0 || in.RetentionDays > 3650:
		fail(w, http.StatusBadRequest, "Retention must be 0 to 3650 days. 0 keeps recordings until disk space runs low.")
	case in.PreSeconds < 0 || in.PreSeconds > 300 || in.PostSeconds < 0 || in.PostSeconds > 300:
		fail(w, http.StatusBadRequest, "Time before and after motion must be 0 to 300 seconds.")
	case in.OutsideMode < store.OutsideOff || in.OutsideMode > store.OutsideMotion:
		fail(w, http.StatusBadRequest, "Choose how the camera records outside its schedule.")
	default:
		old := *c
		c.MotionOnly, c.PreSeconds, c.PostSeconds = in.MotionOnly, in.PreSeconds, in.PostSeconds
		c.Name, c.MainURL, c.SubURL, c.Username = in.Name, in.MainURL, in.SubURL, in.Username
		c.Record, c.RetentionDays, c.Enabled = in.Record, in.RetentionDays, in.Enabled
		c.GroupID, c.NumericID, c.ScheduleID, c.OutsideMode = in.GroupID, in.NumericID, in.ScheduleID, in.OutsideMode
		// When which times keep only motion changes, video from before stays.
		changed := old.Record != c.Record || old.MotionOnly != c.MotionOnly || old.ScheduleID != c.ScheduleID || old.OutsideMode != c.OutsideMode
		if c.UsesMotion() && (changed || old.MotionSince == 0) {
			c.MotionSince = time.Now().Unix()
		}
		if in.Password != nil {
			c.Password = *in.Password
		}
		return true
	}
	return false
}

func (s *Server) createCamera(w http.ResponseWriter, r *http.Request) {
	var in cameraInput
	if !decode(w, r, &in) {
		return
	}
	var c store.Camera
	if !in.apply(w, &c) {
		return
	}
	if in.UseDefaultLogin {
		l, ok := s.defaultLogin(w, r)
		if !ok {
			return
		}
		c.Username, c.Password = l.Username, l.Password
	}
	if !s.numericFree(w, r, c) {
		return
	}
	c, err := s.Store.CreateCamera(r.Context(), c)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.ApplyCamera(r.Context(), c); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, http.StatusNotFound, "Not found.")
		return 0, false
	}
	return id, true
}

func (s *Server) camera(w http.ResponseWriter, r *http.Request) (store.Camera, bool) {
	id, ok := pathID(w, r)
	if !ok {
		return store.Camera{}, false
	}
	c, err := s.Store.Camera(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Camera not found.")
		return c, false
	}
	if err != nil {
		s.internal(w, err)
		return c, false
	}
	return c, true
}

func (s *Server) updateCamera(w http.ResponseWriter, r *http.Request) {
	c, ok := s.camera(w, r)
	if !ok {
		return
	}
	old := c
	var in cameraInput
	if !decode(w, r, &in) || !in.apply(w, &c) {
		return
	}
	if !s.numericFree(w, r, c) {
		return
	}
	c, err := s.Store.UpdateCamera(r.Context(), c)
	if err != nil {
		s.internal(w, err)
		return
	}
	if c.MainURL != old.MainURL || c.SubURL != old.SubURL {
		s.forgetPicture(c.ID)
	}
	if err := s.ApplyCamera(r.Context(), c); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (s *Server) deleteCamera(w http.ResponseWriter, r *http.Request) {
	c, ok := s.camera(w, r)
	if !ok {
		return
	}
	if err := s.Store.DeleteCamera(r.Context(), c.ID); err != nil {
		s.internal(w, err)
		return
	}
	if err := s.Media.Remove(r.Context(), c.ID); err != nil {
		s.internal(w, err)
		return
	}
	s.Monitor.Forget(c.ID)
	s.forgetPicture(c.ID)
	w.WriteHeader(http.StatusNoContent)
}

// --- video ---

func (s *Server) whep(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	stream := "sub"
	if r.URL.Query().Get("stream") == "main" || c.SubURL == "" {
		stream = "main"
	}
	offer, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		fail(w, http.StatusBadRequest, "Invalid offer.")
		return
	}
	code, answer, err := s.Media.WHEP(r.Context(), media.PathName(c.ID, stream), offer)
	if err != nil {
		s.internal(w, err)
		return
	}
	if code != http.StatusCreated {
		s.Log.Warn("whep rejected", "camera", c.ID, "status", code, "body", string(answer))
		fail(w, http.StatusBadGateway, "This camera isn't sending video right now.")
		return
	}
	w.Header().Set("Content-Type", "application/sdp")
	w.WriteHeader(http.StatusCreated)
	w.Write(answer)
}

func (s *Server) recordings(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	start, err1 := time.Parse(time.RFC3339, r.URL.Query().Get("start"))
	end, err2 := time.Parse(time.RFC3339, r.URL.Query().Get("end"))
	if err1 != nil || err2 != nil || !end.After(start) {
		fail(w, http.StatusBadRequest, "Invalid time range.")
		return
	}
	segs, err := s.Media.Recordings(r.Context(), c.ID, start, end)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, segs)
}

// videoRange reads the start and length of a requested stretch of recording.
func videoRange(w http.ResponseWriter, r *http.Request) (time.Time, float64, bool) {
	start, err := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
	if err != nil {
		fail(w, http.StatusBadRequest, "Invalid start time.")
		return start, 0, false
	}
	seconds, err := strconv.ParseFloat(r.URL.Query().Get("duration"), 64)
	if err != nil || seconds <= 0 || seconds > 6*3600 {
		fail(w, http.StatusBadRequest, "Duration must be up to 6 hours.")
		return start, 0, false
	}
	return start, seconds, true
}

// sendVideo streams recorded video. The before function runs once there's video to send.
func (s *Server) sendVideo(w http.ResponseWriter, r *http.Request, c store.Camera, start time.Time, seconds float64, format string, before func()) {
	s.sendVideoWith(w, r, c, start, seconds, format, before, nil)
}

// sendVideoWith is sendVideo with the video passed through send, when it isn't nil.
func (s *Server) sendVideoWith(w http.ResponseWriter, r *http.Request, c store.Camera, start time.Time, seconds float64, format string, before func(), send func(io.Writer, io.Reader) error) {
	res, err := s.Media.Playback(r.Context(), c.ID, start, seconds, format)
	if err != nil {
		if r.Context().Err() == nil {
			s.internal(w, err)
		}
		return
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		s.Log.Info("no recording to play", "camera", c.ID, "start", start.Format(time.RFC3339Nano), "seconds", seconds)
		fail(w, http.StatusNotFound, "No recording at this time.")
		return
	}
	if res.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(res.Body, 2048))
		s.internal(w, fmt.Errorf("mediamtx get: %d %s", res.StatusCode, msg))
		return
	}
	before()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	if send == nil {
		io.Copy(w, res.Body)
		return
	}
	if err := send(w, res.Body); err != nil && r.Context().Err() == nil {
		s.Log.Warn("sending video", "camera", c.ID, "err", err)
	}
}

func (s *Server) playback(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	start, seconds, ok := videoRange(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("keyframes") == "1" && s.FFmpeg != "" {
		s.sendVideoWith(w, r, c, start, seconds, "fmp4", func() {}, func(out io.Writer, in io.Reader) error {
			return keyframesOnly(r.Context(), s.FFmpeg, out, in)
		})
		return
	}
	s.sendVideo(w, r, c, start, seconds, "fmp4", func() {})
}

// keyframesOnly passes on only a video's keyframes, the whole pictures a camera sends
// about once a second, with their times unchanged. Browsers can't decode full-HD video
// eight times faster than real time, and froze at 8x. With keyframes only, 8x
// decodes a few pictures a second. Nothing is re-encoded.
func keyframesOnly(ctx context.Context, ffmpeg string, out io.Writer, in io.Reader) error {
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostats", "-v", "error", "-i", "pipe:0", "-map", "0:v:0", "-c", "copy",
		"-bsf:v", "noise=drop=not(key)", "-f", "mp4", "-movflags", "frag_keyframe+empty_moov+default_base_moof", "pipe:1")
	cmd.Stdin, cmd.Stdout = in, out
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// --- onvif ---

func (s *Server) profiles(w http.ResponseWriter, r *http.Request) {
	var in struct {
		XAddr           string `json:"xaddr"`
		Username        string `json:"username"`
		Password        string `json:"password"`
		UseDefaultLogin bool   `json:"use_default_login"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.UseDefaultLogin {
		l, ok := s.defaultLogin(w, r)
		if !ok {
			return
		}
		in.Username, in.Password = l.Username, l.Password
	}
	u, err := url.Parse(in.XAddr)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		fail(w, http.StatusBadRequest, "Enter a valid ONVIF device address.")
		return
	}
	c := onvif.Client{XAddr: in.XAddr, User: in.Username, Pass: in.Password}
	ps, err := c.Profiles(r.Context())
	if errors.Is(err, onvif.ErrUnauthorized) {
		fail(w, http.StatusUnprocessableEntity, "The camera rejected that username or password.")
		return
	}
	if err != nil {
		s.Log.Warn("onvif profiles failed", "err", err)
		fail(w, http.StatusBadGateway, "Couldn't read stream details from this camera. Check its ONVIF settings, or add it by RTSP URL.")
		return
	}
	writeJSON(w, http.StatusOK, ps)
}

// --- users ---

// --- system ---

func (s *Server) system(w http.ResponseWriter, r *http.Request) {
	free, total, err := s.diskTotals()
	if err != nil {
		s.internal(w, err)
		return
	}
	events, err := s.Store.Events(r.Context(), 100)
	if err != nil {
		s.internal(w, err)
		return
	}
	online, mbps := s.Monitor.Totals()
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"disk_free": free, "disk_total": total, "recorded_bytes": s.Retention.RecordedBytes(),
		"cameras": len(cams), "online": online, "ingest_mbps": mbps, "events": events,
	})
}
