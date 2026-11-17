package api

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"

	"seelynx/internal/store"
)

func currentUser(r *http.Request) store.User {
	return r.Context().Value(ctxKey{}).(store.User)
}

// checkPassword returns what's wrong with a new password, or "".
func checkPassword(password, username string, complex bool) string {
	// bcrypt ignores everything past 72 bytes, so reject rather than truncate.
	if len(password) < minPass || len(password) > 72 {
		return fmt.Sprintf("Password must be %d to 72 characters.", minPass)
	}
	if !complex {
		return ""
	}
	var lower, upper, digit, other int
	for _, c := range password {
		switch {
		case unicode.IsLower(c):
			lower = 1
		case unicode.IsUpper(c):
			upper = 1
		case unicode.IsDigit(c):
			digit = 1
		default:
			other = 1
		}
	}
	if lower+upper+digit+other < 3 {
		return "Use at least three of: lowercase letters, capitals, numbers and symbols."
	}
	if username != "" && strings.Contains(strings.ToLower(password), strings.ToLower(username)) {
		return "Password can't contain the username."
	}
	return ""
}

// changePassword lets signed-in users set their own password, including when forced to.
func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	u := currentUser(r)
	var in struct {
		Current string `json:"current"`
		New     string `json:"new"`
	}
	if !decode(w, r, &in) {
		return
	}
	ip := clientIP(r)
	if !s.limiter.allow(ip) {
		fail(w, http.StatusTooManyRequests, "Too many failed attempts. Wait 15 minutes and try again.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(in.Current)) != nil {
		s.limiter.failed(ip)
		fail(w, http.StatusBadRequest, "Your current password is incorrect.")
		return
	}
	if msg := checkPassword(in.New, u.Username, u.ComplexPassword); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if in.New == in.Current {
		fail(w, http.StatusBadRequest, "Choose a password you haven't just used.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.New), bcrypt.DefaultCost)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.Store.SetPassword(r.Context(), u.ID, string(hash), false); err != nil {
		s.internal(w, err)
		return
	}
	// A changed password should end any other session using the old one.
	c, err := r.Cookie(cookieName)
	if err != nil {
		s.internal(w, err)
		return
	}
	if err := s.Store.EndSessions(r.Context(), u.ID, c.Value); err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type userInput struct {
	Username        string  `json:"username"`
	Password        *string `json:"password"` // nil keeps the current password
	RoleID          int64   `json:"role_id"`
	FirstName       string  `json:"first_name"`
	LastName        string  `json:"last_name"`
	Remarks         string  `json:"remarks"`
	ComplexPassword bool    `json:"complex_password"`
	MustChange      bool    `json:"must_change"`
	PasswordMaxDays int     `json:"password_max_days"`
	ExpiresAt       int64   `json:"expires_at"`
}

func (s *Server) applyUser(w http.ResponseWriter, r *http.Request, in *userInput, u *store.User) bool {
	if in.PasswordMaxDays < 0 || in.PasswordMaxDays > 3650 {
		fail(w, http.StatusBadRequest, "Password change period must be 0 to 3650 days.")
		return false
	}
	if in.ExpiresAt < 0 {
		fail(w, http.StatusBadRequest, "Invalid expiry date.")
		return false
	}
	if len(in.FirstName) > 100 || len(in.LastName) > 100 || len(in.Remarks) > 500 {
		fail(w, http.StatusBadRequest, "Names and remarks are too long.")
		return false
	}
	_, err := s.Store.Role(r.Context(), in.RoleID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusBadRequest, "Choose a role.")
		return false
	}
	if err != nil {
		s.internal(w, err)
		return false
	}
	u.RoleID, u.FirstName, u.LastName, u.Remarks = in.RoleID, strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName), in.Remarks
	u.ComplexPassword, u.MustChange, u.PasswordMaxDays, u.ExpiresAt = in.ComplexPassword, in.MustChange, in.PasswordMaxDays, in.ExpiresAt
	return true
}

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.Users(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, us)
}

