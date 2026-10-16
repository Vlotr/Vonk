package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// NewErrorEvent is one scrubbed Sentry error event, ready to store, with the
// fields of the issue it belongs to.
type NewErrorEvent struct {
	ProjectID   int64
	EventID     string // unique per project; a second event with the same id is a duplicate
	Fingerprint string
	Title       string
	Message     string
	Culprit     string
	Source      string
	Level       string
	Timestamp   time.Time
	ReceivedAt  time.Time
	UserID      string
	URL         string
	Route       string
	Release     string
	Environment string
	Platform    string
	Data        string // the scrubbed event as JSON
}

// ErrorEvent is one stored error event.
type ErrorEvent struct {
	ID          int64
	IssueID     int64
	ProjectID   int64
	EventID     string
	Timestamp   time.Time
	ReceivedAt  time.Time
	UserID      string
	URL         string
	Route       string
	Release     string
	Environment string
	Platform    string
	Data        string
}

// SaveResult tells what SaveErrorEvents did with a batch.
type SaveResult struct {
	Stored     int     // new event rows
	Duplicates int     // events whose id the project already had
	Orphaned   int     // events of projects that no longer exist
	NewIssues  []Issue // issues this batch created, as they are after the batch
	// Regressed are the resolved issues this batch opened again, as they are after the batch.
	Regressed []Issue
}

// SaveErrorEvents stores a batch of events in one transaction: each event is
// added to the issue with its fingerprint (created when new, opened again
// when it was resolved; see upsertIssue), and events whose id is already
// stored are skipped. Events with a route mark their hour for the
// aggregation job (errors per page). An error means nothing was stored.
func (db *DB) SaveErrorEvents(ctx context.Context, events []NewErrorEvent) (SaveResult, error) {
	var result SaveResult
	if len(events) == 0 {
		return result, nil
	}

	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return SaveResult{}, err
	}
	defer tx.Rollback() // no-op after Commit

	projects := map[int64]bool{}
	issues := map[issueKey]*Issue{}
	var created, regressed []*Issue
	dirty := map[projectHour]bool{} // hours whose errors per route change

	for _, ev := range events {
		exists, known := projects[ev.ProjectID]
		if !known {
			if exists, err = projectExists(ctx, tx, ev.ProjectID); err != nil {
				return SaveResult{}, err
			}
			projects[ev.ProjectID] = exists
		}
		if !exists {
			result.Orphaned++
			continue
		}

		var dup int
		err := tx.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM error_events WHERE project_id = ? AND event_id = ?", ev.ProjectID, ev.EventID).Scan(&dup)
		if err != nil {
			return SaveResult{}, err
		}
		if dup > 0 {
			result.Duplicates++
			continue
		}

		issue, isNew, isRegressed, err := upsertIssue(ctx, tx, issues, ev)
		if err != nil {
			return SaveResult{}, fmt.Errorf("issue for event %s: %w", ev.EventID, err)
		}
		if isNew {
			created = append(created, issue)
		}
		if isRegressed {
			regressed = append(regressed, issue)
		}

		_, err = tx.ExecContext(ctx, `
			INSERT INTO error_events (issue_id, project_id, event_id, timestamp, received_at,
				user_id, url, route, release, environment, platform, data)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			issue.ID, ev.ProjectID, ev.EventID, ev.Timestamp.UnixMilli(), ev.ReceivedAt.UnixMilli(),
			ev.UserID, ev.URL, ev.Route, ev.Release, ev.Environment, ev.Platform, ev.Data)
		if err != nil {
			return SaveResult{}, fmt.Errorf("event %s: %w", ev.EventID, err)
		}
		result.Stored++
		if ev.Route != "" {
			dirty[projectHour{ev.ProjectID, hourOf(ev.Timestamp)}] = true
		}
	}
	if err := markDirty(ctx, tx, dirty); err != nil {
		return SaveResult{}, err
	}

	if err := tx.Commit(); err != nil {
		return SaveResult{}, err
	}
	for _, issue := range created {
		result.NewIssues = append(result.NewIssues, *issue)
	}
	for _, issue := range regressed {
		result.Regressed = append(result.Regressed, *issue)
	}
	return result, nil
}

// projectExists reports whether the project exists, and keeps it from
// being deleted until the batch commits (forKeyShare). A deletion that
// comes in meanwhile waits for the batch; without this it could take the
// project first and then wait for the batch's issues or page views while
// the batch waits for the project.
func projectExists(ctx context.Context, tx *writeTx, id int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?"+tx.forKeyShare(), id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// errorEventColumns lists the columns scanErrorEvent expects, in order.
const errorEventColumns = `id, issue_id, project_id, event_id, timestamp, received_at,
	user_id, url, route, release, environment, platform, data`

// ErrorEventByEventID returns the event a project stored under this event id, or ErrNotFound.
func (db *DB) ErrorEventByEventID(ctx context.Context, projectID int64, eventID string) (ErrorEvent, error) {
	return scanErrorEvent(db.read.QueryRowContext(ctx,
		"SELECT "+errorEventColumns+" FROM error_events WHERE project_id = ? AND event_id = ?", projectID, eventID))
}

func scanErrorEvent(s scanner) (ErrorEvent, error) {
	var (
		ev                    ErrorEvent
		timestamp, receivedAt int64
	)
	err := s.Scan(&ev.ID, &ev.IssueID, &ev.ProjectID, &ev.EventID, &timestamp, &receivedAt,
		&ev.UserID, &ev.URL, &ev.Route, &ev.Release, &ev.Environment, &ev.Platform, &ev.Data)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrorEvent{}, ErrNotFound
	}
	if err != nil {
		return ErrorEvent{}, err
	}
	ev.Timestamp = time.UnixMilli(timestamp).UTC()
	ev.ReceivedAt = time.UnixMilli(receivedAt).UTC()
	return ev, nil
}

// CountErrorEvents returns how many events a project has stored.
func (db *DB) CountErrorEvents(ctx context.Context, projectID int64) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM error_events WHERE project_id = ?", projectID).Scan(&n)
	return n, err
}
