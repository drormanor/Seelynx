// Package ai calls the AI service a user has connected. Seelynx ships with none: each
// system brings its own account or local model, so the code stays plain HTTP that works
// across providers.
package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Providers Seelynx can talk to. "openai" also covers anything with the same API, such
// as a local Ollama server.
const (
	Anthropic = "anthropic"
	OpenAI    = "openai"

	// DefaultAnthropicModel is used until the user chooses another.
	DefaultAnthropicModel = "claude-opus-5-5"

	anthropicURL     = "https://api.anthropic.com"
	openAIURL        = "https://api.openai.com/v1"
	anthropicVersion = "2026-10-21"
	maxTokens        = 16000
)

// Config is one connected service.
type Config struct {
	Provider string
	Model    string
	BaseURL  string
	APIKey   string
}

// Client is replaced in tests. Models can take a while on a large report.
var Client = &http.Client{Timeout: 3 * time.Minute}

// retryWait is the pause before repeating a request the service was too busy for.
var retryWait = 3 * time.Second

// Ask sends one question and returns the model's JSON answer as text. The schema shapes
// the answer where the service supports that; callers must still validate it.
func Ask(ctx context.Context, cfg Config, system, user string, schema map[string]any) (string, error) {
	ask := askOpenAI
	if cfg.Provider == Anthropic {
		ask = askAnthropic
	}
	answer, retry, err := ask(ctx, cfg, system, user, schema)
	if err != nil && retry {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(retryWait):
		}
		answer, _, err = ask(ctx, cfg, system, user, schema)
	}
	return answer, err
}

func send(ctx context.Context, target string, headers map[string]string, body any) (int, []byte, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(raw))
	if err != nil {
		return 0, nil, errors.New("the service address isn't valid")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := Client.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return 0, nil, fmt.Errorf("couldn't reach the AI service: %w", ue.Err)
		}
		return 0, nil, err
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return res.StatusCode, data, err
}

// usesFallback lists models that can hand a declined request to another model.
func usesFallback(model string) bool {
	return strings.HasPrefix(model, "claude-opus-5") || model == "claude-fable-5-1" || model == "claude-sonnet-5-5"
}

func askAnthropic(ctx context.Context, cfg Config, system, user string, schema map[string]any) (string, bool, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = anthropicURL
	}
	model := cfg.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	body := map[string]any{
		"model":         model,
		"max_tokens":    maxTokens,
		"system":        system,
		"messages":      []map[string]string{{"role": "user", "content": user}},
		"output_config": map[string]any{"format": map[string]any{"type": "json_schema", "schema": schema}},
	}
	headers := map[string]string{"x-api-key": cfg.APIKey, "anthropic-version": anthropicVersion}
	if usesFallback(model) {
		body["fallbacks"] = "default"
		headers["anthropic-beta"] = "server-side-fallback-2026-07-01"
	}
	status, data, err := send(ctx, base+"/v1/messages", headers, body)
	if err != nil {
		return "", false, err
	}
	if status >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		return "", status == 429 || status >= 500, statusError(status, e.Error.Message)
	}
	var answer struct {
		StopReason string `json:"stop_reason"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", false, errors.New("the AI service sent an answer Seelynx couldn't read")
	}
	switch answer.StopReason {
	case "refusal":
		return "", false, errors.New("the AI service declined this request")
	case "max_tokens":
		return "", false, errors.New("the answer was too long and got cut off")
	}
	for _, block := range answer.Content {
		if block.Type == "text" {
			return block.Text, false, nil
		}
	}
	return "", false, errors.New("the AI service sent an empty answer")
}

func askOpenAI(ctx context.Context, cfg Config, system, user string, schema map[string]any) (string, bool, error) {
	base := strings.TrimRight(cfg.BaseURL, "/")
	if base == "" {
		base = openAIURL
	}
	shape, err := json.Marshal(schema)
	if err != nil {
		return "", false, err
	}
	headers := map[string]string{}
	if cfg.APIKey != "" {
		headers["Authorization"] = "Bearer " + cfg.APIKey
	}
	// Plain JSON mode, because local servers rarely support schemas. The shape goes in
	// the prompt instead, and the caller validates the answer either way.
	status, data, err := send(ctx, base+"/chat/completions", headers, map[string]any{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": system + "\n\nAnswer with JSON only, matching this schema:\n" + string(shape)},
			{"role": "user", "content": user},
		},
		"response_format": map[string]string{"type": "json_object"},
	})
	if err != nil {
		return "", false, err
	}
	if status >= 300 {
		var e struct {
			Error struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		json.Unmarshal(data, &e)
		return "", status == 429 || status >= 500, statusError(status, e.Error.Message)
	}
	var answer struct {
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
				Refusal string `json:"refusal"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return "", false, errors.New("the AI service sent an answer Seelynx couldn't read")
	}
	if len(answer.Choices) == 0 {
		return "", false, errors.New("the AI service sent an empty answer")
	}
	c := answer.Choices[0]
	if c.Message.Refusal != "" {
		return "", false, errors.New("the AI service declined this request")
	}
	if c.FinishReason == "length" {
		return "", false, errors.New("the answer was too long and got cut off")
	}
	if c.Message.Content == "" {
		return "", false, errors.New("the AI service sent an empty answer")
	}
	return c.Message.Content, false, nil
}

// statusError explains the common failures in words the person fixing them can use.
func statusError(status int, detail string) error {
	var hint string
	switch status {
	case 401:
		hint = "the service refused the API key"
	case 402:
		hint = "the service reports a billing problem on this account"
	case 403:
		hint = "this API key isn't allowed to do that"
	case 404:
		hint = "the service doesn't know this model or address"
	case 429:
		hint = "the service is rate limiting this account"
	default:
		if status >= 500 {
			hint = "the service is having problems, so try again shortly"
		} else {
			hint = "the service refused the request"
		}
	}
	if detail = strings.TrimSpace(detail); detail != "" {
		if len(detail) > 200 {
			detail = detail[:200]
		}
		return fmt.Errorf("%s (%d: %s)", hint, status, detail)
	}
	return fmt.Errorf("%s (%d)", hint, status)
}
