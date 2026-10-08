package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"synergy/internal/config"
)

func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRunVersionAndHelp(t *testing.T) {
	var out bytes.Buffer
	if err := run(context.Background(), []string{"version"}, envMap(nil), &out, io.Discard); err != nil {
		t.Fatalf("version: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != version {
		t.Errorf("version output = %q, want %q", got, version)
	}

	out.Reset()
	if err := run(context.Background(), []string{"help"}, envMap(nil), &out, io.Discard); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(out.String(), "serve") {
		t.Errorf("help output does not list commands: %q", out.String())
	}
}

func TestRunUsageErrors(t *testing.T) {
	for _, args := range [][]string{nil, {"bogus"}} {
		var stderr bytes.Buffer
		err := run(context.Background(), args, envMap(nil), io.Discard, &stderr)
		if !errors.Is(err, errUsage) {
			t.Errorf("run(%q) error = %v, want errUsage", args, err)
		}
		if !strings.Contains(stderr.String(), "Usage:") {
			t.Errorf("run(%q) did not print usage", args)
		}
	}
}

func TestServeRejectsInvalidConfig(t *testing.T) {
	err := run(context.Background(), []string{"serve"}, envMap(map[string]string{"LOG_LEVEL": "loud"}), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "LOG_LEVEL") {
		t.Fatalf("error = %v, want a LOG_LEVEL config error", err)
	}
}

func TestMigrateUsage(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"migrate", "sideways"}, "unknown migrate command"},
		{[]string{"migrate", "down"}, "--yes"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		err := run(context.Background(), tt.args, envMap(nil), io.Discard, &stderr)
		if !errors.Is(err, errUsage) {
			t.Errorf("run(%q) error = %v, want errUsage", tt.args, err)
		}
		if !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q) stderr = %q, want it to mention %q", tt.args, stderr.String(), tt.want)
		}
	}
}

func TestSourceTypesWireIntoRegistry(t *testing.T) {
	if _, err := newRegistry(nil, nil); err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	if n := len(sourceTypes()); n != 3 {
		t.Errorf("source types = %d, want 3", n)
	}
}

func TestFetchUsage(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"fetch"}, "specify source"},
		{[]string{"fetch", "--all", "hn-ai"}, "not both"},
		{[]string{"fetch", "--bogus"}, "unknown flag"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		err := run(context.Background(), tt.args, envMap(nil), io.Discard, &stderr)
		if !errors.Is(err, errUsage) || !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q): err=%v stderr=%q, want usage error mentioning %q", tt.args, err, stderr.String(), tt.want)
		}
	}
}

func TestEnrichUsage(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"enrich"}, "no LLM provider is configured"},
		{[]string{"enrich", "--fake", "--limit", "0"}, "--limit must be between 1 and 100"},
		{[]string{"enrich", "--fake", "--limit", "101"}, "--limit must be between 1 and 100"},
		{[]string{"enrich", "--fake", "--limit"}, "unknown argument"},
		{[]string{"enrich", "--all"}, "unknown argument"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		err := run(context.Background(), tt.args, envMap(nil), io.Discard, &stderr)
		if !errors.Is(err, errUsage) || !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q): err=%v stderr=%q, want usage error mentioning %q", tt.args, err, stderr.String(), tt.want)
		}
	}
}

func TestSummarizeUsage(t *testing.T) {
	db := map[string]string{"DATABASE_URL": "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable"}
	tests := []struct {
		args []string
		env  map[string]string
		want string
	}{
		{[]string{"summarize"}, db, "no LLM provider is configured"},
		{[]string{"summarize", "--limit", "0", "--fake"}, nil, "--limit must be between 1 and 20"},
		{[]string{"summarize", "--limit", "21", "--fake"}, nil, "--limit must be between 1 and 20"},
		{[]string{"summarize", "--limit", "many"}, nil, "--limit must be between 1 and 20"},
		{[]string{"summarize", "--limit"}, nil, "unknown argument"},
		{[]string{"summarize", "--all"}, nil, "unknown argument"},
	}
	for _, tt := range tests {
		var stderr bytes.Buffer
		err := run(context.Background(), tt.args, envMap(tt.env), io.Discard, &stderr)
		if !errors.Is(err, errUsage) || !strings.Contains(stderr.String(), tt.want) {
			t.Errorf("run(%q): err=%v stderr=%q, want usage error mentioning %q", tt.args, err, stderr.String(), tt.want)
		}
	}
	var out bytes.Buffer
	if err := run(context.Background(), []string{"summarize", "--help"}, envMap(nil), &out, io.Discard); err != nil ||
		!strings.Contains(out.String(), "default 3, max 20") {
		t.Errorf("summarize --help: %v %q", err, out.String())
	}
}