func (s *Server) createUser(w http.ResponseWriter, r *http.Request) {
	var in userInput
	if !decode(w, r, &in) {
		return
	}
	u := store.User{Username: strings.TrimSpace(in.Username)}
	if u.Username == "" || len(u.Username) > 64 {
		fail(w, http.StatusBadRequest, "Enter a username of up to 64 characters.")
		return
	}
	if !s.applyUser(w, r, &in, &u) {
		return
	}
	if in.Password == nil {
		fail(w, http.StatusBadRequest, "Enter a password.")
		return
	}
	if msg := checkPassword(*in.Password, u.Username, u.ComplexPassword); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	if _, err := s.Store.UserByName(r.Context(), u.Username); err == nil {
		fail(w, http.StatusConflict, "That username is already taken.")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
	if err != nil {
		s.internal(w, err)
		return
	}
	u.PasswordHash = string(hash)
	created, err := s.Store.AddUser(r.Context(), u)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	u, err := s.Store.UserByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "User not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	var in userInput
	if !decode(w, r, &in) {
		return
	}
	wasAdmin := u.RoleID == store.RoleAdmin
	if !s.applyUser(w, r, &in, &u) {
		return
	}
	if wasAdmin && (u.RoleID != store.RoleAdmin || u.ExpiresAt != 0) {
		n, err := s.Store.AdminCount(r.Context())
		if err != nil {
			s.internal(w, err)
			return
		}
		if n <= 1 {
			fail(w, http.StatusConflict, "At least one administrator must remain, with an account that doesn't expire.")
			return
		}
	}
	if in.Password != nil {
		if msg := checkPassword(*in.Password, u.Username, u.ComplexPassword); msg != "" {
			fail(w, http.StatusBadRequest, msg)
			return
		}
	}
	saved, err := s.Store.UpdateUser(r.Context(), u)
	if err != nil {
		s.internal(w, err)
		return
	}
	if in.Password != nil {
		hash, err := bcrypt.GenerateFromPassword([]byte(*in.Password), bcrypt.DefaultCost)
		if err != nil {
			s.internal(w, err)
			return
		}
		if err := s.Store.SetPassword(r.Context(), u.ID, string(hash), in.MustChange); err != nil {
			s.internal(w, err)
			return
		}
		// A reset password ends the user's sessions, unless they're resetting their own.
		keep := ""
		if c, err := r.Cookie(cookieName); err == nil && currentUser(r).ID == u.ID {
			keep = c.Value
		}
		if err := s.Store.EndSessions(r.Context(), u.ID, keep); err != nil {
			s.internal(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteUser(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusConflict, "This user can't be removed. At least one admin must remain.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- roles ---

func (s *Server) listRoles(w http.ResponseWriter, r *http.Request) {
	roles, err := s.Store.Roles(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, roles)
}

func (s *Server) saveRole(w http.ResponseWriter, r *http.Request) {
	var role store.Role
	if !decode(w, r, &role) {
		return
	}
	role.ID = 0
	if r.PathValue("id") != "" {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		role.ID = id
	}
	role.Name = strings.TrimSpace(role.Name)
	if role.Name == "" || len(role.Name) > 64 {
		fail(w, http.StatusBadRequest, "Enter a role name of up to 64 characters.")
		return
	}
	if len(role.Permissions) == 0 {
		fail(w, http.StatusBadRequest, "Choose at least one permission.")
		return
	}
	for _, p := range role.Permissions {
		if !slices.Contains(store.AllPermissions, p) {
			fail(w, http.StatusBadRequest, "Unknown permission.")
			return
		}
	}
	if role.Permissions == nil {
		role.Permissions = []string{}
	}
	if role.CameraIDs == nil {
		role.CameraIDs = []int64{}
	}
	if !role.AllCameras && len(role.CameraIDs) == 0 {
		fail(w, http.StatusBadRequest, "Choose at least one camera, or allow all cameras.")
		return
	}
	roles, err := s.Store.Roles(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	for _, o := range roles {
		if o.ID != role.ID && strings.EqualFold(o.Name, role.Name) {
			fail(w, http.StatusConflict, "A role with that name already exists.")
			return
		}
	}
	code := http.StatusOK
	if role.ID == 0 {
		code = http.StatusCreated
	}
	saved, err := s.Store.SaveRole(r.Context(), role)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusConflict, "Built-in roles can't be changed.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, code, saved)
}

func (s *Server) deleteRole(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	err := s.Store.DeleteRole(r.Context(), id)
	switch {
	case errors.Is(err, store.ErrInUse):
		fail(w, http.StatusConflict, "Users still have this role.")
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusConflict, "Built-in roles can't be removed.")
	case err != nil:
		s.internal(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// viewCamera loads a camera the signed-in user may see. Hidden cameras look missing,
// so a restricted user can't find out which IDs exist.
func (s *Server) viewCamera(w http.ResponseWriter, r *http.Request) (store.Camera, bool) {
	c, ok := s.camera(w, r)
	if !ok {
		return c, false
	}
	if !currentUser(r).CanSeeCamera(c.ID) {
		fail(w, http.StatusNotFound, "Camera not found.")
		return c, false
	}
	return c, true
}

// sessionFor returns the active user for a request, ending the session of an expired account.
func (s *Server) sessionFor(r *http.Request) (store.User, string, error) {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return store.User{}, "", store.ErrNotFound
	}
	u, err := s.Store.SessionUser(r.Context(), c.Value)
	if err != nil {
		return u, "", err
	}
	if u.Expired(time.Now()) {
		if err := s.Store.DeleteSession(r.Context(), c.Value); err != nil {
			return u, "", err
		}
		return u, "", store.ErrNotFound
	}
	return u, c.Value, nil
}
