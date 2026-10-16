package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// Queries for the dashboard. They only read, through the read pool.

// Issue list orders.
const (
	SortLastSeen  = "last_seen"  // most recently seen first (the default)
	SortFirstSeen = "first_seen" // newest first
	SortEvents    = "events"     // most events first
	SortUsers     = "users"      // most users first
)

// issueOrders maps each sort to its ORDER BY. Ties go to the most recently
// seen issue, then the newest id, so pages never overlap.
var issueOrders = map[string]string{
	SortLastSeen:  "last_seen DESC, id DESC",
	SortFirstSeen: "first_seen DESC, id DESC",
	SortEvents:    "times_seen DESC, last_seen DESC, id DESC",
	SortUsers:     "users DESC, last_seen DESC, id DESC",
}

// ValidIssueSort reports whether s is one of the sorts above.
func ValidIssueSort(s string) bool {
	_, ok := issueOrders[s]
	return ok
}

// IssueFilter selects issues for the dashboard list. Empty fields do not filter.
type IssueFilter struct {
	ProjectID int64
	Status    string // IssueOpen, IssueResolved or IssueIgnored
	Source    string // "laravel", "browser", ...
	// Search matches, ignoring case, part of the title, message or culprit,
	// or of the URL of any of the issue's events.
	Search string
	// Route keeps issues with at least one event on this route (exactly).
	Route  string
	Sort   string // SortLastSeen when empty
	Limit  int
	Offset int
}

// conditions returns the WHERE clause of f and its arguments, with or
// without the status (the status tabs count across statuses).
func (f IssueFilter) conditions(withStatus bool) (string, []any) {
	where := []string{"project_id = ?"}
	args := []any{f.ProjectID}
	if withStatus {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.Source != "" {
		where = append(where, "source = ?")
		args = append(args, f.Source)
	}
	if f.Search != "" {
		pattern := likePattern(f.Search)
		where = append(where, `(LOWER(title) LIKE ? ESCAPE '\' OR LOWER(message) LIKE ? ESCAPE '\' OR LOWER(culprit) LIKE ? ESCAPE '\'
			OR EXISTS (SELECT 1 FROM error_events e WHERE e.issue_id = issues.id AND LOWER(e.url) LIKE ? ESCAPE '\'))`)
		args = append(args, pattern, pattern, pattern, pattern)
	}
	if f.Route != "" {
		where = append(where, "EXISTS (SELECT 1 FROM error_events e WHERE e.issue_id = issues.id AND e.route = ?)")
		args = append(args, f.Route)
	}
	return strings.Join(where, " AND "), args
}

// likePattern turns a search into a LIKE pattern that matches it anywhere:
// "%", "_" and "\" in the search are literal. Only ASCII letters are
// lowercased, the same as SQLite's LOWER, so "É" still finds "É".
func likePattern(search string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(asciiLower(search))
	return "%" + escaped + "%"
}

func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, s)
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

// Of returns the count of one status.
func (c StatusCounts) Of(status string) int {
	switch status {
	case IssueOpen:
		return c.Open
	case IssueResolved:
		return c.Resolved
	case IssueIgnored:
		return c.Ignored
	}
	return 0
}

// usersColumn counts the distinct non-empty user ids of an issue's events,
// for queries on the issues table. It is computed at query time for every
// row the query reads; a counter on issues can replace it if it gets slow.
const usersColumn = `(SELECT COUNT(DISTINCT e.user_id) FROM error_events e
	WHERE e.issue_id = issues.id AND e.user_id <> '') AS users`

// ListIssuePage returns one page of the issues f selects, in f's order.
func (db *DB) ListIssuePage(ctx context.Context, f IssueFilter) ([]IssueRow, error) {
	order, ok := issueOrders[f.Sort]
	if f.Sort == "" {
		order, ok = issueOrders[SortLastSeen], true
	}
	if !ok {
		return nil, fmt.Errorf("unknown issue sort %q", f.Sort)
	}
	where, args := f.conditions(true)
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+issueColumns+`, `+usersColumn+`
		FROM issues
		WHERE `+where+`
		ORDER BY `+order+`
		LIMIT ? OFFSET ?`,
		append(args, f.Limit, f.Offset)...)
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
	err := scanIssueRow(db.read.QueryRowContext(ctx,
		"SELECT "+issueColumns+", "+usersColumn+" FROM issues WHERE id = ?", id), &row)
	return row, err
}

