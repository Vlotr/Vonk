package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"monitor/internal/httpjson"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

const (
	defaultLimit = 25
	maxLimit     = 100
	// maxSearch is the longest search the issue list takes, in characters.
	maxSearch = 200
	// maxRelease is the longest release the issue list filters on, in
	// characters (Sentry allows 200).
	maxRelease = 200
	// hoursShown is the length of the "Last 24 hours" bars.
	hoursShown = 24
	// newWithin is how recently an issue was first seen to count as new.
	newWithin = 24 * time.Hour
)

// issueJSON is an issue as the issue list shows it.
type issueJSON struct {
	ID int64 `json:"id"`
	// Type is the exception type as sent, such as the full PHP class
	// Illuminate\Database\QueryException (the dashboard shortens it), or
	// "Unhandled rejection" for an unhandled promise rejection.
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
	Culprit   string    `json:"culprit"`
	Events    int64     `json:"events"`
	Users     int       `json:"users"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	IsNew     bool      `json:"isNew"`
	// Hourly counts the events per clock hour, oldest first; the last entry is the current hour.
	Hourly []int `json:"hourly"`
}

// issueDetailJSON is an issue on its own page.
type issueDetailJSON struct {
	issueJSON
	Status    string `json:"status"`
	ProjectID int64  `json:"projectId"`
	// Release is the release the issue was first seen in (first_release);
	// left out when that event had none.
	Release string `json:"release,omitempty"`
	// RegressedRelease is the release of the event that last opened the
	// issue again after a resolve; left out when it never came back (or
	// that event had no release).
	RegressedRelease string `json:"regressedRelease,omitempty"`
}

type statusCountsJSON struct {
	Open     int `json:"open"`
	Resolved int `json:"resolved"`
	Ignored  int `json:"ignored"`
}

type projectRefJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

// issueListJSON is one page of the issue list. Counts and Total follow the
// filters (source, q, route); OpenIssues and New24h describe the whole
// project, for the headline above them.
type issueListJSON struct {
	Project projectRefJSON `json:"project"`
	// Counts are the issues per status that match the filters: the status tabs.
	Counts statusCountsJSON `json:"counts"`
	// OpenIssues counts the project's open issues, ignoring the filters.
	OpenIssues int `json:"openIssues"`
	// New24h counts the project's open issues first seen in the last 24 hours, ignoring the filters.
	New24h int `json:"new24h"`
	// Total counts the issues with the requested status that match the filters, for paging.
	Total  int         `json:"total"`
	Issues []issueJSON `json:"issues"`
}

type issueDetailResponse struct {
	Issue  issueDetailJSON `json:"issue"`
	Hourly []int           `json:"hourly"`
	// LatestEvent is null when the issue has no stored event left.
	LatestEvent *eventJSON `json:"latestEvent"`
	// BeforeError are the pages the latest event's user saw in the 30
	// minutes before it, oldest first ("Before this error"); empty when the
	// event has no user id or the user no page views.
	BeforeError []journeyStepJSON `json:"beforeError"`
}

// listIssues answers GET /ui/api/projects/{id}/issues with the query
// parameters status, source, sort, q, route, limit and offset (see parseIssueFilter).
func (h *Handler) listIssues(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	// The query is checked before the project is looked up, so a bad
	// request answers the same whether the project exists or not.
	filter, err := parseIssueFilter(r.URL.Query())
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.ProjectID = projectID
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

	now := h.now()
	counts, err := h.db.CountIssueStatuses(ctx, filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	projectCounts := counts
	if filter.Source != "" || filter.Search != "" || filter.Route != "" || filter.Release != "" {
		if projectCounts, err = h.db.CountIssueStatuses(ctx, store.IssueFilter{ProjectID: projectID}); err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	newCount, err := h.db.CountNewIssues(ctx, projectID, store.IssueOpen, now.Add(-newWithin))
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	rows, err := h.db.ListIssuePage(ctx, filter)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	ids := make([]int64, len(rows))
	for i, row := range rows {
		ids[i] = row.ID
	}
	hourly, err := h.hourly(ctx, ids, now)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	list := issueListJSON{
		Project:    projectRefJSON{ID: project.ID, Name: project.Name},
		Counts:     statusCountsJSON{Open: counts.Open, Resolved: counts.Resolved, Ignored: counts.Ignored},
		OpenIssues: projectCounts.Open,
		New24h:     newCount,
		Total:      counts.Of(filter.Status),
		Issues:     make([]issueJSON, 0, len(rows)),
	}
	for _, row := range rows {
		list.Issues = append(list.Issues, toIssueJSON(row, hourly[row.ID], now))
	}
	httpjson.Write(w, http.StatusOK, list)
}

// issueSources are the sources the issue list filters on, as the Source select offers them.
var issueSources = map[string]bool{"laravel": true, "browser": true}

// parseIssueFilter reads the issue list's query parameters, with their defaults:
//
//	status  open (default), resolved or ignored
//	source  laravel or browser; empty for all
//	sort    last_seen (default), first_seen, events or users
//	q       part of the title, message, culprit or an event's URL; at most 200 characters
//	route   issues with an event on this route
//	release issues with an event in this release; at most 200 characters
//	limit   1 to 100, default 25
//	offset  0 or more
func parseIssueFilter(q url.Values) (store.IssueFilter, error) {
	f := store.IssueFilter{Status: store.IssueOpen, Sort: store.SortLastSeen, Limit: defaultLimit}
	if s := q.Get("status"); s != "" {
		if !store.ValidIssueStatus(s) {
			return f, errors.New(`status must be "open", "resolved" or "ignored"`)
		}
		f.Status = s
	}
	if s := q.Get("source"); s != "" {
		if !issueSources[s] {
			return f, errors.New(`source must be "laravel" or "browser"`)
		}
		f.Source = s
	}
	if s := q.Get("sort"); s != "" {
		if !store.ValidIssueSort(s) {
			return f, errors.New(`sort must be "last_seen", "first_seen", "events" or "users"`)
		}
		f.Sort = s
	}
	f.Search = strings.TrimSpace(q.Get("q"))
	if utf8.RuneCountInString(f.Search) > maxSearch {
		return f, fmt.Errorf("q must be at most %d characters", maxSearch)
	}
	f.Route = q.Get("route")
	f.Release = q.Get("release")
	if utf8.RuneCountInString(f.Release) > maxRelease {
		return f, fmt.Errorf("release must be at most %d characters", maxRelease)
	}
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > maxLimit {
			return f, fmt.Errorf("limit must be a number from 1 to %d", maxLimit)
		}
		f.Limit = n
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return f, errors.New("offset must be a number of 0 or more")
		}
		f.Offset = n
	}
	return f, nil
}

// issueDetail answers GET /ui/api/issues/{id}: the issue, its last 24 hours,
// its newest event with the stack trace, and the pages that event's user
// saw before it.
func (h *Handler) issueDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	row, err := h.db.IssueRowByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !canSee(r, row.ProjectID)) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	now := h.now()
	hourly, err := h.hourly(ctx, []int64{id}, now)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	issue := toIssueJSON(row, hourly[id], now)
	resp := issueDetailResponse{
		Issue: issueDetailJSON{issueJSON: issue, Status: row.Status, ProjectID: row.ProjectID,
			Release: row.FirstRelease, RegressedRelease: row.RegressedRelease},
		Hourly:      issue.Hourly,
		BeforeError: []journeyStepJSON{},
	}

	stored, err := h.db.LatestErrorEvent(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No event left; the page shows the issue without a stack trace.
	case err != nil:
		h.internalError(w, r, err)
		return
	default:
		var symbolicate func(*sentry.Event)
		if h.maps != nil {
			symbolicate = func(ev *sentry.Event) { h.maps.Symbolicate(ctx, row.ProjectID, ev) }
		}
		ev, err := eventDetail(stored, symbolicate)
		if err != nil {
			// Stored events were valid JSON when they arrived; log rather than fail the page.
			h.log.Warn("dashboard api: stored event cannot be read", "issue", id, "event", stored.EventID, "err", err)
		}
		resp.LatestEvent = ev
		if resp.BeforeError, err = h.beforeError(ctx, stored); err != nil {
			h.internalError(w, r, err)
			return
		}
	}
	httpjson.Write(w, http.StatusOK, resp)
}

// hourly returns, for each issue, its events per clock hour over the last
// hoursShown hours, oldest first. The current hour is the last bucket.
func (h *Handler) hourly(ctx context.Context, issueIDs []int64, now time.Time) (map[int64][]int, error) {
	counts, err := h.db.HourlyEventCounts(ctx, issueIDs, firstHourStart(now))
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]int, len(issueIDs))
	for _, id := range issueIDs {
		result[id] = hourBuckets(counts[id], now)
	}
	return result, nil
}

// firstHourStart is the start of the oldest of the hoursShown clock hours
// that end with the current one.
func firstHourStart(now time.Time) time.Time {
	return time.Unix((now.Unix()/3600-(hoursShown-1))*3600, 0)
}

// hourBuckets turns counts per hour (unix time divided by one hour) into
// hoursShown buckets, oldest first. Earlier hours are left out; events up to
// an hour ahead of the clock (a client clock that is off) are counted in
// the current hour.
func hourBuckets(counts map[int64]int, now time.Time) []int {
	firstHour := firstHourStart(now).Unix() / 3600
	buckets := make([]int, hoursShown)
	for hour, n := range counts {
		if i := min(int(hour-firstHour), hoursShown-1); i >= 0 {
			buckets[i] += n
		}
	}
	return buckets
}

func toIssueJSON(row store.IssueRow, hourly []int, now time.Time) issueJSON {
	if hourly == nil {
		hourly = make([]int, hoursShown)
	}
	return issueJSON{
		ID:        row.ID,
		Type:      row.Title,
		Message:   row.Message,
		Source:    row.Source,
		Culprit:   row.Culprit,
		Events:    row.TimesSeen,
		Users:     row.Users,
		FirstSeen: row.FirstSeen,
		LastSeen:  row.LastSeen,
		IsNew:     now.Sub(row.FirstSeen) <= newWithin,
		Hourly:    hourly,
	}
}
