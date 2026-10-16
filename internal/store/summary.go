package store

import (
	"context"
	"time"
)

// Queries for alerts and the status page: how many errors came in over a
// period, which issues they belong to, and which issues are new. Periods
// are [from, to) on the event's timestamp (the time the SDK reported,
// clamped by the writer), which the (project_id, timestamp) index serves.

// CountErrorEventsBetween counts a project's error events in [from, to).
func (db *DB) CountErrorEventsBetween(ctx context.Context, projectID int64, from, to time.Time) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM error_events WHERE project_id = ? AND timestamp >= ? AND timestamp < ?",
		projectID, from.UnixMilli(), to.UnixMilli()).Scan(&n)
	return n, err
}

// CountAllErrorEventsSince counts the error events of every project from since on.
func (db *DB) CountAllErrorEventsSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	// Per project, so the (project_id, timestamp) index serves each.
	err := db.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM error_events WHERE project_id IN (SELECT id FROM projects) AND timestamp >= ?",
		since.UnixMilli()).Scan(&n)
	return n, err
}

// IssueCount is an issue with the number of its events in a period.
type IssueCount struct {
	Issue
	Events int
}

// TopIssuesBetween returns the issues with the most events in [from, to),
// at most limit, the busiest first.
func (db *DB) TopIssuesBetween(ctx context.Context, projectID int64, from, to time.Time, limit int) ([]IssueCount, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+issueColumns+`, c.events
		FROM issues
		JOIN (SELECT issue_id, COUNT(*) AS events FROM error_events
		      WHERE project_id = ? AND timestamp >= ? AND timestamp < ?
		      GROUP BY issue_id) c ON c.issue_id = issues.id
		ORDER BY c.events DESC, issues.last_seen DESC, issues.id DESC
		LIMIT ?`, projectID, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []IssueCount{}
	for rows.Next() {
		var ic IssueCount
		ic.Issue, err = scanIssue(rows, &ic.Events)
		if err != nil {
			return nil, err
		}
		list = append(list, ic)
	}
	return list, rows.Err()
}

// NewIssuesBetween counts a project's issues first seen in [from, to) and
// returns the newest of them, at most limit.
func (db *DB) NewIssuesBetween(ctx context.Context, projectID int64, from, to time.Time, limit int) (int, []Issue, error) {
	var n int
	err := db.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE project_id = ? AND first_seen >= ? AND first_seen < ?",
		projectID, from.UnixMilli(), to.UnixMilli()).Scan(&n)
	if err != nil {
		return 0, nil, err
	}
	rows, err := db.read.QueryContext(ctx, "SELECT "+issueColumns+
		" FROM issues WHERE project_id = ? AND first_seen >= ? AND first_seen < ? ORDER BY first_seen DESC, id DESC LIMIT ?",
		projectID, from.UnixMilli(), to.UnixMilli(), limit)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	issues := []Issue{}
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			return 0, nil, err
		}
		issues = append(issues, issue)
	}
	return n, issues, rows.Err()
}
