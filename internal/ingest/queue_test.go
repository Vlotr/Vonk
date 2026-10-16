package ingest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"monitor/internal/sentry"
)

func TestQueueFull_Returns429(t *testing.T) {
	if DefaultQueueCapacity != 10_000 {
		t.Fatalf("DefaultQueueCapacity = %d, want 10000", DefaultQueueCapacity)
	}
	env := newTestEnv(t, DefaultQueueCapacity)
	p := env.project(t, "Webshop", 1_000_000)

	for range DefaultQueueCapacity {
		if !env.queue.TryEnqueue(Item{ProjectID: p.ID}) {
			t.Fatal("queue full before its capacity")
		}
	}
	if env.queue.TryEnqueue(Item{ProjectID: p.ID}) {
		t.Fatal("TryEnqueue on a full queue returned true")
	}
	if got := env.queue.Refused(); got != 1 {
		t.Errorf("Refused = %d, want 1 (admins hear about refused events)", got)
	}

	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, envelopePath(p), bytes.NewReader(envelope(eventIDN(1), "")))
		authHeader(p.SentryKey)(req)
		rec := httptest.NewRecorder()
		env.mux.ServeHTTP(rec, req)
		done <- rec
	}()
	select {
	case rec := <-done:
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d, want 429", rec.Code)
		}
		if got := rec.Header().Get("Retry-After"); got != "5" {
			t.Errorf("Retry-After = %q, want 5", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the handler blocked on a full queue")
	}
	if env.queue.Len() != DefaultQueueCapacity {
		t.Errorf("queue length = %d", env.queue.Len())
	}
	if !strings.Contains(env.logs.String(), "queue is full") {
		t.Errorf("no log line about the full queue: %s", env.logs.String())
	}
}

func TestQueue_KeepsOrderAndCapacity(t *testing.T) {
	q := NewQueue(2)
	if q.Cap() != 2 || q.Len() != 0 {
		t.Fatalf("Cap/Len = %d/%d", q.Cap(), q.Len())
	}
	if !q.TryEnqueue(Item{ProjectID: 1}) || !q.TryEnqueue(Item{ProjectID: 2}) {
		t.Fatal("TryEnqueue refused below capacity")
	}
	if q.TryEnqueue(Item{ProjectID: 3}) {
		t.Fatal("TryEnqueue accepted above capacity")
	}
	if first, second := <-q.items, <-q.items; first.ProjectID != 1 || second.ProjectID != 2 {
		t.Errorf("order = %d, %d; want 1, 2", first.ProjectID, second.ProjectID)
	}
}

func TestQueue_BoundedByBytes(t *testing.T) {
	if DefaultQueueMaxBytes != 256<<20 {
		t.Fatalf("DefaultQueueMaxBytes = %d, want 256 MiB", DefaultQueueMaxBytes)
	}
	q := NewQueue(10)
	q.maxBytes = 1000
	sized := func(n int) Item { return Item{ProjectID: 1, Event: &sentry.Event{Size: n}} }

	if !q.TryEnqueue(sized(600)) {
		t.Fatal("refused the first item although it fits")
	}
	if q.TryEnqueue(sized(600)) {
		t.Fatal("accepted an item that takes the queue over its byte limit")
	}
	if !q.TryEnqueue(sized(400)) {
		t.Fatal("refused an item that exactly fits")
	}
	if got := q.bytes.Load(); got != 1000 {
		t.Errorf("queued bytes = %d, want 1000", got)
	}

	// The writer takes an item off: its bytes are free again.
	q.release(<-q.items)
	if got := q.bytes.Load(); got != 400 {
		t.Errorf("queued bytes after one is taken = %d, want 400", got)
	}
	if !q.TryEnqueue(sized(600)) {
		t.Error("refused an item after room was made")
	}
}

func TestQueueFullOfBytes_Returns429(t *testing.T) {
	env := newTestEnv(t, 100)
	env.queue.maxBytes = 10 // smaller than any event
	p := env.project(t, "Webshop", 1_000_000)

	rec := env.post(envelopePath(p), envelope(eventIDN(1), ""), authHeader(p.SentryKey))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("status %d, Retry-After %q; want 429 and 5", rec.Code, rec.Header().Get("Retry-After"))
	}
	if env.queue.Len() != 0 || env.queue.bytes.Load() != 0 {
		t.Errorf("queue holds %d items, %d bytes; want nothing", env.queue.Len(), env.queue.bytes.Load())
	}
}

func TestWriter_ReleasesQueuedBytes(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{})
	item := errorItem(t, p.ID, eventIDN(1), "charge", t0, t0)
	if item.Event.Size == 0 {
		t.Fatal("a decoded event has no size")
	}
	enqueue(t, env, item)
	w.Close()
	if got := env.queue.bytes.Load(); got != 0 {
		t.Errorf("queued bytes after the writer took everything = %d, want 0", got)
	}
}
