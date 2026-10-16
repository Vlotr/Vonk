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

	"monitor/internal/auth"
	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/issues"
	"monitor/internal/mail"
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
	// After the first signal, restore the default handling, so a second
	// Ctrl-C ends the process at once instead of waiting for the shutdown.
	context.AfterFunc(ctx, stop)

	if getenv("DEV_BASIC_AUTH") != "" {
		logger.Warn("DEV_BASIC_AUTH is no longer used: the dashboard has accounts now; unset it")
	}
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

	// Ingest: handlers put accepted events on the queue, one writer stores
	// them in batches. The writer gets its own context, not ctx, so it keeps
	// writing while the server lets running requests finish; Close below
	// writes whatever is still queued.
	queue := ingest.NewQueue(ingest.DefaultQueueCapacity)
	writer := ingest.NewWriter(db, queue, issues.NoHooks{}, logger)
	go writer.Run(context.Background())
	defer writer.Close() // before db.Close; also on the error paths

	// Mail for invites and reset links (and, later, notifications).
	var mailer auth.Mailer
	if cfg.Mail != nil {
		mailer = mail.NewSender(*cfg.Mail, cfg.BaseURL)
		logger.Info("mail is on", "host", cfg.Mail.Host, "port", cfg.Mail.Port)
	} else {
		logger.Info("mail is off: invite and reset links are only shown, not mailed; set MAIL_URL to mail them")
	}
	if n, err := db.CountUsers(ctx); err == nil && n == 0 {
		logger.Warn("no account yet: open " + cfg.BaseURL + "/setup now and create the first admin; until then anyone who can reach it can")
	}

	srv := &http.Server{
		Handler: server.New(server.Deps{
			Config:  cfg,
			DB:      db,
			Logger:  logger,
			Version: version,
			Queue:   queue,
			Limiter: ingest.NewRateLimiter(),
			Mailer:  mailer,
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
	writer.Close()
	logger.Info("stopped")
	return nil
}
