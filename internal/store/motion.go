package store

import (
	"context"
	"database/sql"
	"time"
)

const motionSchema = `
CREATE TABLE IF NOT EXISTS motion_events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  camera_id INTEGER NOT NULL,
  start_ms INTEGER NOT NULL,
  end_ms INTEGER
);
CREATE INDEX IF NOT EXISTS motion_camera_time ON motion_events(camera_id, start_ms);
CREATE INDEX IF NOT EXISTS motion_time ON motion_events(start_ms);
`

// Motion is one period of motion. End is nil while it is still happening.
type Motion struct {
	ID         int64      `json:"id"`
	CameraID   int64      `json:"camera_id"`
	CameraName string     `json:"camera_name,omitempty"`
	Start      time.Time  `json:"start"`
	End        *time.Time `json:"end"`
}

func (s *Store) StartMotion(ctx context.Context, cameraID int64, at time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO motion_events (camera_id, start_ms) VALUES (?, ?)`, cameraID, at.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) EndMotion(ctx context.Context, id int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE motion_events SET end_ms = ? WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// CloseOpenMotion ends periods left open by a crash, so none stays open forever.
// They're closed at their own start, because the real end is unknown.
func (s *Store) CloseOpenMotion(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE motion_events SET end_ms = start_ms WHERE end_ms IS NULL`)
	return err
}

func scanMotion(rows *sql.Rows, withName bool) (Motion, error) {
	var m Motion
	var start int64
	var end sql.NullInt64
	var err error
	if withName {
		var name sql.NullString
		err = rows.Scan(&m.ID, &m.CameraID, &start, &end, &name)
		m.CameraName = name.String
	} else {
		err = rows.Scan(&m.ID, &m.CameraID, &start, &end)
	}
	m.Start = time.UnixMilli(start).UTC()
	if end.Valid {
		t := time.UnixMilli(end.Int64).UTC()
		m.End = &t
	}
	return m, err
}

// MotionBetween lists a camera's motion overlapping a time range.
func (s *Store) MotionBetween(ctx context.Context, cameraID int64, start, end time.Time) ([]Motion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, camera_id, start_ms, end_ms FROM motion_events
		 WHERE camera_id = ? AND start_ms < ? AND (end_ms IS NULL OR end_ms >= ?) ORDER BY start_ms LIMIT 5000`,
		cameraID, end.UnixMilli(), start.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Motion{}
	for rows.Next() {
		m, err := scanMotion(rows, false)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// HasMotion reports whether a camera has any motion on record.
func (s *Store) HasMotion(ctx context.Context, cameraID int64) (bool, error) {
	var found bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM motion_events WHERE camera_id = ?)`, cameraID).Scan(&found)
	return found, err
}

// RecentMotion lists the latest motion across all cameras.
func (s *Store) RecentMotion(ctx context.Context, limit int) ([]Motion, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.camera_id, m.start_ms, m.end_ms, c.name FROM motion_events m
		 LEFT JOIN cameras c ON c.id = m.camera_id ORDER BY m.start_ms DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Motion{}
	for rows.Next() {
		m, err := scanMotion(rows, true)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// PurgeMotion removes finished motion older than before.
func (s *Store) PurgeMotion(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM motion_events WHERE start_ms < ? AND end_ms IS NOT NULL`, before.UnixMilli())
	return err
}
