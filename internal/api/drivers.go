package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"seelynx/internal/ai"
	"seelynx/internal/drivers"
	"seelynx/internal/onvif"
	"seelynx/internal/store"
)

const (
	probeTime  = 30 * time.Second // a real camera took 12.5 seconds
	probePulls = 3
)

func validateDriver(it *store.Item) string {
	d, ok := parse[drivers.Driver](it)
	if !ok {
		return "Invalid driver."
	}
	return d.Validate()
}

func (s *Server) savedDrivers(ctx context.Context) ([]drivers.Driver, error) {
	items, err := s.Store.Items(ctx, "drivers")
	if err != nil {
		return nil, err
	}
	out := make([]drivers.Driver, 0, len(items))
	for i := range items {
		if d, ok := parse[drivers.Driver](&items[i]); ok {
			out = append(out, d)
		}
	}
	return out, nil
}

// AnalyticsName returns a driver's friendly name for an event, if there is one.
func (s *Server) AnalyticsName(manufacturer, model, topic string) (string, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	list, err := s.savedDrivers(ctx)
	if err != nil {
		return "", false
	}
	d, ok := drivers.Find(list, manufacturer, model)
	if !ok {
		return "", false
	}
	return d.Name(topic)
}

func isAnalyticsTopic(e onvif.Event) bool {
	if !strings.Contains(e.Topic, "Analytics") && !strings.Contains(e.Topic, "RuleEngine") {
		return false
	}
	_, isMotion := motionSignal(e)
	return !isMotion
}

// probe reads what a camera offers. It only asks questions: the one thing it creates is
// a short event subscription, because a camera lists its events' current state on
// subscribing, and that's the only reliable way to see their exact names.
func (s *Server) probe(ctx context.Context, c store.Camera) (drivers.Report, error) {
	addr := deviceAddress(c)
	if s.Motion != nil {
		addr = s.Motion.address(c)
	}
	if addr == "" {
		return drivers.Report{}, errors.New("the camera has no address")
	}
	client := &onvif.Client{XAddr: addr, User: c.Username, Pass: c.Password}
	if err := client.SyncClock(ctx); err != nil {
		s.Log.Debug("camera clock", "camera", c.Name, "err", err)
	}
	info, err := client.DeviceInformation(ctx)
	if err != nil {
		return drivers.Report{}, err
	}
	r := drivers.Report{Manufacturer: info.Manufacturer, Model: info.Model, Firmware: info.Firmware, Profiles: []drivers.Profile{}, Topics: []drivers.Topic{}}
	if profiles, err := client.Profiles(ctx); err == nil {
		for _, p := range profiles {
			r.Profiles = append(r.Profiles, drivers.Profile{Name: p.Name, Codec: p.Codec, Width: p.Width, Height: p.Height})
		}
	}
	if _, err := client.SnapshotURI(ctx); err == nil {
		r.Snapshot = true
	}
	events, err := client.EventsAddress(ctx)
	if err != nil {
		return r, err
	}
	sub, err := client.Subscribe(ctx, events, time.Minute)
	if err != nil {
		return r, err
	}
	defer func() {
		// Cameras allow few subscriptions, so give this one back.
		dctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		client.Unsubscribe(dctx, sub)
	}()
	seen := map[string][]string{}
	for range probePulls {
		list, err := client.Pull(ctx, sub, 2*time.Second)
		if err != nil {
			return r, err
		}
		for _, e := range list {
			if !isAnalyticsTopic(e) {
				continue
			}
			fields := make([]string, 0, len(e.Items))
			for name := range e.Items {
				fields = append(fields, name)
			}
			sort.Strings(fields)
			seen[e.Topic] = fields
		}
	}
	for topic, fields := range seen {
		r.Topics = append(r.Topics, drivers.Topic{Topic: topic, Fields: fields})
	}
	sort.Slice(r.Topics, func(i, j int) bool { return r.Topics[i].Topic < r.Topics[j].Topic })
	return r, nil
}

