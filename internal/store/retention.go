package store

import (
	"context"
	"time"
)

// Retention deletes in batches, each its own short write transaction, so
// the ingest writer gets the write connection in between.

// DeleteErrorEventsBefore deletes at most limit of a project's error
// events with a timestamp before before, and returns how many it deleted.
func (db *DB) DeleteErrorEventsBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	res, err := db.write.ExecContext(ctx, `
		DELETE FROM error_events WHERE id IN (
			SELECT id FROM error_events WHERE project_id = ? AND timestamp < ? LIMIT ?)`,
		projectID, before.UnixMilli(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteIssuesLastSeenBefore deletes at most limit of a project's issues
// last seen before before (with any events they still have), and returns
// how many it deleted.
func (db *DB) DeleteIssuesLastSeenBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	res, err := db.write.ExecContext(ctx, `
		DELETE FROM issues WHERE id IN (
			SELECT id FROM issues WHERE project_id = ? AND last_seen < ? LIMIT ?)`,
		projectID, before.UnixMilli(), limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
