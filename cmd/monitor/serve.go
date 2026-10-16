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

	"monitor/internal/alerts"
	"monitor/internal/auth"
	"monitor/internal/config"
	"monitor/internal/health"
	"monitor/internal/ingest"
	"monitor/internal/jobs"
	"monitor/internal/mail"
	"monitor/internal/server"
)

// shutdownTimeout is how long running requests get to finish after SIGTERM.
const shutdownTimeout = 15 * time.Second

// notifierShutdown is how long notifications under way get to go out after SIGTERM.
const notifierShutdown = 10 * time.Second

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
	started := time.Now()
	logger.Info("starting", "version", version, "addr", cfg.Addr, "base_url", cfg.BaseURL, "sqlite", cfg.SQLitePath, "tz", cfg.Location.String())

	db, err := openDatabase(ctx, cfg, func(path string) {
		logger.Info("database backed up before the update", "path", path)
	})
	if err != nil {
		return err
	}
	defer db.Close() // last, after everything below has stopped

	// Mail for invites, reset links and notifications.
	var mailer auth.Mailer
	if cfg.Mail != nil {
		mailer = mail.NewSender(*cfg.Mail, cfg.BaseURL)
		logger.Info("mail is on", "host", cfg.Mail.Host, "port", cfg.Mail.Port)
	} else {
		logger.Info("mail is off: invites, reset links and notifications are not mailed; set MAIL_URL to mail them")
	}
	if n, err := db.CountUsers(ctx); err == nil && n == 0 {
		logger.Warn("no account yet: open " + cfg.BaseURL + "/setup now and create the first admin; until then anyone who can reach it can")
	}

	// Notifications: the writer tells the notifier about new and returning
	// issues; it delivers in the background.
	notifier := alerts.New(alerts.Options{DB: db, Mailer: mailer, BaseURL: cfg.BaseURL, Logger: logger, Location: cfg.Location})
	notifier.Start()
	defer notifier.Close(notifierShutdown)

	// Ingest: handlers put accepted events on the queue, one writer stores
	// them in batches. The writer gets its own context, not ctx, so it keeps
	// writing while the server lets running requests finish; Close writes
	// whatever is still queued.
	queue := ingest.NewQueue(ingest.DefaultQueueCapacity)
	writer := ingest.NewWriter(db, queue, notifier, logger)
	go writer.Run(context.Background())
	defer writer.Close() // before the notifier and the database close; also on the error paths

	// Background jobs: retention, spike checks, summaries and the checks
	// of the tool itself.
	checker := &health.Checker{DB: db, DataDir: cfg.DataDir, Queue: queue, Version: version, Log: logger}
	adminAlerts := alerts.NewAdminAlerts(notifier, checker, func() alerts.IngestStats {
		ws := writer.Stats()
		return alerts.IngestStats{QueueRefused: queue.Refused(), FailedBatches: ws.FailedBatches, LostEvents: ws.LostEvents, LastError: ws.LastError}
	})
	scheduler := jobs.New(nil, logger)
	scheduler.Add((&jobs.Retention{DB: db, Log: logger}).Job())
	for _, job := range notifier.Jobs() {
		scheduler.Add(job)
	}
	scheduler.Add(adminAlerts.Job())
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan struct{})
	go func() {
		scheduler.Run(jobsCtx)
		close(jobsDone)
	}()
	defer func() { // first of the deferred stops: no job runs while the rest shuts down
		stopJobs()
		<-jobsDone
	}()

	srv := &http.Server{
		Handler: server.New(server.Deps{
			Config:         cfg,
			DB:             db,
			Logger:         logger,
			Version:        version,
			Queue:          queue,
			Limiter:        ingest.NewRateLimiter(),
			Mailer:         mailer,
			Health:         checker,
			Started:        started,
			StoredLastHour: writer.StoredLastHour,
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
	// Then the background work, in order: the jobs, the writer (its last
	// batch still notifies), the notifier. The deferred calls, which do the
	// same on the error paths, find them stopped; the database closes last.
	stopJobs()
	<-jobsDone
	writer.Close()
	notifier.Close(notifierShutdown)
	logger.Info("stopped")
	return nil
}
