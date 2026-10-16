package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// MaxPageDuration is the longest time on a page that counts; longer ones
// (a tab left open overnight) are dropped from the averages, as are
// negative ones (a clock that jumped).
const MaxPageDuration = 6 * time.Hour

// PageEvent is a $pageview or a $pageleave, ready to store.
type PageEvent struct {
	ProjectID int64
	// Leave says this is a $pageleave; else it is a $pageview.
	Leave bool
	// EventID is the uuid of a $pageleave event, so a resent one is
	// stored once. Page views use PageviewID for that.
	EventID string
	// PageviewID is the page view's id ($pageview_id); for a leave, the
	// page view it leaves ("" when the SDK did not say).
	PageviewID string
	SessionID  string // $session_id; "" when none
	WindowID   string // $window_id: one browser tab
	UserID     string // distinct_id
	Route      string // page views only
	Path       string // page views only
	At         time.Time
	IsBot      bool // page views only
}

// PageSaveResult tells what SavePageEvents did with a batch.
type PageSaveResult struct {
	Stored     int // new rows
	Duplicates int // page views or leaves that were stored before
	Orphaned   int // events of projects that no longer exist
}

// SavePageEvents stores a batch of page views and leaves in one
// transaction and gives page views their time on the page.
//
// A page view is closed by its $pageleave (by $pageview_id) and by the
// next page view in the same tab (session and window), whichever comes
// first; a leave without $pageview_id closes the latest page view of its
// tab before it. Every closer can only make a duration shorter, so the
// result is the same whatever order the events arrive in: each new page
// view looks for closers that are already stored, and each new event
// updates the page view it closes. Durations that are negative or longer
// than MaxPageDuration are dropped, and a page view nothing closed stays
// marked left_without_pageleave. The hours whose summaries change are
// marked for the aggregation job. An error means nothing was stored.
func (db *DB) SavePageEvents(ctx context.Context, events []PageEvent) (PageSaveResult, error) {
	var result PageSaveResult
	if len(events) == 0 {
		return result, nil
	}
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback() // no-op after Commit

	s, err := preparePageStatements(ctx, tx)
	if err != nil {
		return result, err
	}
	defer s.close()

	projects := map[int64]bool{}
	dirty := map[projectHour]bool{}
	for _, ev := range events {
		exists, known := projects[ev.ProjectID]
		if !known {
			if exists, err = projectExists(ctx, tx, ev.ProjectID); err != nil {
				return PageSaveResult{}, err
			}
			projects[ev.ProjectID] = exists
		}
		if !exists {
			result.Orphaned++
			continue
		}
		var stored bool
		if ev.Leave {
			stored, err = s.saveLeave(ctx, ev, dirty)
		} else {
			stored, err = s.saveView(ctx, ev, dirty)
		}
		if err != nil {
			return PageSaveResult{}, err
		}
		if stored {
			result.Stored++
		} else {
			result.Duplicates++
		}
	}
	if err := markDirty(ctx, tx, dirty); err != nil {
		return PageSaveResult{}, err
	}
	return result, tx.Commit()
}

// projectHour is one hour of one project's summaries.
type projectHour struct {
	projectID int64
	hour      int64 // unix ms of the hour's start
}

func hourOf(t time.Time) int64 {
	return t.UTC().Truncate(time.Hour).UnixMilli()
}

func hourOfMillis(ms int64) int64 {
	return hourOf(time.UnixMilli(ms))
}

// markDirty records hours whose summaries the aggregation job must rebuild.
func markDirty(ctx context.Context, tx *sql.Tx, hours map[projectHour]bool) error {
	for ph := range hours {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO stats_dirty_hours (project_id, hour) VALUES (?, ?) ON CONFLICT DO NOTHING", ph.projectID, ph.hour); err != nil {
			return err
		}
	}
	return nil
}

