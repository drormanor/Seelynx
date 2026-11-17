package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"seelynx/internal/onvif"
	"seelynx/internal/store"
)

// A picture of each camera on the Cameras page helps tell cameras apart. It comes from
// the live stream when ffmpeg is there, otherwise from the camera itself, and the last
// one is kept on disk, so a camera that is offline still shows what it looked at.
const (
	pictureFolder  = "pictures"
	pictureFresh   = time.Minute
	pictureTimeout = 15 * time.Second
	pictureWidth   = 640
	pictureAtOnce  = 4 // a page of 100 cameras mustn't start 100 ffmpegs
)

// discoverDevices is replaced in tests.
var discoverDevices = onvif.Discover

func (s *Server) picturePath(id int64) string {
	if s.DataDir == "" {
		return ""
	}
	return filepath.Join(s.DataDir, pictureFolder, fmt.Sprintf("%d.jpg", id))
}

func (s *Server) pictureLock(id int64) *sync.Mutex {
	s.pictureMu.Lock()
	defer s.pictureMu.Unlock()
	if s.pictureLocks == nil {
		s.pictureLocks = map[int64]*sync.Mutex{}
		s.pictureSlots = make(chan struct{}, pictureAtOnce)
	}
	l := s.pictureLocks[id]
	if l == nil {
		l = &sync.Mutex{}
		s.pictureLocks[id] = l
	}
	return l
}

// picture returns the camera's picture and when it was taken. A picture under a minute
// old is reused unless fresh is set. If a new one can't be taken, the last one is used.
func (s *Server) picture(ctx context.Context, c store.Camera, fresh bool) ([]byte, time.Time, error) {
	l := s.pictureLock(c.ID)
	l.Lock()
	defer l.Unlock()
	path := s.picturePath(c.ID)
	stored := func() ([]byte, time.Time, error) {
		if path == "" {
			return nil, time.Time{}, os.ErrNotExist
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, time.Time{}, err
		}
		data, err := os.ReadFile(path)
		return data, info.ModTime(), err
	}
	if data, taken, err := stored(); err == nil && !fresh && time.Since(taken) < pictureFresh {
		return data, taken, nil
	}
	select {
	case s.pictureSlots <- struct{}{}:
	case <-ctx.Done():
		return nil, time.Time{}, ctx.Err()
	}
	tctx, cancel := context.WithTimeout(ctx, pictureTimeout)
	data, err := s.takePicture(tctx, c)
	cancel()
	<-s.pictureSlots
	if err != nil {
		s.Log.Debug("no picture from the camera", "camera", c.Name, "err", err)
		if old, taken, serr := stored(); serr == nil {
			return old, taken, nil
		}
		return nil, time.Time{}, err
	}
	now := time.Now()
	if path != "" {
		if err := writeFileAtomic(path, data); err != nil {
			s.Log.Warn("couldn't keep a camera's picture", "camera", c.Name, "err", err)
		}
	}
	return data, now, nil
}

func writeFileAtomic(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// takePicture reads a frame from the live stream, using the sub stream when there is
// one, or failing that asks the camera.
func (s *Server) takePicture(ctx context.Context, c store.Camera) ([]byte, error) {
	if !c.Enabled {
		return nil, errors.New("the camera is disabled")
	}
	if s.FFmpeg != "" && s.Media != nil {
		stream := "main"
		if c.SubURL != "" {
			stream = "sub"
		}
		data, err := grabFrame(ctx, s.FFmpeg, s.Media.ReadURL(c.ID, stream))
		if err == nil {
			return data, nil
		}
		// The address holds a password, so only the error is logged.
		s.Log.Debug("no frame from the live stream", "camera", c.Name, "err", err)
	}
	return s.cameraPicture(ctx, c.ID)
}

func grabFrame(ctx context.Context, ffmpeg, source string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, ffmpeg, "-nostdin", "-v", "fatal", "-rtsp_transport", "tcp", "-i", source,
		"-frames:v", "1", "-vf", fmt.Sprintf("scale='min(%d,iw)':-2", pictureWidth), "-q:v", "4",
		"-f", "image2pipe", "-c:v", "mjpeg", "-")
	data, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	if len(data) < 3 || data[0] != 0xFF || data[1] != 0xD8 {
		return nil, errors.New("no picture in the stream")
	}
	return data, nil
}

func (s *Server) cameraPictureHandler(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	u := currentUser(r)
	if !u.Can(store.PermCameras) && !canSeePictures(u, c.ID) {
		fail(w, http.StatusForbidden, "You don't have permission to see this camera's picture.")
		return
	}
	data, taken, err := s.picture(r.Context(), c, r.URL.Query().Get("fresh") == "1")
	if err != nil {
		fail(w, http.StatusNotFound, "No picture from this camera yet.")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "private, max-age=60")
	w.Header().Set("X-Picture-Taken", taken.UTC().Format(time.RFC3339))
	w.Write(data)
}

// forgetPicture drops a camera's picture, when it's removed or now points elsewhere.
func (s *Server) forgetPicture(id int64) {
	if path := s.picturePath(id); path != "" {
		l := s.pictureLock(id)
		l.Lock()
		os.Remove(path)
		l.Unlock()
	}
}

// foundDevice is a camera that answered a scan, with the cameras already added from it.
type foundDevice struct {
	onvif.Device
	Cameras []string `json:"cameras,omitempty"`
}

func (s *Server) discover(w http.ResponseWriter, r *http.Request) {
	devices, err := discoverDevices(r.Context(), 3*time.Second)
	if err != nil {
		s.internal(w, err)
		return
	}
	cams, err := s.Store.Cameras(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	added := map[string][]string{}
	for _, c := range cams {
		if h := hostOf(c.MainURL); h != "" {
			added[h] = append(added[h], c.Name)
		}
	}
	out := make([]foundDevice, 0, len(devices))
	for _, d := range devices {
		out = append(out, foundDevice{d, added[strings.ToLower(d.Host)]})
	}
	writeJSON(w, http.StatusOK, out)
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
