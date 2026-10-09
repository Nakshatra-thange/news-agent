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
	Server    ServerConfig
	Log       LogConfig
	Database  DatabaseConfig
	Ingest    IngestConfig
	Scheduler SchedulerConfig
	LLM       LLMConfig
	Embedding EmbeddingConfig
}

// EmbeddingConfig configures the embedding provider used by `synergy embed`.
type EmbeddingConfig struct {
	// VoyageAPIKey is optional; without it only the offline fake provider
	// is available. It is a credential and must never be logged.
	VoyageAPIKey string
	// VoyageModel is the Voyage AI embedding model ID.
	VoyageModel string
}

// LogValue keeps the API key out of logs.
func (c EmbeddingConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("voyage_api_key_set", c.VoyageAPIKey != ""),
		slog.String("voyage_model", c.VoyageModel),
	)
}

// LLMConfig configures the LLM provider used by `synergy summarize`.
type LLMConfig struct {
	// AnthropicAPIKey is optional; without it only the offline fake
	// provider is available. It is a credential and must never be logged.
	AnthropicAPIKey string
	// AnthropicModel is the Claude model ID.
	AnthropicModel string
}

// LogValue keeps the API key out of logs.
func (c LLMConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Bool("anthropic_api_key_set", c.AnthropicAPIKey != ""),
		slog.String("anthropic_model", c.AnthropicModel),
	)
}

// SchedulerConfig controls automatic fetching inside `synergy serve`.
type SchedulerConfig struct {
	// Enabled turns the scheduler on. When off, fetches happen only through
	// the CLI or the API.
	Enabled bool
	// Interval is how often the scheduler looks for sources that are due.
	// Each source's own min_fetch_interval decides when it is due.
	Interval time.Duration
}

// IngestConfig controls source fetching.
type IngestConfig struct {
	// FetchTimeout bounds one fetch of one source, all requests included.
	FetchTimeout time.Duration
	// UserAgent identifies Synergy to upstream APIs. arXiv asks clients to
	// include a contact address.
	UserAgent string
	// GitHubToken is optional; it raises GitHub search limits from 10 to 30
	// requests/minute. It is a credential and must never be logged.
	GitHubToken string
}

// LogValue keeps the GitHub token out of logs.
func (c IngestConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Duration("fetch_timeout", c.FetchTimeout),
		slog.String("user_agent", c.UserAgent),
		slog.Bool("github_token_set", c.GitHubToken != ""),
	)
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

// DatabaseConfig controls the PostgreSQL connection pool.
type DatabaseConfig struct {
	// URL is a postgres:// connection string. It may contain a password, so
	// it must never be logged.
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// LogValue keeps the connection string (and its password) out of logs even if
// the config is logged by mistake.
func (d DatabaseConfig) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", "[redacted]"),
		slog.Int("max_conns", int(d.MaxConns)),
		slog.Int("min_conns", int(d.MinConns)),
	)
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

	DefaultDBMaxConns        = 10
	DefaultDBMinConns        = 0
	DefaultDBMaxConnLifetime = time.Hour
	DefaultDBMaxConnIdleTime = 30 * time.Minute
	DefaultDBConnectTimeout  = 5 * time.Second

	DefaultFetchTimeout = 2 * time.Minute

	DefaultAnthropicModel = "claude-opus-5-5"
	DefaultVoyageModel    = "voyage-3.5"

	DefaultSchedulerEnabled  = true
	DefaultSchedulerInterval = time.Minute
	DefaultUserAgent         = "Synergy/0.1 (personal AI research aggregator)"
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
		Database: DatabaseConfig{
			URL:             l.databaseURL("DATABASE_URL"),
			MaxConns:        l.int32("DB_MAX_CONNS", DefaultDBMaxConns, 1, 1000),
			MinConns:        l.int32("DB_MIN_CONNS", DefaultDBMinConns, 0, 1000),
			MaxConnLifetime: l.duration("DB_MAX_CONN_LIFETIME", DefaultDBMaxConnLifetime),
			MaxConnIdleTime: l.duration("DB_MAX_CONN_IDLE_TIME", DefaultDBMaxConnIdleTime),
			ConnectTimeout:  l.duration("DB_CONNECT_TIMEOUT", DefaultDBConnectTimeout),
		},
		Ingest: IngestConfig{
			FetchTimeout: l.duration("FETCH_TIMEOUT", DefaultFetchTimeout),
			UserAgent:    l.str("HTTP_USER_AGENT", DefaultUserAgent),
			GitHubToken:  l.str("GITHUB_TOKEN", ""),
		},
		Scheduler: SchedulerConfig{
			Enabled:  l.boolean("SCHEDULER_ENABLED", DefaultSchedulerEnabled),
			Interval: l.duration("SCHEDULER_INTERVAL", DefaultSchedulerInterval),
		},
		LLM: LLMConfig{
			AnthropicAPIKey: l.str("ANTHROPIC_API_KEY", ""),
			AnthropicModel:  l.str("ANTHROPIC_MODEL", DefaultAnthropicModel),
		},
		Embedding: EmbeddingConfig{
			VoyageAPIKey: l.str("VOYAGE_API_KEY", ""),
			VoyageModel:  l.str("VOYAGE_MODEL", DefaultVoyageModel),
		},
	}
	if cfg.Database.MinConns > cfg.Database.MaxConns {
		l.fail("DB_MIN_CONNS", "must not exceed DB_MAX_CONNS (%d)", cfg.Database.MaxConns)
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

func (l *loader) int32(key string, def, lo, hi int32) int32 {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || int32(n) < lo || int32(n) > hi {
		l.fail(key, "must be an integer between %d and %d, got %q", lo, hi, v)
		return def
	}
	return int32(n)
}

// databaseURL requires a postgres:// URL. Error messages never echo the
// value, because it may contain a password.
func (l *loader) databaseURL(key string) string {
	v, ok := l.lookup(key)
	if !ok {
		l.fail(key, "is required (e.g. postgres://user:pass@localhost:5432/synergy?sslmode=disable)")
		return ""
	}
	if !strings.HasPrefix(v, "postgres://") && !strings.HasPrefix(v, "postgresql://") {
		l.fail(key, "must be a postgres:// or postgresql:// URL")
		return ""
	}
	return v
}

func (l *loader) boolean(key string, def bool) bool {
	v, ok := l.lookup(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be true or false, got %q", v)
		return def
	}
	return b
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
