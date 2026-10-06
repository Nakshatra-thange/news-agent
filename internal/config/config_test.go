package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Server.Addr(), "127.0.0.1:8080"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if cfg.Server.ShutdownTimeout != DefaultShutdownTimeout {
		t.Errorf("ShutdownTimeout = %v, want %v", cfg.Server.ShutdownTimeout, DefaultShutdownTimeout)
	}
	if cfg.Log.Level != slog.LevelInfo {
		t.Errorf("Log.Level = %v, want info", cfg.Log.Level)
	}
	if cfg.Log.Format != LogFormatJSON {
		t.Errorf("Log.Format = %q, want json", cfg.Log.Format)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"SERVER_HOST":      "0.0.0.0",
		"SERVER_PORT":      "9090",
		"SHUTDOWN_TIMEOUT": "3s",
		"LOG_LEVEL":        "DEBUG",
		"LOG_FORMAT":       "Text",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Server.Addr(), "0.0.0.0:9090"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
	if cfg.Server.ShutdownTimeout != 3*time.Second {
		t.Errorf("ShutdownTimeout = %v, want 3s", cfg.Server.ShutdownTimeout)
	}
	if cfg.Log.Level != slog.LevelDebug {
		t.Errorf("Log.Level = %v, want debug", cfg.Log.Level)
	}
	if cfg.Log.Format != LogFormatText {
		t.Errorf("Log.Format = %q, want text", cfg.Log.Format)
	}
}

func TestLoadIPv6Host(t *testing.T) {
	cfg, err := Load(env(map[string]string{"SERVER_HOST": "::1"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := cfg.Server.Addr(), "[::1]:8080"; got != want {
		t.Errorf("Addr = %q, want %q", got, want)
	}
}

func TestLoadWhitespaceTreatedAsUnset(t *testing.T) {
	cfg, err := Load(env(map[string]string{"SERVER_PORT": "   "}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.Port != DefaultServerPort {
		t.Errorf("Port = %d, want default %d", cfg.Server.Port, DefaultServerPort)
	}
}

func TestLoadReportsAllErrors(t *testing.T) {
	_, err := Load(env(map[string]string{
		"SERVER_PORT":      "99999",
		"SHUTDOWN_TIMEOUT": "-1s",
		"LOG_LEVEL":        "loud",
		"LOG_FORMAT":       "xml",
	}))
	if err == nil {
		t.Fatal("Load: expected error, got nil")
	}
	for _, key := range []string{"SERVER_PORT", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error %q does not mention %s", err, key)
		}
	}
}

func TestLoadInvalidValues(t *testing.T) {
	tests := []struct {
		key, value string
	}{
		{"SERVER_PORT", "abc"},
		{"SERVER_PORT", "-1"},
		{"SHUTDOWN_TIMEOUT", "15"},
		{"SHUTDOWN_TIMEOUT", "0s"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			if _, err := Load(env(map[string]string{tt.key: tt.value})); err == nil {
				t.Errorf("expected error for %s=%q", tt.key, tt.value)
			}
		})
	}
}
