package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

const testDBURL = "postgres://synergy:secret-pw@localhost:5432/synergy?sslmode=disable"

// env returns a getenv with a valid DATABASE_URL plus the given overrides.
func env(m map[string]string) func(string) string {
	vars := map[string]string{"DATABASE_URL": testDBURL}
	for k, v := range m {
		vars[k] = v
	}
	return func(k string) string { return vars[k] }
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
	db := cfg.Database
	if db.URL != testDBURL || db.MaxConns != DefaultDBMaxConns || db.MinConns != DefaultDBMinConns ||
		db.MaxConnLifetime != DefaultDBMaxConnLifetime || db.MaxConnIdleTime != DefaultDBMaxConnIdleTime ||
		db.ConnectTimeout != DefaultDBConnectTimeout {
		t.Errorf("Database = %+v, want defaults", db)
	}
	if cfg.Ingest.FetchTimeout != DefaultFetchTimeout {
		t.Errorf("FetchTimeout = %v, want %v", cfg.Ingest.FetchTimeout, DefaultFetchTimeout)
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
		{"DATABASE_URL", ""},
		{"DATABASE_URL", "mysql://localhost/db"},
		{"DB_MAX_CONNS", "0"},
		{"DB_MAX_CONNS", "many"},
		{"DB_MIN_CONNS", "-1"},
		{"DB_CONNECT_TIMEOUT", "never"},
		{"FETCH_TIMEOUT", "0s"},
		{"FETCH_TIMEOUT", "soon"},
	}
	for _, tt := range tests {
		t.Run(tt.key+"="+tt.value, func(t *testing.T) {
			if _, err := Load(env(map[string]string{tt.key: tt.value})); err == nil {
				t.Errorf("expected error for %s=%q", tt.key, tt.value)
			}
		})
	}
}

func TestLoadDatabaseOverrides(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		"DATABASE_URL":          "postgresql://u@db:5432/x",
		"DB_MAX_CONNS":          "4",
		"DB_MIN_CONNS":          "2",
		"DB_MAX_CONN_LIFETIME":  "10m",
		"DB_MAX_CONN_IDLE_TIME": "1m",
		"DB_CONNECT_TIMEOUT":    "2s",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	db := cfg.Database
	if db.URL != "postgresql://u@db:5432/x" || db.MaxConns != 4 || db.MinConns != 2 ||
		db.MaxConnLifetime != 10*time.Minute || db.MaxConnIdleTime != time.Minute || db.ConnectTimeout != 2*time.Second {
		t.Errorf("Database = %+v", db)
	}
}

func TestLoadMinConnsAboveMax(t *testing.T) {
	_, err := Load(env(map[string]string{"DB_MAX_CONNS": "2", "DB_MIN_CONNS": "5"}))
	if err == nil || !strings.Contains(err.Error(), "DB_MIN_CONNS") {
		t.Fatalf("error = %v, want DB_MIN_CONNS error", err)
	}
}

func TestDatabaseURLNeverLeaks(t *testing.T) {
	// Invalid URL errors must not echo the value.
	_, err := Load(env(map[string]string{"DATABASE_URL": "mysql://u:hunter2@h/db"}))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error = %v, want an error without the password", err)
	}

	// Logging the config redacts the URL.
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var buf strings.Builder
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("cfg", "db", cfg.Database)
	if strings.Contains(buf.String(), "secret-pw") {
		t.Errorf("logged config leaked the password: %s", buf.String())
	}
}
