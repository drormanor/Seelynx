package ai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var shape = map[string]any{"type": "object"}

type seen struct {
	path    string
	headers http.Header
	body    map[string]any
}

func service(t *testing.T, status int, answer string) (*httptest.Server, *seen) {
	got := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got.path, got.headers = r.URL.Path, r.Header
		json.Unmarshal(raw, &got.body)
		w.WriteHeader(status)
		io.WriteString(w, answer)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

func TestAnthropicRequestAndAnswer(t *testing.T) {
	srv, got := service(t, 200, `{"stop_reason":"end_turn","content":[{"type":"thinking","thinking":""},{"type":"text","text":"{\"ok\":true}"}]}`)
	out, err := Ask(context.Background(), Config{Provider: Anthropic, BaseURL: srv.URL, APIKey: "key-1"}, "sys", "hello", shape)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("%q %v", out, err)
	}
	if got.path != "/v1/messages" || got.headers.Get("x-api-key") != "key-1" || got.headers.Get("anthropic-version") != anthropicVersion {
		t.Fatalf("%s %v", got.path, got.headers)
	}
	if got.body["model"] != DefaultAnthropicModel || got.body["system"] != "sys" || got.body["fallbacks"] != "default" {
		t.Fatalf("%v", got.body)
	}
	if got.headers.Get("anthropic-beta") != "server-side-fallback-2026-07-01" {
		t.Fatal("fallback beta missing")
	}
	format := got.body["output_config"].(map[string]any)["format"].(map[string]any)
	if format["type"] != "json_schema" || format["schema"] == nil {
		t.Fatalf("%v", format)
	}
	if _, sent := got.body["thinking"]; sent {
		t.Fatal("thinking must not be configured")
	}
}

func TestFallbackOnlyForModelsThatHaveIt(t *testing.T) {
	srv, got := service(t, 200, `{"stop_reason":"end_turn","content":[{"type":"text","text":"{}"}]}`)
	if _, err := Ask(context.Background(), Config{Provider: Anthropic, Model: "claude-haiku-5-5", BaseURL: srv.URL}, "s", "u", shape); err != nil {
		t.Fatal(err)
	}
	if _, sent := got.body["fallbacks"]; sent || got.headers.Get("anthropic-beta") != "" {
		t.Fatalf("%v", got.body)
	}
}

func TestOpenAIRequestAndAnswer(t *testing.T) {
	srv, got := service(t, 200, `{"choices":[{"finish_reason":"stop","message":{"content":"{\"ok\":true}"}}]}`)
	out, err := Ask(context.Background(), Config{Provider: OpenAI, Model: "local-model", BaseURL: srv.URL + "/v1/", APIKey: "key-2"}, "sys", "hello", shape)
	if err != nil || out != `{"ok":true}` {
		t.Fatalf("%q %v", out, err)
	}
	if got.path != "/v1/chat/completions" || got.headers.Get("Authorization") != "Bearer key-2" || got.body["model"] != "local-model" {
		t.Fatalf("%s %v", got.path, got.body)
	}
}

func TestLocalServerNeedsNoKey(t *testing.T) {
	srv, got := service(t, 200, `{"choices":[{"message":{"content":"{}"}}]}`)
	if _, err := Ask(context.Background(), Config{Provider: OpenAI, Model: "m", BaseURL: srv.URL}, "s", "u", shape); err != nil {
		t.Fatal(err)
	}
	if got.headers.Get("Authorization") != "" {
		t.Fatal("sent an empty key")
	}
}

func TestStopReasonsAreReported(t *testing.T) {
	for reason, want := range map[string]string{"refusal": "declined", "max_tokens": "cut off"} {
		srv, _ := service(t, 200, `{"stop_reason":"`+reason+`","content":[{"type":"text","text":"{"}]}`)
		_, err := Ask(context.Background(), Config{Provider: Anthropic, BaseURL: srv.URL}, "s", "u", shape)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", reason, err)
		}
	}
}

func TestErrorsExplainWithoutLeakingTheKey(t *testing.T) {
	for status, want := range map[int]string{401: "API key", 402: "billing", 404: "model", 400: "refused the request"} {
		srv, _ := service(t, status, `{"type":"error","error":{"type":"x","message":"details here"}}`)
		_, err := Ask(context.Background(), Config{Provider: Anthropic, BaseURL: srv.URL, APIKey: "secret-key"}, "s", "u", shape)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "details here") || strings.Contains(err.Error(), "secret-key") {
			t.Errorf("%d: %v", status, err)
		}
	}
	_, err := Ask(context.Background(), Config{Provider: OpenAI, Model: "m", BaseURL: "http://127.0.0.1:1/v1", APIKey: "secret-key"}, "s", "u", shape)
	if err == nil || strings.Contains(err.Error(), "secret-key") || strings.Contains(err.Error(), "127.0.0.1:1/v1/chat") {
		t.Errorf("%v", err)
	}
}

func TestBusyServiceIsRetriedOnce(t *testing.T) {
	saved := retryWait
	retryWait = time.Millisecond
	defer func() { retryWait = saved }()
	for status, want := range map[int]int32{429: 2, 529: 2, 401: 1} {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if calls.Add(1) == 1 {
				w.WriteHeader(status)
				return
			}
			io.WriteString(w, `{"stop_reason":"end_turn","content":[{"type":"text","text":"{}"}]}`)
		}))
		Ask(context.Background(), Config{Provider: Anthropic, BaseURL: srv.URL}, "s", "u", shape)
		srv.Close()
		if calls.Load() != want {
			t.Errorf("%d: %d calls", status, calls.Load())
		}
	}
}
