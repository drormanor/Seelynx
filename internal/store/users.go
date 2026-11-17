package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"time"
)

// Permissions a role can grant.
const (
	PermLive     = "live"
	PermPlayback = "playback"
	PermSystem   = "system"
	PermCameras  = "cameras"
	PermUsers    = "users"
	PermSettings = "settings"
)

// AllPermissions lists every permission.
var AllPermissions = []string{PermLive, PermPlayback, PermSystem, PermCameras, PermUsers, PermSettings}

// Built-in roles have fixed IDs and can't be changed or removed.
const (
	RoleAdmin    int64 = 1
	RoleOperator int64 = 2
	RoleViewer   int64 = 3
)

var ErrInUse = errors.New("in use")

const rolesSchema = `
CREATE TABLE IF NOT EXISTS roles (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE COLLATE NOCASE,
  builtin INTEGER NOT NULL DEFAULT 0,
  permissions TEXT NOT NULL DEFAULT '[]',
  all_cameras INTEGER NOT NULL DEFAULT 1,
  camera_ids TEXT NOT NULL DEFAULT '[]'
);
INSERT OR IGNORE INTO roles (id, name, builtin, permissions) VALUES
  (1, 'Administrator', 1, '["live","playback","system","cameras","users","settings"]'),
  (2, 'Operator', 1, '["live","playback","system"]'),
  (3, 'Viewer', 1, '["live"]');
`

// migrateUsers adds the roles table and the newer user columns.
func migrateUsers(db *sql.DB) error {
	if _, err := db.Exec(rolesSchema); err != nil {
		return err
	}
	have := map[string]bool{}
	rows, err := db.Query(`SELECT name FROM pragma_table_info('users')`)
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
	for _, col := range []struct{ name, def string }{
		{"role_id", "INTEGER NOT NULL DEFAULT 3"},
		{"first_name", "TEXT NOT NULL DEFAULT ''"},
		{"last_name", "TEXT NOT NULL DEFAULT ''"},
		{"remarks", "TEXT NOT NULL DEFAULT ''"},
		{"complex_password", "INTEGER NOT NULL DEFAULT 0"},
		{"must_change", "INTEGER NOT NULL DEFAULT 0"},
		{"password_max_days", "INTEGER NOT NULL DEFAULT 0"},
		{"password_changed_at", "INTEGER NOT NULL DEFAULT 0"},
		{"expires_at", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if have[col.name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE users ADD COLUMN ` + col.name + ` ` + col.def); err != nil {
			return err
		}
	}
	if !have["role_id"] {
		// Carry existing accounts over from the old fixed roles.
		_, err := db.Exec(`UPDATE users SET role_id = CASE role WHEN 'admin' THEN 1 WHEN 'operator' THEN 2 ELSE 3 END, password_changed_at = ?`, time.Now().Unix())
		return err
	}
	return nil
}

// Role is a named set of permissions and camera access.
type Role struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Builtin     bool     `json:"builtin"`
	Permissions []string `json:"permissions"`
	AllCameras  bool     `json:"all_cameras"`
	CameraIDs   []int64  `json:"camera_ids"`
}

// User is an account, with its role's access filled in.
type User struct {
	ID              int64    `json:"id"`
	Username        string   `json:"username"`
	PasswordHash    string   `json:"-"`
	RoleID          int64    `json:"role_id"`
	Role            string   `json:"role"`
	Permissions     []string `json:"permissions"`
	AllCameras      bool     `json:"all_cameras"`
	CameraIDs       []int64  `json:"camera_ids"`
	FirstName       string   `json:"first_name"`
	LastName        string   `json:"last_name"`
	Remarks         string   `json:"remarks"`
	ComplexPassword bool     `json:"complex_password"`
	MustChange      bool     `json:"must_change"`
	PasswordMaxDays int      `json:"password_max_days"`
	PasswordChanged int64    `json:"-"`
	ExpiresAt       int64    `json:"expires_at"` // Unix seconds; 0 never expires
}

// Can reports whether the user has a permission. An empty name means any account.
func (u User) Can(perm string) bool {
	return perm == "" || slices.Contains(u.Permissions, perm)
}

// CanSeeCamera reports whether the user's role includes a camera.
func (u User) CanSeeCamera(id int64) bool {
	return u.AllCameras || slices.Contains(u.CameraIDs, id)
}

// NeedsNewPassword reports whether the user must change password before continuing.
func (u User) NeedsNewPassword(now time.Time) bool {
	if u.MustChange {
		return true
	}
	return u.PasswordMaxDays > 0 && now.Unix() >= u.PasswordChanged+int64(u.PasswordMaxDays)*86400
}

// Expired reports whether the account has passed its expiry date.
func (u User) Expired(now time.Time) bool {
	return u.ExpiresAt > 0 && now.Unix() >= u.ExpiresAt
}

const userSelect = `SELECT u.id, u.username, u.password_hash, u.role_id, r.name, r.permissions, r.all_cameras, r.camera_ids,
  u.first_name, u.last_name, u.remarks, u.complex_password, u.must_change, u.password_max_days, u.password_changed_at, u.expires_at
  FROM users u JOIN roles r ON r.id = u.role_id `

func scanUser(row interface{ Scan(...any) error }) (User, error) {
	var u User
	var perms, cams string
	err := row.Scan(&u.ID, &u.Username, &u.PasswordHash, &u.RoleID, &u.Role, &perms, &u.AllCameras, &cams,
		&u.FirstName, &u.LastName, &u.Remarks, &u.ComplexPassword, &u.MustChange, &u.PasswordMaxDays, &u.PasswordChanged, &u.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return u, ErrNotFound
	}
	if err != nil {
		return u, err
	}
	if err := json.Unmarshal([]byte(perms), &u.Permissions); err != nil {
		return u, err
	}
	return u, json.Unmarshal([]byte(cams), &u.CameraIDs)
}

func (s *Store) Users(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, userSelect+`ORDER BY u.username COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *Store) UserByName(ctx context.Context, name string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.username = ?`, name))
}

func (s *Store) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, userSelect+`WHERE u.id = ?`, id))
}

// CreateUser adds a user with one of the built-in roles.
func (s *Store) CreateUser(ctx context.Context, name, hash, role string) (User, error) {
	id := RoleViewer
	switch role {
	case "admin":
		id = RoleAdmin
	case "operator":
		id = RoleOperator
	}
	return s.AddUser(ctx, User{Username: name, PasswordHash: hash, RoleID: id})
}

// AddUser inserts a user. The old role column stays only to satisfy its constraint.
func (s *Store) AddUser(ctx context.Context, u User) (User, error) {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, role_id, first_name, last_name, remarks, complex_password, must_change, password_max_days, password_changed_at, expires_at)
		 VALUES (?,?,'viewer',?,?,?,?,?,?,?,?,?)`,
		u.Username, u.PasswordHash, u.RoleID, u.FirstName, u.LastName, u.Remarks, u.ComplexPassword, u.MustChange, u.PasswordMaxDays, time.Now().Unix(), u.ExpiresAt)
	if err != nil {
		return u, err
	}
	return s.UserByName(ctx, u.Username)
}

// UpdateUser saves profile, role and policy fields, but not the password.
func (s *Store) UpdateUser(ctx context.Context, u User) (User, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET role_id=?, first_name=?, last_name=?, remarks=?, complex_password=?, must_change=?, password_max_days=?, expires_at=? WHERE id=?`,
		u.RoleID, u.FirstName, u.LastName, u.Remarks, u.ComplexPassword, u.MustChange, u.PasswordMaxDays, u.ExpiresAt, u.ID)
	if err != nil {
		return u, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return u, ErrNotFound
	}
	return s.UserByID(ctx, u.ID)
}

// SetPassword stores a new password hash.
func (s *Store) SetPassword(ctx context.Context, id int64, hash string, mustChange bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET password_hash=?, must_change=?, password_changed_at=? WHERE id=?`, hash, mustChange, time.Now().Unix(), id)
	return err
}

// EndSessions signs a user out everywhere, except for one token if given.
func (s *Store) EndSessions(ctx context.Context, userID int64, keepToken string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ? AND token_hash <> ?`, userID, hashToken(keepToken))
	return err
}

// AdminCount returns how many accounts hold the Administrator role.
func (s *Store) AdminCount(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role_id = ?`, RoleAdmin).Scan(&n)
	return n, err
}

