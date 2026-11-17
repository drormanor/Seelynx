// Command seelynx runs the VMS server.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"seelynx/internal/api"
	"seelynx/internal/media"
	"seelynx/internal/retention"
	"seelynx/internal/rules"
	"seelynx/internal/storage"
	"seelynx/internal/store"
	"seelynx/web"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8480", "web server address; use :8480 to allow other machines (with -tls-cert)")
	dataDir := flag.String("data", "data", "directory for the database and logs")
	recDir := flag.String("recordings", "", "directory for recordings (default <data>/recordings)")
	mediamtx := flag.String("mediamtx", "mediamtx", "path to the mediamtx executable")
	ffmpeg := flag.String("ffmpeg", "", "path to ffmpeg, used for snapshots (default: next to seelynx, then PATH)")
	webrtcUDP := flag.String("webrtc-udp", ":8189", "UDP address for WebRTC media")
	minFreeGB := flag.Uint64("min-free-gb", 50, "delete oldest recordings to keep this much disk free")
	portOffset := flag.Int("port-offset", 0, "added to the engine's local ports, to run a second copy for testing")
	tlsCert := flag.String("tls-cert", "", "TLS certificate file")
	tlsKey := flag.String("tls-key", "", "TLS private key file")
	flag.Parse()

	work := func(ctx context.Context, console io.Writer) error {
		return run(ctx, console, *listen, *dataDir, *recDir, *mediamtx, *ffmpeg, *webrtcUDP, *minFreeGB, *portOffset, *tlsCert, *tlsKey)
	}
	if isService() {
		// A service has no console, and a failed write to one would stop the log file too.
		if err := runService(func(ctx context.Context) error { return work(ctx, io.Discard) }); err != nil {
			os.Exit(1)
		}
		return
	}
	// Only a console run listens for signals. Windows also signals a service at shutdown,
	// and that would race its proper shutdown request and look like a crash.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := work(ctx, os.Stderr); err != nil {
		slog.New(slog.NewTextHandler(os.Stderr, nil)).Error("fatal", "err", err)
		os.Exit(1)
	}
}

// findFFmpeg returns ffmpeg's path, or "" if there isn't one. Snapshots still work
// without it, by asking the camera.
func findFFmpeg(path string) string {
	if path == "" {
		if exe, err := os.Executable(); err == nil {
			for _, name := range []string{"ffmpeg.exe", "ffmpeg"} {
				next := filepath.Join(filepath.Dir(exe), name)
				if info, err := os.Stat(next); err == nil && !info.IsDir() {
					return next
				}
			}
		}
		path = "ffmpeg"
	}
	found, err := exec.LookPath(path)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(found); err == nil {
		return abs
	}
	return found
}

