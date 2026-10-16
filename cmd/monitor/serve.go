package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"monitor/internal/config"
	"monitor/internal/server"
	"monitor/internal/store"
)

// shutdownTimeout is how long running requests get to finish after SIGTERM.
const shutdownTimeout = 15 * time.Second

// serve runs the HTTP server until SIGINT or SIGTERM.
func serve(args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintf(stderr, "monitor: serve takes no arguments\n\n%s", usage)
		return 2
	}
	cfg, err := config.Load(getenv)
	if err != nil {
		printConfigError(stderr, err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := runServer(ctx, cfg, logger); err != nil {
		logger.Error("server stopped", "err", err)
		return 1
	}
	return 0
}

func runServer(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
	logger.Info("starting", "version", version, "addr", cfg.Addr, "base_url", cfg.BaseURL, "sqlite", cfg.SQLitePath)

	db, err := store.Open(ctx, cfg)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("database migration failed: %w", err)
	}

	if cfg.DevBasicAuth == "" {
		logger.Info("dashboard answers this machine only; set DEV_BASIC_AUTH=user:password to open it to others")
	} else {
		logger.Info("dashboard is protected with DEV_BASIC_AUTH")
	}

	srv := &http.Server{
		Handler: server.New(server.Deps{
			Config:  cfg,
			DB:      db,
			Logger:  logger,
			Version: version,
		}),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	// Listen first, so a busy port fails right away with a clear error.
	listener, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return err
	}
	logger.Info("listening", "addr", listener.Addr().String())

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	logger.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	logger.Info("stopped")
	return nil
}
