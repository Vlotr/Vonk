// Package ingest accepts SDK traffic and stores it. HTTP handlers parse,
// authenticate, rate-limit and scrub what SDKs send, then put it on a bounded
// Queue; one Writer takes it off in batches and writes it to the database.
// When the queue is full, handlers answer 429 at once instead of waiting, so
// a slow disk never makes SDK requests pile up.
package ingest

import (
	"sync"
	"sync/atomic"
	"time"

	"monitor/internal/posthog"
	"monitor/internal/sentry"
)

const (
	// DefaultQueueCapacity is how many items wait for the writer at most.
	DefaultQueueCapacity = 10_000
	// DefaultQueueMaxBytes is how many bytes of event JSON wait for the
	// writer at most, so 10 000 large events cannot take gigabytes of memory.
	DefaultQueueMaxBytes = 256 << 20
)

// Item is one piece of ingested data waiting for the writer: a Sentry
// error event, a PostHog page event or a PostHog custom event.
type Item struct {
	ProjectID  int64
	ReceivedAt time.Time
	// Route is the page it happened on (routes.Normalize with the
	// project's patterns), "" when unknown.
	Route string
	// Event is a scrubbed Sentry error event, or nil.
	Event *sentry.Event
	// SentAt is when the SDK sent the event's envelope by the client's
	// clock (its sent_at); zero when it did not say. The writer uses it to
	// move the event's time to the server's clock (ruling 41).
	SentAt time.Time
	// Page is a scrubbed PostHog $pageview or $pageleave, or nil.
	Page *posthog.Event
	// Custom is a scrubbed PostHog custom event (a name without "$"), or nil.
	Custom *posthog.Event
	// Path is the URL path of a page view's or custom event's page, without
	// query and fragment.
	Path string
	// IsBot marks a page view or custom event sent by a bot.
	IsBot bool
}

// size is the item's approximate weight in the queue: the bytes of JSON its
// event was decoded from.
func (it Item) size() int64 {
	switch {
	case it.Event != nil:
		return int64(it.Event.Size)
	case it.Page != nil:
		return int64(it.Page.Size)
	case it.Custom != nil:
		return int64(it.Custom.Size)
	}
	return 0
}

// Queue is a bounded first-in, first-out queue between the handlers and the
// writer. It is full at its capacity in items or at maxBytes of events,
// whichever comes first.
type Queue struct {
	items    chan Item
	maxBytes int64
	bytes    atomic.Int64 // the size of the items waiting
	refused  atomic.Int64 // items TryEnqueue turned away since the start

	// adding is held while a batch is checked and added, so the room it
	// saw is still there: only the writer takes items off meanwhile.
	adding sync.Mutex
}

// NewQueue returns an empty queue that holds up to capacity items and
// DefaultQueueMaxBytes of events.
func NewQueue(capacity int) *Queue {
	return &Queue{items: make(chan Item, capacity), maxBytes: DefaultQueueMaxBytes}
}

// TryEnqueue adds item and reports whether there was room. It never waits.
func (q *Queue) TryEnqueue(item Item) bool {
	return q.TryEnqueueAll([]Item{item})
}

// TryEnqueueAll adds every item when there is room for all of them, else
// none, and reports which. It never waits (only for another handler that is
// adding a batch, which takes microseconds).
func (q *Queue) TryEnqueueAll(items []Item) bool {
	var size int64
	for _, it := range items {
		size += it.size()
	}
	q.adding.Lock()
	defer q.adding.Unlock()
	if !q.HasRoom(len(items), size) {
		q.refused.Add(int64(len(items)))
		return false
	}
	q.bytes.Add(size)
	for _, it := range items {
		q.items <- it // there is room: only the writer takes items off meanwhile
	}
	return true
}

// HasRoom reports whether n more items of size bytes fit now. Handlers ask
// before counting a request against its rate limit, so a full queue spends
// no budget.
func (q *Queue) HasRoom(n int, size int64) bool {
	return cap(q.items)-len(q.items) >= n && q.bytes.Load()+size <= q.maxBytes
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
