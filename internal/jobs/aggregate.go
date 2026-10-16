package jobs

import (
	"context"
	"log/slog"
	"time"

	"monitor/internal/store"
)

// Aggregate rebuilds the hourly page statistics (hourly_stats) that the
// dashboard reads: every minute, each hour that received data since the
// last run (late data too: the writer marks the hour of every page view,
// page leave and error it stores) and the current hour of every project.
type Aggregate struct {
	DB  *store.DB
	Log *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Job returns the aggregation job: every minute, the first a minute after the start.
func (a *Aggregate) Job() Job {
	return Job{Name: "hourly stats", Schedule: Every(time.Minute), Run: a.Run}
}

// Run rebuilds the hours that are out of date and the current hour.
func (a *Aggregate) Run(ctx context.Context, _ time.Time) {
	if _, err := a.run(ctx); err != nil {
		a.Log.Error("hourly stats", "err", err)
	}
}

// run returns how many project hours it rebuilt.
func (a *Aggregate) run(ctx context.Context) (int, error) {
	now := time.Now
	if a.Now != nil {
		now = a.Now
	}
	hours, err := a.DB.DirtyHours(ctx)
	if err != nil {
		return 0, err
	}
	ids, err := a.DB.ProjectIDs(ctx)
	if err != nil {
		return 0, err
	}
	current := now().UTC().Truncate(time.Hour)
	for _, id := range ids {
		hours = append(hours, store.ProjectHour{ProjectID: id, Hour: current})
	}

	done := map[store.ProjectHour]bool{}
	for _, ph := range hours {
		if ctx.Err() != nil {
			return len(done), ctx.Err()
		}
		if done[ph] {
			continue
		}
		done[ph] = true
		if err := a.DB.RecomputeHour(ctx, ph.ProjectID, ph.Hour); err != nil {
			// The mark stays, so the next run tries again.
			a.Log.Error("hourly stats: rebuild an hour", "project", ph.ProjectID, "hour", ph.Hour, "err", err)
		}
	}
	return len(done), nil
}
