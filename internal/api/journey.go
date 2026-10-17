package api

import (
	"context"
	"time"

	"monitor/internal/store"
)

// "Before this error" on the issue page: the pages the user of the latest
// event saw in the half hour before it. Sentry's user.id and PostHog's
// distinct_id are the same id when the app gives both SDKs its user id.

const (
	// journeyWindow is how long before the error page views count.
	journeyWindow = 30 * time.Minute
	// journeySteps is how many page views the card shows at most: the ones
	// nearest the error.
	journeySteps = 10
)

// journeyStepJSON is one page the user saw before the error.
type journeyStepJSON struct {
	Route string `json:"route"`
	// DurationMs is the time on the page; null while nothing closed it
	// (often the page of the error itself, until the user leaves it).
	DurationMs *int64    `json:"durationMs"`
	Start      time.Time `json:"start"`
}

// beforeError returns the page views (bots left out) of the event's user
// in the 30 minutes before the event, at most 10, oldest first; none when
// the event has no user id.
func (h *Handler) beforeError(ctx context.Context, ev store.ErrorEvent) ([]journeyStepJSON, error) {
	steps := []journeyStepJSON{}
	if ev.UserID == "" {
		return steps, nil
	}
	visits, err := h.db.PagesBefore(ctx, ev.ProjectID, ev.UserID, ev.Timestamp, journeyWindow, journeySteps)
	if err != nil {
		return nil, err
	}
	for _, v := range visits {
		steps = append(steps, journeyStepJSON{Route: v.Route, DurationMs: v.DurationMs, Start: v.Start})
	}
	return steps, nil
}
