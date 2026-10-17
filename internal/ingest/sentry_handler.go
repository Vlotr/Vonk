package ingest

import (
	"bufio"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/routes"
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
	full    *fullQueue
	lookups *failedLookups

	// Symbolicate, when set, maps an accepted event's minified frames back
	// to the original code with the project's source maps
	// (sourcemaps.Service.Symbolicate). It runs before scrubbing, while
	// the frames still have the URLs the maps were uploaded for, so the
	// writer groups the event by its original frames.
	Symbolicate func(ctx context.Context, projectID int64, ev *sentry.Event) bool
}

// NewSentryHandler returns the envelope handler. Accepted events go on queue.
func NewSentryHandler(db *store.DB, queue *Queue, limiter *RateLimiter, logger *slog.Logger) *SentryHandler {
	return &SentryHandler{db: db, queue: queue, limiter: limiter, logger: logger, now: time.Now,
		full: &fullQueue{queue: queue, logger: logger, prefix: "sentry"}, lookups: &failedLookups{logger: logger, prefix: "sentry"}}
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
		httpjson.Error(w, http.StatusMethodNotAllowed, "envelopes are sent with POST")
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
			h.lookups.answer(w, r, err)
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
			httpjson.Error(w, http.StatusRequestEntityTooLarge, "envelope larger than 20 MiB")
			return
		}
		badRequest(w, err.Error())
		return
	}

	if project.ID == 0 {
		p, err := authenticate(ctx, h.db, keyFromDSN(env.Header.DSN), projectID)
		if err != nil {
			h.lookups.answer(w, r, err)
			return
		}
		project = p
	}
	if env.Dropped > 0 {
		h.logger.Debug("sentry: dropped events larger than 1 MiB", "project", project.ID, "events", env.Dropped)
	}
	if len(env.Events) == 0 {
		httpjson.Write(w, http.StatusOK, map[string]string{}) // sessions, client reports, ...
		return
	}

	// A refused envelope leaves nothing behind: the queue's room is checked
	// before the budget is counted, and the budget is given back when the
	// queue filled up in between.
	var size int64
	for _, ev := range env.Events {
		size += int64(ev.Size)
	}
	if !h.queue.HasRoom(len(env.Events), size) {
		h.full.answer(w)
		return
	}
	asks := []Ask{{ProjectID: project.ID, Limit: project.RateLimitPerMinute, Events: len(env.Events)}}
	minute, refused, wait := h.limiter.AllowAll(asks)
	if refused != nil {
		seconds := strconv.Itoa(max(1, int(math.Ceil(wait.Seconds()))))
		w.Header().Set("Retry-After", seconds)
		w.Header().Set("X-Sentry-Rate-Limits", seconds+"::project")
		httpjson.Error(w, http.StatusTooManyRequests,
			fmt.Sprintf("this project accepts %d events a minute", project.RateLimitPerMinute))
		return
	}

	received := h.now()
	sentAt := env.Header.SentTime() // the writer moves event times to the server's clock with it
	items := make([]Item, len(env.Events))
	for i, ev := range env.Events {
		if ev.EventID == "" {
			ev.SetEventID(newEventID())
		}
		if h.Symbolicate != nil {
			h.Symbolicate(ctx, project.ID, ev)
		}
		scrub.Event(ev)
		items[i] = Item{ProjectID: project.ID, ReceivedAt: received, SentAt: sentAt, Event: ev, Route: errorRoute(ev, project.RoutePatterns)}
	}
	if !h.queue.TryEnqueueAll(items) {
		h.limiter.Return(minute, asks)
		h.full.answer(w)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"id": env.Events[0].EventID})
}

// errorRoute returns the route of the page an error happened on: from the
// request URL (server SDKs send the request, browser SDKs the page), else
// the "url" tag; "" when the event names no page.
func errorRoute(ev *sentry.Event, patterns []string) string {
	page := ev.Request.URL
	if page == "" {
		page = ev.Tags["url"]
	}
	if page == "" {
		return ""
	}
	return routes.Normalize(page, patterns)
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
	httpjson.Error(w, http.StatusBadRequest, reason)
}
