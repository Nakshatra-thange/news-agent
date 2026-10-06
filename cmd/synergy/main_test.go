package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
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

// TestServeLifecycle starts the real server on an ephemeral port, calls
// /health over TCP, and checks that cancellation triggers a clean shutdown.
func TestServeLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logR, logW := io.Pipe()
	env := envMap(map[string]string{"SERVER_PORT": "0", "SHUTDOWN_TIMEOUT": "5s"})
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
