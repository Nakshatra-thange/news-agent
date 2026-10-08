package enrich

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/option"
)

const testKey = "sk-ant-test-key-never-sent-anywhere-real"

// fakeAnthropic serves the Messages API locally; tests never reach the real
// API. It records the last request and replies with the given status/body.
func fakeAnthropic(t *testing.T, status int, reply string) (*httptest.Server, *capturedRequest) {
	t.Helper()
	got := &capturedRequest{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.path, got.header = r.URL.Path, r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("request-id", "req_test123")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, got
}

type capturedRequest struct {
	path   string
	header http.Header
	body   map[string]any
}

func testProvider(srv *httptest.Server) *AnthropicProvider {
	return NewAnthropicProvider(testKey, "", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
}

func message(stopReason string, blocks string) string {
	return `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5",
		"content":[` + blocks + `],"stop_reason":"` + stopReason + `","usage":{"input_tokens":10,"output_tokens":5}}`
}

func TestAnthropicProviderRequestAndReply(t *testing.T) {
	srv, got := fakeAnthropic(t, 200, message("end_turn",
		`{"type":"thinking","thinking":"","signature":"x"},{"type":"text","text":"A paper about agents "},{"type":"text","text":"at scale."}`))
	p := testProvider(srv)
	if p.Name() != "anthropic" || p.Model() != DefaultAnthropicModel {
		t.Errorf("provider = %s/%s", p.Name(), p.Model())
	}

	out, err := p.Complete(context.Background(), Prompt{System: "SYSTEM TEXT", User: "<item>\nTitle: X\n</item>"})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "A paper about agents at scale." {
		t.Errorf("text = %q, want the text blocks joined (thinking ignored)", out)
	}

	if got.path != "/v1/messages" {
		t.Errorf("path = %q", got.path)
	}
	if got.header.Get("X-Api-Key") != testKey || got.header.Get("Anthropic-Version") == "" {
		t.Errorf("auth headers missing: key set=%v version=%q", got.header.Get("X-Api-Key") != "", got.header.Get("Anthropic-Version"))
	}
	if !strings.Contains(got.header.Get("Anthropic-Beta"), "server-side-fallback-2026-07-01") {
		t.Errorf("anthropic-beta = %q, want the server-side fallback beta", got.header.Get("Anthropic-Beta"))
	}
	b := got.body
	if b["model"] != DefaultAnthropicModel || b["fallbacks"] != "default" || b["max_tokens"] != float64(anthropicMaxTokens) {
		t.Errorf("body model/fallbacks/max_tokens = %v/%v/%v", b["model"], b["fallbacks"], b["max_tokens"])
	}
	if oc, _ := b["output_config"].(map[string]any); oc["effort"] != "low" {
		t.Errorf("output_config = %v, want effort low", b["output_config"])
	}
	if sys, _ := json.Marshal(b["system"]); !strings.Contains(string(sys), "SYSTEM TEXT") {
		t.Errorf("system = %s", sys)
	}
	if msgs, _ := json.Marshal(b["messages"]); !strings.Contains(string(msgs), `"role":"user"`) || !strings.Contains(string(msgs), "Title: X") {
		t.Errorf("messages = %s", msgs)
	}
}

func TestAnthropicProviderFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		reply  string
		want   string
	}{
		{"refusal", 200, message("refusal", `{"type":"text","text":"partial"}`), "declined"},
		{"cut off", 200, message("max_tokens", `{"type":"text","text":"A paper about"}`), "cut off"},
		{"no text", 200, message("end_turn", ``), "no text"},
		{"bad key", 401, `{"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`, "HTTP 401 (request req_test123)"},
		{"overloaded", 529, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, "HTTP 529"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := fakeAnthropic(t, tt.status, tt.reply)
			_, err := testProvider(srv).Complete(context.Background(), Prompt{System: "s", User: "u"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
			if strings.Contains(err.Error(), testKey) {
				t.Error("the API key appears in the error")
			}
		})
	}
}

func TestAnthropicProviderModelOverride(t *testing.T) {
	srv, got := fakeAnthropic(t, 200, message("end_turn", `{"type":"text","text":"ok"}`))
	p := NewAnthropicProvider(testKey, "claude-sonnet-5-5", option.WithBaseURL(srv.URL), option.WithMaxRetries(0))
	if _, err := p.Complete(context.Background(), Prompt{System: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if p.Model() != "claude-sonnet-5-5" || got.body["model"] != "claude-sonnet-5-5" {
		t.Errorf("model = %s / %v", p.Model(), got.body["model"])
	}
}
