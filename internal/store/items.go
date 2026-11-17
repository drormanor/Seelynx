package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Item is a named piece of configuration. Groups, schedules, views, tours and
// access-list entries share this shape, and their Data is validated by the API.
type Item struct {
	ID   int64           `json:"id"`
	Name string          `json:"name"`
	Data json.RawMessage `json:"data"`
}

const itemsSchema = `
CREATE TABLE IF NOT EXISTS config_items (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  kind TEXT NOT NULL,
  name TEXT NOT NULL,
  data TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX IF NOT EXISTS config_items_kind ON config_items(kind);
`

// migrate brings a database from an earlier version up to date.
func migrate(db *sql.DB) error {
	if _, err := db.Exec(itemsSchema); err != nil {
		return err
	}
	if _, err := db.Exec(motionSchema); err != nil {
		return err
	}
	if err := migrateUsers(db); err != nil {
		return err
	}
	if _, err := db.Exec(alarmsSchema); err != nil {
		return err
	}
	if _, err := db.Exec(loginSchema); err != nil {
		return err
	}
	if _, err := db.Exec(storageSchema); err != nil {
		return err
	}
	var hasMoving int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('storage') WHERE name = 'moving_to'`).Scan(&hasMoving); err != nil {
		return err
	}
	if hasMoving == 0 {
		if _, err := db.Exec(`ALTER TABLE storage ADD COLUMN moving_to INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	for _, col := range []string{"share_user", "share_password"} {
		var has int
		if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('storage') WHERE name = ?`, col).Scan(&has); err != nil {
			return err
		}
		if has == 0 {
			if _, err := db.Exec(`ALTER TABLE storage ADD COLUMN ` + col + ` TEXT NOT NULL DEFAULT ''`); err != nil {
				return err
			}
		}
	}
	var hasStandby int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('storage') WHERE name = 'standby'`).Scan(&hasStandby); err != nil {
		return err
	}
	if hasStandby == 0 {
		if _, err := db.Exec(`ALTER TABLE storage ADD COLUMN standby INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
	}
	if _, err := db.Exec(aiSchema); err != nil {
		return err
	}
	var hasSnapshot int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('alarms') WHERE name = 'snapshot'`).Scan(&hasSnapshot); err != nil {
		return err
	}
	if hasSnapshot == 0 {
		if _, err := db.Exec(`ALTER TABLE alarms ADD COLUMN snapshot TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('cameras')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return err
		}
		have[n] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	for _, col := range []string{"group_id", "numeric_id", "schedule_id", "motion_only", "pre_seconds", "post_seconds", "motion_since", "storage_id", "outside_mode", "rotation"} {
		if !have[col] {
			if _, err := db.Exec(fmt.Sprintf(`ALTER TABLE cameras ADD COLUMN %s INTEGER NOT NULL DEFAULT 0`, col)); err != nil {
				return err
			}
		}
	}
	if !have["fisheye"] {
		if _, err := db.Exec(`ALTER TABLE cameras ADD COLUMN fisheye TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Items(ctx context.Context, kind string) ([]Item, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, data FROM config_items WHERE kind = ? ORDER BY name COLLATE NOCASE, id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		var it Item
		var data string
		if err := rows.Scan(&it.ID, &it.Name, &data); err != nil {
			return nil, err
		}
		it.Data = json.RawMessage(data)
		out = append(out, it)
	}
	return out, rows.Err()
}

func (s *Store) Item(ctx context.Context, kind string, id int64) (Item, error) {
	it := Item{ID: id}
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT name, data FROM config_items WHERE kind = ? AND id = ?`, kind, id).Scan(&it.Name, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return it, ErrNotFound
	}
	it.Data = json.RawMessage(data)
	return it, err
}

// PutItem inserts when ID is zero and updates otherwise.
func (s *Store) PutItem(ctx context.Context, kind string, it Item) (Item, error) {
	if it.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO config_items (kind, name, data) VALUES (?,?,?)`, kind, it.Name, string(it.Data))
		if err != nil {
			return it, err
		}
		it.ID, err = res.LastInsertId()
		return it, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE config_items SET name = ?, data = ? WHERE kind = ? AND id = ?`, it.Name, string(it.Data), kind, it.ID)
	if err != nil {
		return it, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return it, ErrNotFound
	}
	return it, nil
}

func (s *Store) DeleteItem(ctx context.Context, kind string, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM config_items WHERE kind = ? AND id = ?`, kind, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Snapshot writes a consistent copy of the database to path.
func (s *Store) Snapshot(ctx context.Context, path string) error {
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
