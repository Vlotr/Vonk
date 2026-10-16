package ingest

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"monitor/internal/scrub"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

// SentryEnvelopePattern is the route of the envelope endpoint, for http.ServeMux.
const SentryEnvelopePattern = "/api/{projectID}/envelope/{$}"

// Limits for one envelope request.
const (
	MaxBodyBytes  = 20 << 20 // the body after decompression
	MaxEventBytes = 1 << 20  // one event item; larger events are dropped
)

const (
	// queueFullRetryAfter is what SDKs are told to wait when the writer is behind.
	queueFullRetryAfter = "5"
	// dbErrorRetryAfter is what SDKs are told to wait when the database fails.
	dbErrorRetryAfter = "60"
	// queueFullLogEvery limits the "queue is full" warning to one line per period.
	queueFullLogEvery = 10 * time.Second
)

var errBodyTooLarge = errors.New("body larger than 20 MiB")

// SentryHandler accepts envelopes from Sentry SDKs: POST (and the CORS
// preflight OPTIONS) on /api/{projectID}/envelope/. It answers 200, or a 4xx
// for traffic it cannot use; 503 only when the database fails.
type SentryHandler struct {
	db      *store.DB
	queue   *Queue
	limiter *RateLimiter
	logger  *slog.Logger
	now     func() time.Time

	lastQueueFullLog atomic.Int64 // unix nanoseconds
}

// NewSentryHandler returns the envelope handler. Accepted events go on queue.
func NewSentryHandler(db *store.DB, queue *Queue, limiter *RateLimiter, logger *slog.Logger) *SentryHandler {
	return &SentryHandler{db: db, queue: queue, limiter: limiter, logger: logger, now: time.Now}
}

