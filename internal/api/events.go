package api

import (
	"net/http"
	"time"

	"monitor/internal/httpjson"
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
	Project eventsProjectJSON `json:"project"`
	Range   string            `json:"range"`
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

// eventsProjectJSON is the project of the Events screen. HasCustomEvents
// tells "no events yet" from "none in this period".
type eventsProjectJSON struct {
	pagesProjectJSON
	HasCustomEvents bool `json:"hasCustomEvents"`
}

// eventPointJSON is one bar: the events of the hour or day that starts at Start.
type eventPointJSON struct {
	Start time.Time `json:"start"`
	Count int64     `json:"count"`
}

// projectEvents answers GET /ui/api/projects/{id}/events.
func (h *Handler) projectEvents(w http.ResponseWriter, r *http.Request) {
	project, rangeName, period, ok := h.projectRange(w, r)
	if !ok {
		return
	}
	stats, err := h.db.EventStats(r.Context(), project.ID, period.from, period.to)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	hasEvents, err := h.db.HasCustomEvents(r.Context(), project.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	resp := eventsJSON{
		Project: eventsProjectJSON{pagesProjectJSON{ID: project.ID, Name: project.Name, TracksPageviews: project.TracksPageviews}, hasEvents},
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
	project, rangeName, period, ok := h.projectRange(w, r)
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
