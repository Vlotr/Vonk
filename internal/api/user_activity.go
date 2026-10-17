package api

import (
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/routes"
	"monitor/internal/store"
)

// The user activity page: GET /ui/api/projects/{id}/users/{userId}/activity
// ?before=<RFC 3339>&limit=<1 to 100> answers what one user id did in a
// project, the newest first: page views (bots left out), error events and
// custom events. The user id is a path segment, percent-encoded (ids hold
// dots, @ and even slashes). next is the before= of the following, older
// page, or null after the last.

// defaultActivityLimit is how many items a page of activity has.
const defaultActivityLimit = 50

type activityJSON struct {
	Project projectRefJSON     `json:"project"`
	UserID  string             `json:"userId"`
	Items   []activityItemJSON `json:"items"`
	// Next is the before= of the next (older) page; null when there is none.
	Next *time.Time `json:"next"`
}

// activityItemJSON is one thing the user did. Kind is "pageview", "error"
// or "event"; fields of other kinds are left out.
type activityItemJSON struct {
	Kind string    `json:"kind"`
	At   time.Time `json:"at"`
	// Path is the page's address (secrets masked), Route its route.
	Path  string `json:"path,omitempty"`
	Route string `json:"route,omitempty"`
	// DurationMs is a page view's time on the page; left out without one.
	DurationMs *int64 `json:"durationMs,omitempty"`
	// IssueID, Type and Message are an error's issue.
	IssueID int64  `json:"issueId,omitempty"`
	Type    string `json:"type,omitempty"`
	Message string `json:"message,omitempty"`
	// Name and Properties are a custom event's (its own properties, scrubbed).
	Name       string         `json:"name,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

// userActivity answers GET /ui/api/projects/{id}/users/{userId}/activity.
func (h *Handler) userActivity(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	before, limit, err := parseActivityQuery(r)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if !canSee(r, projectID) {
		notFound(w)
		return
	}
	ctx := r.Context()
	project, err := h.db.ProjectByID(ctx, projectID)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	userID := r.PathValue("userId")

	// One more than a page tells whether an older page follows.
	items, err := h.db.UserActivity(ctx, projectID, userID, before, limit+1)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	page, next := activityPage(items, limit)

	resp := activityJSON{Project: projectRefJSON{ID: project.ID, Name: project.Name}, UserID: userID,
		Items: make([]activityItemJSON, 0, len(page)), Next: next}
	for _, a := range page {
		resp.Items = append(resp.Items, h.activityItem(a))
	}
	httpjson.Write(w, http.StatusOK, resp)
}

// parseActivityQuery reads before= (RFC 3339; none means from now on back)
// and limit= (1 to 100, default 50).
func parseActivityQuery(r *http.Request) (before time.Time, limit int, err error) {
	q := r.URL.Query()
	before = time.UnixMilli(math.MaxInt64 / 2) // no end: anything stored
	if s := q.Get("before"); s != "" {
		if before, err = time.Parse(time.RFC3339Nano, s); err != nil {
			return before, 0, errors.New("before must be a time such as 2026-10-16T12:30:00Z")
		}
	}
	limit = defaultActivityLimit
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			return before, 0, errors.New("limit must be a number from 1 to 100")
		}
		limit = n
	}
	return before, limit, nil
}

// activityPage cuts items (newest first, up to limit+1) to one page and
// returns the next page's before. Items with the same time never straddle
// two pages, since before= is exclusive: when they would, the page ends
// before them and the next one starts with them. Only a page that is all
// one time (more than limit items in one millisecond) is cut there.
func activityPage(items []store.Activity, limit int) ([]store.Activity, *time.Time) {
	if len(items) <= limit {
		return items, nil
	}
	page := items[:limit]
	last := page[len(page)-1].At
	if items[limit].At.Equal(last) {
		cut := len(page)
		for cut > 0 && page[cut-1].At.Equal(last) {
			cut--
		}
		if cut > 0 {
			next := last.Add(time.Millisecond) // includes the items at last
			return page[:cut], &next
		}
	}
	return page, &last
}

func (h *Handler) activityItem(a store.Activity) activityItemJSON {
	item := activityItemJSON{Kind: a.Kind, At: a.At, Path: a.Path, Route: a.Route}
	switch a.Kind {
	case store.ActivityPageview:
		item.DurationMs = a.DurationMs
	case store.ActivityError:
		item.IssueID, item.Type, item.Message = a.IssueID, a.Title, a.Message
		if a.URL != "" {
			item.Path = routes.Path(a.URL)
		}
	case store.ActivityEvent:
		item.Name = a.Name
		if a.Properties != "" && a.Properties != "{}" {
			if err := json.Unmarshal([]byte(a.Properties), &item.Properties); err != nil {
				h.log.Warn("dashboard api: stored custom event properties cannot be read", "err", err)
			}
		}
	}
	return item
}