// CreateFirstUser creates an admin only if no users exist, atomically, so two
// concurrent first-run requests cannot both succeed.
func (s *Store) CreateFirstUser(ctx context.Context, name, hash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (username, password_hash, role, role_id, password_changed_at)
		 SELECT ?, ?, 'admin', ?, ? WHERE NOT EXISTS (SELECT 1 FROM users)`, name, hash, RoleAdmin, time.Now().Unix())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// DeleteUser removes a user but refuses to remove the last admin.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ? AND (role_id <> ? OR (SELECT COUNT(*) FROM users WHERE role_id = ?) > 1)`, id, RoleAdmin, RoleAdmin)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func scanRole(row interface{ Scan(...any) error }) (Role, error) {
	var r Role
	var perms, cams string
	err := row.Scan(&r.ID, &r.Name, &r.Builtin, &perms, &r.AllCameras, &cams)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(perms), &r.Permissions); err != nil {
		return r, err
	}
	return r, json.Unmarshal([]byte(cams), &r.CameraIDs)
}

const roleSelect = `SELECT id, name, builtin, permissions, all_cameras, camera_ids FROM roles `

func (s *Store) Roles(ctx context.Context) ([]Role, error) {
	rows, err := s.db.QueryContext(ctx, roleSelect+`ORDER BY builtin DESC, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Role{}
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Role(ctx context.Context, id int64) (Role, error) {
	return scanRole(s.db.QueryRowContext(ctx, roleSelect+`WHERE id = ?`, id))
}

// SaveRole inserts when ID is zero and updates otherwise. Built-in roles never change.
func (s *Store) SaveRole(ctx context.Context, r Role) (Role, error) {
	perms, err := json.Marshal(r.Permissions)
	if err != nil {
		return r, err
	}
	cams, err := json.Marshal(r.CameraIDs)
	if err != nil {
		return r, err
	}
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO roles (name, permissions, all_cameras, camera_ids) VALUES (?,?,?,?)`, r.Name, string(perms), r.AllCameras, string(cams))
		if err != nil {
			return r, err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return r, err
		}
		return s.Role(ctx, id)
	}
	res, err := s.db.ExecContext(ctx, `UPDATE roles SET name=?, permissions=?, all_cameras=?, camera_ids=? WHERE id=? AND builtin=0`, r.Name, string(perms), r.AllCameras, string(cams), r.ID)
	if err != nil {
		return r, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return r, ErrNotFound
	}
	return s.Role(ctx, r.ID)
}

// DeleteRole removes a custom role that nobody holds.
func (s *Store) DeleteRole(ctx context.Context, id int64) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrInUse
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM roles WHERE id = ? AND builtin = 0`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
