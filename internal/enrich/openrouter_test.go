package enrich

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testOpenRouterKey = "sk-or-v1-test-key-never-sent-anywhere-real"

// fakeOpenRouter serves the chat completions API locally; tests never reach
// the real API. Each request gets the next scripted reply (the last one
// repeats) and is recorded.
type fakeOpenRouterReply struct {
	status     int
	body       string
	retryAfter string
}

func fakeOpenRouter(t *testing.T, replies ...fakeOpenRouterReply) (*httptest.Server, *capturedRequest, *atomic.Int32) {
	t.Helper()
	got := &capturedRequest{}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1)) - 1
		rep := replies[min(n, len(replies)-1)]
		got.path, got.header = r.URL.Path, r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		got.body = nil
		_ = json.Unmarshal(body, &got.body)
		w.Header().Set("Content-Type", "application/json")
		if rep.retryAfter != "" {
			w.Header().Set("Retry-After", rep.retryAfter)
		}
		w.WriteHeader(rep.status)
		_, _ = io.WriteString(w, rep.body)
	}))
	t.Cleanup(srv.Close)
	return srv, got, &calls
}

func completion(finish, content string) string {
	return `{"id":"gen-1","object":"chat.completion","model":"nvidia/nemotron-3.5-lightning:free",
		"choices":[{"index":0,"finish_reason":"` + finish + `","message":{"role":"assistant","content":` + content + `}}]}`
}

func TestOpenRouterProviderRequestAndReply(t *testing.T) {
	srv, got, _ := fakeOpenRouter(t, fakeOpenRouterReply{200, completion("stop", `"A lab released an open model. The authors report faster inference."`), ""})
	p := NewOpenRouterProvider(testOpenRouterKey, "", srv.URL)
	if p.Name() != "openrouter" || p.Model() != DefaultOpenRouterModel {
		t.Errorf("provider = %s/%s", p.Name(), p.Model())
	}

	out, err := p.Complete(context.Background(), Prompt{System: "SYSTEM TEXT", User: "<item>\nTitle: X\n</item>"})
	if err != nil || out != "A lab released an open model. The authors report faster inference." {
		t.Fatalf("Complete = %q, %v", out, err)
	}
	if got.header.Get("Authorization") != "Bearer "+testOpenRouterKey || got.header.Get("Content-Type") != "application/json" {
		t.Errorf("headers = %v", got.header)
	}
	b := got.body
	if b["model"] != DefaultOpenRouterModel || b["max_tokens"] != float64(openRouterMaxTokens) || b["stream"] != nil {
		t.Errorf("body = %v", b)
	}
	if r, _ := b["reasoning"].(map[string]any); r["enabled"] != false {
		t.Errorf("reasoning = %v, want disabled", b["reasoning"])
	}
	msgs, _ := b["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("messages = %v", b["messages"])
	}
	sys, _ := msgs[0].(map[string]any)
	user, _ := msgs[1].(map[string]any)
	if sys["role"] != "system" || sys["content"] != "SYSTEM TEXT" || user["role"] != "user" || user["content"] != "<item>\nTitle: X\n</item>" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestOpenRouterProviderModelOverride(t *testing.T) {
	srv, got, _ := fakeOpenRouter(t, fakeOpenRouterReply{200, completion("stop", `"ok text"`), ""})
	p := NewOpenRouterProvider(testOpenRouterKey, "meta-llama/llama-4-scout:free", srv.URL)
	if _, err := p.Complete(context.Background(), Prompt{System: "s", User: "u"}); err != nil {
		t.Fatal(err)
	}
	if p.Model() != "meta-llama/llama-4-scout:free" || got.body["model"] != "meta-llama/llama-4-scout:free" {
		t.Errorf("model = %s / %v", p.Model(), got.body["model"])
	}
}

// noBackoff removes the rate-limit backoff for the test.
func noBackoff(t *testing.T) {
	t.Helper()
	saved := openRouterBackoff
	openRouterBackoff = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { openRouterBackoff = saved })
}

