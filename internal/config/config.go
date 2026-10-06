// Package config loads and validates Synergy's runtime configuration from
// environment variables. All validation errors are reported together so a
// misconfigured deployment fails fast with a complete explanation.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config is the fully validated runtime configuration.
type Config struct {
	Server ServerConfig
	Log    LogConfig
}

// ServerConfig controls the HTTP API listener.
type ServerConfig struct {
	Host            string
	Port            int
	ShutdownTimeout time.Duration
}

// Addr returns the host:port the server should listen on.
func (s ServerConfig) Addr() string {
	return net.JoinHostPort(s.Host, strconv.Itoa(s.Port))
}

// LogFormat selects the structured log encoding.
type LogFormat string

const (
	LogFormatJSON LogFormat = "json"
	LogFormatText LogFormat = "text"
)

// LogConfig controls structured logging.
type LogConfig struct {
	Level  slog.Level
	Format LogFormat
}

// Defaults. The server binds to loopback by default because Phase 1 has no
// authentication and is intended for local, single-user use.
const (
	DefaultServerHost      = "127.0.0.1"
	DefaultServerPort      = 8080
	DefaultShutdownTimeout = 15 * time.Second
	DefaultLogLevel        = "info"
	DefaultLogFormat       = LogFormatJSON
)

// Load reads configuration using getenv (typically os.Getenv). Unset or empty
// variables fall back to defaults.
func Load(getenv func(string) string) (Config, error) {
	l := loader{getenv: getenv}

	cfg := Config{
		Server: ServerConfig{
			Host:            l.str("SERVER_HOST", DefaultServerHost),
			Port:            l.port("SERVER_PORT", DefaultServerPort),
			ShutdownTimeout: l.duration("SHUTDOWN_TIMEOUT", DefaultShutdownTimeout),
		},
		Log: LogConfig{
			Level:  l.logLevel("LOG_LEVEL", DefaultLogLevel),
			Format: l.logFormat("LOG_FORMAT", DefaultLogFormat),
		},
	}

	if err := errors.Join(l.errs...); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

type loader struct {
	getenv func(string) string
	errs   []error
}

func (l *loader) lookup(key string) (string, bool) {
	v := strings.TrimSpace(l.getenv(key))
	return v, v != ""
}

func (l *loader) fail(key, format string, args ...any) {
	l.errs = append(l.errs, fmt.Errorf("%s: %s", key, fmt.Sprintf(format, args...)))
}

func (l *loader) str(key, def string) string {
	if v, ok := l.lookup(key); ok {
		return v
	}
	return def
}

func (l *loader) port(key string, def int) int {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 65535 {
		l.fail(key, "must be an integer between 0 and 65535, got %q", v)
		return def
	}
	return n
}

func (l *loader) duration(key string, def time.Duration) time.Duration {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		l.fail(key, "must be a positive duration such as 15s or 1m, got %q", v)
		return def
	}
	return d
}

func (l *loader) logLevel(key, def string) slog.Level {
	v := l.str(key, def)
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(v)); err != nil {
		l.fail(key, "must be one of debug, info, warn, error, got %q", v)
		return slog.LevelInfo
	}
	return lvl
}

func (l *loader) logFormat(key string, def LogFormat) LogFormat {
	v := LogFormat(strings.ToLower(l.str(key, string(def))))
	switch v {
	case LogFormatJSON, LogFormatText:
		return v
	default:
		l.fail(key, "must be json or text, got %q", v)
		return def
	}
}
