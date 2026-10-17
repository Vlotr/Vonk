package store

import (
	"context"
	"time"
)

// Queries for alerts and the status page: how many errors came in over a
// period, which issues they belong to, and which issues are new. Periods
// are [from, to) on the event's timestamp (the time the SDK reported,
// clamped by the writer), which the (project_id, timestamp) index serves.

// IssueScope says whose events CountErrorEventsBetween and
// TopIssuesBetween count.
type IssueScope int

const (
	// AnyIssue counts the events of every issue (the summaries).
	AnyIssue IssueScope = iota
	// NotIgnored leaves out the events of ignored issues (the spike
	// alert: an ignored issue is one the team chose not to hear about).
	NotIgnored
)

// CountErrorEventsBetween counts a project's error events in [from, to),
// of the issues in scope.
func (db *DB) CountErrorEventsBetween(ctx context.Context, projectID int64, from, to time.Time, scope IssueScope) (int, error) {
	query := "SELECT COUNT(*) FROM error_events WHERE project_id = ? AND timestamp >= ? AND timestamp < ?"
	if scope == NotIgnored {
		query += " AND NOT EXISTS (SELECT 1 FROM issues WHERE issues.id = error_events.issue_id AND issues.status = 'ignored')"
	}
	var n int
	err := db.read.QueryRowContext(ctx, query, projectID, from.UnixMilli(), to.UnixMilli()).Scan(&n)
	return n, err
}

// IssueCount is an issue with the number of its events in a period.
type IssueCount struct {
	Issue
	Events int
}

// TopIssuesBetween returns the issues in scope with the most events in
// [from, to), at most limit, the busiest first.
func (db *DB) TopIssuesBetween(ctx context.Context, projectID int64, from, to time.Time, limit int, scope IssueScope) ([]IssueCount, error) {
	where := ""
	if scope == NotIgnored {
		where = "WHERE issues.status <> 'ignored'"
	}
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+issueColumns+`, c.events
		FROM issues
		JOIN (SELECT issue_id, COUNT(*) AS events FROM error_events
		      WHERE project_id = ? AND timestamp >= ? AND timestamp < ?
		      GROUP BY issue_id) c ON c.issue_id = issues.id
		`+where+`
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
