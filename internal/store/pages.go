package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"monitor/internal/hll"
)

// Page statistics: the aggregation job rebuilds hourly_stats from the raw
// page views and error events (RecomputeHour, which also rebuilds the
// hour's custom events in event_hourly), and the dashboard reads only
// hourly_stats (PageStats, PageviewTotals).

// ProjectHour is one clock hour (UTC) of one project.
type ProjectHour struct {
	ProjectID int64
	Hour      time.Time
}

// DirtyHours returns the hours whose summaries are out of date: data came
// in for them since they were last rebuilt.
func (db *DB) DirtyHours(ctx context.Context) ([]ProjectHour, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT project_id, hour FROM stats_dirty_hours ORDER BY project_id, hour")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var hours []ProjectHour
	for rows.Next() {
		var ph ProjectHour
		var hour int64
		if err := rows.Scan(&ph.ProjectID, &hour); err != nil {
			return nil, err
		}
		ph.Hour = time.UnixMilli(hour).UTC()
		hours = append(hours, ph)
	}
	return hours, rows.Err()
}

// hourAgg is one route's numbers while an hour is rebuilt.
type hourAgg struct {
	views, bots, errors int64
	totalMs, timed      int64
	visitors            *hll.Sketch
}

// RecomputeHour rebuilds the summaries of one project's clock hour (the
// hour that contains hour) from its page views, error events and custom
// events, and clears the hour's dirty mark, in one write transaction:
// running it again gives the same rows. The mark is removed first: a
// writer that marked the hour and has not committed yet holds the mark
// locked (markDirty), so this waits for it and reads its rows; one that
// marks the hour afterwards makes a new mark for the next run. The project
// is kept first (projectlocks.go); a project deleted meanwhile has nothing
// left to rebuild. Per route (hourly_stats): page
// views by people, their visitors (a sketch of their user ids), the page
// views with a duration and their sum, page views by bots, and error
// events. Per custom event name (event_hourly): see recomputeEventHour.
func (db *DB) RecomputeHour(ctx context.Context, projectID int64, hour time.Time) error {
	from := hour.UTC().Truncate(time.Hour)
	start, end := from.UnixMilli(), from.Add(time.Hour).UnixMilli()

	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	if exists, err := projectExists(ctx, tx, projectID); err != nil || !exists {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM stats_dirty_hours WHERE project_id = ? AND hour = ?", projectID, start); err != nil {
		return err
	}

	routes := map[string]*hourAgg{}
	get := func(route string) *hourAgg {
		a := routes[route]
		if a == nil {
			a = &hourAgg{visitors: hll.New()}
			routes[route] = a
		}
		return a
	}

	rows, err := tx.QueryContext(ctx, `SELECT route, user_id, duration_ms, is_bot FROM pageviews
		WHERE project_id = ? AND start >= ? AND start < ?`, projectID, start, end)
	if err != nil {
		return err
	}
	for rows.Next() {
		var (
			route, user string
			duration    sql.NullInt64
			bot         bool
		)
		if err := rows.Scan(&route, &user, &duration, &bot); err != nil {
			rows.Close()
			return err
		}
		a := get(route)
		if bot {
			a.bots++
			continue
		}
		a.views++
		if user != "" {
			a.visitors.Add(user)
		}
		if duration.Valid {
			a.totalMs += duration.Int64
			a.timed++
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	rows, err = tx.QueryContext(ctx, `SELECT route, COUNT(*) FROM error_events
		WHERE project_id = ? AND timestamp >= ? AND timestamp < ? AND route <> '' GROUP BY route`, projectID, start, end)
	if err != nil {
		return err
	}
	for rows.Next() {
		var route string
		var n int64
		if err := rows.Scan(&route, &n); err != nil {
			rows.Close()
			return err
		}
		get(route).errors = n
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	// Replace the hour's rows: routes that lost their data (retention, a
	// deleted event) lose their row too.
	if _, err := tx.ExecContext(ctx, "DELETE FROM hourly_stats WHERE project_id = ? AND hour = ?", projectID, start); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO hourly_stats (project_id, route, hour, views, visitors_hll,
		total_duration_ms, duration_count, bots, errors) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for route, a := range routes {
		if _, err := insert.ExecContext(ctx, projectID, route, start, a.views, a.visitors.Encode(), a.totalMs, a.timed, a.bots, a.errors); err != nil {
			return fmt.Errorf("hourly stats for %s: %w", route, err)
		}
	}
	if err := recomputeEventHour(ctx, tx, projectID, start, end); err != nil {
		return err
	}
	return tx.Commit()
}

// RouteStats are one route's numbers over a period (or, as PageStats.Totals,
// the whole project's, with Route "").
type RouteStats struct {
	Route  string
	Views  int64 // page views by people
	Bots   int64 // page views by bots
	Errors int64 // error events on the route
	// Visitors are the distinct people, counted from the hours' sketches
	// (exact up to 4096, within about 1 % beyond).
	Visitors int
	// TotalDurationMs over DurationCount page views is the average time on
	// the page; page views left without a page leave are not in it.
	TotalDurationMs int64
	DurationCount   int64
}

// AvgDurationMs returns the average time on the page, and false when no
// page view had a duration.
func (r RouteStats) AvgDurationMs() (int64, bool) {
	if r.DurationCount == 0 {
		return 0, false
	}
	return r.TotalDurationMs / r.DurationCount, true
}

// HourViews are the page views by people in one clock hour.
type HourViews struct {
	Hour  time.Time
	Views int64
}

// PageStats are a project's page statistics over a period.
type PageStats struct {
	Totals RouteStats
	// Routes are sorted by views, the busiest first (then by route).
	Routes []RouteStats
	// Hours are the hours that have data, oldest first.
	Hours []HourViews
}

// PageStats sums a project's hourly summaries over the hours that start in
// [from, to): totals, per route, and views per hour. Visitors are merged
// from the hours' sketches, so a visitor who came back counts once.
func (db *DB) PageStats(ctx context.Context, projectID int64, from, to time.Time) (PageStats, error) {
	rows, err := db.read.QueryContext(ctx, `SELECT route, hour, views, visitors_hll, total_duration_ms, duration_count, bots, errors
		FROM hourly_stats WHERE project_id = ? AND hour >= ? AND hour < ?`, projectID, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return PageStats{}, err
	}
	defer rows.Close()

	type routeAcc struct {
		RouteStats
		visitors *hll.Sketch
	}
	routes := map[string]*routeAcc{}
	hours := map[int64]int64{}
	allVisitors := hll.New()
	var totals RouteStats
	var sketch []byte
	for rows.Next() {
		var (
			r    RouteStats
			hour int64
		)
		if err := rows.Scan(&r.Route, &hour, &r.Views, &sketch, &r.TotalDurationMs, &r.DurationCount, &r.Bots, &r.Errors); err != nil {
			return PageStats{}, err
		}
		acc := routes[r.Route]
		if acc == nil {
			acc = &routeAcc{RouteStats: RouteStats{Route: r.Route}, visitors: hll.New()}
			routes[r.Route] = acc
		}
		acc.Views += r.Views
		acc.Bots += r.Bots
		acc.Errors += r.Errors
		acc.TotalDurationMs += r.TotalDurationMs
		acc.DurationCount += r.DurationCount
		if err := acc.visitors.MergeEncoded(sketch); err != nil {
			return PageStats{}, fmt.Errorf("visitors of %s: %w", r.Route, err)
		}
		allVisitors.MergeEncoded(sketch) // checked just above
		hours[hour] += r.Views

		totals.Views += r.Views
		totals.Bots += r.Bots
		totals.Errors += r.Errors
		totals.TotalDurationMs += r.TotalDurationMs
		totals.DurationCount += r.DurationCount
	}
	if err := rows.Err(); err != nil {
		return PageStats{}, err
	}

	stats := PageStats{Totals: totals, Routes: make([]RouteStats, 0, len(routes)), Hours: make([]HourViews, 0, len(hours))}
	stats.Totals.Visitors = allVisitors.Count()
	for _, acc := range routes {
		acc.Visitors = acc.visitors.Count()
		stats.Routes = append(stats.Routes, acc.RouteStats)
	}
	sort.Slice(stats.Routes, func(i, j int) bool {
		a, b := stats.Routes[i], stats.Routes[j]
		if a.Views != b.Views {
			return a.Views > b.Views
		}
		return a.Route < b.Route
	})
	for hour, views := range hours {
		stats.Hours = append(stats.Hours, HourViews{Hour: time.UnixMilli(hour).UTC(), Views: views})
	}
	sort.Slice(stats.Hours, func(i, j int) bool { return stats.Hours[i].Hour.Before(stats.Hours[j].Hour) })
	return stats, nil
}

// PageTotals are a project's page views by people over a period and the
// sum and count of their durations (the average time on a page).
type PageTotals struct {
	Views           int64
	TotalDurationMs int64
	DurationCount   int64
}

// PageviewTotals returns the page totals of the hours that start in
// [from, to) for each of the projects (for All projects); projects without
// data are left out.
func (db *DB) PageviewTotals(ctx context.Context, projectIDs []int64, from, to time.Time) (map[int64]PageTotals, error) {
	totals := map[int64]PageTotals{}
	if len(projectIDs) == 0 {
		return totals, nil
	}
	args := []any{from.UnixMilli(), to.UnixMilli()}
	for _, id := range projectIDs {
		args = append(args, id)
	}
	rows, err := db.read.QueryContext(ctx, `SELECT project_id, SUM(views), SUM(total_duration_ms), SUM(duration_count)
		FROM hourly_stats WHERE hour >= ? AND hour < ? AND project_id IN (?`+strings.Repeat(", ?", len(projectIDs)-1)+`)
		GROUP BY project_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t PageTotals
		if err := rows.Scan(&id, &t.Views, &t.TotalDurationMs, &t.DurationCount); err != nil {
			return nil, err
		}
		totals[id] = t
	}
	return totals, rows.Err()
}
