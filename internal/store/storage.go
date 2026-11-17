package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

const storageSchema = `
CREATE TABLE IF NOT EXISTS storage (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  path TEXT NOT NULL,
  min_free_gb INTEGER NOT NULL DEFAULT 50,
  recording INTEGER NOT NULL DEFAULT 1
);
`

// Location is a folder that holds recordings. Cameras are spread across the locations
// that take new video, and video in every location can be played back.
type Location struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path"`
	MinFreeGB uint64 `json:"min_free_gb"` // free space to keep, by deleting the oldest video
	Recording bool   `json:"recording"`   // false keeps the video for playback, but records nothing new
	// MovingTo is the location its video is being moved to before it's removed, or 0.
	MovingTo int64 `json:"moving_to"`
	// Username and Password sign in to a network share. The password is stored encrypted
	// and never sent to a browser.
	Username string `json:"username"`
	Password string `json:"-"`
	// Standby takes video only while no other location can. What it records is moved to
	// the cameras' own locations once they're back, and it stays, ready for next time.
	Standby bool `json:"standby"`
}

const locationCols = `id, name, path, min_free_gb, recording, moving_to, share_user, share_password, standby`

func (s *Store) scanLocation(row interface{ Scan(...any) error }) (Location, error) {
	var l Location
	var stored string
	if err := row.Scan(&l.ID, &l.Name, &l.Path, &l.MinFreeGB, &l.Recording, &l.MovingTo, &l.Username, &stored, &l.Standby); err != nil {
		return l, err
	}
	// A password that can't be read, as with a lost secret.key, signs in as nothing,
	// and the share then says why.
	l.Password, _ = decryptSecret(s.key, stored)
	return l, nil
}

var ErrDuplicate = errors.New("duplicate")

func (s *Store) Locations(ctx context.Context) ([]Location, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+locationCols+` FROM storage ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Location{}
	for rows.Next() {
		l, err := s.scanLocation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Location(ctx context.Context, id int64) (Location, error) {
	l, err := s.scanLocation(s.db.QueryRowContext(ctx, `SELECT `+locationCols+` FROM storage WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// PutLocation inserts when ID is zero and updates otherwise. Two locations can't share a
// name, and paths are compared without case, as Windows does.
func (s *Store) PutLocation(ctx context.Context, l Location) (Location, error) {
	all, err := s.Locations(ctx)
	if err != nil {
		return l, err
	}
	for _, o := range all {
		if o.ID != l.ID && (strings.EqualFold(o.Name, l.Name) || strings.EqualFold(o.Path, l.Path)) {
			return l, ErrDuplicate
		}
	}
	password, err := encryptSecret(s.key, l.Password)
	if err != nil {
		return l, err
	}
	if l.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO storage (name, path, min_free_gb, recording, share_user, share_password, standby) VALUES (?,?,?,?,?,?,?)`, l.Name, l.Path, l.MinFreeGB, l.Recording, l.Username, password, l.Standby)
		if err != nil {
			return l, err
		}
		l.ID, err = res.LastInsertId()
		return l, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE storage SET name = ?, path = ?, min_free_gb = ?, recording = ?, share_user = ?, share_password = ?, standby = ? WHERE id = ?`, l.Name, l.Path, l.MinFreeGB, l.Recording, l.Username, password, l.Standby, l.ID)
	if err != nil {
		return l, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return l, ErrNotFound
	}
	return l, nil
}

// DeleteLocation removes a location and lets its cameras be placed again. The files
// stay where they are.
func (s *Store) DeleteLocation(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM storage WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `UPDATE cameras SET storage_id = 0 WHERE storage_id = ?`, id)
	return err
}

// SetMoving starts or stops moving a location's video to another. A location being
// emptied takes no new video.
func (s *Store) SetMoving(ctx context.Context, id, to int64) error {
	q := `UPDATE storage SET moving_to = ? WHERE id = ?`
	if to != 0 {
		q = `UPDATE storage SET moving_to = ?, recording = 0 WHERE id = ?`
	}
	res, err := s.db.ExecContext(ctx, q, to, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetCameraStorage records the location a camera normally records to.
func (s *Store) SetCameraStorage(ctx context.Context, cameraID, locationID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cameras SET storage_id = ? WHERE id = ?`, locationID, cameraID)
	return err
}

// SetMotionSince protects a camera's video from before now from motion pruning.
func (s *Store) SetMotionSince(ctx context.Context, cameraID, unix int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE cameras SET motion_since = ? WHERE id = ?`, unix, cameraID)
	return err
}
