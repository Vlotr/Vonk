package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"monitor/internal/hll"
)

// Custom events are the app's own PostHog events (checkout_completed, ...).
// The writer stores them (SaveCustomEvents) and marks their hour in
// stats_dirty_hours; RecomputeHour rebuilds event_hourly with the page
// statistics; the Events screen reads only event_hourly (EventStats,
// EventSeries). One person's events are read raw for their activity.

// CustomEvent is one custom event, ready to store.
type CustomEvent struct {
	ProjectID int64
	// EventID is the event's uuid, so a resent event is stored once.
	EventID string
	Name    string
	UserID  string // distinct_id
	Path    string // the page it happened on, secrets masked
	Route   string
	At      time.Time
	IsBot   bool
	// Properties are the app's own properties as JSON, scrubbed.
	Properties string
}

// CustomSaveResult tells what SaveCustomEvents did with a batch.
type CustomSaveResult struct {
	Stored     int // new rows
	Duplicates int // events whose uuid the project already had
	Orphaned   int // events of projects that no longer exist
}

// SaveCustomEvents stores a batch of custom events in one transaction,
// once per uuid, and marks their hours for the aggregation job. An error
// means nothing was stored.
func (db *DB) SaveCustomEvents(ctx context.Context, events []CustomEvent) (CustomSaveResult, error) {
	var result CustomSaveResult
	if len(events) == 0 {
		return result, nil
	}
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback() // no-op after Commit

	insert, err := tx.PrepareContext(ctx, `INSERT INTO custom_events (project_id, event_id, name, user_id, path, route, at, is_bot, properties)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT (project_id, event_id) DO NOTHING`)
	if err != nil {
		return result, err
	}
	defer insert.Close()

	projects := map[int64]bool{}
	dirty := map[projectHour]bool{}
	for _, ev := range events {
		exists, known := projects[ev.ProjectID]
		if !known {
			if exists, err = projectExists(ctx, tx, ev.ProjectID); err != nil {
				return CustomSaveResult{}, err
			}
			projects[ev.ProjectID] = exists
		}
		if !exists {
			result.Orphaned++
			continue
		}
		props := ev.Properties
		if props == "" {
			props = "{}"
		}
		res, err := insert.ExecContext(ctx, ev.ProjectID, ev.EventID, ev.Name, ev.UserID, ev.Path, ev.Route, ev.At.UnixMilli(), ev.IsBot, props)
		if err != nil {
			return CustomSaveResult{}, fmt.Errorf("custom event %s: %w", ev.EventID, err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			result.Duplicates++
			continue
		}
		result.Stored++
		dirty[projectHour{ev.ProjectID, hourOf(ev.At)}] = true
	}
	if err := markDirty(ctx, tx, dirty); err != nil {
		return CustomSaveResult{}, err
	}
	return result, tx.Commit()
}

// eventAgg is one event name's numbers while an hour is rebuilt.
type eventAgg struct {
	events, bots int64
	users        *hll.Sketch
}

// recomputeEventHour replaces a project's event_hourly rows of the hour
// [start, end) from its custom events, inside RecomputeHour's transaction:
// per name, the events sent by people, a sketch of their user ids, and the
// events sent by bots.
func recomputeEventHour(ctx context.Context, tx *sql.Tx, projectID, start, end int64) error {
	names := map[string]*eventAgg{}
	rows, err := tx.QueryContext(ctx, `SELECT name, user_id, is_bot FROM custom_events
		WHERE project_id = ? AND at >= ? AND at < ?`, projectID, start, end)
	if err != nil {
		return err
	}
	for rows.Next() {
		var name, user string
		var bot bool
		if err := rows.Scan(&name, &user, &bot); err != nil {
			rows.Close()
			return err
		}
		a := names[name]
		if a == nil {
			a = &eventAgg{users: hll.New()}
			names[name] = a
		}
		if bot {
			a.bots++
			continue
		}
		a.events++
		if user != "" {
			a.users.Add(user)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM event_hourly WHERE project_id = ? AND hour = ?", projectID, start); err != nil {
		return err
	}
	insert, err := tx.PrepareContext(ctx, `INSERT INTO event_hourly (project_id, name, hour, events, users_hll, bots)
		VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer insert.Close()
	for name, a := range names {
		if _, err := insert.ExecContext(ctx, projectID, name, start, a.events, a.users.Encode(), a.bots); err != nil {
			return fmt.Errorf("hourly events of %s: %w", name, err)
		}
	}
	return nil
}

// EventTotals are one event name's numbers over a period.
type EventTotals struct {
	Name   string
	Events int64 // sent by people
	// Users are the distinct user ids that sent it, from the hours'
	// sketches (exact up to 4096, within about 1 % beyond).
	Users int
	Bots  int64 // sent by bots, in no other number
}

// EventStats are a project's custom events over a period.
type EventStats struct {
	// Events are sorted by how often they happened, the most first (then
	// by name). Names that only bots sent are left out.
	Events []EventTotals
	// Bots counts the events bots sent, all names together.
	Bots int64
}

// ProjectsWithCustomEvents returns the ids of the projects that have any
// custom event in their hourly summaries.
func (db *DB) ProjectsWithCustomEvents(ctx context.Context) (map[int64]bool, error) {
	ids, err := queryIDs(ctx, db.read,
		"SELECT id FROM projects WHERE EXISTS (SELECT 1 FROM event_hourly e WHERE e.project_id = projects.id)")
	if err != nil {
		return nil, err
	}
	out := make(map[int64]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out, nil
}

// HasCustomEvents reports whether a project has any custom event in its
// hourly summaries.
func (db *DB) HasCustomEvents(ctx context.Context, projectID int64) (bool, error) {
	var n int
	err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM (SELECT 1 FROM event_hourly WHERE project_id = ? LIMIT 1)", projectID).Scan(&n)
	return n > 0, err
}

// EventStats sums a project's hourly event summaries over the hours that
// start in [from, to).
func (db *DB) EventStats(ctx context.Context, projectID int64, from, to time.Time) (EventStats, error) {
	totals, _, err := db.eventHours(ctx, projectID, "", from, to)
	if err != nil {
		return EventStats{}, err
	}
	stats := EventStats{Events: []EventTotals{}}
	for _, t := range totals {
		stats.Bots += t.Bots
		if t.Events > 0 {
			stats.Events = append(stats.Events, t)
		}
	}
	sort.Slice(stats.Events, func(i, j int) bool {
		a, b := stats.Events[i], stats.Events[j]
		if a.Events != b.Events {
			return a.Events > b.Events
		}
		return a.Name < b.Name
	})
	return stats, nil
}

// HourCount is how often something happened in one clock hour.
type HourCount struct {
	Hour  time.Time
	Count int64
}

// EventSeries returns one event name's totals over the hours that start in
// [from, to), and its events by people per hour (the hours that have
// some), oldest first.
func (db *DB) EventSeries(ctx context.Context, projectID int64, name string, from, to time.Time) (EventTotals, []HourCount, error) {
	totals, hours, err := db.eventHours(ctx, projectID, name, from, to)
	if err != nil {
		return EventTotals{}, nil, err
	}
	t := totals[name]
	t.Name = name
	return t, hours, nil
}

// eventHours reads the hourly event summaries of [from, to), of one name
// (or of all when name is ""): the totals per name, and the events by
// people per hour, oldest first.
func (db *DB) eventHours(ctx context.Context, projectID int64, name string, from, to time.Time) (map[string]EventTotals, []HourCount, error) {
	query := `SELECT name, hour, events, users_hll, bots FROM event_hourly WHERE project_id = ? AND hour >= ? AND hour < ?`
	args := []any{projectID, from.UnixMilli(), to.UnixMilli()}
	if name != "" {
		query += " AND name = ?"
		args = append(args, name)
	}
	rows, err := db.read.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	totals := map[string]EventTotals{}
	users := map[string]*hll.Sketch{}
	perHour := map[int64]int64{}
	var sketch []byte
	for rows.Next() {
		var (
			n            string
			hour, events int64
			bots         int64
		)
		if err := rows.Scan(&n, &hour, &events, &sketch, &bots); err != nil {
			return nil, nil, err
		}
		t := totals[n]
		t.Name = n
		t.Events += events
		t.Bots += bots
		totals[n] = t
		if users[n] == nil {
			users[n] = hll.New()
		}
		if err := users[n].MergeEncoded(sketch); err != nil {
			return nil, nil, fmt.Errorf("users of %s: %w", n, err)
		}
		perHour[hour] += events
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	for n, sk := range users {
		t := totals[n]
		t.Users = sk.Count()
		totals[n] = t
	}
	hours := make([]HourCount, 0, len(perHour))
	for hour, n := range perHour {
		hours = append(hours, HourCount{Hour: time.UnixMilli(hour).UTC(), Count: n})
	}
	sort.Slice(hours, func(i, j int) bool { return hours[i].Hour.Before(hours[j].Hour) })
	return totals, hours, nil
}

// DeleteCustomEventsBefore deletes at most limit of a project's custom
// events from before before, and returns how many it deleted.
func (db *DB) DeleteCustomEventsBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.deleteBatch(ctx, "custom_events", "at", projectID, before.UnixMilli(), limit)
}

// DeleteEventHourlyBefore deletes at most limit of a project's hourly event
// summaries of hours that started before the hour of before: an hour that
// is partly kept keeps its summary (as DeleteHourlyStatsBefore).
func (db *DB) DeleteEventHourlyBefore(ctx context.Context, projectID int64, before time.Time, limit int) (int64, error) {
	return db.deleteBatch(ctx, "event_hourly", "hour", projectID, hourOf(before), limit)
}