func run(parent context.Context, console io.Writer, listen, dataDir, recDir, mediamtx, ffmpeg, webrtcUDP string, minFreeGB uint64, portOffset int, tlsCert, tlsKey string) error {
	dataDir, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	if recDir == "" {
		recDir = filepath.Join(dataDir, "recordings")
	}
	if recDir, err = filepath.Abs(recDir); err != nil {
		return err
	}
	// The engine runs from the data directory, so a relative path would break.
	if mediamtx, err = exec.LookPath(mediamtx); err != nil {
		return fmt.Errorf("mediamtx not found: %w", err)
	}
	if mediamtx, err = filepath.Abs(mediamtx); err != nil {
		return err
	}
	if err := os.MkdirAll(recDir, 0o750); err != nil {
		return err
	}
	if err := api.MigrateLegacyNames(dataDir); err != nil {
		return fmt.Errorf("migrate old file names: %w", err)
	}
	logFile, err := os.OpenFile(filepath.Join(dataDir, "seelynx.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer logFile.Close()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(console, logFile), nil))

	if restored, err := api.ApplyPendingRestore(dataDir); err != nil {
		return fmt.Errorf("restore: %w", err)
	} else if restored {
		log.Info("restored configuration from backup")
	}

	st, err := store.Open(filepath.Join(dataDir, "seelynx.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	key, err := store.LoadOrCreateKey(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return fmt.Errorf("camera password key: %w", err)
	}
	unreadable, err := st.UseKey(context.Background(), key)
	if err != nil {
		return fmt.Errorf("encrypt camera passwords: %w", err)
	}
	if unreadable > 0 {
		msg := fmt.Sprintf("%d camera passwords can't be read with this secret.key. Restore the original key file or re-enter them.", unreadable)
		log.Error(msg)
		if err := st.AddEvent(context.Background(), "warning", msg); err != nil {
			return err
		}
	}

	// The folder and free space given at startup only set up the first location. After
	// that, locations are managed on the Storage screen.
	if err := storage.Bootstrap(context.Background(), st, recDir, minFreeGB); err != nil {
		return fmt.Errorf("storage: %w", err)
	}

	mm, err := media.New(media.Config{Binary: mediamtx, DataDir: dataDir, RecordingDir: recDir, WebRTCUDP: webrtcUDP, PortOffset: portOffset, Log: log})
	if err != nil {
		return err
	}

	ctx, stop := context.WithCancel(parent)
	defer stop()

	engine := &rules.Engine{Store: st, Log: log}
	stor := &storage.Manager{Store: st, Log: log}
	stor.Notify = func(level, msg string) {
		if err := st.AddEvent(context.Background(), level, msg); err != nil {
			log.Error("event", "err", err)
		}
		if level == "warning" {
			engine.Emit(rules.Event{Type: rules.TriggerDisk, Message: msg})
		}
	}
	ret := &retention.Worker{Store: st, Locations: stor.Readable, Log: log}
	ret.OnLowDisk = func(msg string) { engine.Emit(rules.Event{Type: rules.TriggerDisk, Message: msg}) }
	mon := &api.Monitor{Store: st, Media: mm, Log: log, Events: engine.Emit}
	motion := &api.MotionWatcher{Store: st, Log: log, Events: engine.Emit}
	srv := &api.Server{Store: st, Media: mm, Retention: ret, Storage: stor, Monitor: mon, Motion: motion, Rules: engine, Web: web.Files(), RecordingDir: recDir, FFmpeg: findFFmpeg(ffmpeg), DataDir: dataDir, Secure: tlsCert != "", Log: log}
	mon.Recording = srv.Recording
	motion.Names = srv.AnalyticsName
	ret.MotionWatched = motion.Watched
	ret.MotionTimes = srv.MotionTimes
	if srv.FFmpeg == "" {
		log.Warn("ffmpeg not found, so snapshots come from cameras and can be late")
	}

	// Placing cameras needs to know which locations work, before the engine starts. The
	// engine sets every camera up when it starts, so this first check doesn't.
	if err := stor.Refresh(ctx); err != nil {
		return fmt.Errorf("storage: %w", err)
	}
	stor.Apply = srv.ApplyCamera
	go stor.Run(ctx, 30*time.Second)

	mediaDone := make(chan error, 1)
	go func() {
		mediaDone <- mm.Run(ctx, func(ctx context.Context) {
			cams, err := st.Cameras(ctx)
			if err != nil {
				log.Error("load cameras", "err", err)
				return
			}
			for _, c := range cams {
				if err := srv.ApplyCamera(ctx, c); err != nil {
					log.Error("apply camera", "camera", c.ID, "err", err)
				}
			}
			log.Info("media engine ready", "cameras", len(cams))
		})
	}()
	go ret.Run(ctx, 5*time.Minute)
	go srv.RunSchedules(ctx, 30*time.Second)
	go srv.RunBackups(ctx)
	go motion.Run(ctx, 30*time.Second)
	engine.Snapshot = srv.CaptureSnapshot
	engine.SnapshotDir = filepath.Join(dataDir, rules.SnapshotFolder)
	go engine.Run(ctx)
	go mon.Run(ctx, 5*time.Second)
	go func() {
		for ctx.Err() == nil {
			if err := st.PurgeExpired(ctx, time.Now().AddDate(0, 0, -90)); err != nil && ctx.Err() == nil {
				log.Error("purge", "err", err)
			}
			if err := st.PurgeAlarms(ctx, time.Now().AddDate(0, 0, -90)); err != nil && ctx.Err() == nil {
				log.Error("purge alarms", "err", err)
			}
			if err := rules.PurgeSnapshots(engine.SnapshotDir, time.Now().Add(-rules.SnapshotKeep), rules.SnapshotMaxBytes); err != nil {
				log.Error("purge snapshots", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Hour):
			}
		}
	}()

	hs := &http.Server{Addr: listen, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	webDone := make(chan error, 1)
	go func() {
		log.Info("web server listening", "address", listen)
		if tlsCert != "" {
			webDone <- hs.ListenAndServeTLS(tlsCert, tlsKey)
		} else {
			webDone <- hs.ListenAndServe()
		}
	}()

	select {
	case err := <-webDone:
		stop()
		<-mediaDone
		return err
	case err := <-mediaDone:
		hs.Close()
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := hs.Shutdown(sctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Warn("shutdown", "err", err)
	}
	<-mediaDone
	return nil
}
