package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const loginSchema = `
CREATE TABLE IF NOT EXISTS default_login (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  config TEXT NOT NULL
);
`

// Login is the username and password new cameras can start with.
type Login struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

// Set reports whether there's a default login.
func (l Login) Set() bool { return l.Username != "" }

// DefaultLogin returns the default camera login, which is stored encrypted. The result is
// empty when none is set.
func (s *Store) DefaultLogin(ctx context.Context) (Login, error) {
	var stored string
	var l Login
	err := s.db.QueryRowContext(ctx, `SELECT config FROM default_login WHERE id = 1`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return l, nil
	}
	if err != nil {
		return l, err
	}
	plain, err := decryptSecret(s.key, stored)
	if err != nil || plain == "" {
		return l, nil // an unreadable login just needs entering again
	}
	return l, json.Unmarshal([]byte(plain), &l)
}

// SaveDefaultLogin replaces the default login. An empty login removes it.
func (s *Store) SaveDefaultLogin(ctx context.Context, l Login) error {
	if !l.Set() {
		_, err := s.db.ExecContext(ctx, `DELETE FROM default_login`)
		return err
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	enc, err := encryptSecret(s.key, string(raw))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO default_login (id, config) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET config = excluded.config`, enc)
	return err
}
