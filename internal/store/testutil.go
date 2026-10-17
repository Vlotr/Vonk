package store

import "context"

// Helpers for tests, this package's and other packages' (ingest, api,
// alerts, jobs, issues, cmd): shortcuts the tool itself never calls. They
// live in a file of their own, not in a _test.go file, because tests of
// other packages cannot see those. Production code reads and changes
// these rows through the functions next to their queries instead (the
// issue list with its filters, ChangeProject, UpdateAlertSettings, ...).

// ErrorEventByEventID returns the event a project stored under this event id, or ErrNotFound.
func (db *DB) ErrorEventByEventID(ctx context.Context, projectID int64, eventID string) (ErrorEvent, error) {
	return scanErrorEvent(db.read.QueryRowContext(ctx,
		"SELECT "+errorEventColumns+" FROM error_events WHERE project_id = ? AND event_id = ?", projectID, eventID))
}

// CountErrorEvents returns how many events a project has stored.
func (db *DB) CountErrorEvents(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM error_events WHERE project_id = ?", projectID).Scan(&n)
	return n, err
}

// CountPageRows returns how many page views and page leaves a project has stored.
func (db *DB) CountPageRows(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx,
		"SELECT (SELECT COUNT(*) FROM pageviews WHERE project_id = ?) + (SELECT COUNT(*) FROM pageleaves WHERE project_id = ?)",
		projectID, projectID).Scan(&n)
	return n, err
}

// ListIssues returns all issues of a project, the most recently seen first.
func (db *DB) ListIssues(ctx context.Context, projectID int64) ([]Issue, error) {
	rows, err := db.read.QueryContext(ctx,
		"SELECT "+issueColumns+" FROM issues WHERE project_id = ? ORDER BY last_seen DESC, id DESC", projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var issues []Issue
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		issues = append(issues, issue)
	}
	return issues, rows.Err()
}

// SaveAlertSettings replaces all of a project's notification choices and
// its Slack webhook, or returns ErrNotFound. The spike threshold and the
// digest frequency are stored on both channels' rows.
func (db *DB) SaveAlertSettings(ctx context.Context, projectID int64, s AlertSettings) error {
	_, err := db.UpdateAlertSettings(ctx, projectID, func(old *AlertSettings) { *old = s })
	return err
}

// UpdateProject replaces a project's settings (and its route patterns,
// when s has them) in one write, and returns the project as it is
// afterwards, or ErrNotFound. When anything is refused nothing changes.
// The slug stays as it was created.
func (db *DB) UpdateProject(ctx context.Context, id int64, s ProjectSettings) (Project, error) {
	return db.ChangeProject(ctx, id, func(old *ProjectSettings) error {
		if s.RateLimitPerMinute == 0 {
			s.RateLimitPerMinute = old.RateLimitPerMinute // left out: kept
		}
		*old = s
		return nil
	})
}
