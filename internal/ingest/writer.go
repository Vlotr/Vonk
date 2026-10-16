package ingest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"monitor/internal/issues"
	"monitor/internal/posthog"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

const (
	// defaultFlushInterval is the longest an item waits in a batch.
	defaultFlushInterval = time.Second
	// defaultBatchSize items are written in one transaction at most.
	defaultBatchSize = 500
	// maxFutureSkew is how far ahead of the receive time an event may be;
	// later timestamps (a client clock that is off) become the receive time.
	maxFutureSkew = time.Hour
	// maxAge is how far before the receive time an event may be. SDKs send
	// late (a browser that was offline), but not a month late; older
	// timestamps ("timestamp": 1) become the receive time too.
	maxAge = 30 * 24 * time.Hour
)

// defaultRetryDelays are the waits between attempts when a batch fails to
// write, for example on a full disk. SQLite itself already waits up to 5 s
// for a lock before an attempt fails.
var defaultRetryDelays = []time.Duration{200 * time.Millisecond, time.Second, 5 * time.Second}

// Writer takes items off the queue and writes them to the database in
// batches: every second, or as soon as 500 items are waiting. It is the only
// goroutine that writes ingested data (error events, page events and custom
// events), so SQLite never sees two writers.
type Writer struct {
	db    *store.DB
	queue *Queue
	hooks issues.Hooks
	log   *slog.Logger

	flushInterval time.Duration
	batchSize     int
	retryDelays   []time.Duration

	stop      chan struct{}
	closeOnce sync.Once
	runOnce   sync.Once

	mu    sync.Mutex
	stats WriterStats

	stored hourCounter // events stored, per minute of the last hour
}

// WriterStats count what the writer could not store, since the start.
type WriterStats struct {
	// FailedBatches are batches the database refused after every retry.
	FailedBatches int64
	// LostEvents are the events of those batches; they are not stored.
	LostEvents int64
	// LastError is the database's answer to the last failed batch.
	LastError string
}

// StoredLastHour returns how many events (errors, page views, page leaves
// and custom events) the writer stored in the last hour (since the start,
// when that was less than an hour ago).
func (w *Writer) StoredLastHour() int {
	return w.stored.lastHour(time.Now())
}

// Stats returns what the writer could not store so far.
func (w *Writer) Stats() WriterStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.stats
}

// NewWriter returns a writer for queue. hooks hears about new and regressed issues.
func NewWriter(db *store.DB, queue *Queue, hooks issues.Hooks, logger *slog.Logger) *Writer {
	return &Writer{
		db:            db,
		queue:         queue,
		hooks:         hooks,
		log:           logger,
		flushInterval: defaultFlushInterval,
		batchSize:     defaultBatchSize,
		retryDelays:   defaultRetryDelays,
		stop:          make(chan struct{}),
	}
}

// Run writes queued items until ctx ends or Close is called, then writes
// everything still queued and returns. Calling it more than once does nothing.
func (w *Writer) Run(ctx context.Context) {
	w.runOnce.Do(func() { w.run(ctx) })
}

// Close stops Run after it has written everything that was queued, and
// returns when that is done. Stop the HTTP server first, so nothing is
// queued after the last batch. Without a running Run, Close writes the queue itself.
func (w *Writer) Close() {
	w.closeOnce.Do(func() { close(w.stop) })
	// Waits for a running Run to finish, or runs the final flush here.
	w.runOnce.Do(func() { w.run(context.Background()) })
}

