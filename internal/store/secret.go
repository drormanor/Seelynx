package store

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Camera passwords must be readable again to log in to cameras, so they are encrypted
// rather than hashed. This protects the database and its backups if they leak on their
// own. It can't protect against someone who also has the key file.
const secretPrefix = "enc:v1:"

// LoadOrCreateKey reads the 32-byte key at path, creating it on first use.
func LoadOrCreateKey(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err == nil {
		key, err := hex.DecodeString(strings.TrimSpace(string(raw)))
		if err != nil || len(key) != 32 {
			return nil, fmt.Errorf("%s is not a valid key file", path)
		}
		return key, nil
	}
	if !os.IsNotExist(err) {
		return nil, err
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	// O_EXCL so two starting processes can't overwrite each other's key.
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if _, err := f.WriteString(hex.EncodeToString(key) + "\n"); err != nil {
		return nil, err
	}
	return key, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func encryptSecret(key []byte, plain string) (string, error) {
	if plain == "" || len(key) == 0 {
		return plain, nil
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	return secretPrefix + base64.StdEncoding.EncodeToString(gcm.Seal(nonce, nonce, []byte(plain), nil)), nil
}

func decryptSecret(key []byte, stored string) (string, error) {
	rest, ok := strings.CutPrefix(stored, secretPrefix)
	if !ok {
		return stored, nil // not yet encrypted
	}
	if len(key) == 0 {
		return "", errors.New("no key loaded")
	}
	data, err := base64.StdEncoding.DecodeString(rest)
	if err != nil {
		return "", err
	}
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	if len(data) < gcm.NonceSize() {
		return "", errors.New("stored secret is too short")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	return string(plain), err
}

// UseKey turns on encryption, encrypts any plain camera passwords, and rewrites the
// database file so the old text doesn't linger in free pages. It returns how many
// stored passwords can't be read with this key.
func (s *Store) UseKey(ctx context.Context, key []byte) (unreadable int, err error) {
	s.key = key
	rows, err := s.db.QueryContext(ctx, `SELECT id, password FROM cameras WHERE password <> ''`)
	if err != nil {
		return 0, err
	}
	type row struct {
		id       int64
		password string
	}
	var plain []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.password); err != nil {
			rows.Close()
			return 0, err
		}
		if !strings.HasPrefix(r.password, secretPrefix) {
			plain = append(plain, r)
		} else if _, err := decryptSecret(key, r.password); err != nil {
			unreadable++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, r := range plain {
		enc, err := encryptSecret(key, r.password)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE cameras SET password = ? WHERE id = ?`, enc, r.id); err != nil {
			return 0, err
		}
	}
	// Connections saved before a key was set are stored as plain JSON.
	conns, err := s.db.QueryContext(ctx, `SELECT id, config FROM connections WHERE config <> '' AND config NOT LIKE 'enc:%'`)
	if err != nil {
		return 0, err
	}
	var plainConns []row
	for conns.Next() {
		var r row
		if err := conns.Scan(&r.id, &r.password); err != nil {
			conns.Close()
			return 0, err
		}
		plainConns = append(plainConns, r)
	}
	conns.Close()
	if err := conns.Err(); err != nil {
		return 0, err
	}
	for _, r := range plainConns {
		enc, err := encryptSecret(key, r.password)
		if err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE connections SET config = ? WHERE id = ?`, enc, r.id); err != nil {
			return 0, err
		}
	}
	if len(plain)+len(plainConns) > 0 {
		if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `VACUUM`); err != nil {
			return 0, err
		}
		if _, err := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
			return 0, err
		}
	}
	return unreadable, nil
}
