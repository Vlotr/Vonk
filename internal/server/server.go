// Package server wires the HTTP routes and the middleware around them.
//
// Routes fall in two groups. Public routes are SDK traffic (/api/... for
// Sentry, later the PostHog endpoints) and /health; they are registered on the
// public mux. Every other request falls through to the dashboard mux (the SPA
// and /ui/api/...), which sits behind a temporary guard until real logins
// exist: without DEV_BASIC_AUTH only direct loopback clients get in; with it,
// everyone needs those credentials. New dashboard routes are therefore
// guarded by default; only routes added to the public mux are open.
package server

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"monitor/internal/api"
	"monitor/internal/config"
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
}

// New returns the handler for the whole server.
func New(deps Deps) http.Handler {
	spa := deps.SPA
	if spa == nil {
		spa = web.Handler()
	}

	// Dashboard: the JSON API and the single-page app.
	dashboard := http.NewServeMux()
	api.New(deps.DB, deps.Logger).Register(dashboard)
	dashboard.HandleFunc("/ui/api/", notFoundJSON)
	// "/" rather than "GET /": Go's mux refuses a method-only catch-all next to
	// "/ui/api/". The SPA handler answers other methods with 405 itself.
	dashboard.Handle("/", spa)

	// Public: SDK traffic and the health check. Anything else goes through the
	// guard to the dashboard.
	public := http.NewServeMux()
	public.HandleFunc("GET /health", healthHandler(deps))
	public.Handle(ingest.SentryEnvelopePattern, ingest.NewSentryHandler(deps.DB, deps.Queue, deps.Limiter, deps.Logger))
	public.HandleFunc("/api/", notFoundJSON)
	public.Handle("/", dashboardGuard(deps.Config.DevBasicAuth)(dashboard))

	return logRequests(deps.Logger, recoverPanics(deps.Logger, public))
}

// healthHandler answers whether the process and its database work.
func healthHandler(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		body := map[string]string{"status": "ok", "version": deps.Version, "db": "ok"}
		status := http.StatusOK
		if err := deps.DB.Ping(ctx); err != nil {
			deps.Logger.Error("health check: database", "err", err)
			body["status"] = "error"
			body["db"] = "error"
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, body)
	}
}

// dashboardGuard returns middleware that keeps the dashboard private until
// real logins arrive. devBasicAuth is "user:pass" or empty.
func dashboardGuard(devBasicAuth string) func(http.Handler) http.Handler {
	wantUser, wantPass, useBasicAuth := strings.Cut(devBasicAuth, ":")

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if useBasicAuth {
				user, pass, ok := r.BasicAuth()
				if !ok || !equalSecret(user, wantUser) || !equalSecret(pass, wantPass) {
					w.Header().Set("WWW-Authenticate", `Basic realm="dashboard", charset="UTF-8"`)
					writeGuardError(w, r, http.StatusUnauthorized, "sign in with the DEV_BASIC_AUTH credentials")
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			if !fromLoopback(r) {
				writeGuardError(w, r, http.StatusForbidden,
					"the dashboard only answers requests made directly from this machine (not through a proxy); set DEV_BASIC_AUTH=user:password to open it to others")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// writeGuardError answers in JSON for the dashboard API and in plain text for pages.
func writeGuardError(w http.ResponseWriter, r *http.Request, status int, message string) {
	if strings.HasPrefix(r.URL.Path, "/ui/api/") {
		writeError(w, status, message)
		return
	}
	http.Error(w, message, status)
}

// equalSecret compares in constant time, so the response time leaks nothing.
// Hashing first makes the comparison independent of the lengths too.
func equalSecret(got, want string) bool {
	g, w := sha256.Sum256([]byte(got)), sha256.Sum256([]byte(want))
	return subtle.ConstantTimeCompare(g[:], w[:]) == 1
}

// forwardingHeaders are set by reverse proxies. A loopback peer that sends one
// is most likely a proxy on the same host (or a container with --network
// host) relaying someone else's request.
var forwardingHeaders = []string{"Forwarded", "X-Forwarded-For", "X-Real-IP"}

// fromLoopback reports whether the request comes from this machine itself:
// the TCP peer is loopback and no forwarding header is present. The headers
// can only make the check stricter, never looser: anyone can send them, so a
// non-loopback peer is refused whatever they say.
func fromLoopback(r *http.Request) bool {
	for _, name := range forwardingHeaders {
		if r.Header.Get(name) != "" {
			return false
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return addr.Unmap().IsLoopback()
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

// writeJSON writes v as a JSON response.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// notFoundJSON answers unknown API paths, so SDKs and the dashboard never get HTML.
func notFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

// writeError writes the {"error":"..."} body every JSON endpoint uses for errors.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
