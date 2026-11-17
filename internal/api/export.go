package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"seelynx/internal/media"
	"seelynx/internal/store"
)

const (
	minClip  = 200 * time.Millisecond // shorter pieces aren't worth a join
	maxClips = 500
)

var unsafeFileName = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

// exportName is the downloaded file's name. Only plain characters are kept, so a camera's
// name can't break the header or the file path.
func exportName(camera string, start time.Time) string {
	name := strings.Trim(unsafeFileName.ReplaceAllString(camera, "_"), "_")
	if name == "" {
		name = "camera"
	}
	return name + "_" + start.Local().Format("2006-01-02_15-04-05") + ".mp4"
}

type clip struct {
	start   time.Time
	seconds float64
}

// clipsIn trims recorded spans to a range. The video engine stops at the first gap, so
// each span has to be asked for separately or the rest would silently be missing.
func clipsIn(spans []media.Segment, start, end time.Time) []clip {
	var out []clip
	for _, s := range spans {
		from := s.Start
		to := s.Start.Add(time.Duration(s.Duration * float64(time.Second)))
		if from.Before(start) {
			from = start
		}
		if to.After(end) {
			to = end
		}
		if to.Sub(from) >= minClip {
			out = append(out, clip{from, to.Sub(from).Seconds()})
		}
	}
	return out
}

func clipsLength(clips []clip) time.Duration {
	var total float64
	for _, c := range clips {
		total += c.seconds
	}
	return (time.Duration(total) * time.Second).Round(time.Second)
}

// saveClip writes one stretch of video to a file.
func (s *Server) saveClip(ctx context.Context, c store.Camera, part clip, path string) error {
	res, err := s.Media.Playback(ctx, c.ID, part.start, part.seconds, "mp4")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("video engine answered %d", res.StatusCode)
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, res.Body)
	return err
}

// joinClips builds one file from several stretches, and returns its path. The video isn't
// re-encoded, so this is quick and keeps the camera's quality.
func (s *Server) joinClips(ctx context.Context, c store.Camera, clips []clip, dir string) (string, error) {
	var list strings.Builder
	for i, part := range clips {
		path := filepath.Join(dir, fmt.Sprintf("clip-%d.mp4", i))
		if err := s.saveClip(ctx, c, part, path); err != nil {
			return "", err
		}
		fmt.Fprintf(&list, "file '%s'\n", filepath.ToSlash(path))
	}
	listPath := filepath.Join(dir, "clips.txt")
	if err := os.WriteFile(listPath, []byte(list.String()), 0o600); err != nil {
		return "", err
	}
	out := filepath.Join(dir, "export.mp4")
	cmd := exec.CommandContext(ctx, s.FFmpeg, "-nostdin", "-v", "error", "-f", "concat", "-safe", "0", "-i", listPath,
		"-c", "copy", "-movflags", "+faststart", out)
	if msg, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("joining clips: %w: %.200s", err, msg)
	}
	return out, nil
}

// exportVideo downloads a stretch of recording as a standard MP4, joining the recorded
// parts if there are gaps. It's noted in the event log, because video leaving the system
// is something to be able to account for.
func (s *Server) exportVideo(w http.ResponseWriter, r *http.Request) {
	c, ok := s.viewCamera(w, r)
	if !ok {
		return
	}
	start, seconds, ok := videoRange(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	end := start.Add(time.Duration(seconds * float64(time.Second)))
	spans, err := s.Media.Recordings(ctx, c.ID, start, end)
	if err != nil {
		s.internal(w, err)
		return
	}
	clips := clipsIn(spans, start, end)
	switch {
	case len(clips) == 0:
		fail(w, http.StatusNotFound, "No recording in this range.")
		return
	case len(clips) > maxClips:
		fail(w, http.StatusBadRequest, fmt.Sprintf("This range has %d separate clips. Choose a shorter range.", len(clips)))
		return
	case len(clips) > 1 && s.FFmpeg == "":
		fail(w, http.StatusConflict, "This range has gaps, and joining the clips needs ffmpeg. Export each clip separately.")
		return
	}
	note := func() {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, exportName(c.Name, clips[0].start)))
		msg := fmt.Sprintf("%s exported %s of video from %s, starting %s", currentUser(r).Username, clipsLength(clips), c.Name, clips[0].start.Local().Format("2006-01-02 15:04:05"))
		if err := s.Store.AddEvent(ctx, "info", msg); err != nil {
			s.Log.Error("event", "err", err)
		}
	}
	if len(clips) == 1 {
		s.sendVideo(w, r, c, clips[0].start, clips[0].seconds, "mp4", note)
		return
	}
	dir, err := os.MkdirTemp(s.DataDir, "export-")
	if err != nil {
		s.internal(w, err)
		return
	}
	defer os.RemoveAll(dir)
	path, err := s.joinClips(ctx, c, clips, dir)
	if err != nil {
		if !errors.Is(ctx.Err(), context.Canceled) {
			s.internal(w, err)
		}
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.internal(w, err)
		return
	}
	defer f.Close()
	note()
	w.Header().Set("Content-Type", "video/mp4")
	w.Header().Set("Cache-Control", "no-store")
	io.Copy(w, f)
}
