package ingest

import (
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"monitor/internal/httpjson"
)

// The answers the Sentry and the PostHog handler share when they refuse a
// request: a full queue and a failed project lookup. prefix ("sentry",
// "posthog") starts their log lines.

// fullQueue answers requests the queue has no room for.
type fullQueue struct {
	queue   *Queue
	logger  *slog.Logger
	prefix  string
	lastLog atomic.Int64 // unix nanoseconds of the last warning
}

// answer answers 429 with Retry-After and logs a warning, at most once per
// queueFullLogEvery.
func (f *fullQueue) answer(w http.ResponseWriter) {
	now := time.Now().UnixNano()
	last := f.lastLog.Load()
	if now-last >= int64(queueFullLogEvery) && f.lastLog.CompareAndSwap(last, now) {
		f.logger.Warn(f.prefix+": ingest queue is full; answering 429 until the writer catches up",
			"items", f.queue.Len(), "capacity", f.queue.Cap(), "bytes", f.queue.Bytes(), "max_bytes", f.queue.maxBytes)
	}
	w.Header().Set("Retry-After", queueFullRetryAfter)
	httpjson.Error(w, http.StatusTooManyRequests, "too busy; try again in a few seconds")
}

// lookupFailedLogEvery limits the "project lookup failed" error to one
// line per period: during a database outage every request fails it.
const lookupFailedLogEvery = 30 * time.Second

// failedLookups answers requests whose project lookup failed.
type failedLookups struct {
	logger *slog.Logger
	prefix string
	now    func() time.Time // the clock; nil means time.Now

	mu         sync.Mutex
	lastLog    time.Time
	suppressed int // failed lookups since the last line
}

// answer answers 401 for an unknown DSN key or project token and 503
// when the lookup itself failed (the database). That is logged as an
// error at most once per lookupFailedLogEvery, with the count of the ones
// in between, and not at all when the client went away.
func (f *failedLookups) answer(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, errUnauthorized) || errors.Is(err, errUnknownToken) {
		httpjson.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	if httpjson.ClientGone(r) {
		f.logger.Debug(f.prefix+": the client went away", "err", err)
		return
	}
	f.log(err)
	w.Header().Set("Retry-After", dbErrorRetryAfter)
	httpjson.Error(w, http.StatusServiceUnavailable, "temporarily unavailable")
}

func (f *failedLookups) log(err error) {
	now := time.Now()
	if f.now != nil {
		now = f.now()
	}
	f.mu.Lock()
	if !f.lastLog.IsZero() && now.Sub(f.lastLog) < lookupFailedLogEvery {
		f.suppressed++
		f.mu.Unlock()
		return
	}
	since := f.suppressed
	f.lastLog, f.suppressed = now, 0
	f.mu.Unlock()

	args := []any{"err", err}
	if since > 0 {
		args = append(args, "refused_since_last", since)
	}
	f.logger.Error(f.prefix+": project lookup failed; answering 503 (logged at most every 30 s)", args...)
}
