package ingest

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