// pageStatements are the statements SavePageEvents runs for every event,
// prepared once per batch.
type pageStatements struct {
	viewExists, insertView, nextInTab, prevInTab, leavesOf, tabLeaves *sql.Stmt
	viewByID, updateView, insertLeave, latestInTab                    *sql.Stmt
}

func preparePageStatements(ctx context.Context, tx *sql.Tx) (*pageStatements, error) {
	s := &pageStatements{}
	queries := []struct {
		stmt  **sql.Stmt
		query string
	}{
		{&s.viewExists, "SELECT COUNT(*) FROM pageviews WHERE project_id = ? AND pageview_id = ?"},
		{&s.insertView, `INSERT INTO pageviews (project_id, pageview_id, session_id, window_id, user_id, route, path, start,
			duration_ms, left_without_pageleave, is_bot) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`},
		// The next and the previous page view in the same tab.
		{&s.nextInTab, `SELECT start FROM pageviews WHERE project_id = ? AND session_id = ? AND window_id = ? AND start > ?
			ORDER BY start LIMIT 1`},
		{&s.prevInTab, `SELECT id, start, duration_ms, left_without_pageleave FROM pageviews
			WHERE project_id = ? AND session_id = ? AND window_id = ? AND start < ? ORDER BY start DESC, id DESC LIMIT 1`},
		// The leave of a page view, and the leaves without a page view id in a tab from a time on.
		{&s.leavesOf, "SELECT at FROM pageleaves WHERE project_id = ? AND pageview_id = ?"},
		{&s.tabLeaves, `SELECT at FROM pageleaves WHERE project_id = ? AND pageview_id = '' AND session_id = ? AND window_id = ?
			AND at >= ? AND at < ?`},
		{&s.viewByID, "SELECT id, start, duration_ms, left_without_pageleave FROM pageviews WHERE project_id = ? AND pageview_id = ?"},
		{&s.latestInTab, `SELECT id, start, duration_ms, left_without_pageleave FROM pageviews
			WHERE project_id = ? AND session_id = ? AND window_id = ? AND start <= ? ORDER BY start DESC, id DESC LIMIT 1`},
		{&s.updateView, "UPDATE pageviews SET duration_ms = ?, left_without_pageleave = ? WHERE id = ?"},
		{&s.insertLeave, `INSERT INTO pageleaves (project_id, event_id, pageview_id, session_id, window_id, at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, event_id) DO NOTHING`},
	}
	for _, q := range queries {
		stmt, err := tx.PrepareContext(ctx, q.query)
		if err != nil {
			s.close()
			return nil, fmt.Errorf("prepare: %w", err)
		}
		*q.stmt = stmt
	}
	return s, nil
}

func (s *pageStatements) close() {
	for _, stmt := range []*sql.Stmt{s.viewExists, s.insertView, s.nextInTab, s.prevInTab, s.leavesOf, s.tabLeaves,
		s.viewByID, s.updateView, s.insertLeave, s.latestInTab} {
		if stmt != nil {
			stmt.Close()
		}
	}
}

// closing is a page view's state while closers are applied: its duration
// (nil: none yet, or none plausible) and whether anything closed it.
type closing struct {
	start    int64
	duration sql.NullInt64
	closed   bool
}

// close applies one closer at unix ms at: the page view counts as left,
// and its duration becomes at - start when that is plausible and shorter
// than what it had. It reports whether anything changed.
func (c *closing) close(at int64) bool {
	changed := !c.closed
	c.closed = true
	d := at - c.start
	if d >= 0 && d <= MaxPageDuration.Milliseconds() && (!c.duration.Valid || d < c.duration.Int64) {
		c.duration = sql.NullInt64{Int64: d, Valid: true}
		changed = true
	}
	return changed
}

