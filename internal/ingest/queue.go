// Package ingest accepts SDK traffic and stores it. HTTP handlers parse,
// authenticate, rate-limit and scrub what SDKs send, then put it on a bounded
// Queue; one Writer takes it off in batches and writes it to the database.
// When the queue is full, handlers answer 429 at once instead of waiting, so
// a slow disk never makes SDK requests pile up.
package ingest

import (
	"sync/atomic"
	"time"

	"monitor/internal/sentry"
)

const (
	// DefaultQueueCapacity is how many items wait for the writer at most.
	DefaultQueueCapacity = 10_000
	// DefaultQueueMaxBytes is how many bytes of event JSON wait for the
	// writer at most, so 10 000 large events cannot take gigabytes of memory.
	DefaultQueueMaxBytes = 256 << 20
)

// Item is one piece of ingested data waiting for the writer.
type Item struct {
	ProjectID  int64
	ReceivedAt time.Time
	Event      *sentry.Event // a scrubbed Sentry error event
}

// size is the item's approximate weight in the queue: the bytes of JSON its
// event was decoded from.
func (it Item) size() int64 {
	if it.Event == nil {
		return 0
	}
	return int64(it.Event.Size)
}

// Queue is a bounded first-in, first-out queue between the handlers and the
// writer. It is full at its capacity in items or at maxBytes of events,
// whichever comes first.
type Queue struct {
	items    chan Item
	maxBytes int64
	bytes    atomic.Int64 // the size of the items waiting
	refused  atomic.Int64 // items TryEnqueue turned away since the start
}

// NewQueue returns an empty queue that holds up to capacity items and
// DefaultQueueMaxBytes of events.
func NewQueue(capacity int) *Queue {
	return &Queue{items: make(chan Item, capacity), maxBytes: DefaultQueueMaxBytes}
}

// TryEnqueue adds item and reports whether there was room. It never waits.
func (q *Queue) TryEnqueue(item Item) bool {
	size := item.size()
	// Reserve the bytes first; two handlers racing near the limit may both
	// be refused, never both let in.
	if q.bytes.Add(size) > q.maxBytes {
		q.bytes.Add(-size)
		q.refused.Add(1)
		return false
	}
	select {
	case q.items <- item:
		return true
	default:
		q.bytes.Add(-size)
		q.refused.Add(1)
		return false
	}
}

// Refused returns how many items the queue turned away because it was
// full, since the start: events the SDKs were told to send again later
// (429), which admins hear about.
func (q *Queue) Refused() int64 {
	return q.refused.Load()
}

// release frees the bytes of an item the writer took off the queue.
func (q *Queue) release(item Item) {
	q.bytes.Add(-item.size())
}

// Len returns how many items are waiting.
func (q *Queue) Len() int {
	return len(q.items)
}

// Bytes returns the size of the items waiting, in bytes of event JSON.
func (q *Queue) Bytes() int64 {
	return q.bytes.Load()
}

// Cap returns how many items the queue holds at most.
func (q *Queue) Cap() int {
	return cap(q.items)
}