func (s *Server) probeDriver(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CameraID int64 `json:"camera_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	c, err := s.Store.Camera(r.Context(), in.CameraID)
	if errors.Is(err, store.ErrNotFound) {
		fail(w, http.StatusNotFound, "Camera not found.")
		return
	}
	if err != nil {
		s.internal(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), probeTime)
	defer cancel()
	report, err := s.probe(ctx, c)
	if err != nil {
		fail(w, http.StatusBadGateway, "Couldn't read this camera: "+err.Error())
		return
	}
	if err := report.Check(); err != nil {
		fail(w, http.StatusUnprocessableEntity, "Can't build a driver: "+err.Error()+".")
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) aiConfig(ctx context.Context) (ai.Config, bool, error) {
	cfg, err := s.Store.AIService(ctx)
	if err != nil || cfg == nil {
		return ai.Config{}, false, err
	}
	return ai.Config{Provider: cfg["provider"], Model: cfg["model"], BaseURL: cfg["base_url"], APIKey: cfg["api_key"]}, true, nil
}

// buildDriver takes the report the person has just reviewed, so what goes to the AI
// service is exactly what they saw.
func (s *Server) buildDriver(w http.ResponseWriter, r *http.Request) {
	var report drivers.Report
	if !decode(w, r, &report) {
		return
	}
	if err := report.Check(); err != nil {
		fail(w, http.StatusBadRequest, "Can't build a driver: "+err.Error()+".")
		return
	}
	cfg, ok, err := s.aiConfig(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if !ok {
		fail(w, http.StatusConflict, "Connect an AI service first.")
		return
	}
	d, err := drivers.Build(r.Context(), cfg, report)
	if err != nil {
		fail(w, http.StatusBadGateway, "The AI service couldn't build this driver: "+err.Error()+".")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type aiView struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	BaseURL  string `json:"base_url"`
	HasKey   bool   `json:"has_key"`
}

func (s *Server) getAI(w http.ResponseWriter, r *http.Request) {
	cfg, ok, err := s.aiConfig(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, aiView{})
		return
	}
	writeJSON(w, http.StatusOK, aiView{cfg.Provider, cfg.Model, cfg.BaseURL, cfg.APIKey != ""})
}

func (s *Server) saveAI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		Model    string `json:"model"`
		BaseURL  string `json:"base_url"`
		APIKey   string `json:"api_key"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Provider == "" {
		if err := s.Store.SaveAIService(r.Context(), nil); err != nil {
			s.internal(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !slices.Contains([]string{ai.Anthropic, ai.OpenAI}, in.Provider) {
		fail(w, http.StatusBadRequest, "Choose a service.")
		return
	}
	in.Model, in.BaseURL = strings.TrimSpace(in.Model), strings.TrimSpace(in.BaseURL)
	if in.Provider == ai.OpenAI && in.Model == "" {
		fail(w, http.StatusBadRequest, "Enter the model to use.")
		return
	}
	if in.BaseURL != "" {
		u, err := url.Parse(in.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			fail(w, http.StatusBadRequest, "The service address must start with http:// or https://.")
			return
		}
	}
	current, _, err := s.aiConfig(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	// An empty key keeps the stored one, but a key never carries over to another
	// service, because that would hand it to whoever runs the new address.
	key := strings.TrimSpace(in.APIKey)
	if key == "" && current.Provider == in.Provider && current.BaseURL == in.BaseURL {
		key = current.APIKey
	}
	if key == "" && in.Provider == ai.Anthropic {
		fail(w, http.StatusBadRequest, "Enter the API key.")
		return
	}
	if key == "" && in.BaseURL == "" {
		fail(w, http.StatusBadRequest, "Enter the API key, or the address of your own server.")
		return
	}
	err = s.Store.SaveAIService(r.Context(), map[string]string{"provider": in.Provider, "model": in.Model, "base_url": in.BaseURL, "api_key": key})
	if err != nil {
		s.internal(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) testAI(w http.ResponseWriter, r *http.Request) {
	cfg, ok, err := s.aiConfig(r.Context())
	if err != nil {
		s.internal(w, err)
		return
	}
	if !ok {
		fail(w, http.StatusConflict, "Connect an AI service first.")
		return
	}
	answer, err := ai.Ask(r.Context(), cfg, "You check that a connection works.", `Reply with this JSON: {"ok": true}`, map[string]any{
		"type": "object", "properties": map[string]any{"ok": map[string]any{"type": "boolean"}}, "required": []string{"ok"}, "additionalProperties": false,
	})
	if err != nil {
		fail(w, http.StatusBadGateway, "The test failed: "+err.Error()+".")
		return
	}
	var got map[string]any
	if json.Unmarshal([]byte(answer), &got) != nil {
		fail(w, http.StatusBadGateway, "The service answered, but not with JSON. Try a different model.")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
