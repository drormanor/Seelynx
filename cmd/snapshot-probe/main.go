// Command snapshot-probe fetches one snapshot from a camera, using the address and login
// in Seelynx's database, so no password is typed or shown. Development tool.
//
//	snapshot-probe -data data -camera 101 -out shot.jpg
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"seelynx/internal/api"
	"seelynx/internal/store"
)

func main() {
	data := flag.String("data", "data", "Seelynx data directory")
	id := flag.Int64("camera", 0, "camera ID")
	out := flag.String("out", "snapshot.jpg", "where to save the picture")
	ffmpeg := flag.String("ffmpeg", "", "path to ffmpeg; empty asks the camera instead of the recording")
	at := flag.String("at", "", "today's time to look at, such as 10:07:01; empty means 3 seconds ago")
	flag.Parse()
	if err := run(*data, *id, *out, *ffmpeg, *at); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(dataDir string, id int64, out, ffmpeg, at string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st, err := store.Open(filepath.Join(dataDir, "seelynx.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	key, err := store.LoadOrCreateKey(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}
	if _, err := st.UseKey(ctx, key); err != nil {
		return err
	}
	cam, err := st.Camera(ctx, id)
	if err != nil {
		return err
	}
	srv := &api.Server{Store: st, Log: slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})), FFmpeg: ffmpeg, RecordingDir: filepath.Join(dataDir, "recordings")}
	moment := time.Now().Add(-3 * time.Second)
	if at != "" {
		clock, err := time.ParseInLocation("15:04:05", at, time.Local)
		if err != nil {
			return err
		}
		now := time.Now()
		moment = time.Date(now.Year(), now.Month(), now.Day(), clock.Hour(), clock.Minute(), clock.Second(), 0, time.Local)
	}
	start := time.Now()
	data, err := srv.CaptureSnapshot(ctx, id, moment)
	if err != nil {
		return err
	}
	fmt.Printf("%s: %d KB in %s\n", cam.Name, len(data)/1024, time.Since(start).Round(time.Millisecond))
	return os.WriteFile(out, data, 0o600)
}