// scanIssueRow reads the issue columns followed by the user count.
func scanIssueRow(s scanner, row *IssueRow) error {
	issue, err := scanIssue(s, &row.Users)
	row.Issue = issue
	return err
}

// CountIssueStatuses counts the issues f selects per status; f's status,
// order and page are ignored. IssueFilter{ProjectID: id} counts a whole project.
func (db *DB) CountIssueStatuses(ctx context.Context, f IssueFilter) (StatusCounts, error) {
	where, args := f.conditions(false)
	rows, err := db.read.QueryContext(ctx,
		"SELECT status, COUNT(*) FROM issues WHERE "+where+" GROUP BY status", args...)
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
	return db.hourlyCounts(ctx, "issue_id", issueIDs, since)
}

// HourlyProjectEventCounts is HourlyEventCounts for whole projects: project
// id to hour to the number of error events.
func (db *DB) HourlyProjectEventCounts(ctx context.Context, projectIDs []int64, since time.Time) (map[int64]map[int64]int, error) {
	return db.hourlyCounts(ctx, "project_id", projectIDs, since)
}

// hourlyCounts counts error events per value of column (issue_id or
// project_id, never user input) and clock hour, for the given ids.
func (db *DB) hourlyCounts(ctx context.Context, column string, ids []int64, since time.Time) (map[int64]map[int64]int, error) {
	counts := map[int64]map[int64]int{}
	if len(ids) == 0 {
		return counts, nil
	}
	in, args := inList(ids)
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+column+`, timestamp / 3600000 AS hour, COUNT(*)
		FROM error_events
		WHERE `+column+` IN (`+in+`) AND timestamp >= ?
		GROUP BY `+column+`, hour`, append(args, since.UnixMilli())...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var id, hour int64
		var n int
		if err := rows.Scan(&id, &hour, &n); err != nil {
			return nil, err
		}
		if counts[id] == nil {
			counts[id] = map[int64]int{}
		}
		counts[id][hour] = n
	}
	return counts, rows.Err()
}

// CountNewIssuesPerProject counts, for each of the projects that has any,
// the issues with this status first seen at or after since.
func (db *DB) CountNewIssuesPerProject(ctx context.Context, projectIDs []int64, status string, since time.Time) (map[int64]int, error) {
	counts := map[int64]int{}
	if len(projectIDs) == 0 {
		return counts, nil
	}
	in, args := inList(projectIDs)
	rows, err := db.read.QueryContext(ctx, `
		SELECT project_id, COUNT(*) FROM issues
		WHERE project_id IN (`+in+`) AND status = ? AND first_seen >= ?
		GROUP BY project_id`, append(args, status, since.UnixMilli())...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
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

// ProjectIssue is an issue with the name of its project, for lists that
// span projects.
type ProjectIssue struct {
	Issue
	ProjectName string
}

// NewIssuesAcrossProjects returns up to limit issues of the given projects
// with this status that were first seen at or after since, the most
// recently seen first.
func (db *DB) NewIssuesAcrossProjects(ctx context.Context, projectIDs []int64, status string, since time.Time, limit int) ([]ProjectIssue, error) {
	list := []ProjectIssue{}
	if len(projectIDs) == 0 {
		return list, nil
	}
	in, args := inList(projectIDs)
	rows, err := db.read.QueryContext(ctx, `
		SELECT `+issueColumns+`, (SELECT p.name FROM projects p WHERE p.id = issues.project_id)
		FROM issues
		WHERE project_id IN (`+in+`) AND status = ? AND first_seen >= ?
		ORDER BY last_seen DESC, id DESC
		LIMIT ?`, append(args, status, since.UnixMilli(), limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var row ProjectIssue
		issue, err := scanIssue(rows, &row.ProjectName)
		if err != nil {
			return nil, err
		}
		row.Issue = issue
		list = append(list, row)
	}
	return list, rows.Err()
}

// inList returns "?,?,?" and the ids as query arguments.
func inList(ids []int64) (string, []any) {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(ids)), ","), args
}

// LatestErrorEvent returns the newest event of an issue, or ErrNotFound.
func (db *DB) LatestErrorEvent(ctx context.Context, issueID int64) (ErrorEvent, error) {
	return scanErrorEvent(db.read.QueryRowContext(ctx,
		"SELECT "+errorEventColumns+" FROM error_events WHERE issue_id = ? ORDER BY timestamp DESC, id DESC LIMIT 1", issueID))
}
