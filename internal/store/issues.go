package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Issue statuses.
const (
	IssueOpen     = "open"
	IssueResolved = "resolved"
	IssueIgnored  = "ignored"
)

// Issue is a group of error events with the same fingerprint in one project.
type Issue struct {
	ID          int64
	ProjectID   int64
	Fingerprint string
	Title       string // the exception type, or the log message
	Message     string // the exception value of the newest event
	Culprit     string // file and line of the newest event, such as "PaymentGateway.php:12"
	Source      string // laravel, browser or the SDK platform
	Level       string
	Status      string // IssueOpen, IssueResolved or IssueIgnored
	FirstSeen   time.Time
	LastSeen    time.Time
	ResolvedAt  *time.Time // nil unless resolved
	TimesSeen   int64
}

// issueColumns lists the columns scanIssue expects, in order.
const issueColumns = `id, project_id, fingerprint, title, message, culprit, source, level, status,
	first_seen, last_seen, resolved_at, times_seen`

// IssueByID returns the issue with this id, or ErrNotFound.
func (db *DB) IssueByID(ctx context.Context, id int64) (Issue, error) {
	return scanIssue(db.read.QueryRowContext(ctx, "SELECT "+issueColumns+" FROM issues WHERE id = ?", id))
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

// issueKey identifies an issue within a batch.
type issueKey struct {
	projectID   int64
	fingerprint string
}

// upsertIssue adds ev to its issue, creating the issue when it is new.
// issues caches the issues this batch has touched, so each is read once.
func upsertIssue(ctx context.Context, tx *sql.Tx, issues map[issueKey]*Issue, ev NewErrorEvent) (issue *Issue, created bool, err error) {
	key := issueKey{ev.ProjectID, ev.Fingerprint}
	issue, ok := issues[key]
	if !ok {
		found, err := scanIssue(tx.QueryRowContext(ctx,
			"SELECT "+issueColumns+" FROM issues WHERE project_id = ? AND fingerprint = ?", ev.ProjectID, ev.Fingerprint))
		switch {
		case errors.Is(err, ErrNotFound):
			issue, err := insertIssue(ctx, tx, ev)
			if err != nil {
				return nil, false, err
			}
			issues[key] = issue
			return issue, true, nil
		case err != nil:
			return nil, false, err
		}
		issue = &found
		issues[key] = issue
	}

	// Events can arrive out of order: keep the earliest first_seen and the
	// latest last_seen, and show what the newest event says.
	ts := msTime(ev.Timestamp)
	if ts.Before(issue.FirstSeen) {
		issue.FirstSeen = ts
	}
	if !ts.Before(issue.LastSeen) {
		issue.LastSeen = ts
		issue.Title, issue.Message, issue.Culprit, issue.Level = ev.Title, ev.Message, ev.Culprit, ev.Level
	}
	issue.TimesSeen++

	_, err = tx.ExecContext(ctx, `
		UPDATE issues SET title = ?, message = ?, culprit = ?, level = ?, first_seen = ?, last_seen = ?, times_seen = ?
		WHERE id = ?`,
		issue.Title, issue.Message, issue.Culprit, issue.Level,
		issue.FirstSeen.UnixMilli(), issue.LastSeen.UnixMilli(), issue.TimesSeen, issue.ID)
	if err != nil {
		return nil, false, err
	}
	return issue, false, nil
}

func insertIssue(ctx context.Context, tx *sql.Tx, ev NewErrorEvent) (*Issue, error) {
	ts := msTime(ev.Timestamp)
	issue := &Issue{
		ProjectID:   ev.ProjectID,
		Fingerprint: ev.Fingerprint,
		Title:       ev.Title,
		Message:     ev.Message,
		Culprit:     ev.Culprit,
		Source:      ev.Source,
		Level:       ev.Level,
		Status:      IssueOpen,
		FirstSeen:   ts,
		LastSeen:    ts,
		TimesSeen:   1,
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, message, culprit, source, level, status,
			first_seen, last_seen, times_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		issue.ProjectID, issue.Fingerprint, issue.Title, issue.Message, issue.Culprit, issue.Source, issue.Level,
		issue.Status, issue.FirstSeen.UnixMilli(), issue.LastSeen.UnixMilli(), issue.TimesSeen,
	).Scan(&issue.ID)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

func scanIssue(s scanner) (Issue, error) {
	var (
		issue               Issue
		firstSeen, lastSeen int64
		resolvedAt          sql.NullInt64
	)
	err := s.Scan(&issue.ID, &issue.ProjectID, &issue.Fingerprint, &issue.Title, &issue.Message, &issue.Culprit,
		&issue.Source, &issue.Level, &issue.Status, &firstSeen, &lastSeen, &resolvedAt, &issue.TimesSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, err
	}
	issue.FirstSeen = time.UnixMilli(firstSeen).UTC()
	issue.LastSeen = time.UnixMilli(lastSeen).UTC()
	if resolvedAt.Valid {
		t := time.UnixMilli(resolvedAt.Int64).UTC()
		issue.ResolvedAt = &t
	}
	return issue, nil
}

// msTime truncates t to the milliseconds the database stores.
func msTime(t time.Time) time.Time {
	return time.UnixMilli(t.UnixMilli()).UTC()
}