func TestOpenRouterProviderFailures(t *testing.T) {
	noBackoff(t)
	apiErr := func(code int, msg string) string {
		return `{"error":{"code":` + strconv.Itoa(code) + `,"message":"` + msg + `"}}`
	}
	tests := []struct {
		name    string
		reply   fakeOpenRouterReply
		want    string
		limited bool
	}{
		{"bad key", fakeOpenRouterReply{401, apiErr(401, "No auth credentials found"), ""}, "HTTP 401: No auth credentials found", false},
		{"no credits", fakeOpenRouterReply{402, apiErr(402, "Insufficient credits"), ""}, "HTTP 402", false},
		{"rate limited", fakeOpenRouterReply{429, apiErr(429, "Rate limit exceeded: free-models-per-day"), ""}, "free-models-per-day", true},
		{"rate limited, long wait", fakeOpenRouterReply{429, apiErr(429, "slow down"), "3600"}, "HTTP 429", true},
		{"upstream pool", fakeOpenRouterReply{429, `{"error":{"code":429,"message":"Provider returned error","metadata":{"raw":"google/gemma-4-31b-it:free is temporarily rate-limited upstream."}}}`, ""}, "temporarily rate-limited upstream", true},
		{"no provider", fakeOpenRouterReply{503, apiErr(503, "No available provider"), ""}, "HTTP 503", true},
		{"cut off", fakeOpenRouterReply{200, completion("length", `"A paper about"`), ""}, "cut off", false},
		{"filtered", fakeOpenRouterReply{200, completion("content_filter", `""`), ""}, "content filter", false},
		{"empty", fakeOpenRouterReply{200, completion("stop", `"   "`), ""}, "no text", false},
		{"null content", fakeOpenRouterReply{200, completion("stop", `null`), ""}, "no text", false},
		{"no choices", fakeOpenRouterReply{200, `{"choices":[]}`, ""}, "no choices", false},
		{"error in 200", fakeOpenRouterReply{200, apiErr(502, "upstream failed"), ""}, "upstream failed", false},
		{"malformed", fakeOpenRouterReply{200, `not json`, ""}, "malformed", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv, _, _ := fakeOpenRouter(t, tt.reply)
			_, err := NewOpenRouterProvider(testOpenRouterKey, "", srv.URL).Complete(context.Background(), Prompt{System: "s", User: "u"})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.want)
			}
			if errors.Is(err, ErrRateLimited) != tt.limited {
				t.Errorf("errors.Is(err, ErrRateLimited) = %v, want %v", !tt.limited, tt.limited)
			}
			if strings.Contains(err.Error(), testOpenRouterKey) {
				t.Error("the API key appears in the error")
			}
		})
	}
}

func TestOpenRouterProviderRetriesShortRateLimitOnce(t *testing.T) {
	srv, _, calls := fakeOpenRouter(t,
		fakeOpenRouterReply{429, `{"error":{"code":429,"message":"wait"}}`, "1"},
		fakeOpenRouterReply{200, completion("stop", `"Second time lucky, a summary of the item."`), ""})
	out, err := NewOpenRouterProvider(testOpenRouterKey, "", srv.URL).Complete(context.Background(), Prompt{System: "s", User: "u"})
	if err != nil || out != "Second time lucky, a summary of the item." || calls.Load() != 2 {
		t.Fatalf("Complete = %q, %v after %d calls; want success on the retry", out, err, calls.Load())
	}
}

func TestOpenRouterProviderBacksOffWithoutRetryAfter(t *testing.T) {
	noBackoff(t)
	busy := fakeOpenRouterReply{429, `{"error":{"code":429,"message":"busy"}}`, ""}
	srv, _, calls := fakeOpenRouter(t, busy, busy, fakeOpenRouterReply{200, completion("stop", `"Third time lucky, a summary of the item."`), ""})
	out, err := NewOpenRouterProvider(testOpenRouterKey, "", srv.URL).Complete(context.Background(), Prompt{System: "s", User: "u"})
	if err != nil || calls.Load() != 3 {
		t.Fatalf("Complete = %q, %v after %d calls; want success on the third", out, err, calls.Load())
	}

	srv, _, calls = fakeOpenRouter(t, busy)
	_, err = NewOpenRouterProvider(testOpenRouterKey, "", srv.URL).Complete(context.Background(), Prompt{System: "s", User: "u"})
	if !errors.Is(err, ErrRateLimited) || calls.Load() != int32(1+len(openRouterBackoff)) {
		t.Errorf("error = %v after %d calls; want ErrRateLimited after %d", err, calls.Load(), 1+len(openRouterBackoff))
	}
}

func TestOpenRouterProviderHonorsCancellation(t *testing.T) {
	srv, _, _ := fakeOpenRouter(t, fakeOpenRouterReply{429, `{"error":{"code":429,"message":"wait"}}`, "15"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewOpenRouterProvider(testOpenRouterKey, "", srv.URL).Complete(ctx, Prompt{System: "s", User: "u"}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}
