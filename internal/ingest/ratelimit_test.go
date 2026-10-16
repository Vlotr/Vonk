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

// AllowAll counts the events of every project or of none.
func TestRateLimiter_AllowAllCountsAllOrNothing(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 16, 12, 0, 10, 0, time.UTC)}
	l := NewRateLimiter()
	l.now = clock.Now
	_, ok, wait := l.AllowAll([]Ask{{ProjectID: 1, Limit: 10, Events: 4}, {ProjectID: 2, Limit: 3, Events: 4}})
	if ok || wait != 50*time.Second {
		t.Fatalf("over project 2's limit: ok %v, wait %s", ok, wait)
	}
	// Project 1 kept its whole budget.
	minute, ok, _ := l.AllowAll([]Ask{{ProjectID: 1, Limit: 10, Events: 10}})
	if !ok {
		t.Fatal("project 1 lost budget to a refused request")
	}
	if ok, _ := l.Allow(1, 10, 1); ok {
		t.Error("project 1 is over its 10 now")
	}

	// Return gives back what a request counted.
	l.Return(minute, []Ask{{ProjectID: 1, Limit: 10, Events: 3}})
	if ok, _ := l.Allow(1, 10, 3); !ok {
		t.Error("returned events were not given back")
	}
	// Events counted in a minute that is over do not come back to the next one.
	clock.Advance(time.Minute)
	if ok, _ := l.Allow(1, 10, 10); !ok {
		t.Fatal("a new minute has the whole budget")
	}
	l.Return(minute, []Ask{{ProjectID: 1, Limit: 10, Events: 5}})
	if ok, _ := l.Allow(1, 10, 1); ok {
		t.Error("events of the minute before were given back to this one")
	}
}

// An envelope the full queue refused spends none of the project's budget.
func TestRateLimit_FullQueueSpendsNoBudget(t *testing.T) {
	env := newTestEnv(t, 2)
	p := env.project(t, "Webshop", 2)
	env.queue.TryEnqueue(Item{ProjectID: p.ID})
	env.queue.TryEnqueue(Item{ProjectID: p.ID})
	if rec := env.post(envelopePath(p), envelope(eventIDN(1), ""), authHeader(p.SentryKey)); rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "5" {
		t.Fatalf("full queue = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	env.drain()
	for i := range 2 {
		if rec := env.post(envelopePath(p), envelope(eventIDN(i+2), ""), authHeader(p.SentryKey)); rec.Code != http.StatusOK {
			t.Errorf("envelope %d of the budget of 2 = %d", i+1, rec.Code)
		}
	}
}
