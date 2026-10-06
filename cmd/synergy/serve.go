package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"synergy/internal/api"
	"synergy/internal/config"
)

// HTTP server timeouts. These protect against slow or stalled clients and are
// not expected to need per-deployment tuning.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 15 * time.Second
	writeTimeout      = 30 * time.Second
	idleTimeout       = 60 * time.Second
)

// serve runs the HTTP API until ctx is cancelled, then shuts down gracefully,
// giving in-flight requests up to the configured shutdown timeout.
func serve(ctx context.Context, getenv func(string) string, logOut io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return fmt.Errorf("load config:\n%w", err)
	}
	logger := newLogger(logOut, cfg.Log)

	srv := &http.Server{
		Handler:           api.New(api.Options{Logger: logger, Version: version}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	ln, err := net.Listen("tcp", cfg.Server.Addr())
	if err != nil {
		logger.Error("listen failed", "addr", cfg.Server.Addr(), "err", err)
		return fmt.Errorf("listen on %s: %w", cfg.Server.Addr(), err)
	}
	logger.Info("server started",
		"addr", ln.Addr().String(),
		"version", version,
		"log_level", cfg.Log.Level.String(),
	)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		logger.Error("server stopped unexpectedly", "err", err)
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", cfg.Server.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.Server.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed", "err", err)
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	logger.Info("server stopped")
	return nil
}

func newLogger(w io.Writer, cfg config.LogConfig) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	var h slog.Handler
	if cfg.Format == config.LogFormatText {
		h = slog.NewTextHandler(w, opts)
	} else {
		h = slog.NewJSONHandler(w, opts)
	}
	return slog.New(h).With("service", "synergy")
}
