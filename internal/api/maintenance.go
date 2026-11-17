package api

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"seelynx/internal/store"
)

const (
	backupsKept  = 14
	maxRestore   = 1 << 30
	maxLogBytes  = 20 << 20
	restoreName  = "seelynx.db.restore"
	databaseName = "seelynx.db"
)

var backupName = regexp.MustCompile(`^seelynx-\d{8}-\d{6}\.db$`)

type backupInfo struct {
	Name string    `json:"name"`
	Size int64     `json:"size"`
	At   time.Time `json:"at"`
}

func (s *Server) backupDir() string { return filepath.Join(s.DataDir, "backups") }

func (s *Server) backups() ([]backupInfo, error) {
	entries, err := os.ReadDir(s.backupDir())
	if os.IsNotExist(err) {
		return []backupInfo{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []backupInfo{}
	for _, e := range entries {
		if !backupName.MatchString(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, backupInfo{e.Name(), info.Size(), info.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name > out[j].Name })
	return out, nil
}

// Backup writes a snapshot and removes the oldest beyond the kept count.
func (s *Server) Backup(ctx context.Context) (string, error) {
	if err := os.MkdirAll(s.backupDir(), 0o750); err != nil {
		return "", err
	}
	name := "seelynx-" + time.Now().Format("20060102-150405") + ".db"
	path := filepath.Join(s.backupDir(), name)
	if _, err := os.Stat(path); err == nil {
		return name, nil // a second request in the same second
	}
	if err := s.Store.Snapshot(ctx, path); err != nil {
		return "", err
	}
	list, err := s.backups()
	if err != nil {
		return name, err
	}
	for i := backupsKept; i < len(list); i++ {
		if err := os.Remove(filepath.Join(s.backupDir(), list[i].Name)); err != nil {
			s.Log.Warn("remove old backup", "err", err)
		}
	}
	return name, nil
}

// RunBackups takes one backup a day until ctx ends.
func (s *Server) RunBackups(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
		if _, err := s.Backup(ctx); err != nil && ctx.Err() == nil {
			s.Log.Error("scheduled backup failed", "err", err)
			if err := s.Store.AddEvent(ctx, "warning", "Scheduled backup failed. Check the server log."); err != nil {
				s.Log.Error("event", "err", err)
			}
		}
	}
}

func (s *Server) listBackups(w http.ResponseWriter, r *http.Request) {
	list, err := s.backups()
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createBackup(w http.ResponseWriter, r *http.Request) {
	name, err := s.Backup(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (s *Server) downloadBackup(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !backupName.MatchString(name) {
		fail(w, http.StatusNotFound, "Backup not found.")
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	http.ServeFile(w, r, filepath.Join(s.backupDir(), name))
}

// checkDatabase confirms a file is a Seelynx database with at least one user,
// so a restore can't leave the system with nobody able to sign in.
func checkDatabase(ctx context.Context, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	head := make([]byte, 16)
	_, err = io.ReadFull(f, head)
	f.Close()
	if err != nil || !bytes.Equal(head, []byte("SQLite format 3\x00")) {
		return fmt.Errorf("not a database")
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	n, err := st.UserCount(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("no users")
	}
	return nil
}

// stageRestore accepts a backup, either by name or as the request body. It takes
// effect at the next start, because the live database can't be swapped safely.
func (s *Server) stageRestore(w http.ResponseWriter, r *http.Request) {
	tmp, err := os.CreateTemp(s.DataDir, "restore-*.tmp")
	if err != nil {
		s.internal(w, err)
		return
	}
	defer os.Remove(tmp.Name())

	var src io.Reader = http.MaxBytesReader(w, r.Body, maxRestore)
	if name := r.URL.Query().Get("name"); name != "" {
		if !backupName.MatchString(name) {
			tmp.Close()
			fail(w, http.StatusNotFound, "Backup not found.")
			return
		}
		f, err := os.Open(filepath.Join(s.backupDir(), name))
		if err != nil {
			tmp.Close()
			fail(w, http.StatusNotFound, "Backup not found.")
			return
		}
		defer f.Close()
		src = f
	}
	_, err = io.Copy(tmp, src)
	tmp.Close()
	if err != nil {
		fail(w, http.StatusBadRequest, "The backup file couldn't be read.")
		return
	}
	if err := checkDatabase(r.Context(), tmp.Name()); err != nil {
		s.Log.Warn("restore rejected", "err", err)
		fail(w, http.StatusBadRequest, "That file isn't a valid Seelynx backup.")
		return
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		os.Remove(tmp.Name() + suffix)
	}
	if err := os.Rename(tmp.Name(), filepath.Join(s.DataDir, restoreName)); err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"restart_required": true})
}

// ApplyPendingRestore swaps in a staged backup. Call it before opening the
// database. The replaced database is kept beside it as seelynx.db.pre-restore.
func ApplyPendingRestore(dataDir string) (bool, error) {
	staged := filepath.Join(dataDir, restoreName)
	if _, err := os.Stat(staged); os.IsNotExist(err) {
		return false, nil
	}
	db := filepath.Join(dataDir, databaseName)
	if _, err := os.Stat(db); err == nil {
		// A snapshot-free rename needs the WAL gone, or old pages would replay.
		for _, suffix := range []string{"-wal", "-shm"} {
			if err := os.Rename(db+suffix, db+".pre-restore"+suffix); err != nil && !os.IsNotExist(err) {
				return false, err
			}
		}
		if err := os.Rename(db, db+".pre-restore"); err != nil {
			return false, err
		}
	}
	return true, os.Rename(staged, db)
}

// --- log collection ---

// Camera URLs can carry credentials, so they're scrubbed from collected logs.
var urlCredentials = regexp.MustCompile(`(?i)(rtsps?://)[^\s/@]+@`)

func redact(b []byte) []byte { return urlCredentials.ReplaceAll(b, []byte("${1}***@")) }

func readTail(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > max {
		if _, err := f.Seek(-max, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(f)
}

func (s *Server) downloadLogs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="seelynx-logs-`+time.Now().Format("20060102-150405")+`.zip"`)
	z := zip.NewWriter(w)
	defer z.Close()
	for _, name := range []string{"seelynx.log", "mediamtx.log"} {
		data, err := readTail(filepath.Join(s.DataDir, name), maxLogBytes)
		if err != nil {
			continue // a missing log shouldn't fail the others
		}
		f, err := z.Create(name)
		if err != nil {
			return
		}
		if _, err := f.Write(redact(data)); err != nil {
			return
		}
	}
}

// MigrateLegacyNames moves files created before the project was renamed. It leaves
// anything alone if the new name already exists, so it is safe to run on every start.
func MigrateLegacyNames(dataDir string) error {
	for _, old := range []string{"night" + "jar", "still" + "watch", "fra" + "mix"} {
		if err := migrateFrom(dataDir, old); err != nil {
			return err
		}
	}
	return nil
}

func migrateFrom(dataDir, oldName string) error {
	for _, suffix := range []string{".db", ".db-wal", ".db-shm", ".db.restore", ".log"} {
		from := filepath.Join(dataDir, oldName+suffix)
		to := filepath.Join(dataDir, "seelynx"+suffix)
		if _, err := os.Stat(from); err != nil {
			continue
		}
		if _, err := os.Stat(to); err == nil {
			continue
		}
		if err := os.Rename(from, to); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "backups"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), oldName+"-")
		if !ok {
			continue
		}
		dir := filepath.Join(dataDir, "backups")
		if err := os.Rename(filepath.Join(dir, e.Name()), filepath.Join(dir, "seelynx-"+rest)); err != nil {
			return err
		}
	}
	return nil
}
