package ingest

import (
	"errors"
	"log/slog"
	"net/http"
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

// lookupFailed answers 401 for an unknown DSN key or project token and 503
// when the lookup itself failed (the database).
func lookupFailed(w http.ResponseWriter, logger *slog.Logger, prefix string, err error) {
	if errors.Is(err, errUnauthorized) || errors.Is(err, errUnknownToken) {
		httpjson.Error(w, http.StatusUnauthorized, err.Error())
		return
	}
	logger.Error(prefix+": project lookup failed", "err", err)
	w.Header().Set("Retry-After", dbErrorRetryAfter)
	httpjson.Error(w, http.StatusServiceUnavailable, "temporarily unavailable")
}
