package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

const aiSchema = `
CREATE TABLE IF NOT EXISTS ai_service (
  id INTEGER PRIMARY KEY CHECK (id = 1),
  config TEXT NOT NULL
);
`

// AIService returns the connected AI service's settings, which hold its API key and are
// stored encrypted. It returns nil when none is connected.
func (s *Store) AIService(ctx context.Context) (map[string]string, error) {
	var stored string
	err := s.db.QueryRowContext(ctx, `SELECT config FROM ai_service WHERE id = 1`).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	plain, err := decryptSecret(s.key, stored)
	if err != nil || plain == "" {
		return nil, nil // unreadable settings just need entering again
	}
	out := map[string]string{}
	return out, json.Unmarshal([]byte(plain), &out)
}

// SaveAIService replaces the settings. Nil removes them.
func (s *Store) SaveAIService(ctx context.Context, cfg map[string]string) error {
	if cfg == nil {
		_, err := s.db.ExecContext(ctx, `DELETE FROM ai_service`)
		return err
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	enc, err := encryptSecret(s.key, string(raw))
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO ai_service (id, config) VALUES (1, ?) ON CONFLICT(id) DO UPDATE SET config = excluded.config`, enc)
	return err
}