func TestSeedRequiresDatabase(t *testing.T) {
	env := envMap(map[string]string{
		"DATABASE_URL":       "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable",
		"DB_CONNECT_TIMEOUT": "1s",
	})
	err := run(context.Background(), []string{"seed"}, env, io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "cannot reach PostgreSQL") {
		t.Fatalf("error = %v, want unreachable database error", err)
	}
}

func TestMigrateRequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), []string{"migrate", "status"}, envMap(nil), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("error = %v, want DATABASE_URL error", err)
	}
}

func TestMigrateUnreachableDatabase(t *testing.T) {
	env := envMap(map[string]string{
		"DATABASE_URL":       "postgres://nobody:hunter2@127.0.0.1:1/none?sslmode=disable",
		"DB_CONNECT_TIMEOUT": "1s",
	})
	var logs bytes.Buffer
	err := run(context.Background(), []string{"migrate", "up"}, env, io.Discard, &logs)
	if err == nil || !strings.Contains(err.Error(), "cannot reach PostgreSQL") {
		t.Fatalf("error = %v, want unreachable database error", err)
	}
	if strings.Contains(err.Error()+logs.String(), "hunter2") {
		t.Error("password leaked into error or logs")
	}
}

// TestServeLifecycle starts the real server on an ephemeral port, calls
// /health over TCP, and checks that cancellation triggers a clean shutdown.
func TestServeLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logR, logW := io.Pipe()
	// Port 1 on loopback refuses connections: the server must still start and
	// report the database as unavailable rather than exit.
	env := envMap(map[string]string{
		"SERVER_PORT":        "0",
		"SHUTDOWN_TIMEOUT":   "5s",
		"DATABASE_URL":       "postgres://nobody:pw@127.0.0.1:1/none?sslmode=disable",
		"DB_CONNECT_TIMEOUT": "1s",
	})
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"serve"}, env, io.Discard, logW)
		logW.Close()
	}()

	logs := bufio.NewScanner(logR)
	addr := waitForStartup(t, logs)
	go func() { _, _ = io.Copy(io.Discard, logR) }() // keep draining so the server never blocks on logging

	resp, err := http.Get("http://" + addr + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /health status = %d, want 200", resp.StatusCode)
	}

	resp, err = http.Get("http://" + addr + "/health/db")
	if err != nil {
		t.Fatalf("GET /health/db: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("GET /health/db status = %d, want 503 with the database down", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v, want nil after graceful shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server did not shut down within 10s")
	}
}

func waitForStartup(t *testing.T, logs *bufio.Scanner) string {
	t.Helper()
	found := make(chan string, 1)
	go func() {
		for logs.Scan() {
			var entry struct {
				Msg  string `json:"msg"`
				Addr string `json:"addr"`
			}
			if json.Unmarshal(logs.Bytes(), &entry) == nil && entry.Msg == "server started" {
				found <- entry.Addr
				return
			}
		}
	}()
	select {
	case addr := <-found:
		return addr
	case <-time.After(5 * time.Second):
		t.Fatal("server did not log startup within 5s")
		return ""
	}
}

// Every registered source type must have exactly one fetch adapter, so a
// type cannot be seeded and then fail every fetch with "unsupported".
func TestEverySourceTypeHasAnAdapter(t *testing.T) {
	got := map[string]int{}
	for _, a := range adapters(config.IngestConfig{}, nil) {
		got[string(a.Type())]++
	}
	for _, s := range sourceTypes() {
		if got[string(s.Type())] != 1 {
			t.Errorf("source type %s has %d adapters, want 1", s.Type(), got[string(s.Type())])
		}
	}
	if len(got) != len(sourceTypes()) {
		t.Errorf("adapters %v do not match source types", got)
	}
}

func TestIsLoopback(t *testing.T) {
	for _, tt := range []struct {
		ip   string
		want bool
	}{
		{"127.0.0.1", true}, {"::1", true}, {"0.0.0.0", false}, {"::", false}, {"192.168.1.10", false},
	} {
		if got := isLoopback(&net.TCPAddr{IP: net.ParseIP(tt.ip), Port: 8080}); got != tt.want {
			t.Errorf("isLoopback(%s) = %v, want %v", tt.ip, got, tt.want)
		}
	}
}
