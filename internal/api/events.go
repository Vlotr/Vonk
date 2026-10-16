package api

import (
	"errors"
	"net/http"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// The Events screen, from the hourly event summaries only (store.EventStats
// and store.EventSeries), never the raw custom events:
//
//	GET /ui/api/projects/{id}/events?range=24h|7d|30d
//	    every custom event of the range with how often it happened and by
//	    how many users, the most frequent first; bots apart
//	GET /ui/api/projects/{id}/events/{name}?range=24h|7d|30d&tz=…
//	    one event's totals and a bar per hour (24 hours) or day (7 and 30
//	    days, in the viewer's time zone)
//
// The ranges are the Pages screen's (pageRangeFor). The name is a path
// segment, percent-encoded.

// maxEventsListed is how many event names the list shows: the most frequent.
const maxEventsListed = 100

type eventsJSON struct {
	Project pagesProjectJSON `json:"project"`
	Range   string           `json:"range"`
	// Events are the custom events people sent in the range, the most
	// frequent first (then by name), at most 100.
	Events []eventTotalsJSON `json:"events"`
	// Bots are events sent by bots, in no other number.
	Bots int64 `json:"bots"`
}

type eventTotalsJSON struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
	Users int    `json:"users"`
}

type eventSeriesJSON struct {
	Name   string           `json:"name"`
	Range  string           `json:"range"`
	Count  int64            `json:"count"`
	Users  int              `json:"users"`
	Series []eventPointJSON `json:"series"`
}

// eventPointJSON is one bar: the events of the hour or day that starts at Start.
type eventPointJSON struct {
	Start time.Time `json:"start"`
	Count int64     `json:"count"`
}

// eventsRange reads the project of the URL (404 for one the user may not
// see) and the range of the query (400 for an unknown one), answering
// either failure itself.
func (h *Handler) eventsRange(w http.ResponseWriter, r *http.Request) (store.Project, string, pageRange, bool) {
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

// projectEvents answers GET /ui/api/projects/{id}/events.
func (h *Handler) projectEvents(w http.ResponseWriter, r *http.Request) {
	project, rangeName, period, ok := h.eventsRange(w, r)
	if !ok {
		return
	}
	stats, err := h.db.EventStats(r.Context(), project.ID, period.from, period.to)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	resp := eventsJSON{
		Project: pagesProjectJSON{ID: project.ID, Name: project.Name, TracksPageviews: project.TracksPageviews},
		Range:   rangeName,
		Events:  []eventTotalsJSON{},
		Bots:    stats.Bots,
	}
	for _, e := range stats.Events { // the most frequent first
		if len(resp.Events) == maxEventsListed {
			break
		}
		resp.Events = append(resp.Events, eventTotalsJSON{Name: e.Name, Count: e.Events, Users: e.Users})
	}
	httpjson.Write(w, http.StatusOK, resp)
}

// eventSeries answers GET /ui/api/projects/{id}/events/{name}: an event
// without data in the range has zeros, not 404.
func (h *Handler) eventSeries(w http.ResponseWriter, r *http.Request) {
	project, rangeName, period, ok := h.eventsRange(w, r)
	if !ok {
		return
	}
	name := r.PathValue("name")
	totals, hours, err := h.db.EventSeries(r.Context(), project.ID, name, period.from, period.to)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	resp := eventSeriesJSON{Name: name, Range: rangeName, Count: totals.Events, Users: totals.Users,
		Series: make([]eventPointJSON, len(period.starts))}
	for i, start := range period.starts {
		resp.Series[i].Start = start
	}
	for _, hc := range hours {
		if i := period.bar(hc.Hour); i >= 0 {
			resp.Series[i].Count += hc.Count
		}
	}
	httpjson.Write(w, http.StatusOK, resp)
}
