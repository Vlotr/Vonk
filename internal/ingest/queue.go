// Package ingest accepts SDK traffic and stores it. HTTP handlers parse,
// authenticate, rate-limit and scrub what SDKs send, then put it on a bounded
// Queue; one Writer takes it off in batches and writes it to the database.
// When the queue is full, handlers answer 429 at once instead of waiting, so
// a slow disk never makes SDK requests pile up.
package ingest

import (
	"time"

	"monitor/internal/sentry"
)

// DefaultQueueCapacity is how many items wait for the writer at most.
const DefaultQueueCapacity = 10_000

// Item is one piece of ingested data waiting for the writer.
type Item struct {
	ProjectID  int64
	ReceivedAt time.Time
	Event      *sentry.Event // a scrubbed Sentry error event
}

// Queue is a bounded first-in, first-out queue between the handlers and the writer.
type Queue struct {
	items chan Item
}

// NewQueue returns an empty queue that holds up to capacity items.
func NewQueue(capacity int) *Queue {
	return &Queue{items: make(chan Item, capacity)}
}

// TryEnqueue adds item and reports whether there was room. It never waits.
func (q *Queue) TryEnqueue(item Item) bool {
	select {
	case q.items <- item:
		return true
	default:
		return false
	}
}

// Len returns how many items are waiting.
func (q *Queue) Len() int {
	return len(q.items)
}

// Cap returns how many items the queue holds at most.
func (q *Queue) Cap() int {
	return cap(q.items)
}
