package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) (*Server, *bytes.Buffer) {
	t.Helper()
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return New(Options{Logger: logger, Version: "test"}), &logs
}

func do(t *testing.T, h http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", ct)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealth(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/health", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := decode[healthResponse](t, rec)
	if body.Status != "ok" || body.Version != "test" {
		t.Errorf("body = %+v, want status ok and version test", body)
	}
}

func TestUnknownRouteReturnsJSON404(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/nope", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if body := decode[errorBody](t, rec); body.Error.Code != codeNotFound {
		t.Errorf("code = %q, want %q", body.Error.Code, codeNotFound)
	}
}

func TestWrongMethodReturnsJSON405(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodPost, "/health", nil)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want it to include GET", allow)
	}
	if body := decode[errorBody](t, rec); body.Error.Code != codeMethodNotAllowed {
		t.Errorf("code = %q, want %q", body.Error.Code, codeMethodNotAllowed)
	}
}

func TestRequestIDGenerated(t *testing.T) {
	s, _ := newTestServer(t)
	rec := do(t, s, http.MethodGet, "/health", nil)

	if id := rec.Header().Get(headerRequestID); len(id) != 24 {
		t.Errorf("generated request ID = %q, want 24 hex chars", id)
	}
}

func TestRequestIDPropagation(t *testing.T) {
	tests := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"valid id is kept", "abc-123_X.y", true},
		{"header injection is replaced", "abc\r\nevil: 1", false},
		{"too long is replaced", strings.Repeat("a", 65), false},
		{"spaces are replaced", "a b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			rec := do(t, s, http.MethodGet, "/health", http.Header{headerRequestID: {tt.incoming}})
			got := rec.Header().Get(headerRequestID)
			if tt.keep && got != tt.incoming {
				t.Errorf("request ID = %q, want %q", got, tt.incoming)
			}
			if !tt.keep && (got == tt.incoming || got == "") {
				t.Errorf("request ID = %q, want a freshly generated ID", got)
			}
		})
	}
}

func TestPanicRecovery(t *testing.T) {
	s, logs := newTestServer(t)
	s.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("kaboom") })

	rec := do(t, s, http.MethodGet, "/boom", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if body := decode[errorBody](t, rec); body.Error.Code != codeInternal {
		t.Errorf("code = %q, want %q", body.Error.Code, codeInternal)
	}
	if strings.Contains(rec.Body.String(), "kaboom") {
		t.Error("panic value leaked into response body")
	}
	if !strings.Contains(logs.String(), "kaboom") {
		t.Error("panic value was not logged")
	}
}

func TestAccessLog(t *testing.T) {
	s, logs := newTestServer(t)
	do(t, s, http.MethodGet, "/nope", http.Header{headerRequestID: {"req-1"}})

	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("decode log line %q: %v", logs.String(), err)
	}
	want := map[string]any{
		"msg": "http request", "level": "INFO", "request_id": "req-1",
		"method": "GET", "path": "/nope", "status": float64(404),
	}
	for k, v := range want {
		if entry[k] != v {
			t.Errorf("log[%q] = %v, want %v", k, entry[k], v)
		}
	}
}

func TestHealthProbesLogAtDebug(t *testing.T) {
	s, logs := newTestServer(t)
	do(t, s, http.MethodGet, "/health", nil)

	if !strings.Contains(logs.String(), `"level":"DEBUG"`) {
		t.Errorf("health probe log = %q, want DEBUG level", logs.String())
	}
}
