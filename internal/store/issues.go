package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Issue statuses.
const (
	IssueOpen     = "open"
	IssueResolved = "resolved"
	IssueIgnored  = "ignored"
)

// ValidIssueStatus reports whether s is one of the three statuses.
func ValidIssueStatus(s string) bool {
	return s == IssueOpen || s == IssueResolved || s == IssueIgnored
}

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
	// RegressedAt is when the issue last came back: an event arrived while it
	// was resolved and opened it again. Nil when that never happened.
	RegressedAt *time.Time
	TimesSeen   int64
	// FirstRelease is the release of the event that created the issue; ""
	// when it had none. Later events never change it.
	FirstRelease string
	// RegressedRelease is the release of the event that last opened the
	// issue again (at RegressedAt); "" when it never came back or that event
	// had no release.
	RegressedRelease string
}

// issueColumns lists the columns scanIssue expects, in order.
const issueColumns = `id, project_id, fingerprint, title, message, culprit, source, level, status,
	first_seen, last_seen, resolved_at, regressed_at, times_seen, first_release, regressed_release`

// IssueByID returns the issue with this id, or ErrNotFound.
func (db *DB) IssueByID(ctx context.Context, id int64) (Issue, error) {
	return scanIssue(db.read.QueryRowContext(ctx, "SELECT "+issueColumns+" FROM issues WHERE id = ?", id))
}

// issueKey identifies an issue within a batch.
type issueKey struct {
	projectID   int64
	fingerprint string
}

