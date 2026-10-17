package ingest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"monitor/internal/bots"
	"monitor/internal/httpjson"
	"monitor/internal/posthog"
	"monitor/internal/routes"
	"monitor/internal/store"
)

// PosthogPrefixes are the paths PostHog SDKs call, relative to their
// api_host: the capture endpoints (/i/v0/e/, /e/, /capture/, /batch/),
// flags (/flags/, the older /decide/), the remote configuration
// (/array/<token>/config and config.js), the scripts of extensions
// (/static/...) and session recordings (/s/).
var PosthogPrefixes = []string{"/i/", "/e/", "/capture/", "/batch/", "/flags/", "/decide/", "/array/", "/static/", "/s/"}

// RegisterPosthog puts h on mux for every PostHog path, with and without
// the trailing slash (some SDKs leave it out, and a redirect would turn
// their POST into a GET).
func RegisterPosthog(mux *http.ServeMux, h http.Handler) {
	for _, prefix := range PosthogPrefixes {
		mux.Handle(prefix, h)
		mux.Handle(strings.TrimSuffix(prefix, "/"), h)
	}
}

// IsPosthogPath reports whether path is one RegisterPosthog routes to the
// PostHog handler.
func IsPosthogPath(path string) bool {
	for _, prefix := range PosthogPrefixes {
		if strings.HasPrefix(path+"/", prefix) {
			return true
		}
	}
	return false
}

// PosthogHandler accepts what PostHog SDKs send. Every project token is
// accepted, whether or not the project shows page views. Page views, page
// leaves and custom events (names without "$") are scrubbed and queued for
// the writer; PostHog's other own events ($identify, $autocapture, ...)
// are accepted and not stored. It answers 200, or a 4xx for traffic it
// cannot use; 503 only when the database fails.
type PosthogHandler struct {
	db      *store.DB
	queue   *Queue
	limiter *RateLimiter
	logger  *slog.Logger
	now     func() time.Time
	full    *fullQueue
	lookups *failedLookups
}

// NewPosthogHandler returns the PostHog handler. Accepted page events go
// on queue, the one the Sentry handler uses; limiter holds the projects'
// events-per-minute limits for PostHog traffic.
func NewPosthogHandler(db *store.DB, queue *Queue, limiter *RateLimiter, logger *slog.Logger) *PosthogHandler {
	return &PosthogHandler{db: db, queue: queue, limiter: limiter, logger: logger, now: time.Now,
		full: &fullQueue{queue: queue, logger: logger, prefix: "posthog"}, lookups: &failedLookups{logger: logger, prefix: "posthog"}}
}

func (h *PosthogHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowPosthogCORS(w.Header())
	switch r.Method {
	case http.MethodGet, http.MethodPost:
	case http.MethodOptions:
		posthogPreflight(w, r)
		return
	default:
		w.Header().Set("Allow", posthogAllowMethods)
		httpjson.Error(w, http.StatusMethodNotAllowed, "PostHog endpoints take GET and POST")
		return
	}

	path := strings.TrimSuffix(r.URL.Path, "/") + "/"
	switch {
	case path == "/i/v0/e/" || path == "/e/" || path == "/capture/" || path == "/batch/":
		h.capture(w, r)
	case path == "/flags/":
		httpjson.Write(w, http.StatusOK, posthog.Flags())
	case path == "/decide/":
		httpjson.Write(w, http.StatusOK, posthog.Decide())
	case strings.HasPrefix(path, "/array/"):
		h.config(w, strings.TrimSuffix(strings.TrimPrefix(path, "/array/"), "/"))
	case strings.HasPrefix(path, "/static/"):
		// Extensions this tool turns off (surveys, recording, ...): an
		// empty script, so posthog-js loads nothing and logs no failure.
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		io.WriteString(w, "// Not served by this monitor: the extension is turned off.\n")
	case strings.HasPrefix(path, "/s/"):
		httpjson.Write(w, http.StatusOK, map[string]string{"status": "Ok"}) // session recordings: not kept
	default:
		h.logger.Debug("posthog: unknown path; answered 200", "method", r.Method, "path", httpjson.LogPath(r.URL.Path))
		httpjson.Write(w, http.StatusOK, map[string]string{})
	}
}

