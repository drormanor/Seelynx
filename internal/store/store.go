// Package store persists configuration, users, sessions and events in SQLite.
package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

const schema = `
CREATE TABLE IF NOT EXISTS cameras (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  main_url TEXT NOT NULL,
  sub_url TEXT NOT NULL DEFAULT '',
  username TEXT NOT NULL DEFAULT '',
  password TEXT NOT NULL DEFAULT '',
  record INTEGER NOT NULL DEFAULT 1,
  retention_days INTEGER NOT NULL DEFAULT 30,
  enabled INTEGER NOT NULL DEFAULT 1
);
CREATE TABLE IF NOT EXISTS users (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  username TEXT NOT NULL UNIQUE COLLATE NOCASE,
  password_hash TEXT NOT NULL,
  role TEXT NOT NULL CHECK (role IN ('admin','operator','viewer'))
);
CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  level TEXT NOT NULL,
  message TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS events_at ON events(at);
`

// Camera is a configured video source. Password never leaves the server.
type Camera struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	MainURL        string `json:"main_url"`
	SubURL         string `json:"sub_url"`
	Username       string `json:"username"`
	Password       string `json:"-"`
	storedPassword string // as held in the database
	HasPassword    bool   `json:"has_password"`
	Record         bool   `json:"record"`
	RetentionDays  int    `json:"retention_days"`
	GroupID        int64  `json:"group_id"`
	NumericID      int64  `json:"numeric_id"`
	ScheduleID     int64  `json:"schedule_id"`
	Enabled        bool   `json:"enabled"`
	MotionOnly     bool   `json:"motion_only"`  // keep only video near motion
	PreSeconds     int    `json:"pre_seconds"`  // video to keep before motion
	PostSeconds    int    `json:"post_seconds"` // video to keep after motion
	MotionSince    int64  `json:"-"`            // unix time motion-only was turned on; nothing older is touched
	StorageID      int64  `json:"-"`            // the location it normally records to; 0 until placed
	// OutsideMode is how a camera with a schedule records outside it. Inside it,
	// MotionOnly decides.
	OutsideMode int `json:"outside_mode"`
	// Fisheye describes a fisheye lens, so viewers can show a normal view of it.
	// Nil for a normal lens.
	Fisheye *Fisheye `json:"fisheye"`
	// Rotation is how many degrees clockwise viewers turn the picture: 0, 90, 180 or
	// 270. The video itself is kept as the camera sends it.
	Rotation int `json:"rotation"`
}

// Fisheye is where a fisheye lens's circle sits in the picture and how it's mounted.
// The video itself is kept as the camera sends it; viewers dewarp it.
type Fisheye struct {
	Mount string  `json:"mount"` // FisheyeWall, FisheyeCeiling or FisheyeDesk
	X     float64 `json:"x"`     // centre of the circle, as a fraction of the width
	Y     float64 `json:"y"`     // and of the height
	R     float64 `json:"r"`     // radius, as a fraction of the height
	FOV   float64 `json:"fov"`   // degrees the circle covers, edge to edge
	Tilt  float64 `json:"tilt"`  // degrees a wall camera looks down
}

// How a fisheye camera is mounted.
const (
	FisheyeWall    = "wall"
	FisheyeCeiling = "ceiling"
	FisheyeDesk    = "desk"
)

// How a camera with a schedule records outside it.
const (
	OutsideOff    = 0
	OutsideAll    = 1
	OutsideMotion = 2
)

// UsesMotion reports whether the camera keeps only video near motion at any time.
func (c Camera) UsesMotion() bool {
	return c.Record && (c.MotionOnly || (c.ScheduleID != 0 && c.OutsideMode == OutsideMotion))
}