// upsertIssue adds ev to its issue, creating the issue when it is new.
// issues caches the issues this batch has touched, so each is read once.
//
// A resolved issue opens again (regressed) when ev was received at or after
// the moment it was resolved; an event that was already waiting in the queue
// when someone clicked Resolve does not undo the click. Ignored issues stay
// ignored. Either way the event counts.
//
// The issue is read locked (forUpdate) and stays locked until the batch
// commits: a status change made meanwhile (SetIssueStatus) waits, and one
// made just before is what this reads, so the status written back is never
// one that was already changed.
func upsertIssue(ctx context.Context, tx *writeTx, issues map[issueKey]*Issue, ev NewErrorEvent) (issue *Issue, created, regressed bool, err error) {
	key := issueKey{ev.ProjectID, ev.Fingerprint}
	issue, ok := issues[key]
	if !ok {
		found, err := scanIssue(tx.QueryRowContext(ctx,
			"SELECT "+issueColumns+" FROM issues WHERE project_id = ? AND fingerprint = ?"+tx.forUpdate(), ev.ProjectID, ev.Fingerprint))
		switch {
		case errors.Is(err, ErrNotFound):
			issue, err := insertIssue(ctx, tx, ev)
			if err != nil {
				return nil, false, false, err
			}
			issues[key] = issue
			return issue, true, false, nil
		case err != nil:
			return nil, false, false, err
		}
		issue = &found
		issues[key] = issue
	}

	if issue.Status == IssueResolved && (issue.ResolvedAt == nil || ev.ReceivedAt.UnixMilli() >= issue.ResolvedAt.UnixMilli()) {
		received := msTime(ev.ReceivedAt)
		issue.Status, issue.ResolvedAt, issue.RegressedAt, issue.RegressedRelease = IssueOpen, nil, &received, ev.Release
		regressed = true
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
		UPDATE issues SET title = ?, message = ?, culprit = ?, level = ?, status = ?, first_seen = ?, last_seen = ?,
			resolved_at = ?, regressed_at = ?, times_seen = ?, regressed_release = ?
		WHERE id = ?`,
		issue.Title, issue.Message, issue.Culprit, issue.Level, issue.Status,
		issue.FirstSeen.UnixMilli(), issue.LastSeen.UnixMilli(),
		nullMillis(issue.ResolvedAt), nullMillis(issue.RegressedAt), issue.TimesSeen, issue.RegressedRelease, issue.ID)
	if err != nil {
		return nil, false, false, err
	}
	return issue, false, regressed, nil
}

// SetIssueStatus changes an issue's status and returns the issue as it is
// afterwards, or ErrNotFound. Resolving records now as resolved_at; opening
// or ignoring clears it. Setting the status an issue already has changes
// nothing, so a second click on Resolve keeps the first time. The issue's
// project is kept first (projectlocks.go), then the issue is read locked,
// as the ingest writer reads it (upsertIssue).
func (db *DB) SetIssueStatus(ctx context.Context, id int64, status string, now time.Time) (Issue, error) {
	if !ValidIssueStatus(status) {
		return Issue{}, fmt.Errorf("unknown issue status %q", status)
	}
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return Issue{}, err
	}
	defer tx.Rollback() // no-op after Commit

	var projectID int64
	err = tx.QueryRowContext(ctx, "SELECT project_id FROM issues WHERE id = ?", id).Scan(&projectID)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, err
	}
	exists, err := projectExists(ctx, tx, projectID)
	if err != nil {
		return Issue{}, err
	}
	if !exists {
		return Issue{}, ErrNotFound
	}
	issue, err := scanIssue(tx.QueryRowContext(ctx, "SELECT "+issueColumns+" FROM issues WHERE id = ?"+tx.forUpdate(), id))
	if err != nil {
		return Issue{}, err
	}
	if issue.Status == status {
		return issue, nil
	}
	issue.Status, issue.ResolvedAt = status, nil
	if status == IssueResolved {
		resolved := msTime(now)
		issue.ResolvedAt = &resolved
	}
	_, err = tx.ExecContext(ctx, "UPDATE issues SET status = ?, resolved_at = ? WHERE id = ?",
		issue.Status, nullMillis(issue.ResolvedAt), issue.ID)
	if err != nil {
		return Issue{}, err
	}
	return issue, tx.Commit()
}

func insertIssue(ctx context.Context, tx *writeTx, ev NewErrorEvent) (*Issue, error) {
	ts := msTime(ev.Timestamp)
	issue := &Issue{
		ProjectID:    ev.ProjectID,
		Fingerprint:  ev.Fingerprint,
		Title:        ev.Title,
		Message:      ev.Message,
		Culprit:      ev.Culprit,
		Source:       ev.Source,
		Level:        ev.Level,
		Status:       IssueOpen,
		FirstSeen:    ts,
		LastSeen:     ts,
		TimesSeen:    1,
		FirstRelease: ev.Release,
	}
	err := tx.QueryRowContext(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, message, culprit, source, level, status,
			first_seen, last_seen, times_seen, first_release)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		issue.ProjectID, issue.Fingerprint, issue.Title, issue.Message, issue.Culprit, issue.Source, issue.Level,
		issue.Status, issue.FirstSeen.UnixMilli(), issue.LastSeen.UnixMilli(), issue.TimesSeen, issue.FirstRelease,
	).Scan(&issue.ID)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// scanIssue reads the issueColumns, then any extra columns into extra.
func scanIssue(s scanner, extra ...any) (Issue, error) {
	var (
		issue                   Issue
		firstSeen, lastSeen     int64
		resolvedAt, regressedAt sql.NullInt64
	)
	dest := []any{&issue.ID, &issue.ProjectID, &issue.Fingerprint, &issue.Title, &issue.Message, &issue.Culprit,
		&issue.Source, &issue.Level, &issue.Status, &firstSeen, &lastSeen, &resolvedAt, &regressedAt, &issue.TimesSeen,
		&issue.FirstRelease, &issue.RegressedRelease}
	err := s.Scan(append(dest, extra...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Issue{}, ErrNotFound
	}
	if err != nil {
		return Issue{}, err
	}
	issue.FirstSeen = time.UnixMilli(firstSeen).UTC()
	issue.LastSeen = time.UnixMilli(lastSeen).UTC()
	issue.ResolvedAt = timePtr(resolvedAt)
	issue.RegressedAt = timePtr(regressedAt)
	return issue, nil
}

// timePtr turns a nullable unix-milliseconds column into a time, nil for NULL.
func timePtr(ms sql.NullInt64) *time.Time {
	if !ms.Valid {
		return nil
	}
	t := time.UnixMilli(ms.Int64).UTC()
	return &t
}

// nullMillis is the column value for an optional time: unix milliseconds, or NULL for nil.
func nullMillis(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

// msTime truncates t to the milliseconds the database stores.
func msTime(t time.Time) time.Time {
	return time.UnixMilli(t.UnixMilli()).UTC()
}
