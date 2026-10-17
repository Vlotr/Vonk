package store

import (
	"context"
	"database/sql"
	"sort"
	"time"
)

// One person's trail: the page views before an error (the issue page's
// "Before this error") and everything a user id did (the user activity
// page). These read raw rows of one user through the (project, user,
// time) indexes; they are lookups, never statistics.

// PageVisit is one page view of a person.
type PageVisit struct {
	Route string
	Path  string
	Start time.Time
	// DurationMs is the time on the page; nil while nothing closed it, or
	// when the time was not plausible.
	DurationMs *int64
}

// PagesBefore returns a user's page views (bots left out) that started in
// the window before at, at itself included: the limit nearest at, oldest
// first. An empty user id has no page views.
func (db *DB) PagesBefore(ctx context.Context, projectID int64, userID string, at time.Time, window time.Duration, limit int) ([]PageVisit, error) {
	visits := []PageVisit{}
	if userID == "" {
		return visits, nil
	}
	rows, err := db.read.QueryContext(ctx, `SELECT route, path, start, duration_ms FROM pageviews
		WHERE project_id = ? AND user_id = ? AND start >= ? AND start <= ? AND is_bot = FALSE
		ORDER BY start DESC, id DESC LIMIT ?`,
		projectID, userID, at.Add(-window).UnixMilli(), at.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var v PageVisit
		var start int64
		var duration sql.NullInt64
		if err := rows.Scan(&v.Route, &v.Path, &start, &duration); err != nil {
			return nil, err
		}
		v.Start = time.UnixMilli(start).UTC()
		if duration.Valid {
			v.DurationMs = &duration.Int64
		}
		visits = append(visits, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i, j := 0, len(visits)-1; i < j; i, j = i+1, j-1 {
		visits[i], visits[j] = visits[j], visits[i]
	}
	return visits, nil
}

// Kinds of activity.
const (
	ActivityPageview = "pageview"
	ActivityError    = "error"
	ActivityEvent    = "event"
)

// Activity is one thing a user did: a page view, an error event or a
// custom event. Fields that do not belong to its kind are empty.
type Activity struct {
	Kind string
	At   time.Time
	// Path and Route are the page (page views, custom events; errors have Route).
	Path  string
	Route string
	// DurationMs is a page view's time on the page; nil without one.
	DurationMs *int64
	// IssueID, Title and Message are an error's issue; URL is the
	// error's page address (secrets masked).
	IssueID        int64
	Title, Message string
	URL            string
	// Name and Properties (JSON) are a custom event's.
	Name       string
	Properties string
}

// UserActivity returns up to limit of a user's page views, error events
// and custom events from before before, the newest first (bots left out).
// Items with the same time list the error first, then the custom event,
// then the page view: read from the bottom up, the page came first.
func (db *DB) UserActivity(ctx context.Context, projectID int64, userID string, before time.Time, limit int) ([]Activity, error) {
	items := []Activity{}
	if userID == "" {
		return items, nil
	}
	end := before.UnixMilli()
	queries := []struct {
		query string
		scan  func(*sql.Rows) (Activity, error)
	}{
		{`SELECT e.issue_id, i.title, i.message, e.url, e.route, e.timestamp FROM error_events e JOIN issues i ON i.id = e.issue_id
			WHERE e.project_id = ? AND e.user_id = ? AND e.timestamp < ? ORDER BY e.timestamp DESC, e.id DESC LIMIT ?`,
			func(rows *sql.Rows) (Activity, error) {
				a := Activity{Kind: ActivityError}
				var at int64
				err := rows.Scan(&a.IssueID, &a.Title, &a.Message, &a.URL, &a.Route, &at)
				a.At = time.UnixMilli(at).UTC()
				return a, err
			}},
		{`SELECT name, path, route, at, properties FROM custom_events
			WHERE project_id = ? AND user_id = ? AND at < ? AND is_bot = FALSE ORDER BY at DESC, id DESC LIMIT ?`,
			func(rows *sql.Rows) (Activity, error) {
				a := Activity{Kind: ActivityEvent}
				var at int64
				err := rows.Scan(&a.Name, &a.Path, &a.Route, &at, &a.Properties)
				a.At = time.UnixMilli(at).UTC()
				return a, err
			}},
		{`SELECT route, path, start, duration_ms FROM pageviews
			WHERE project_id = ? AND user_id = ? AND start < ? AND is_bot = FALSE ORDER BY start DESC, id DESC LIMIT ?`,
			func(rows *sql.Rows) (Activity, error) {
				a := Activity{Kind: ActivityPageview}
				var at int64
				var duration sql.NullInt64
				err := rows.Scan(&a.Route, &a.Path, &at, &duration)
				a.At = time.UnixMilli(at).UTC()
				if duration.Valid {
					a.DurationMs = &duration.Int64
				}
				return a, err
			}},
	}
	for _, q := range queries {
		rows, err := db.read.QueryContext(ctx, q.query, projectID, userID, end, limit)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			a, err := q.scan(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			items = append(items, a)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	// Each list is newest first; a stable sort keeps the kinds' order within a time.
	sort.SliceStable(items, func(i, j int) bool { return items[i].At.After(items[j].At) })
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}
