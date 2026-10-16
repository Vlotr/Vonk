package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"monitor/internal/store"
)

const (
	defaultLimit = 25
	maxLimit     = 100
	// hoursShown is the length of the "Last 24 hours" bars.
	hoursShown = 24
	// newWithin is how recently an issue was first seen to count as new.
	newWithin = 24 * time.Hour
)

// issueJSON is an issue as the issue list shows it.
type issueJSON struct {
	ID int64 `json:"id"`
	// Type is the exception type as sent, such as the full PHP class
	// Illuminate\Database\QueryException; the dashboard shortens it.
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

type issueListJSON struct {
	Project projectRefJSON   `json:"project"`
	Counts  statusCountsJSON `json:"counts"`
	// New24h counts the issues with the requested status first seen in the last 24 hours.
	New24h int `json:"new24h"`
	// Total counts the issues with the requested status, for paging.
	Total  int         `json:"total"`
	Issues []issueJSON `json:"issues"`
}

type issueDetailResponse struct {
	Issue  issueDetailJSON `json:"issue"`
	Hourly []int           `json:"hourly"`
	// LatestEvent is null when the issue has no stored event left.
	LatestEvent *eventJSON `json:"latestEvent"`
}

// listIssues answers GET /ui/api/projects/{id}/issues?status=open&limit=25&offset=0.
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
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	filter.ProjectID = projectID

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
	counts, err := h.db.CountIssueStatuses(ctx, projectID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	newCount, err := h.db.CountNewIssues(ctx, projectID, filter.Status, now.Add(-newWithin))
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
		Project: projectRefJSON{ID: project.ID, Name: project.Name},
		Counts:  statusCountsJSON{Open: counts.Open, Resolved: counts.Resolved, Ignored: counts.Ignored},
		New24h:  newCount,
		Issues:  make([]issueJSON, 0, len(rows)),
	}
	switch filter.Status {
	case store.IssueOpen:
		list.Total = counts.Open
	case store.IssueResolved:
		list.Total = counts.Resolved
	case store.IssueIgnored:
		list.Total = counts.Ignored
	}
	for _, row := range rows {
		list.Issues = append(list.Issues, toIssueJSON(row, hourly[row.ID], now))
	}
	writeJSON(w, http.StatusOK, list)
}

// parseIssueFilter reads status, limit and offset, with their defaults.
func parseIssueFilter(q url.Values) (store.IssueFilter, error) {
	f := store.IssueFilter{Status: store.IssueOpen, Limit: defaultLimit}
	if s := q.Get("status"); s != "" {
		if s != store.IssueOpen && s != store.IssueResolved && s != store.IssueIgnored {
			return f, errors.New(`status must be "open", "resolved" or "ignored"`)
		}
		f.Status = s
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

// issueDetail answers GET /ui/api/issues/{id}: the issue, its last 24 hours
// and its newest event with the stack trace.
func (h *Handler) issueDetail(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	row, err := h.db.IssueRowByID(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
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
		Issue:  issueDetailJSON{issueJSON: issue, Status: row.Status, ProjectID: row.ProjectID},
		Hourly: issue.Hourly,
	}

	stored, err := h.db.LatestErrorEvent(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No event left; the page shows the issue without a stack trace.
	case err != nil:
		h.internalError(w, r, err)
		return
	default:
		ev, err := eventDetail(stored)
		if err != nil {
			// Stored events were valid JSON when they arrived; log rather than fail the page.
			h.log.Warn("dashboard api: stored event cannot be read", "issue", id, "event", stored.EventID, "err", err)
		}
		resp.LatestEvent = ev
	}
	writeJSON(w, http.StatusOK, resp)
}

// hourly returns, for each issue, its events per clock hour over the last
// hoursShown hours, oldest first. The current hour is the last bucket.
func (h *Handler) hourly(ctx context.Context, issueIDs []int64, now time.Time) (map[int64][]int, error) {
	firstHour := now.Unix()/3600 - (hoursShown - 1)
	counts, err := h.db.HourlyEventCounts(ctx, issueIDs, time.Unix(firstHour*3600, 0))
	if err != nil {
		return nil, err
	}
	result := make(map[int64][]int, len(issueIDs))
	for _, id := range issueIDs {
		buckets := make([]int, hoursShown)
		for hour, n := range counts[id] {
			// Events up to an hour ahead of the clock (a client clock that is
			// off) are counted in the current hour.
			i := min(int(hour-firstHour), hoursShown-1)
			if i >= 0 {
				buckets[i] += n
			}
		}
		result[id] = buckets
	}
	return result, nil
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
