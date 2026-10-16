package ingest

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/store"
)

// fakeClock is a settable clock for the handler and the rate limiter.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer collects log output from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv is a database, a queue, rate limiters and the Sentry and PostHog handlers.
type testEnv struct {
	db      *store.DB
	dbPath  string
	queue   *Queue
	limiter *RateLimiter
	clock   *fakeClock
	logs    *syncBuffer
	logger  *slog.Logger
	mux     *http.ServeMux
	sentry  *SentryHandler
}

func newTestEnv(t *testing.T, queueCapacity int) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	// 12:00:10, so a minute window has 50 seconds left.
	clock := &fakeClock{t: time.Date(2026, 10, 16, 12, 0, 10, 0, time.UTC)}
	logs := &syncBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	env := &testEnv{
		db:      db,
		dbPath:  cfg.SQLitePath,
		queue:   NewQueue(queueCapacity),
		limiter: NewRateLimiter(),
		clock:   clock,
		logs:    logs,
		logger:  logger,
		mux:     http.NewServeMux(),
	}
	env.limiter.now = clock.Now
	h := NewSentryHandler(db, env.queue, env.limiter, logger)
	h.now = clock.Now
	env.sentry = h
	env.mux.Handle(SentryEnvelopePattern, h)
	// PostHog traffic has limits of its own, on the same queue.
	posthogLimiter := NewRateLimiter()
	posthogLimiter.now = clock.Now
	ph := NewPosthogHandler(db, env.queue, posthogLimiter, logger)
	ph.now = clock.Now
	RegisterPosthog(env.mux, ph)
	return env
}

func (e *testEnv) project(t *testing.T, name string, rateLimit int) store.Project {
	t.Helper()
	p, err := e.db.CreateProject(context.Background(), store.NewProject{Name: name, RateLimitPerMinute: rateLimit})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// post sends body to path as a Sentry SDK would and returns the response.
func (e *testEnv) post(path string, body []byte, setup ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/x-sentry-envelope")
	for _, f := range setup {
		f(req)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// drain takes everything out of the queue.
func (e *testEnv) drain() []Item {
	var items []Item
	for {
		select {
		case item := <-e.queue.items:
			items = append(items, item)
		default:
			return items
		}
	}
}

func envelopePath(p store.Project) string {
	return fmt.Sprintf("/api/%d/envelope/", p.ID)
}

func authHeader(key string) func(*http.Request) {
	return func(r *http.Request) {
		r.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_client=sentry.php/4.34.0, sentry_key="+key)
	}
}

// envelope returns an envelope with one event. headerExtra goes into the envelope header.
func envelope(eventID, headerExtra string) []byte {
	header := `{"event_id":"` + eventID + `"` + headerExtra + `}`
	event := `{"event_id":"` + eventID + `","platform":"php","timestamp":1792152010,` +
		`"exception":{"values":[{"type":"RuntimeException","value":"Checkout failed","stacktrace":{"frames":[` +
		`{"filename":"/app/Services/PaymentGateway.php","function":"charge","lineno":12,"in_app":true}]}}]},` +
		`"user":{"id":"42","ip_address":"203.0.113.9"},"request":{"url":"https://shop.example.com/checkout?token=abc"}}`
	return []byte(header + "\n" + `{"type":"event"}` + "\n" + event + "\n")
}

func eventIDN(n int) string {
	return fmt.Sprintf("%032x", n)
}
