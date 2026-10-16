package ingest

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimit_PerProjectPerMinute(t *testing.T) {
	env := newTestEnv(t, DefaultQueueCapacity)
	webshop := env.project(t, "Webshop", 0) // the default: 1000 events a minute
	admin := env.project(t, "Admin", 0)
	if webshop.RateLimitPerMinute != 1000 {
		t.Fatalf("default rate limit = %d, want 1000", webshop.RateLimitPerMinute)
	}

	for i := range 1000 {
		if rec := env.post(envelopePath(webshop), envelope(eventIDN(i), ""), authHeader(webshop.SentryKey)); rec.Code != http.StatusOK {
			t.Fatalf("event %d: status = %d, want 200", i, rec.Code)
		}
	}
	rec := env.post(envelopePath(webshop), envelope(eventIDN(1000), ""), authHeader(webshop.SentryKey))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("event 1001: status = %d, want 429", rec.Code)
	}
	// The clock is at 12:00:10, so the limit lifts in 50 seconds.
	if got := rec.Header().Get("Retry-After"); got != "50" {
		t.Errorf("Retry-After = %q, want 50", got)
	}
	if got := rec.Header().Get("X-Sentry-Rate-Limits"); got != "50::project" {
		t.Errorf("X-Sentry-Rate-Limits = %q, want 50::project", got)
	}

	// Another project has its own budget.
	if rec := env.post(envelopePath(admin), envelope(eventIDN(1), ""), authHeader(admin.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("other project: status = %d, want 200", rec.Code)
	}
	// Envelopes without events (sessions, client reports) are not counted.
	session := []byte("{}\n" + `{"type":"session"}` + "\n" + `{"sid":"x"}` + "\n")
	if rec := env.post(envelopePath(webshop), session, authHeader(webshop.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("session over the limit: status = %d, want 200", rec.Code)
	}

	// The next minute starts a fresh count.
	env.clock.Advance(50 * time.Second)
	if rec := env.post(envelopePath(webshop), envelope(eventIDN(1001), ""), authHeader(webshop.SentryKey)); rec.Code != http.StatusOK {
		t.Errorf("next minute: status = %d, want 200", rec.Code)
	}
	if n := len(env.drain()); n != 1002 {
		t.Errorf("queued %d items, want 1002", n)
	}

	// A project's own limit is used.
	strict := env.project(t, "Strict", 3)
	for i := range 4 {
		rec := env.post(envelopePath(strict), envelope(eventIDN(i), ""), authHeader(strict.SentryKey))
		want := http.StatusOK
		if i == 3 {
			want = http.StatusTooManyRequests
		}
		if rec.Code != want {
			t.Errorf("strict project, event %d: status = %d, want %d", i+1, rec.Code, want)
		}
	}
}

func TestRateLimiter_Window(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 16, 12, 0, 59, 600_000_000, time.UTC)}
	l := NewRateLimiter()
	l.now = clock.Now

	if ok, _ := l.Allow(1, 2, 2); !ok {
		t.Fatal("2 events within a limit of 2 refused")
	}
	ok, wait := l.Allow(1, 2, 1)
	if ok || wait != 400*time.Millisecond {
		t.Errorf("third event: ok=%v wait=%v, want refused with 400ms left", ok, wait)
	}
	if ok, _ := l.Allow(1, 2, 3); ok {
		t.Error("an envelope larger than the whole limit was allowed")
	}
	if ok, _ := l.Allow(2, 2, 1); !ok {
		t.Error("another project shares the count")
	}
	if ok, _ := l.Allow(1, 0, 1_000_000); !ok {
		t.Error("a limit of 0 should mean no limit")
	}

	clock.Advance(time.Second) // 12:01:00.6, a new minute
	if ok, _ := l.Allow(1, 2, 2); !ok {
		t.Error("the count did not start over in the next minute")
	}
}
