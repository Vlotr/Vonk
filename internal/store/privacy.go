package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Deleting a person's data on request (privacy). The SDKs send a user id:
// Sentry's user.id and PostHog's distinct_id. DeleteUserData removes every
// row stored with that id, in the chosen projects or all of them: error
// events, page views (with the page leaves that closed them) and custom
// events. Issues that lose events get their counters back in line, and
// issues left without events go. The hours those rows were counted in are
// marked dirty, so the aggregation job rebuilds their summaries
// (hourly_stats and event_hourly, with the visitor sketches) without them
// within a minute. Backups made before keep the data until they are
// removed.

// ErrNoUserID means the request named no user id. Rows without a user id
// store an empty one, so an empty id must never select anything.
var ErrNoUserID = errors.New("a user id is required")

// IssueText is what an issue shows of its newest event: the title,
// message, culprit and level the ingest writer copies onto it.
type IssueText struct {
	Title, Message, Culprit, Level string
}

// UserDataRequest says whose data DeleteUserData deletes, and where.
type UserDataRequest struct {
	// UserID is the id as the SDK sent it, matched exactly. Not empty.
	UserID string
	// ProjectIDs limits the deletion to these projects; nil means every project.
	ProjectIDs []int64
	// Describe reads the text of an issue from a stored event's JSON. An
	// issue whose newest event is deleted takes the text of its newest
	// remaining event (the deleted one may have named the person); nil
	// keeps the text it had.
	Describe func(eventData string) (IssueText, error)
}

// UserDataDeleted counts what DeleteUserData removed.
type UserDataDeleted struct {
	Projects      int // projects it looked in
	ErrorEvents   int64
	Pageviews     int64
	Pageleaves    int64
	CustomEvents  int64
	IssuesDeleted int64 // issues left without events
	IssuesUpdated int64 // issues that keep other events; their counters were recomputed
	Hours         int   // hours of summaries marked for a rebuild
}

// Rows is the number of rows deleted: events, page views and leaves, and issues.
func (d UserDataDeleted) Rows() int64 {
	return d.ErrorEvents + d.Pageviews + d.Pageleaves + d.CustomEvents + d.IssuesDeleted
}

func (d *UserDataDeleted) add(o UserDataDeleted) {
	d.Projects += o.Projects
	d.ErrorEvents += o.ErrorEvents
	d.Pageviews += o.Pageviews
	d.Pageleaves += o.Pageleaves
	d.CustomEvents += o.CustomEvents
	d.IssuesDeleted += o.IssuesDeleted
	d.IssuesUpdated += o.IssuesUpdated
	d.Hours += o.Hours
}

// userDataRetryDelays are the waits before a project's deletion is tried
// again after Postgres ended it to resolve a deadlock (Retryable), say
// with the ingest writer storing an event of two of the person's issues in
// the other order.
var userDataRetryDelays = []time.Duration{100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}

// DeleteUserData deletes every row stored with req.UserID in req's
// projects and returns what it removed. Unknown project ids
// (ErrUnknownProject) and an empty user id (ErrNoUserID) delete nothing.
// Each project goes in one write transaction of its own, so a project is
// done completely or not at all; when one fails, the projects before it
// stay done (running it again finishes the rest).
func (db *DB) DeleteUserData(ctx context.Context, req UserDataRequest) (UserDataDeleted, error) {
	var total UserDataDeleted
	if strings.TrimSpace(req.UserID) == "" {
		return total, ErrNoUserID
	}
	ids := uniqueSorted(req.ProjectIDs)
	if req.ProjectIDs == nil {
		all, err := db.ProjectIDs(ctx)
		if err != nil {
			return total, err
		}
		ids = all
	}
	for _, id := range ids {
		if _, err := db.ProjectByID(ctx, id); errors.Is(err, ErrNotFound) {
			return total, &UnknownProjectError{ID: id}
		} else if err != nil {
			return total, err
		}
	}
	for _, id := range ids {
		var (
			d   UserDataDeleted
			err error
		)
		for attempt := 0; ; attempt++ {
			d, err = db.deleteUserDataIn(ctx, id, req)
			if !Retryable(err) || attempt == len(userDataRetryDelays) {
				break
			}
			select {
			case <-ctx.Done():
				return total, ctx.Err()
			case <-time.After(userDataRetryDelays[attempt]):
			}
		}
		if err != nil {
			return total, fmt.Errorf("project %d: %w", id, err)
		}
		total.add(d)
	}
	return total, nil
}

