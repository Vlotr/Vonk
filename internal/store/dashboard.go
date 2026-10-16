package store

import (
	"context"
	"strings"
	"time"
)

// Queries for the dashboard. They only read, through the read pool.

// IssueFilter selects issues for the dashboard list.
type IssueFilter struct {
	ProjectID int64
	Status    string // IssueOpen, IssueResolved or IssueIgnored
	Limit     int
	Offset    int
}

// IssueRow is an issue as the dashboard lists it.
type IssueRow struct {
	Issue
	Users int // distinct non-empty user ids among its events
}

// StatusCounts tells how many issues a project has per status.
type StatusCounts struct {
	Open, Resolved, Ignored int
}

// ListIssuePage returns one page of a project's issues with the status in f,
// the most recently seen first.
func (db *DB) ListIssuePage(ctx context.Context, f IssueFilter) ([]IssueRow, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+issueColumns+`,
			(SELECT COUNT(DISTINCT e.user_id) FROM error_events e
			 WHERE e.issue_id = issues.id AND e.user_id <> '') AS users
		FROM issues
		WHERE project_id = ? AND status = ?
		ORDER BY last_seen DESC, id DESC
		LIMIT ? OFFSET ?`,
		f.ProjectID, f.Status, f.Limit, f.Offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	list := []IssueRow{}
	for rows.Next() {
		var row IssueRow
		if err := scanIssueRow(rows, &row); err != nil {
			return nil, err
		}
		list = append(list, row)
	}
	return list, rows.Err()
}

// IssueRowByID returns one issue with its user count, or ErrNotFound.
func (db *DB) IssueRowByID(ctx context.Context, id int64) (IssueRow, error) {
	var row IssueRow
	err := scanIssueRow(db.read.QueryRowContext(ctx, `
		SELECT `+issueColumns+`,
			(SELECT COUNT(DISTINCT e.user_id) FROM error_events e
			 WHERE e.issue_id = issues.id AND e.user_id <> '') AS users
		FROM issues WHERE id = ?`, id), &row)
	return row, err
}

// scanIssueRow reads the issue columns followed by the user count.
func scanIssueRow(s scanner, row *IssueRow) error {
	issue, err := scanIssue(s, &row.Users)
	row.Issue = issue
	return err
}

// CountIssueStatuses counts a project's issues per status.
func (db *DB) CountIssueStatuses(ctx context.Context, projectID int64) (StatusCounts, error) {
	rows, err := db.read.QueryContext(ctx,
		"SELECT status, COUNT(*) FROM issues WHERE project_id = ? GROUP BY status", projectID)
	if err != nil {
		return StatusCounts{}, err
	}
	defer rows.Close()

	var counts StatusCounts
	for rows.Next() {
		var (
			status string
			n      int
		)
		if err := rows.Scan(&status, &n); err != nil {
			return StatusCounts{}, err
		}
		switch status {
		case IssueOpen:
			counts.Open = n
		case IssueResolved:
			counts.Resolved = n
		case IssueIgnored:
			counts.Ignored = n
		}
	}
	return counts, rows.Err()
}

// CountNewIssues counts a project's issues with this status that were first seen at or after since.
func (db *DB) CountNewIssues(ctx context.Context, projectID int64, status string, since time.Time) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM issues WHERE project_id = ? AND status = ? AND first_seen >= ?",
		projectID, status, since.UnixMilli()).Scan(&n)
	return n, err
}

// CountOpenIssuesPerProject returns the number of open issues of every
// project that has any.
func (db *DB) CountOpenIssuesPerProject(ctx context.Context) (map[int64]int, error) {
	rows, err := db.read.QueryContext(ctx,
		"SELECT project_id, COUNT(*) FROM issues WHERE status = ? GROUP BY project_id", IssueOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[int64]int{}
	for rows.Next() {
		var projectID int64
		var n int
		if err := rows.Scan(&projectID, &n); err != nil {
			return nil, err
		}
		counts[projectID] = n
	}
	return counts, rows.Err()
}

// HourlyEventCounts counts the events of each issue per clock hour (UTC),
// from since on. The result maps issue id to hour (unix time divided by one
// hour) to count; hours without events are left out.
func (db *DB) HourlyEventCounts(ctx context.Context, issueIDs []int64, since time.Time) (map[int64]map[int64]int, error) {
	counts := map[int64]map[int64]int{}
	if len(issueIDs) == 0 {
		return counts, nil
	}
	args := make([]any, 0, len(issueIDs)+1)
	for _, id := range issueIDs {
		args = append(args, id)
	}
	args = append(args, since.UnixMilli())
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(issueIDs)), ",")

	rows, err := db.read.QueryContext(ctx, `
		SELECT issue_id, timestamp / 3600000 AS hour, COUNT(*)
		FROM error_events
		WHERE issue_id IN (`+placeholders+`) AND timestamp >= ?
		GROUP BY issue_id, hour`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var issueID, hour int64
		var n int
		if err := rows.Scan(&issueID, &hour, &n); err != nil {
			return nil, err
		}
		if counts[issueID] == nil {
			counts[issueID] = map[int64]int{}
		}
		counts[issueID][hour] = n
	}
	return counts, rows.Err()
}

// LatestErrorEvent returns the newest event of an issue, or ErrNotFound.
func (db *DB) LatestErrorEvent(ctx context.Context, issueID int64) (ErrorEvent, error) {
	return scanErrorEvent(db.read.QueryRowContext(ctx,
		"SELECT "+errorEventColumns+" FROM error_events WHERE issue_id = ? ORDER BY timestamp DESC, id DESC LIMIT 1", issueID))
}
