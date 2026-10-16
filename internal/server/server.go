// Package server wires the HTTP routes and the middleware around them.
//
// Routes fall in two groups. Public routes are SDK traffic (/api/... for
// Sentry, later the PostHog endpoints) and /health; they are registered on
// the public mux. Every other request is the dashboard: the sign-in pages
// and their API (auth.PublicPages, auth.RegisterPublic) and the app's
// assets are open, and everything else falls through auth.RequireUser to
// the dashboard mux (the SPA and /ui/api/...). New dashboard routes are
// therefore signed-in only by default.
package server

import (
	"log/slog"
	"net/http"
	"strings"
	"time"

	"monitor/internal/alerts"
	"monitor/internal/api"
	"monitor/internal/auth"
	"monitor/internal/config"
	"monitor/internal/health"
	"monitor/internal/httpjson"
	"monitor/internal/ingest"
	"monitor/internal/store"
	"monitor/internal/web"
)

// Deps holds everything the handlers need.
type Deps struct {
	Config  config.Config
	DB      *store.DB
	Logger  *slog.Logger
	Version string
	// SPA serves the dashboard; nil means the build embedded in the binary.
	SPA http.Handler
	// Queue receives accepted SDK data; an ingest.Writer empties it.
	Queue *ingest.Queue
	// Limiter enforces each project's events-per-minute limit.
	Limiter *ingest.RateLimiter
	// Mailer sends invites and reset links; nil when MAIL_URL is not set.
	Mailer auth.Mailer
	// Health runs the checks of /health and the status page; nil means
	// one for DB, Queue and Config.DataDir.
	Health *health.Checker
	// Slack posts the test messages of the Alerts and Status pages; nil
	// means the real Slack client.
	Slack alerts.SlackPoster
	// Started is when the process started, for the status page's uptime.
	Started time.Time
	// StoredLastHour counts the events stored in the last hour, for the status page.
	StoredLastHour func() int
}

// New returns the handler for the whole server.
func New(deps Deps) http.Handler {
	spa := deps.SPA
	if spa == nil {
		spa = web.Handler()
	}
	checker := deps.Health
	if checker == nil {
		checker = &health.Checker{DB: deps.DB, DataDir: deps.Config.DataDir, Queue: deps.Queue, Version: deps.Version, Log: deps.Logger}
	}
	accounts := auth.New(auth.Options{
		DB:        deps.DB,
		SecretKey: deps.Config.SecretKey,
		BaseURL:   deps.Config.BaseURL,
		Mailer:    deps.Mailer,
		Logger:    deps.Logger,
	})

	// Signed in: the JSON API and the dashboard's pages.
	dashboard := http.NewServeMux()
	accounts.Register(dashboard)
	api.New(api.Options{
		DB:                deps.DB,
		Auth:              accounts,
		BaseURL:           deps.Config.BaseURL,
		Logger:            deps.Logger,
		Health:            checker,
		Slack:             deps.Slack,
		Started:           deps.Started,
		StoredLastHour:    deps.StoredLastHour,
		AllowHTTPWebhooks: deps.Config.AllowHTTPWebhooks,
	}).Register(dashboard)
	dashboard.HandleFunc("/ui/api/", notFoundJSON)
	// "/" rather than "GET /": Go's mux refuses a method-only catch-all next to
	// "/ui/api/". The SPA handler answers other methods with 405 itself.
	dashboard.Handle("/", spa)

	// The dashboard's front door: the pages and API to sign in, the app's
	// files (the sign-in pages need them; they hold no data), and the rest
	// only with a session.
	ui := http.NewServeMux()
	accounts.RegisterPublic(ui)
	for _, page := range auth.PublicPages {
		ui.Handle("GET "+page, spa)
	}
	ui.Handle("GET /assets/", spa)
	ui.Handle("/", accounts.RequireUser(dashboard))

	// Browsers say where a request comes from; a change sent from another
	// site is refused before it reaches a handler, on top of the CSRF token.
	sameOrigin := http.NewCrossOriginProtection()
	// BASE_URL is ours even when a proxy hands the request on with another
	// Host header (which an old browser without Sec-Fetch-Site is compared with).
	if err := sameOrigin.AddTrustedOrigin(deps.Config.BaseURL); err != nil {
		deps.Logger.Warn("BASE_URL cannot be a trusted origin", "err", err)
	}
	sameOrigin.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpjson.Error(w, http.StatusForbidden, "requests from other sites are refused")
	}))

	// Public: SDK traffic and the health check. Anything else is the dashboard.
	public := http.NewServeMux()
	public.HandleFunc("GET /health", health.Handler(checker))
	public.Handle(ingest.SentryEnvelopePattern, ingest.NewSentryHandler(deps.DB, deps.Queue, deps.Limiter, deps.Logger))
	public.HandleFunc("/api/", notFoundJSON)
	public.Handle("/", dashboardHeaders(sameOrigin.Handler(ui)))

	return logRequests(deps.Logger, recoverPanics(deps.Logger, public))
}

// dashboardHeaders adds the headers every dashboard answer carries: no
// framing by other sites (clickjacking), no MIME sniffing, no full URLs in
// Referer headers to other sites, and no caching of API answers, which hold
// one user's data.
func dashboardHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if strings.HasPrefix(r.URL.Path, "/ui/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// logRequests logs one line per request. It leaves out the query string
// (SDKs can put keys there) and the client address (IP addresses are never stored).
func logRequests(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		switch {
		case r.URL.Path == "/health":
			level = slog.LevelDebug // the Docker health check calls it every 30 s
		case strings.HasPrefix(r.URL.Path, "/api/") && rec.status < 300:
			level = slog.LevelDebug // accepted SDK traffic: one line per error event would flood the log
		}
		logger.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"bytes", rec.bytes,
			"duration", time.Since(start).Round(time.Microsecond),
		)
	})
}

// recoverPanics turns a panicking handler into a 500 and a log line instead of a dropped connection.
func recoverPanics(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			err := recover()
			if err == nil {
				return
			}
			if err == http.ErrAbortHandler {
				panic(err) // the documented way to abort a response; let net/http handle it
			}
			logger.Error("handler panicked", "method", r.Method, "path", r.URL.Path, "panic", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
		}()
		next.ServeHTTP(w, r)
	})
}

// statusRecorder remembers the status code and size of a response for the log.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	n, err := s.ResponseWriter.Write(b)
	s.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (for Flush and deadlines).
func (s *statusRecorder) Unwrap() http.ResponseWriter {
	return s.ResponseWriter
}

// notFoundJSON answers unknown API paths, so SDKs and the dashboard never get HTML.
func notFoundJSON(w http.ResponseWriter, _ *http.Request) {
	httpjson.Error(w, http.StatusNotFound, "not found")
}
