package api

import (
	"net/http"
	"sort"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// newestIssuesShown is how many issues the overview's Newest issues card lists.
const newestIssuesShown = 5

// overviewJSON is the All projects page.
type overviewJSON struct {
	// UpdatedAt is when the numbers were read, for "Updated 10:42."
	UpdatedAt time.Time             `json:"updatedAt"`
	Projects  []overviewProjectJSON `json:"projects"`
	// NewestIssues are the open issues first seen in the last 24 hours,
	// across projects, the most recently seen first.
	NewestIssues []newestIssueJSON `json:"newestIssues"`
}

type overviewProjectJSON struct {
	ID      int64    `json:"id"`
	Name    string   `json:"name"`
	Sources []string `json:"sources"`
	// NewIssues24h counts the open issues first seen in the last 24 hours (the "6 new" tag).
	NewIssues24h int `json:"newIssues24h"`
	// Errors24h counts every error event of the last 24 hours, whatever its issue's status.
	Errors24h int `json:"errors24h"`
	// Hourly counts the error events per clock hour, oldest first; the last entry is the current hour.
	Hourly []int `json:"hourly"`
	// Pageviews24h and AvgTimeOnPageMs are the page views by people of the
	// last 24 clock hours and their average time on a page, for projects
	// that track page views; null for the others ("Add page tracking").
	// AvgTimeOnPageMs is also null while no page view has a time.
	Pageviews24h    *int64 `json:"pageviews24h"`
	AvgTimeOnPageMs *int64 `json:"avgTimeOnPageMs"`
}

type newestIssueJSON struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"projectId"`
	ProjectName string `json:"projectName"`
	// Type is the exception type as sent, like the issue list's type.
	Type     string    `json:"type"`
	Message  string    `json:"message"`
	LastSeen time.Time `json:"lastSeen"`
}

// overview answers GET /ui/api/overview: per project the new issues and
// errors of the last 24 hours, and the newest issues across projects.
// Projects with new issues come first, then the busiest, then by name.
func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	// Only the projects this user may see: every count below is limited to them.
	projects, err := h.visibleProjects(r)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	ids := make([]int64, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}

	now := h.now()
	since := now.Add(-24 * time.Hour)
	newCounts, err := h.db.CountNewIssuesPerProject(ctx, ids, store.IssueOpen, since)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	// From 24 h ago, a little before the oldest bar: errors24h covers
	// exactly 24 hours, the bars the 24 clock hours that end now.
	hours, err := h.db.HourlyProjectEventCounts(ctx, ids, since)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	newest, err := h.db.NewIssuesAcrossProjects(ctx, ids, store.IssueOpen, since, newestIssuesShown)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	// Page views of the 24 clock hours that end with the current one, from
	// the hourly summaries, for the projects that track them.
	var tracking []int64
	for _, p := range projects {
		if p.TracksPageviews {
			tracking = append(tracking, p.ID)
		}
	}
	thisHour := now.UTC().Truncate(time.Hour)
	pages, err := h.db.PageviewTotals(ctx, tracking, thisHour.Add(-23*time.Hour), thisHour.Add(time.Hour))
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	resp := overviewJSON{
		UpdatedAt:    now,
		Projects:     make([]overviewProjectJSON, 0, len(projects)),
		NewestIssues: make([]newestIssueJSON, 0, len(newest)),
	}
	for _, p := range projects {
		events := 0
		for _, n := range hours[p.ID] {
			events += n
		}
		row := overviewProjectJSON{
			ID:           p.ID,
			Name:         p.Name,
			Sources:      sources(p),
			NewIssues24h: newCounts[p.ID],
			Errors24h:    events,
			Hourly:       hourBuckets(hours[p.ID], now),
		}
		if p.TracksPageviews {
			t := pages[p.ID] // zero without data
			row.Pageviews24h = &t.Views
			if t.DurationCount > 0 {
				avg := t.TotalDurationMs / t.DurationCount
				row.AvgTimeOnPageMs = &avg
			}
		}
		resp.Projects = append(resp.Projects, row)
	}
	// ListProjects sorts by name, and a stable sort keeps that for ties.
	sort.SliceStable(resp.Projects, func(i, j int) bool {
		a, b := resp.Projects[i], resp.Projects[j]
		if a.NewIssues24h != b.NewIssues24h {
			return a.NewIssues24h > b.NewIssues24h
		}
		return a.Errors24h > b.Errors24h
	})
	for _, issue := range newest {
		resp.NewestIssues = append(resp.NewestIssues, newestIssueJSON{
			ID:          issue.ID,
			ProjectID:   issue.ProjectID,
			ProjectName: issue.ProjectName,
			Type:        issue.Title,
			Message:     issue.Message,
			LastSeen:    issue.LastSeen,
		})
	}
	httpjson.Write(w, http.StatusOK, resp)
}