type Event struct {
	ID      int64     `json:"id"`
	At      time.Time `json:"at"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
}

type Store struct {
	db  *sql.DB
	key []byte // encrypts camera passwords; see UseKey
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has one writer; this avoids SQLITE_BUSY under load.
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

const cameraCols = `id, name, main_url, sub_url, username, password, record, retention_days, enabled, group_id, numeric_id, schedule_id, motion_only, pre_seconds, post_seconds, motion_since, storage_id, outside_mode, fisheye, rotation`

func (s *Store) scanCamera(row interface{ Scan(...any) error }) (Camera, error) {
	var c Camera
	var fisheye string
	err := row.Scan(&c.ID, &c.Name, &c.MainURL, &c.SubURL, &c.Username, &c.storedPassword, &c.Record, &c.RetentionDays, &c.Enabled, &c.GroupID, &c.NumericID, &c.ScheduleID, &c.MotionOnly, &c.PreSeconds, &c.PostSeconds, &c.MotionSince, &c.StorageID, &c.OutsideMode, &fisheye, &c.Rotation)
	c.HasPassword = c.storedPassword != ""
	if fisheye != "" {
		var f Fisheye
		if json.Unmarshal([]byte(fisheye), &f) == nil {
			c.Fisheye = &f
		}
	}
	if err == nil {
		// A password that can't be read shouldn't hide the camera, so it reads as empty.
		c.Password, _ = decryptSecret(s.key, c.storedPassword)
	}
	return c, err
}

// passwordToStore returns what to save. An unchanged password keeps its stored form,
// so a camera whose password can't be read isn't overwritten with an empty value.
func (s *Store) passwordToStore(c Camera) (string, error) {
	if c.storedPassword != "" {
		if current, err := decryptSecret(s.key, c.storedPassword); err != nil && c.Password == "" {
			return c.storedPassword, nil
		} else if err == nil && current == c.Password && len(s.key) == 0 {
			return c.storedPassword, nil
		}
	}
	return encryptSecret(s.key, c.Password)
}

func (s *Store) Cameras(ctx context.Context) ([]Camera, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cameraCols+` FROM cameras ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Camera{}
	for rows.Next() {
		c, err := s.scanCamera(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Camera(ctx context.Context, id int64) (Camera, error) {
	c, err := s.scanCamera(s.db.QueryRowContext(ctx, `SELECT `+cameraCols+` FROM cameras WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	return c, err
}

func (s *Store) CreateCamera(ctx context.Context, c Camera) (Camera, error) {
	password, err := s.passwordToStore(c)
	if err != nil {
		return c, err
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO cameras (name, main_url, sub_url, username, password, record, retention_days, enabled, group_id, numeric_id, schedule_id, motion_only, pre_seconds, post_seconds, motion_since, outside_mode, fisheye, rotation) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		c.Name, c.MainURL, c.SubURL, c.Username, password, c.Record, c.RetentionDays, c.Enabled, c.GroupID, c.NumericID, c.ScheduleID, c.MotionOnly, c.PreSeconds, c.PostSeconds, c.MotionSince, c.OutsideMode, fisheyeText(c.Fisheye), c.Rotation)
	if err != nil {
		return c, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return c, err
	}
	return s.Camera(ctx, id)
}

func (s *Store) UpdateCamera(ctx context.Context, c Camera) (Camera, error) {
	password, err := s.passwordToStore(c)
	if err != nil {
		return c, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE cameras SET name=?, main_url=?, sub_url=?, username=?, password=?, record=?, retention_days=?, enabled=?, group_id=?, numeric_id=?, schedule_id=?, motion_only=?, pre_seconds=?, post_seconds=?, motion_since=?, outside_mode=?, fisheye=?, rotation=? WHERE id=?`,
		c.Name, c.MainURL, c.SubURL, c.Username, password, c.Record, c.RetentionDays, c.Enabled, c.GroupID, c.NumericID, c.ScheduleID, c.MotionOnly, c.PreSeconds, c.PostSeconds, c.MotionSince, c.OutsideMode, fisheyeText(c.Fisheye), c.Rotation, c.ID)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return c, ErrNotFound
	}
	return s.Camera(ctx, c.ID)
}

func fisheyeText(f *Fisheye) string {
	if f == nil {
		return ""
	}
	b, _ := json.Marshal(f)
	return string(b)
}

func (s *Store) DeleteCamera(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cameras WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UserCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n)
	return n, err
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Store) CreateSession(ctx context.Context, token string, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, expires_at) VALUES (?,?,?)`, hashToken(token), userID, expires.Unix())
	return err
}

func (s *Store) SessionUser(ctx context.Context, token string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`JOIN sessions s ON s.user_id = u.id WHERE s.token_hash = ? AND s.expires_at > ?`, hashToken(token), time.Now().Unix()))
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, hashToken(token))
	return err
}

func (s *Store) PurgeExpired(ctx context.Context, eventsBefore time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM events WHERE at < ?`, eventsBefore.Unix())
	return err
}

func (s *Store) AddEvent(ctx context.Context, level, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO events (at, level, message) VALUES (?,?,?)`, time.Now().Unix(), level, message)
	return err
}

func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, at, level, message FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		var e Event
		var at int64
		if err := rows.Scan(&e.ID, &at, &e.Level, &e.Message); err != nil {
			return nil, err
		}
		e.At = time.Unix(at, 0).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