func (h *SentryHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowCORS(w.Header())
	switch r.Method {
	case http.MethodPost:
	case http.MethodOptions:
		preflight(w)
		return
	default:
		w.Header().Set("Allow", corsAllowMethods)
		writeError(w, http.StatusMethodNotAllowed, "envelopes are sent with POST")
		return
	}

	ctx := r.Context()
	// An id that is not a number stays 0, which no project has.
	projectID, _ := strconv.ParseInt(r.PathValue("projectID"), 10, 64)

	// Server SDKs send the key in a header and browsers in the query string,
	// so most requests are checked before the body is read. Others carry it
	// only in the envelope header (dsn).
	var project store.Project
	if key := keyFromRequest(r); key != "" {
		p, err := authenticate(ctx, h.db, key, projectID)
		if err != nil {
			h.authFailed(w, err)
			return
		}
		project = p
	}

	body, err := decodeBody(w, r)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	env, err := sentry.ParseEnvelope(body, MaxEventBytes)
	if err != nil {
		var maxBytes *http.MaxBytesError
		if errors.Is(err, errBodyTooLarge) || errors.As(err, &maxBytes) {
			writeError(w, http.StatusRequestEntityTooLarge, "envelope larger than 20 MiB")
			return
		}
		badRequest(w, err.Error())
		return
	}

	if project.ID == 0 {
		p, err := authenticate(ctx, h.db, keyFromDSN(env.Header.DSN), projectID)
		if err != nil {
			h.authFailed(w, err)
			return
		}
		project = p
	}
	if env.Dropped > 0 {
		h.logger.Debug("sentry: dropped events larger than 1 MiB", "project", project.ID, "events", env.Dropped)
	}
	if len(env.Events) == 0 {
		writeJSON(w, http.StatusOK, map[string]string{}) // sessions, client reports, ...
		return
	}

	if ok, wait := h.limiter.Allow(project.ID, project.RateLimitPerMinute, len(env.Events)); !ok {
		seconds := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
		w.Header().Set("Retry-After", seconds)
		w.Header().Set("X-Sentry-Rate-Limits", seconds+"::project")
		writeError(w, http.StatusTooManyRequests,
			fmt.Sprintf("this project accepts %d events a minute", project.RateLimitPerMinute))
		return
	}

	received := h.now()
	for _, ev := range env.Events {
		if ev.EventID == "" {
			ev.SetEventID(newEventID())
		}
		scrub.Event(ev)
		if !h.queue.TryEnqueue(Item{ProjectID: project.ID, ReceivedAt: received, Event: ev}) {
			h.queueFull(w)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": env.Events[0].EventID})
}

// authFailed answers 401 for a bad key and 503 when the lookup itself failed.
func (h *SentryHandler) authFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, errUnauthorized) {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	h.logger.Error("sentry: project lookup failed", "err", err)
	w.Header().Set("Retry-After", dbErrorRetryAfter)
	writeError(w, http.StatusServiceUnavailable, "temporarily unavailable")
}

// queueFull answers 429 and logs a warning, at most once per queueFullLogEvery.
func (h *SentryHandler) queueFull(w http.ResponseWriter) {
	now := time.Now().UnixNano()
	last := h.lastQueueFullLog.Load()
	if now-last >= int64(queueFullLogEvery) && h.lastQueueFullLog.CompareAndSwap(last, now) {
		h.logger.Warn("sentry: ingest queue is full; answering 429 until the writer catches up", "capacity", h.queue.Cap())
	}
	w.Header().Set("Retry-After", queueFullRetryAfter)
	writeError(w, http.StatusTooManyRequests, "too busy; try again in a few seconds")
}

// decodeBody returns the request body, decompressed per Content-Encoding and
// limited to MaxBodyBytes both before and after decompression. Going over the
// limit shows up as a read error wrapping errBodyTooLarge or *http.MaxBytesError.
func decodeBody(w http.ResponseWriter, r *http.Request) (io.Reader, error) {
	raw := http.MaxBytesReader(w, r.Body, MaxBodyBytes)

	var decoded io.Reader
	switch encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); encoding {
	case "", "identity":
		decoded = raw
	case "gzip", "x-gzip":
		zr, err := gzip.NewReader(raw)
		if err != nil {
			return nil, fmt.Errorf("body is not valid gzip: %v", err)
		}
		decoded = zr
	case "deflate":
		dr, err := deflateReader(raw)
		if err != nil {
			return nil, fmt.Errorf("body is not valid deflate: %v", err)
		}
		decoded = dr
	default:
		return nil, fmt.Errorf("Content-Encoding %q is not supported; use gzip or deflate", encoding)
	}
	return &limitedReader{r: decoded, limit: MaxBodyBytes}, nil
}

// deflateReader reads "deflate" bodies: zlib-wrapped, as HTTP specifies, or
// raw deflate, which some clients send instead.
func deflateReader(r io.Reader) (io.Reader, error) {
	br := bufio.NewReader(r)
	head, _ := br.Peek(2)
	isZlib := len(head) == 2 && head[0]&0x0f == 8 && (uint16(head[0])<<8|uint16(head[1]))%31 == 0
	if isZlib {
		return zlib.NewReader(br)
	}
	return flate.NewReader(br), nil
}

// limitedReader fails with errBodyTooLarge once more than limit bytes came out of r.
type limitedReader struct {
	r     io.Reader
	limit int64
	read  int64
}

func (l *limitedReader) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	l.read += int64(n)
	if l.read > l.limit {
		return 0, errBodyTooLarge
	}
	return n, err
}

// newEventID returns a random event id for events that came without one.
func newEventID() string {
	b := make([]byte, 16)
	rand.Read(b) // never fails; see crypto/rand
	return hex.EncodeToString(b)
}

// badRequest answers 400 and, as Sentry does, repeats the reason in X-Sentry-Error.
func badRequest(w http.ResponseWriter, reason string) {
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 200 {
		reason = reason[:200]
	}
	w.Header().Set("X-Sentry-Error", reason)
	writeError(w, http.StatusBadRequest, reason)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
