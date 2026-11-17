package api

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"seelynx/internal/rules"
	"seelynx/internal/store"
)

const secretSet = "********" // stands in for a stored secret in API responses

func validateRule(it *store.Item) string {
	r, ok := parse[rules.Rule](it)
	if !ok {
		return "Invalid rule."
	}
	if !slices.Contains([]string{rules.TriggerMotion, rules.TriggerAnalytics, rules.TriggerOffline, rules.TriggerOnline, rules.TriggerDisk, rules.TriggerExternal}, r.Trigger) {
		return "Choose a trigger."
	}
	if !slices.Contains([]string{"low", "medium", "high"}, r.Priority) {
		return "Choose a priority."
	}
	if r.Cooldown < 0 || r.Cooldown > 86400 {
		return "Cooldown must be 0 to 86400 seconds."
	}
	if !r.Alarm && len(r.Connections) == 0 {
		return "Choose at least one action: an alarm or a connection."
	}
	if r.Trigger == rules.TriggerExternal && r.Token == "" {
		b := make([]byte, 24)
		if _, err := rand.Read(b); err != nil {
			return "Couldn't create the rule's address."
		}
		r.Token = hex.EncodeToString(b)
	}
	if r.Trigger != rules.TriggerExternal {
		r.Token = ""
		r.CameraID = 0
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return "Invalid rule."
	}
	it.Data = raw
	return ""
}

// inboundHook lets another system trigger a rule. The secret in the address is the
// only credential, so it's compared in constant time and failures all look alike.
func (s *Server) inboundHook(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	items, err := s.Store.Items(r.Context(), "rules")
	if err != nil {
		s.internal(w, err)
		return
	}
	for i := range items {
		rule, ok := parse[rules.Rule](&items[i])
		if !ok || rule.Trigger != rules.TriggerExternal || rule.Token == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(rule.Token), []byte(token)) != 1 {
			continue
		}
		var in struct {
			Message string `json:"message"`
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			fail(w, http.StatusRequestEntityTooLarge, "Too large.")
			return
		}
		if json.Unmarshal(body, &in) != nil || in.Message == "" {
			in.Message = "External event"
		}
		if len(in.Message) > 500 {
			in.Message = in.Message[:500]
		}
		s.Rules.Emit(rules.Event{Type: rules.TriggerExternal, RuleID: items[i].ID, Message: in.Message, At: time.Now()})
		w.WriteHeader(http.StatusAccepted)
		return
	}
	fail(w, http.StatusNotFound, "Not found.")
}

// --- connections ---

func (s *Server) connectionTypes(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, rules.Types)
}

// masked hides secrets, so stored tokens never go back to a browser.
func masked(c store.Connection) store.Connection {
	t, _ := rules.FindType(c.Type)
	out := make(map[string]string, len(c.Config))
	for k, v := range c.Config {
		out[k] = v
	}
	for _, f := range t.Fields {
		if f.Secret && out[f.Key] != "" {
			out[f.Key] = secretSet
		}
	}
	c.Config = out
	return c
}

func (s *Server) listConnections(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Connections(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	for i := range list {
		list[i] = masked(list[i])
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) saveConnection(w http.ResponseWriter, r *http.Request) {
	var in store.Connection
	if !decode(w, r, &in) {
		return
	}
	in.ID = 0
	var existing store.Connection
	if r.PathValue("id") != "" {
		id, ok := pathID(w, r)
		if !ok {
			return
		}
		var err error
		existing, err = s.Store.Connection(r.Context(), id)
		if errors.Is(err, store.ErrNotFound) {
			fail(w, http.StatusNotFound, "Connection not found.")
			return
		}
		if err != nil {
			s.internal(w, err)
			return
		}
		in.ID = id
	}
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || len(in.Name) > 100 {
		fail(w, http.StatusBadRequest, "Enter a name of up to 100 characters.")
		return
	}
	t, ok := rules.FindType(in.Type)
	if !ok {
		fail(w, http.StatusBadRequest, "Choose a connection type.")
		return
	}
	cfg := map[string]string{}
	for _, f := range t.Fields {
		v := strings.TrimSpace(in.Config[f.Key])
		if f.Secret && v == secretSet {
			v = existing.Config[f.Key] // unchanged
		}
		if v == "" && f.Required {
			fail(w, http.StatusBadRequest, "Enter "+strings.ToLower(f.Label)+".")
			return
		}
		if f.Key == "url" && v != "" {
			if u, err := url.Parse(v); err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
				fail(w, http.StatusBadRequest, "Enter a web address starting with https://.")
				return
			}
		}
		cfg[f.Key] = v
	}
	in.Config = cfg
	saved, err := s.Store.SaveConnection(r.Context(), in)
	if err != nil {
		s.internal(w, err)
		return
	}
	writeJSON(w, http.StatusOK, masked(saved))
}

func (s *Server) deleteConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	items, err := s.Store.Items(r.Context(), "rules")
	if err != nil {
		s.internal(w, err)
		return
	}
	for i := range items {
		if rule, ok := parse[rules.Rule](&items[i]); ok && slices.Contains(rule.Connections, id) {
			fail(w, http.StatusConflict, "A rule is still using this connection.")
			return
		}
	}
	err = s.Store.DeleteConnection(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Connection not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testConnection(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	c, err := s.Store.Connection(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Connection not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	m := rules.Message{Title: "Seelynx test", Text: "This is a test message from Seelynx.", Priority: "low", Event: "test", At: time.Now()}
	if rules.SendsImages(c.Type) {
		m.Text = "This is a test message from Seelynx, with a sample picture."
		m.Snapshot = rules.SamplePicture()
	}
	if err := rules.Send(r.Context(), c, m); err != nil {
		fail(w, http.StatusBadGateway, "The test failed: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- alarms ---

func (s *Server) listAlarms(w http.ResponseWriter, r *http.Request) {
	list, err := s.Store.Alarms(r.Context(), 1000)
	if err != nil {
		s.internal(w, err)
		return
	}
	u := currentUser(r)
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit < 1 || limit > 500 {
		limit = 100
	}
	out := make([]store.Alarm, 0, limit)
	for _, a := range list {
		if len(out) == limit {
			break
		}
		// Alarms that aren't tied to a camera are visible to everyone with this page.
		if a.CameraID == 0 || u.CanSeeCamera(a.CameraID) {
			a.HasSnapshot = a.HasSnapshot && canSeePictures(u, a.CameraID)
			out = append(out, a)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) ackAlarm(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		Note string `json:"note"`
	}
	if !decode(w, r, &in) {
		return
	}
	if len(in.Note) > 1000 {
		fail(w, http.StatusBadRequest, "Notes can be up to 1000 characters.")
		return
	}
	err := s.Store.AckAlarm(r.Context(), id, currentUser(r).Username, strings.TrimSpace(in.Note))
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusConflict, "This alarm has already been acknowledged.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