// saveView stores a page view, closed by what is already stored, and
// closes the page view before it in its tab. False when it was stored before.
func (s *pageStatements) saveView(ctx context.Context, ev PageEvent, dirty map[projectHour]bool) (bool, error) {
	var n int
	if err := s.viewExists.QueryRowContext(ctx, ev.ProjectID, ev.PageviewID).Scan(&n); err != nil || n > 0 {
		return false, err
	}
	start := ev.At.UnixMilli()
	c := closing{start: start}
	inTab := ev.SessionID != ""

	// The next page view in the tab, if it arrived first.
	next := sql.NullInt64{}
	if inTab {
		err := s.nextInTab.QueryRowContext(ctx, ev.ProjectID, ev.SessionID, ev.WindowID, start).Scan(&next)
		if err != nil && err != sql.ErrNoRows {
			return false, err
		}
		if next.Valid {
			c.close(next.Int64)
		}
	}
	// Its own leave, and leaves without a page view id between it and the next one.
	if err := closeByRows(ctx, &c, s.leavesOf, ev.ProjectID, ev.PageviewID); err != nil {
		return false, err
	}
	if inTab {
		until := int64(1<<62 - 1)
		if next.Valid {
			until = next.Int64
		}
		if err := closeByRows(ctx, &c, s.tabLeaves, ev.ProjectID, ev.SessionID, ev.WindowID, start, until); err != nil {
			return false, err
		}
	}
	_, err := s.insertView.ExecContext(ctx, ev.ProjectID, ev.PageviewID, ev.SessionID, ev.WindowID, ev.UserID, ev.Route, ev.Path,
		start, c.duration, !c.closed, ev.IsBot)
	if err != nil {
		return false, fmt.Errorf("page view %s: %w", ev.PageviewID, err)
	}
	dirty[projectHour{ev.ProjectID, hourOfMillis(start)}] = true

	// This page view ends the one before it in the tab.
	if inTab {
		if err := s.closeRow(ctx, s.prevInTab.QueryRowContext(ctx, ev.ProjectID, ev.SessionID, ev.WindowID, start), ev.ProjectID, start, dirty); err != nil {
			return false, err
		}
	}
	return true, nil
}

// saveLeave stores a page leave and closes its page view: the one it
// names, or else the latest one of its tab before it. False when it was
// stored before.
func (s *pageStatements) saveLeave(ctx context.Context, ev PageEvent, dirty map[projectHour]bool) (bool, error) {
	at := ev.At.UnixMilli()
	res, err := s.insertLeave.ExecContext(ctx, ev.ProjectID, ev.EventID, ev.PageviewID, ev.SessionID, ev.WindowID, at)
	if err != nil {
		return false, fmt.Errorf("page leave %s: %w", ev.EventID, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return false, nil
	}
	switch {
	case ev.PageviewID != "":
		err = s.closeRow(ctx, s.viewByID.QueryRowContext(ctx, ev.ProjectID, ev.PageviewID), ev.ProjectID, at, dirty)
	case ev.SessionID != "":
		err = s.closeRow(ctx, s.latestInTab.QueryRowContext(ctx, ev.ProjectID, ev.SessionID, ev.WindowID, at), ev.ProjectID, at, dirty)
	}
	return true, err
}

// closeRow applies a closer at unix ms at to the page view row (if there
// is one) and saves it when that changed it.
func (s *pageStatements) closeRow(ctx context.Context, row *sql.Row, projectID, at int64, dirty map[projectHour]bool) error {
	var id int64
	var c closing
	var left bool
	err := row.Scan(&id, &c.start, &c.duration, &left)
	if err == sql.ErrNoRows {
		return nil // its page view has not arrived (yet)
	}
	if err != nil {
		return err
	}
	c.closed = !left
	if !c.close(at) {
		return nil
	}
	if _, err := s.updateView.ExecContext(ctx, c.duration, !c.closed, id); err != nil {
		return err
	}
	dirty[projectHour{projectID, hourOfMillis(c.start)}] = true
	return nil
}

// closeByRows applies every time the query returns as a closer.
func closeByRows(ctx context.Context, c *closing, stmt *sql.Stmt, args ...any) error {
	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var at int64
		if err := rows.Scan(&at); err != nil {
			return err
		}
		c.close(at)
	}
	return rows.Err()
}
