package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const alarmsSchema = `
CREATE TABLE IF NOT EXISTS connections (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  type TEXT NOT NULL,
  config TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS alarms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  at INTEGER NOT NULL,
  rule TEXT NOT NULL,
  camera_id INTEGER NOT NULL DEFAULT 0,
  camera_name TEXT NOT NULL DEFAULT '',
  message TEXT NOT NULL,
  priority TEXT NOT NULL DEFAULT 'medium',
  acked_by TEXT NOT NULL DEFAULT '',
  acked_at INTEGER NOT NULL DEFAULT 0,
  note TEXT NOT NULL DEFAULT '',
  snapshot TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS alarms_at ON alarms(at);
`

// Connection is a link to an outside service. Config holds its settings, including
// secrets, and is stored encrypted.
type Connection struct {
	ID     int64             `json:"id"`
	Name   string            `json:"name"`
	Type   string            `json:"type"`
	Config map[string]string `json:"config"`
}

func (s *Store) scanConnection(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var stored string
	err := row.Scan(&c.ID, &c.Name, &c.Type, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		return c, ErrNotFound
	}
	if err != nil {
		return c, err
	}
	c.Config = map[string]string{}
	plain, err := decryptSecret(s.key, stored)
	if err != nil || plain == "" {
		return c, nil // unreadable settings show as empty, and the connection fails its test
	}
	return c, json.Unmarshal([]byte(plain), &c.Config)
}

func (s *Store) Connections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, type, config FROM connections ORDER BY name COLLATE NOCASE, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := s.scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Connection(ctx context.Context, id int64) (Connection, error) {
	return s.scanConnection(s.db.QueryRowContext(ctx, `SELECT id, name, type, config FROM connections WHERE id = ?`, id))
}

// SaveConnection inserts when ID is zero and updates otherwise.
func (s *Store) SaveConnection(ctx context.Context, c Connection) (Connection, error) {
	raw, err := json.Marshal(c.Config)
	if err != nil {
		return c, err
	}
	enc, err := encryptSecret(s.key, string(raw))
	if err != nil {
		return c, err
	}
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO connections (name, type, config) VALUES (?,?,?)`, c.Name, c.Type, enc)
		if err != nil {
			return c, err
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return c, err
		}
		return c, nil
	}
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET name=?, type=?, config=? WHERE id=?`, c.Name, c.Type, enc, c.ID)
	if err != nil {
		return c, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return c, ErrNotFound
	}
	return c, nil
}

func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Alarm is something an operator should look at.
type Alarm struct {
	ID         int64      `json:"id"`
	At         time.Time  `json:"at"`
	Rule       string     `json:"rule"`
	CameraID   int64      `json:"camera_id"`
	CameraName string     `json:"camera_name"`
	Message    string     `json:"message"`
	Priority   string     `json:"priority"`
	AckedBy    string     `json:"acked_by"`
	AckedAt    *time.Time `json:"acked_at"`
	Note       string     `json:"note"`

	Snapshot    string `json:"-"` // picture's file name
	HasSnapshot bool   `json:"has_snapshot"`
}

const alarmColumns = `id, at, rule, camera_id, camera_name, message, priority, acked_by, acked_at, note, snapshot`

func scanAlarm(row interface{ Scan(...any) error }) (Alarm, error) {
	var a Alarm
	var at, acked int64
	if err := row.Scan(&a.ID, &at, &a.Rule, &a.CameraID, &a.CameraName, &a.Message, &a.Priority, &a.AckedBy, &acked, &a.Note, &a.Snapshot); err != nil {
		return a, err
	}
	a.At = time.Unix(at, 0).UTC()
	if acked > 0 {
		t := time.Unix(acked, 0).UTC()
		a.AckedAt = &t
	}
	a.HasSnapshot = a.Snapshot != ""
	return a, nil
}

// Alarm reads one alarm.
func (s *Store) Alarm(ctx context.Context, id int64) (Alarm, error) {
	a, err := scanAlarm(s.db.QueryRowContext(ctx, `SELECT `+alarmColumns+` FROM alarms WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// SetAlarmSnapshot records the picture taken for an alarm. It arrives after the alarm,
// because cameras can take seconds to answer.
func (s *Store) SetAlarmSnapshot(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alarms SET snapshot=? WHERE id=?`, name, id)
	return err
}

func (s *Store) AddAlarm(ctx context.Context, a Alarm) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO alarms (at, rule, camera_id, camera_name, message, priority) VALUES (?,?,?,?,?,?)`,
		a.At.Unix(), a.Rule, a.CameraID, a.CameraName, a.Message, a.Priority)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Alarms lists alarms, newest first.
func (s *Store) Alarms(ctx context.Context, limit int) ([]Alarm, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+alarmColumns+` FROM alarms ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alarm{}
	for rows.Next() {
		a, err := scanAlarm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckAlarm marks an alarm handled. An alarm can only be acknowledged once, so the
// first person's name and note are kept.
func (s *Store) AckAlarm(ctx context.Context, id int64, by, note string) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alarms SET acked_by=?, acked_at=?, note=? WHERE id=? AND acked_at=0`, by, time.Now().Unix(), note, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeAlarms removes handled alarms older than before.
func (s *Store) PurgeAlarms(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM alarms WHERE at < ? AND acked_at > 0`, before.Unix())
	return err
}
