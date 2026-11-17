package rules

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// SnapshotFolder is where alarm pictures are kept, inside the data directory.
	SnapshotFolder = "snapshots"
	// SnapshotKeep and SnapshotMaxBytes bound the folder. A busy site could otherwise
	// fill a disk, because a picture can be saved for every alarm on every camera.
	SnapshotKeep     = 30 * 24 * time.Hour
	SnapshotMaxBytes = 2 << 30

	snapshotWait = 16 * time.Second // the search takes 7 seconds, and a first camera picture took 8
	maxCaptures  = 8                // pictures being fetched at once
)

// SendsImages reports whether a connection type can carry a picture. The others would
// need the picture at a public web address, so they get the text alone.
func SendsImages(kind string) bool {
	switch kind {
	case "webhook", "discord", "telegram", "pushover", "ntfy", "email", "gmail", "microsoft365":
		return true
	}
	return false
}

// eventShot is one event's picture, shared by every rule the event fires.
type eventShot struct {
	capture, save sync.Once
	data          []byte
	name          string
}

// wantsSnapshot leaves out offline events, because the camera can't answer.
func (e *Engine) wantsSnapshot(r Rule, ev Event) bool {
	return r.Snapshot && e.Snapshot != nil && ev.CameraID != 0 && ev.Type != TriggerOffline
}

func (e *Engine) fetch(ctx context.Context, shot *eventShot, ev Event) {
	shot.capture.Do(func() {
		select {
		case e.slots <- struct{}{}:
			defer func() { <-e.slots }()
		default:
			e.Log.Warn("too many snapshots at once, sending without a picture", "camera", ev.CameraName)
			return
		}
		cctx, cancel := context.WithTimeout(ctx, snapshotWait)
		defer cancel()
		data, err := e.Snapshot(cctx, ev.CameraID, ev.At)
		if err != nil {
			e.Log.Warn("snapshot failed", "camera", ev.CameraName, "err", err)
			if err := e.Store.AddEvent(ctx, "warning", fmt.Sprintf("Couldn't get a snapshot from %s: %v", ev.CameraName, err)); err != nil {
				e.Log.Error("event", "err", err)
			}
			return
		}
		shot.data = data
	})
}

func (e *Engine) keep(shot *eventShot) string {
	shot.save.Do(func() {
		if len(shot.data) == 0 || e.SnapshotDir == "" {
			return
		}
		suffix := make([]byte, 4)
		rand.Read(suffix)
		name := time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(suffix) + ".jpg"
		if err := os.MkdirAll(e.SnapshotDir, 0o700); err != nil {
			e.Log.Error("snapshot folder", "err", err)
			return
		}
		if err := os.WriteFile(filepath.Join(e.SnapshotDir, name), shot.data, 0o600); err != nil {
			e.Log.Error("snapshot save", "err", err)
			return
		}
		shot.name = name
	})
	return shot.name
}

// withSnapshot finishes a rule that wants a picture. It runs beside the event queue,
// because a camera can take seconds to answer and other events shouldn't wait for it.
func (e *Engine) withSnapshot(ctx context.Context, shot *eventShot, ev Event, rule string, alarmID int64, connections []int64, m Message) {
	e.fetch(ctx, shot, ev)
	if alarmID != 0 {
		if name := e.keep(shot); name != "" {
			if err := e.Store.SetAlarmSnapshot(ctx, alarmID, name); err != nil {
				e.Log.Error("alarm snapshot", "err", err)
			}
		}
	}
	m.Snapshot = shot.data
	e.deliver(ctx, rule, connections, m)
}

// Wait blocks until pictures being fetched have been handled.
func (e *Engine) Wait() { e.pending.Wait() }

// PurgeSnapshots removes pictures older than before, then the oldest ones until the
// folder is within maxBytes. An alarm whose picture has gone just shows none.
func PurgeSnapshots(dir string, before time.Time, maxBytes int64) error {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	type picture struct {
		path string
		size int64
		at   time.Time
	}
	var kept []picture
	var total int64
	for _, d := range entries {
		if d.IsDir() || !strings.HasSuffix(d.Name(), ".jpg") {
			continue
		}
		info, err := d.Info()
		if err != nil {
			continue
		}
		p := picture{filepath.Join(dir, d.Name()), info.Size(), info.ModTime()}
		if p.at.Before(before) {
			os.Remove(p.path)
			continue
		}
		kept = append(kept, p)
		total += p.size
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].at.Before(kept[j].at) })
	for _, p := range kept {
		if total <= maxBytes {
			break
		}
		if os.Remove(p.path) == nil {
			total -= p.size
		}
	}
	return nil
}

// SamplePicture is the image the Test button sends, so a test shows whether pictures
// arrive as well as text.
func SamplePicture() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 640, 360))
	for y := range 360 {
		for x := range 640 {
			img.Set(x, y, color.RGBA{0x10 + uint8(x/40), 0x18 + uint8(y/4), 0x23 + uint8(x/4), 0xFF})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, nil); err != nil {
		return nil
	}
	return b.Bytes()
}

// postPicture sends form fields and the picture as a multipart upload.
func postPicture(ctx context.Context, target string, fields [][2]string, field string, picture []byte) error {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return err
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="snapshot.jpg"`, field))
	header.Set("Content-Type", "image/jpeg")
	part, err := w.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(picture); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return post(ctx, target, w.FormDataContentType(), b.Bytes(), nil)
}