// config answers /array/<token>/config and /array/<token>/config.js.
func (h *PosthogHandler) config(w http.ResponseWriter, rest string) {
	token, file, ok := strings.Cut(rest, "/")
	if !ok || !isToken(token) || (file != "config" && file != "config.js") {
		h.logger.Debug("posthog: unknown path; answered 404", "path", "/array/"+rest)
		httpjson.Error(w, http.StatusNotFound, "not found")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=300")
	if file == "config" {
		httpjson.Write(w, http.StatusOK, posthog.RemoteConfig(token))
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	io.WriteString(w, posthog.RemoteConfigJS(token))
}

// isToken reports whether s can be a project token ("phc_" and URL-safe base64).
func isToken(s string) bool {
	if s == "" || len(s) > 200 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// capture accepts a batch of events.
func (h *PosthogHandler) capture(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	body, err := readBody(w, r)
	if err != nil {
		h.bodyError(w, err)
		return
	}
	query := r.URL.Query()
	batch, err := posthog.ParseBody(body, posthog.Options{
		ContentType:   r.Header.Get("Content-Type"),
		Compression:   query.Get("compression"),
		Data:          query.Get("data"),
		MaxBytes:      MaxBodyBytes,
		MaxEventBytes: MaxEventBytes,
	})
	if err != nil {
		h.bodyError(w, err)
		return
	}
	if batch.Dropped > 0 {
		h.logger.Debug("posthog: dropped events larger than 1 MiB or without a name", "events", batch.Dropped)
	}

	// Each event belongs to the project of its own token, else to the
	// batch's. One unknown token refuses the whole request.
	projects := map[string]store.Project{}
	eventProjects := make([]store.Project, len(batch.Events))
	for i, ev := range batch.Events {
		token := ev.Token
		if token == "" {
			token = batch.Token
		}
		p, ok := projects[token]
		if !ok {
			p, err = h.project(ctx, token)
			if err != nil {
				h.lookups.answer(w, r, err)
				return
			}
			projects[token] = p
		}
		eventProjects[i] = p
	}

	// Every event counts against its project's limit; page views, page
	// leaves and custom events are queued. A refused request leaves nothing
	// behind: the queue's room is checked first, every project's limit
	// before any is counted, and the budgets are given back when the queue
	// filled up in between.
	perProject := map[int64]int{}
	var stored []int // the events to queue
	var size int64
	for i, ev := range batch.Events {
		perProject[eventProjects[i].ID]++
		if ev.Name == posthog.EventPageview || ev.Name == posthog.EventPageleave || posthog.IsCustom(ev.Name) {
			stored = append(stored, i)
			size += int64(ev.Size)
		}
	}
	if !h.queue.HasRoom(len(stored), size) {
		h.full.answer(w)
		return
	}
	asks := make([]Ask, 0, len(projects))
	for _, p := range projects { // one token per project, so each project once
		asks = append(asks, Ask{ProjectID: p.ID, Limit: p.RateLimitPerMinute, Events: perProject[p.ID]})
	}
	minute, refused, wait := h.limiter.AllowAll(asks)
	if refused != nil {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(wait.Seconds())))))
		httpjson.Error(w, http.StatusTooManyRequests,
			fmt.Sprintf("This project accepts %d events a minute. Try again after the Retry-After time.", refused.Limit))
		return
	}

	received := h.now()
	userAgent := r.Header.Get("User-Agent")
	items := make([]Item, 0, len(stored))
	for _, i := range stored {
		ev, p := &batch.Events[i], eventProjects[i]
		ev.Scrub()
		item := Item{ProjectID: p.ID, ReceivedAt: received}
		switch ev.Name {
		case posthog.EventPageleave:
			item.Page = ev
			items = append(items, item)
			continue
		case posthog.EventPageview:
			item.Page = ev
		default:
			item.Custom = ev
		}
		// A custom event sent from a server has no page.
		if item.Page != nil || ev.URL != "" || ev.Pathname != "" {
			item.Path = pagePath(ev)
			// From the stored path, so the raw paths on Edit project preview the routes exactly.
			item.Route = routes.Normalize(item.Path, p.RoutePatterns)
		}
		item.IsBot = bots.IsBot(userAgent) || bots.IsBot(ev.RawUserAgent) || ev.BrowserType == "bot"
		items = append(items, item)
	}
	if !h.queue.TryEnqueueAll(items) {
		h.limiter.Return(minute, asks)
		h.full.answer(w)
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]string{"status": "Ok"})
}

// errUnknownToken means the token is missing or no project has it.
var errUnknownToken = errors.New("unknown project token (api_key)")

// project returns the project with this token, errUnknownToken, or a database error.
func (h *PosthogHandler) project(ctx context.Context, token string) (store.Project, error) {
	if token == "" {
		return store.Project{}, errUnknownToken
	}
	p, err := h.db.ProjectByPosthogToken(ctx, token)
	if errors.Is(err, store.ErrNotFound) {
		return store.Project{}, errUnknownToken
	}
	return p, err
}

// bodyError answers 413 for a body over the limit and 400 for anything else.
func (h *PosthogHandler) bodyError(w http.ResponseWriter, err error) {
	var maxBytes *http.MaxBytesError
	if errors.Is(err, errBodyTooLarge) || errors.Is(err, posthog.ErrTooLarge) || errors.As(err, &maxBytes) {
		httpjson.Error(w, http.StatusRequestEntityTooLarge, "body larger than 20 MiB")
		return
	}
	reason := strings.Join(strings.Fields(err.Error()), " ")
	if len(reason) > 200 {
		reason = reason[:200]
	}
	httpjson.Error(w, http.StatusBadRequest, reason)
}

// readBody reads the request body, undoing Content-Encoding, within
// MaxBodyBytes before and after decompression.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	reader, err := decodeBody(w, r)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(reader)
}

// pagePath returns the path of the page an event happened on, as it is
// stored: from $current_url, else $pathname; never the query or fragment,
// segments decoded, and the secrets in it masked (routes.Path, ruling 38).
func pagePath(ev *posthog.Event) string {
	if ev.URL != "" {
		return routes.Path(ev.URL)
	}
	return routes.Path(ev.Pathname)
}
