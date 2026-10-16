package api

import (
	"errors"
	"net/http"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// The Pages screen: GET /ui/api/projects/{id}/pages?range=24h|7d|30d&tz=…
// answers a project's page views, visitors and average time on a page over
// the range, views per hour (24 hours) or per day (7 and 30 days), the
// busiest pages, and the page views of bots, counted apart. It reads only
// the hourly summaries (store.PageStats), never the raw page views.

// maxPagesListed is how many pages the table shows: the busiest.
const maxPagesListed = 100

type pagesJSON struct {
	Project pagesProjectJSON `json:"project"`
	// Range is 24h, 7d or 30d.
	Range  string          `json:"range"`
	Totals pageTotalsJSON  `json:"totals"`
	Series []pagePointJSON `json:"series"`
	// Pages are the pages people or bots visited, the most views first,
	// at most 100. Pages that only had errors are left out.
	Pages []pageJSON `json:"pages"`
	// Bots are page views by bots, in no other number.
	Bots int64 `json:"bots"`
}

type pagesProjectJSON struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	TracksPageviews bool   `json:"tracksPageviews"`
}

type pageTotalsJSON struct {
	Views    int64 `json:"views"`
	Visitors int   `json:"visitors"`
	// AvgTimeMs is null when no page view has a time (nobody left a page yet).
	AvgTimeMs *int64 `json:"avgTimeMs"`
}

// pagePointJSON is one bar of the chart: the page views of the hour or day
// that starts at Start (in the viewer's time zone for days).
type pagePointJSON struct {
	Start time.Time `json:"start"`
	Views int64     `json:"views"`
}

type pageJSON struct {
	Route     string `json:"route"`
	Views     int64  `json:"views"`
	Visitors  int    `json:"visitors"`
	AvgTimeMs *int64 `json:"avgTimeMs"`
	// Errors are the error events on the page; the issue list filtered by
	// route shows their issues.
	Errors int64 `json:"errors"`
}

// pageRange is a period of the Pages screen: the hours that start in
// [from, to), split into the chart's bars.
type pageRange struct {
	from, to time.Time
	// starts are the bars' starts, oldest first; the last bar ends at to.
	starts []time.Time
}

// pageRangeFor returns the period of name (24h, 7d or 30d) at now. 24
// hours are the 24 clock hours that end with the current one, a bar each;
// 7 and 30 days are calendar days in loc that end with today, a bar a day.
func pageRangeFor(name string, now time.Time, loc *time.Location) (pageRange, error) {
	to := now.UTC().Truncate(time.Hour).Add(time.Hour)
	var r pageRange
	switch name {
	case "24h":
		for i := 24; i > 0; i-- {
			r.starts = append(r.starts, to.Add(-time.Duration(i)*time.Hour))
		}
	case "7d", "30d":
		days := 7
		if name == "30d" {
			days = 30
		}
		y, m, d := now.In(loc).Date()
		for i := days - 1; i >= 0; i-- {
			r.starts = append(r.starts, time.Date(y, m, d-i, 0, 0, 0, 0, loc))
		}
	default:
		return pageRange{}, errors.New("range is 24h, 7d or 30d")
	}
	r.from, r.to = r.starts[0], to
	return r, nil
}

// bar returns the index of the bar the hour belongs to, or -1.
func (r pageRange) bar(hour time.Time) int {
	if hour.Before(r.from) || !hour.Before(r.to) {
		return -1
	}
	for i := len(r.starts) - 1; i >= 0; i-- {
		if !hour.Before(r.starts[i]) {
			return i
		}
	}
	return -1
}

// viewerLocation is the time zone the browser named (an IANA name such as
// Europe/Amsterdam), for the days of the chart; UTC when it named none or
// one this server does not know.
func viewerLocation(name string) *time.Location {
	if name == "" || len(name) > 64 {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

func avgMs(r store.RouteStats) *int64 {
	if avg, ok := r.AvgDurationMs(); ok {
		return &avg
	}
	return nil
}

// projectRange reads the project of the URL (404 for one the user may not
// see) and the range of the query (7d without one; 400 for an unknown
// one), answering either failure itself. The Pages and Events screens
// share it.
func (h *Handler) projectRange(w http.ResponseWriter, r *http.Request) (store.Project, string, pageRange, bool) {
	id, ok := pathID(r, "id")
	if !ok || !canSee(r, id) {
		notFound(w)
		return store.Project{}, "", pageRange{}, false
	}
	project, err := h.db.ProjectByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return store.Project{}, "", pageRange{}, false
	}
	if err != nil {
		h.internalError(w, r, err)
		return store.Project{}, "", pageRange{}, false
	}
	name := r.URL.Query().Get("range")
	if name == "" {
		name = "7d"
	}
	period, err := pageRangeFor(name, h.now(), viewerLocation(r.URL.Query().Get("tz")))
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return store.Project{}, "", pageRange{}, false
	}
	return project, name, period, true
}

// projectPages answers GET /ui/api/projects/{id}/pages.
func (h *Handler) projectPages(w http.ResponseWriter, r *http.Request) {
	project, name, period, ok := h.projectRange(w, r)
	if !ok {
		return
	}
	stats, err := h.db.PageStats(r.Context(), project.ID, period.from, period.to)
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	resp := pagesJSON{
		Project: pagesProjectJSON{ID: project.ID, Name: project.Name, TracksPageviews: project.TracksPageviews},
		Range:   name,
		Totals:  pageTotalsJSON{Views: stats.Totals.Views, Visitors: stats.Totals.Visitors, AvgTimeMs: avgMs(stats.Totals)},
		Series:  make([]pagePointJSON, len(period.starts)),
		Pages:   []pageJSON{},
		Bots:    stats.Totals.Bots,
	}
	for i, start := range period.starts {
		resp.Series[i].Start = start
	}
	for _, hv := range stats.Hours {
		if i := period.bar(hv.Hour); i >= 0 {
			resp.Series[i].Views += hv.Views
		}
	}
	for _, route := range stats.Routes { // sorted by views, the busiest first
		if route.Views == 0 && route.Bots == 0 {
			continue // only errors: they count on the issues side
		}
		if len(resp.Pages) == maxPagesListed {
			break
		}
		resp.Pages = append(resp.Pages, pageJSON{Route: route.Route, Views: route.Views, Visitors: route.Visitors,
			AvgTimeMs: avgMs(route), Errors: route.Errors})
	}
	httpjson.Write(w, http.StatusOK, resp)
}
