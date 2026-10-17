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
	// retryDelays replaces the package's retryDelays; tests shorten them.
	retryDelays []time.Duration
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

// deleteFunc deletes at most limit of a project's rows from before a time
// and returns how many it deleted.
type deleteFunc func(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error)

// dataKind is one kind of a project's data that retention deletes.
type dataKind struct {
	key, what string // its key in the summary line, and what an error line says
	deletes   []deleteFunc
}

// retryDelays are the waits before a batch is tried again after Postgres
// ended it to resolve a deadlock (store.Retryable), say with the ingest
// writer storing events of old issues, or with a project's deletion.
var retryDelays = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}

// Run enforces every project's retention once. Each kind of data is
// deleted on its own: one that fails is logged and the others still go,
// and the next run tries it again.
func (r *Retention) Run(ctx context.Context, _ time.Time) {
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	batch := r.BatchSize
	if batch <= 0 {
		batch = DefaultRetentionBatch
	}

	// A shutdown ends ctx: the run stops with one line, not an error for
	// every kind of every project; the next run deletes the rest.
	stopped := func() bool {
		if ctx.Err() == nil {
			return false
		}
		r.Log.Info("retention: stopped because the tool is shutting down; the next run deletes the rest")
		return true
	}
	projects, err := r.DB.ListProjects(ctx)
	if err != nil {
		if !stopped() {
			r.Log.Error("retention: list projects", "err", err)
		}
		return
	}
	for _, p := range projects {
		if stopped() {
			return
		}
		if p.RetentionDays == nil {
			continue // kept indefinitely
		}
		cutoff := now().AddDate(0, 0, -*p.RetentionDays)
		// Each kind goes on its own, so the order does not matter.
		var kinds []dataKind
		if r.SourceMaps != nil {
			kinds = append(kinds, dataKind{"source_map_files", "source maps", []deleteFunc{r.SourceMaps.DeleteUploadedBefore}})
		}
		kinds = append(kinds,
			dataKind{"events", "events", []deleteFunc{r.DB.DeleteErrorEventsBefore}},
			dataKind{"issues", "issues", []deleteFunc{r.DB.DeleteIssuesLastSeenBefore}},
			dataKind{"page_rows", "page views", []deleteFunc{r.DB.DeletePageviewsBefore, r.DB.DeletePageleavesBefore, r.DB.DeleteHourlyStatsBefore}},
			dataKind{"custom_event_rows", "custom events", []deleteFunc{r.DB.DeleteCustomEventsBefore, r.DB.DeleteEventHourlyBefore}},
		)
		summary := []any{"project", p.ID, "days", *p.RetentionDays}
		deletedAny := false
		for _, k := range kinds {
			var total int64
			for _, del := range k.deletes {
				n, err := r.deleteInBatches(ctx, batch, func(limit int) (int64, error) { return del(ctx, p.ID, cutoff, limit) })
				total += n
				if err != nil && stopped() {
					return
				}
				if err != nil {
					r.Log.Error("retention: delete old "+k.what, "project", p.ID, "deleted", total, "err", err)
					break // the next kind still goes
				}
			}
			summary = append(summary, k.key, total)
			deletedAny = deletedAny || total > 0
		}
		if deletedAny {
			r.Log.Info("retention: deleted old data", summary...)
		}
	}
}

// deleteInBatches calls del until it deletes fewer rows than a full batch,
// and returns the total. A batch Postgres ended to resolve a deadlock is
// tried again after retryDelays.
func (r *Retention) deleteInBatches(ctx context.Context, batch int, del func(limit int) (int64, error)) (int64, error) {
	delays := r.retryDelays
	if delays == nil {
		delays = retryDelays
	}
	var total int64
	for ctx.Err() == nil {
		n, err := del(batch)
		for attempt := 0; store.Retryable(err) && attempt < len(delays); attempt++ {
			r.Log.Warn("retention: the database ended a delete to resolve a deadlock; trying again", "err", err)
			time.Sleep(delays[attempt])
			n, err = del(batch)
		}
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
