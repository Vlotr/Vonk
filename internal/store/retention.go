package store

import (
	"context"
	"time"
)

// Retention deletes in batches, each its own short write transaction that
// keeps the project first (inProject, projectlocks.go), so the ingest
// writer gets the write connection in between and a deletion of the
// project never deadlocks with a batch.

// DeleteErrorEventsBefore deletes at most limit of a project's error
// events with a timestamp before before, and returns how many it deleted.
func (db *DB) DeleteErrorEventsBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.deleteBatch(ctx, "error_events", "timestamp", projectID, before.UnixMilli(), limit)
}

// DeleteIssuesLastSeenBefore deletes at most limit of a project's issues
// last seen before before (with any events they still have), and returns
// how many it deleted. last_seen is checked again outside the subquery:
// Postgres rechecks that condition on a row that the writer changed while
// the delete waited for it, so an issue that just got an event stays.
func (db *DB) DeleteIssuesLastSeenBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.inProject(ctx, projectID, func(tx *writeTx) (int64, error) {
		res, err := tx.ExecContext(ctx, `
			DELETE FROM issues WHERE id IN (
				SELECT id FROM issues WHERE project_id = ? AND last_seen < ? LIMIT ?) AND last_seen < ?`,
			projectID, before.UnixMilli(), limit, before.UnixMilli())
		if err != nil {
			return 0, err
		}
		return res.RowsAffected()
	})
}

// DeletePageviewsBefore deletes at most limit of a project's page views
// that started before before, and returns how many it deleted.
func (db *DB) DeletePageviewsBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.deleteBatch(ctx, "pageviews", "start", projectID, before.UnixMilli(), limit)
}

// DeletePageleavesBefore deletes at most limit of a project's page leaves
// from before before.
func (db *DB) DeletePageleavesBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.deleteBatch(ctx, "pageleaves", "at", projectID, before.UnixMilli(), limit)
}

// DeleteHourlyStatsBefore deletes at most limit of a project's hourly
// summaries of hours that started before the hour of before (and their
// dirty marks): an hour that is partly kept keeps its summary.
func (db *DB) DeleteHourlyStatsBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	hour := hourOf(before)
	return db.inProject(ctx, projectID, func(tx *writeTx) (int64, error) {
		if _, err := tx.ExecContext(ctx, "DELETE FROM stats_dirty_hours WHERE project_id = ? AND hour < ?", projectID, hour); err != nil {
			return 0, err
		}
		return deleteRows(ctx, tx, "hourly_stats", "hour", projectID, hour, limit)
	})
}

// deleteBatch deletes at most limit rows of table whose column is before
// the value, for one project, in a transaction that keeps the project.
// Table and column are constants of this file.
func (db *DB) deleteBatch(ctx context.Context, table, column string, projectID, before int64, limit int) (int64, error) {
	return db.inProject(ctx, projectID, func(tx *writeTx) (int64, error) {
		return deleteRows(ctx, tx, table, column, projectID, before, limit)
	})
}

func deleteRows(ctx context.Context, tx *writeTx, table, column string, projectID, before int64, limit int) (int64, error) {
	res, err := tx.ExecContext(ctx, `DELETE FROM `+table+` WHERE id IN (
			SELECT id FROM `+table+` WHERE project_id = ? AND `+column+` < ? LIMIT ?)`,
		projectID, before, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountPageRows returns how many page views and page leaves a project has stored.
func (db *DB) CountPageRows(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx,
		"SELECT (SELECT COUNT(*) FROM pageviews WHERE project_id = ?) + (SELECT COUNT(*) FROM pageleaves WHERE project_id = ?)",
		projectID, projectID).Scan(&n)
	return n, err
}