// touchedIssue is an issue that lost events of the user: how many, and
// the time of the first and the last of them.
type touchedIssue struct {
	id              int64
	events          int64
	firstAt, lastAt int64
}

// deleteUserDataIn deletes the user's rows of one project in one write
// transaction. Each DELETE says what it deleted (RETURNING): the hours to
// mark and the issues to bring in line come from the rows that are really
// gone, also when the ingest writer stores more of the user's events
// meanwhile (those stay, and are counted). Locks go in the order the
// project's other writers take them (projectlocks.go): the project first,
// then the rows, then each issue (FOR UPDATE, in id order) and the dirty
// marks (markDirty, sorted). A project deleted meanwhile has nothing left
// to delete.
func (db *DB) deleteUserDataIn(ctx context.Context, projectID int64, req UserDataRequest) (UserDataDeleted, error) {
	var d UserDataDeleted
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return d, err
	}
	defer tx.Rollback() // no-op after Commit
	if exists, err := projectExists(ctx, tx, projectID); err != nil || !exists {
		return d, err
	}
	d.Projects = 1
	user := req.UserID

	// Page leaves first, while the page views they belong to are still
	// there: the leaves of the user's page views, and the leaves without a
	// page view id that closed one of them (the latest page view of their
	// tab at their time is the user's; see saveLeave).
	n, err := execCount(ctx, tx, `DELETE FROM pageleaves WHERE project_id = ? AND pageview_id <> '' AND pageview_id IN (
		SELECT pageview_id FROM pageviews WHERE project_id = ? AND user_id = ?)`, projectID, projectID, user)
	if err != nil {
		return d, err
	}
	d.Pageleaves += n
	n, err = execCount(ctx, tx, `DELETE FROM pageleaves WHERE project_id = ? AND pageview_id = '' AND session_id <> '' AND EXISTS (
		SELECT 1 FROM pageviews v WHERE v.project_id = pageleaves.project_id AND v.session_id = pageleaves.session_id
			AND v.window_id = pageleaves.window_id AND v.user_id = ? AND v.start <= pageleaves.at
			AND NOT EXISTS (SELECT 1 FROM pageviews w WHERE w.project_id = v.project_id AND w.session_id = v.session_id
				AND w.window_id = v.window_id AND w.start > v.start AND w.start <= pageleaves.at))`, projectID, user)
	if err != nil {
		return d, err
	}
	d.Pageleaves += n

	// The hours whose summaries counted the rows: page views, custom
	// events, and error events on a page (errors per route).
	hours := map[projectHour]bool{}
	for _, del := range []struct {
		count *int64
		query string
	}{
		{&d.Pageviews, "DELETE FROM pageviews WHERE project_id = ? AND user_id = ? RETURNING start"},
		{&d.CustomEvents, "DELETE FROM custom_events WHERE project_id = ? AND user_id = ? RETURNING at"},
	} {
		times, err := queryIDs(ctx, tx, del.query, projectID, user)
		if err != nil {
			return d, err
		}
		*del.count = int64(len(times))
		for _, at := range times {
			hours[projectHour{projectID, hourOfMillis(at)}] = true
		}
	}
	issues, err := deleteErrorEvents(ctx, tx, projectID, user, hours, &d.ErrorEvents)
	if err != nil {
		return d, err
	}

	for _, issue := range issues {
		deleted, updated, err := recountIssue(ctx, tx, issue, req.Describe)
		if err != nil {
			return d, fmt.Errorf("issue %d: %w", issue.id, err)
		}
		if deleted {
			d.IssuesDeleted++
		}
		if updated {
			d.IssuesUpdated++
		}
	}

	if err := markDirty(ctx, tx, hours); err != nil {
		return d, err
	}
	d.Hours = len(hours)
	return d, tx.Commit()
}

