package api

import (
	"net/http"
	"strings"

	"seelynx/internal/store"
)

// loginView is what browsers see of the default login. The password stays on the server.
type loginView struct {
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
}

func (s *Server) getDefaultLogin(w http.ResponseWriter, r *http.Request) {
	l, err := s.Store.DefaultLogin(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, loginView{l.Username, l.Password != ""})
}

func (s *Server) saveDefaultLogin(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Username string  `json:"username"`
		Password *string `json:"password"` // nil keeps the stored password
	}
	if !decode(w, r, &in) {
		return
	}
	l, err := s.Store.DefaultLogin(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	l.Username = strings.TrimSpace(in.Username)
	if in.Password != nil {
		l.Password = *in.Password
	}
	if !l.Set() && l.Password != "" {
		fail(w, http.StatusBadRequest, "Enter a username, or remove the default login.")
		return
	}
	if err := s.Store.SaveDefaultLogin(r.Context(), l); err != nil {
		s.internal(w, err)
		return
	}
	msg := "Default camera login changed by " + currentUser(r).Username
	if !l.Set() {
		msg = "Default camera login removed by " + currentUser(r).Username
	}
	if err := s.Store.AddEvent(r.Context(), "info", msg); err != nil {
		s.Log.Warn("event not saved", "err", err)
	}
	writeJSON(w, http.StatusOK, loginView{l.Username, l.Password != ""})
}

// defaultLogin reads the login for a request that asked to use it.
func (s *Server) defaultLogin(w http.ResponseWriter, r *http.Request) (store.Login, bool) {
	l, err := s.Store.DefaultLogin(r.Context())
	if err != nil {
		s.internal(w, err)
		return l, false
	}
	if !l.Set() {
		fail(w, http.StatusBadRequest, "There's no default login. Set one, or enter a login for this camera.")
		return l, false
	}
	return l, true
}
