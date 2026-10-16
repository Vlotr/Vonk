package jobs

import (
	"context"
	"log/slog"
	"time"

	"monitor/internal/store"
)

// DefaultRetentionBatch is how many rows one delete removes at most. Each
// batch is its own short transaction, so ingest keeps writing in between.
const DefaultRetentionBatch = 5000

// Retention deletes data older than each project's retention: error events
// older than it, issues last seen before it (with their events), page
// views, page leaves and custom events older than it, the hourly
// summaries (of pages and of events) of the hours before it, and source
// maps uploaded before it. Projects that keep data indefinitely are left
// alone.
type Retention struct {
	DB  *store.DB
	Log *slog.Logger
	// SourceMaps deletes uploaded source maps (sourcemaps.Service); nil keeps them.
	SourceMaps SourceMaps
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// BatchSize is the rows per delete; 0 means DefaultRetentionBatch.
	BatchSize int
}

// SourceMaps deletes a project's source map files uploaded before a time,
// at most limit at once, and returns how many it deleted.
type SourceMaps interface {
	DeleteUploadedBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error)
}

// Job returns the daily retention job; its first run is a minute after the start.
func (r *Retention) Job() Job {
	return Job{Name: "retention", Schedule: Every(24 * time.Hour), FirstRun: time.Minute, Run: r.Run}
}

// Run enforces every project's retention once.
func (r *Retention) Run(ctx context.Context, _ time.Time) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	batch := r.BatchSize
	if batch <= 0 {
		batch = DefaultRetentionBatch
	}

	projects, err := r.DB.ListProjects(ctx)
	if err != nil {
		r.Log.Error("retention: list projects", "err", err)
		return
	}
	for _, p := range projects {
		if p.RetentionDays == nil {
			continue // kept indefinitely
		}
		cutoff := now().AddDate(0, 0, -*p.RetentionDays)
		events, err := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
			return r.DB.DeleteErrorEventsBefore(ctx, p.ID, cutoff, limit)
		})
		if err != nil {
			r.Log.Error("retention: delete old events", "project", p.ID, "deleted", events, "err", err)
			continue
		}
		issues, err := deleteInBatches(ctx, batch, func(limit int) (int64, error) {
			return r.DB.DeleteIssuesLastSeenBefore(ctx, p.ID, cutoff, limit)
		})
		if err != nil {
			r.Log.Error("retention: delete old issues", "project", p.ID, "deleted", issues, "err", err)
			continue
		}
		pages, err := r.deleteAll(ctx, batch, p.ID, cutoff,
			r.DB.DeletePageviewsBefore, r.DB.DeletePageleavesBefore, r.DB.DeleteHourlyStatsBefore)
		if err != nil {
			r.Log.Error("retention: delete old page views", "project", p.ID, "deleted", pages, "err", err)
			continue
		}
		custom, err := r.deleteAll(ctx, batch, p.ID, cutoff, r.DB.DeleteCustomEventsBefore, r.DB.DeleteEventHourlyBefore)
		if err != nil {
			r.Log.Error("retention: delete old custom events", "project", p.ID, "deleted", custom, "err", err)
			continue
		}
		var maps int64
		if r.SourceMaps != nil {
			maps, err = deleteInBatches(ctx, batch, func(limit int) (int64, error) {
				return r.SourceMaps.DeleteUploadedBefore(ctx, p.ID, cutoff, limit)
			})
			if err != nil {
				r.Log.Error("retention: delete old source maps", "project", p.ID, "deleted", maps, "err", err)
				continue
			}
		}
		if events > 0 || issues > 0 || pages > 0 || custom > 0 || maps > 0 {
			r.Log.Info("retention: deleted old data", "project", p.ID, "days", *p.RetentionDays, "events", events, "issues", issues,
				"page_rows", pages, "custom_event_rows", custom, "source_map_files", maps)
		}
	}
}

// deleteAll runs each delete in batches for a project's rows from before
// cutoff, one after the other, and returns how many rows they deleted.
func (r *Retention) deleteAll(ctx context.Context, batch int, projectID int64, cutoff time.Time,
	deletes ...func(context.Context, int64, time.Time, int) (int64, error)) (int64, error) {
	var total int64
	for _, del := range deletes {
		n, err := deleteInBatches(ctx, batch, func(limit int) (int64, error) { return del(ctx, projectID, cutoff, limit) })
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// deleteInBatches calls del until it deletes fewer rows than a full batch,
// and returns the total.
func deleteInBatches(ctx context.Context, batch int, del func(limit int) (int64, error)) (int64, error) {
	var total int64
	for ctx.Err() == nil {
		n, err := del(batch)
		total += n
		if err != nil {
			return total, err
		}
		if n < int64(batch) {
			break
		}
	}
	return total, ctx.Err()
}