// deleteErrorEvents deletes the user's error events of a project, counts
// them in *count, marks the hours of the routed ones in hours, and returns
// the issues they belonged to, in id order.
func deleteErrorEvents(ctx context.Context, tx *writeTx, projectID int64, user string, hours map[projectHour]bool, count *int64) ([]touchedIssue, error) {
	rows, err := tx.QueryContext(ctx, "DELETE FROM error_events WHERE project_id = ? AND user_id = ? RETURNING issue_id, timestamp, route",
		projectID, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	byID := map[int64]*touchedIssue{}
	for rows.Next() {
		var (
			issueID, ts int64
			route       string
		)
		if err := rows.Scan(&issueID, &ts, &route); err != nil {
			return nil, err
		}
		*count++
		if route != "" {
			hours[projectHour{projectID, hourOfMillis(ts)}] = true
		}
		ti := byID[issueID]
		if ti == nil {
			ti = &touchedIssue{id: issueID, firstAt: ts, lastAt: ts}
			byID[issueID] = ti
		}
		ti.events++
		ti.firstAt, ti.lastAt = min(ti.firstAt, ts), max(ti.lastAt, ts)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	issues := make([]touchedIssue, 0, len(byID))
	for _, ti := range byID {
		issues = append(issues, *ti)
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].id < issues[j].id })
	return issues, nil
}

// recountIssue brings an issue in line after the user's events of it are
// gone: deleted when none are left, else updated. It locks the issue
// (FOR UPDATE, as the ingest writer reads it) first, so an event the
// writer stores meanwhile is in what it reads. times_seen goes down by the
// events deleted (it also counts events retention removed earlier, so it
// is not recounted from the rows). first_seen and last_seen move only when
// the deleted events were the first or the last: retention removes the
// oldest events, so the rows left say nothing about an earlier first_seen.
// When the newest event was the user's, the issue shows the text of the
// newest one left. An issue that is gone already (retention) is neither.
func recountIssue(ctx context.Context, tx *writeTx, ti touchedIssue, describe func(string) (IssueText, error)) (deleted, updated bool, err error) {
	var first, last, timesSeen int64
	err = tx.QueryRowContext(ctx, "SELECT first_seen, last_seen, times_seen FROM issues WHERE id = ?"+tx.forUpdate(), ti.id).
		Scan(&first, &last, &timesSeen)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	var left bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM error_events WHERE issue_id = ?)", ti.id).Scan(&left); err != nil {
		return false, false, err
	}
	if !left {
		_, err := tx.ExecContext(ctx, "DELETE FROM issues WHERE id = ?", ti.id)
		return err == nil, false, err
	}

	if ti.firstAt <= first {
		if err := tx.QueryRowContext(ctx, "SELECT MIN(timestamp) FROM error_events WHERE issue_id = ?", ti.id).Scan(&first); err != nil {
			return false, false, err
		}
	}
	times := max(timesSeen-ti.events, 1)
	if ti.lastAt < last {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET first_seen = ?, times_seen = ? WHERE id = ?", first, times, ti.id)
		return false, err == nil, err
	}

	var data string
	err = tx.QueryRowContext(ctx, "SELECT timestamp, data FROM error_events WHERE issue_id = ? ORDER BY timestamp DESC, id DESC LIMIT 1", ti.id).
		Scan(&last, &data)
	if err != nil {
		return false, false, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE issues SET first_seen = ?, last_seen = ?, times_seen = ? WHERE id = ?", first, last, times, ti.id); err != nil {
		return false, false, err
	}
	if describe == nil {
		return false, true, nil
	}
	text, err := describe(data)
	if err != nil {
		return false, false, fmt.Errorf("read its newest event: %w", err)
	}
	_, err = tx.ExecContext(ctx, "UPDATE issues SET title = ?, message = ?, culprit = ?, level = ? WHERE id = ?",
		text.Title, text.Message, text.Culprit, text.Level, ti.id)
	return false, err == nil, err
}

// execCount runs a statement and returns the rows it affected.
func execCount(ctx context.Context, tx *writeTx, query string, args ...any) (int64, error) {
	res, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