func (w *Writer) run(ctx context.Context) {
	ticker := time.NewTicker(w.flushInterval)
	defer ticker.Stop()
	batch := make([]Item, 0, w.batchSize)

	for {
		select {
		case item := <-w.queue.items:
			w.queue.release(item)
			batch = append(batch, item)
			if len(batch) >= w.batchSize {
				w.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ticker.C:
			if len(batch) > 0 {
				w.flush(ctx, batch)
				batch = batch[:0]
			}
		case <-ctx.Done():
			w.drain(context.WithoutCancel(ctx), batch)
			return
		case <-w.stop:
			w.drain(ctx, batch)
			return
		}
	}
}

// drain writes batch and everything still in the queue.
func (w *Writer) drain(ctx context.Context, batch []Item) {
	for {
		select {
		case item := <-w.queue.items:
			w.queue.release(item)
			batch = append(batch, item)
			if len(batch) >= w.batchSize {
				w.flush(ctx, batch)
				batch = batch[:0]
			}
		default:
			if len(batch) > 0 {
				w.flush(ctx, batch)
			}
			return
		}
	}
}

// flush writes one batch, retrying when the database fails, and then
// tells the hooks about new and regressed issues. Error events, page
// events and custom events go in a transaction each. A part that still
// fails after the retries is logged and dropped, so one bad moment never
// stops ingest.
func (w *Writer) flush(ctx context.Context, batch []Item) {
	var (
		errorEvents  []store.NewErrorEvent
		pageEvents   []store.PageEvent
		customEvents []store.CustomEvent
	)
	for _, item := range batch {
		switch {
		case item.Event != nil:
			ev, err := errorEvent(item)
			if err != nil {
				w.log.Error("ingest: event cannot be encoded; dropped", "project", item.ProjectID, "err", err)
				continue
			}
			errorEvents = append(errorEvents, ev)
		case item.Page != nil:
			pageEvents = append(pageEvents, pageEvent(item))
		case item.Custom != nil:
			ev, err := customEvent(item)
			if err != nil {
				w.log.Error("ingest: custom event cannot be encoded; dropped", "project", item.ProjectID, "err", err)
				continue
			}
			customEvents = append(customEvents, ev)
		}
	}

	if len(errorEvents) > 0 {
		var result store.SaveResult
		if w.save(len(errorEvents), func() (err error) {
			result, err = w.db.SaveErrorEvents(ctx, errorEvents)
			return err
		}) {
			w.stored.add(time.Now(), result.Stored)
			if result.Orphaned > 0 {
				w.log.Warn("ingest: events of deleted projects skipped", "events", result.Orphaned)
			}
			w.log.Debug("ingest: batch written", "stored", result.Stored, "duplicates", result.Duplicates,
				"new_issues", len(result.NewIssues), "regressed_issues", len(result.Regressed))
			for _, issue := range result.NewIssues {
				w.hooks.OnIssueCreated(ctx, issue)
			}
			for _, issue := range result.Regressed {
				w.hooks.OnIssueRegressed(ctx, issue)
			}
		}
	}
	if len(pageEvents) > 0 {
		var result store.PageSaveResult
		if w.save(len(pageEvents), func() (err error) {
			result, err = w.db.SavePageEvents(ctx, pageEvents)
			return err
		}) {
			w.stored.add(time.Now(), result.Stored)
			if result.Orphaned > 0 {
				w.log.Warn("ingest: page events of deleted projects skipped", "events", result.Orphaned)
			}
			w.log.Debug("ingest: page events written", "stored", result.Stored, "duplicates", result.Duplicates)
		}
	}
	if len(customEvents) > 0 {
		var result store.CustomSaveResult
		if w.save(len(customEvents), func() (err error) {
			result, err = w.db.SaveCustomEvents(ctx, customEvents)
			return err
		}) {
			w.stored.add(time.Now(), result.Stored)
			if result.Orphaned > 0 {
				w.log.Warn("ingest: custom events of deleted projects skipped", "events", result.Orphaned)
			}
			w.log.Debug("ingest: custom events written", "stored", result.Stored, "duplicates", result.Duplicates)
		}
	}
}

// save runs write until it succeeds, waiting retryDelays between
// attempts, and reports whether it did. When it never does, the n events
// are counted as lost.
func (w *Writer) save(n int, write func() error) bool {
	for attempt := 0; ; attempt++ {
		err := write()
		if err == nil {
			return true
		}
		if attempt == len(w.retryDelays) {
			w.log.Error("ingest: database write failed; events dropped", "events", n, "attempts", attempt+1, "err", err)
			w.mu.Lock()
			w.stats.FailedBatches++
			w.stats.LostEvents += int64(n)
			w.stats.LastError = err.Error()
			w.mu.Unlock()
			return false
		}
		w.log.Warn("ingest: database write failed; retrying", "events", n, "attempt", attempt+1, "err", err)
		time.Sleep(w.retryDelays[attempt])
	}
}

// clampTime returns ts, or received when ts is missing, more than an hour
// after received (a client clock that is off) or more than 30 days before it.
func clampTime(ts, received time.Time) time.Time {
	if ts.IsZero() || ts.After(received.Add(maxFutureSkew)) || ts.Before(received.Add(-maxAge)) {
		return received
	}
	return ts
}

// pageEvent turns a queued PostHog event into a page view or page leave
// for the store. A page view is known by its $pageview_id (posthog-js:
// its uuid), so a resent one is stored once.
func pageEvent(item Item) store.PageEvent {
	ev := item.Page
	pe := store.PageEvent{
		ProjectID: item.ProjectID,
		Leave:     ev.Name == posthog.EventPageleave,
		SessionID: ev.SessionID,
		WindowID:  ev.WindowID,
		UserID:    ev.DistinctID,
		At:        clampTime(ev.Time(item.ReceivedAt), item.ReceivedAt),
	}
	if pe.Leave {
		pe.EventID = cmp.Or(ev.UUID, newEventID())
		pe.PageviewID = ev.PageviewID
		return pe
	}
	pe.PageviewID = cmp.Or(ev.PageviewID, ev.UUID, newEventID())
	pe.Route = item.Route
	pe.Path = item.Path
	pe.IsBot = item.IsBot
	return pe
}

// customEvent turns a queued PostHog custom event into its database row:
// known by its uuid (so a resent one is stored once), at the time PostHog's
// rule and the clamp give, with the app's own properties as JSON.
func customEvent(item Item) (store.CustomEvent, error) {
	ev := item.Custom
	var props bytes.Buffer
	enc := json.NewEncoder(&props)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(ev.OwnProperties()); err != nil {
		return store.CustomEvent{}, err
	}
	return store.CustomEvent{
		ProjectID:  item.ProjectID,
		EventID:    cmp.Or(ev.UUID, newEventID()),
		Name:       ev.Name,
		UserID:     ev.DistinctID,
		Path:       item.Path,
		Route:      item.Route,
		At:         clampTime(ev.Time(item.ReceivedAt), item.ReceivedAt),
		IsBot:      item.IsBot,
		Properties: strings.TrimSuffix(props.String(), "\n"),
	}, nil
}

// errorEvent turns a queued event into its database row: grouping, title,
// culprit and source, the route, the stored JSON, and a timestamp that is
// neither in the future nor more than 30 days old.
func errorEvent(item Item) (store.NewErrorEvent, error) {
	ev := item.Event
	if ev.EventID == "" {
		ev.SetEventID(newEventID())
	}
	data, err := ev.JSON()
	if err != nil {
		return store.NewErrorEvent{}, err
	}

	ts := clampTime(ev.Timestamp, item.ReceivedAt)
	summary := sentry.Summarize(ev)
	return store.NewErrorEvent{
		ProjectID:   item.ProjectID,
		EventID:     ev.EventID,
		Fingerprint: sentry.Fingerprint(ev),
		Title:       summary.Title,
		Message:     summary.Message,
		Culprit:     summary.Culprit,
		Source:      sentry.Source(ev),
		Level:       ev.Level,
		Timestamp:   ts,
		ReceivedAt:  item.ReceivedAt,
		UserID:      ev.User.ID,
		URL:         ev.Request.URL,
		Route:       item.Route,
		Release:     ev.Release,
		Environment: ev.Environment,
		Platform:    ev.Platform,
		Data:        string(data),
	}, nil
}
