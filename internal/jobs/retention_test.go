package jobs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"monitor/internal/config"
	"monitor/internal/sourcemaps"
	"monitor/internal/store"
	"monitor/internal/store/storetest"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db"),
		DatabaseURL: storetest.DatabaseURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func projectKeeping(t *testing.T, db *store.DB, name string, days int) store.Project {
	t.Helper()
	np := store.NewProject{Name: name, RetentionDays: days, KeepForever: days == 0}
	p, err := db.CreateProject(context.Background(), np)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// saveEvents stores n events of one issue (fingerprint) in the project,
// all at the same age.
func saveEvents(t *testing.T, db *store.DB, projectID int64, fingerprint string, age time.Duration, n int) {
	t.Helper()
	at := testNow.Add(-age)
	events := make([]store.NewErrorEvent, n)
	for i := range events {
		events[i] = store.NewErrorEvent{
			ProjectID: projectID, EventID: fmt.Sprintf("%s-%d-%d", fingerprint, age/time.Hour, i), Fingerprint: fingerprint,
			Title: fingerprint, Timestamp: at, ReceivedAt: at, Data: "{}",
		}
	}
	if _, err := db.SaveErrorEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

func issueTitles(t *testing.T, db *store.DB, projectID int64) map[string]bool {
	t.Helper()
	list, err := db.ListIssues(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, i := range list {
		titles[i.Title] = true
	}
	return titles
}

func countEvents(t *testing.T, db *store.DB, projectID int64) int {
	t.Helper()
	n, err := db.CountErrorEvents(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

const day = 24 * time.Hour

func TestRetention_DeletesOnlyOldRowsOfThatProject(t *testing.T) {
	db := openDB(t)
	short := projectKeeping(t, db, "Short", 30)
	long := projectKeeping(t, db, "Long", 90)

	// Short: an issue only seen 40 days ago (goes whole), and an issue with
	// events 40 and 2 days ago (keeps the issue and the recent events).
	saveEvents(t, db, short.ID, "old", 40*day, 7)
	saveEvents(t, db, short.ID, "busy", 40*day, 5)
	saveEvents(t, db, short.ID, "busy", 2*day, 3)
	// Long: the same ages are all within 90 days.
	saveEvents(t, db, long.ID, "old", 40*day, 7)
	saveEvents(t, db, long.ID, "busy", 2*day, 3)
	saveEvents(t, db, long.ID, "ancient", 100*day, 2)

	// A batch of 4 makes several rounds per project.
	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }, BatchSize: 4}
	r.Run(context.Background(), testNow)

	if got := countEvents(t, db, short.ID); got != 3 {
		t.Errorf("Short keeps %d events, want the 3 of the last 30 days", got)
	}
	if got := issueTitles(t, db, short.ID); !got["busy"] || got["old"] || len(got) != 1 {
		t.Errorf("Short issues = %v, want only busy", got)
	}
	if got := countEvents(t, db, long.ID); got != 10 {
		t.Errorf("Long keeps %d events, want 10 (7 + 3, not the 100-day-old ones)", got)
	}
	if got := issueTitles(t, db, long.ID); !got["old"] || !got["busy"] || got["ancient"] {
		t.Errorf("Long issues = %v, want old and busy", got)
	}
}

func TestRetention_UnlimitedKeepsAll(t *testing.T) {
	db := openDB(t)
	forever := projectKeeping(t, db, "Forever", 0)
	saveEvents(t, db, forever.ID, "old", 3*365*day, 5)
	saveEvents(t, db, forever.ID, "new", time.Hour, 1)

	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }}
	r.Run(context.Background(), testNow)

	if got := countEvents(t, db, forever.ID); got != 6 {
		t.Errorf("%d events kept, want all 6", got)
	}
	if got := issueTitles(t, db, forever.ID); len(got) != 2 {
		t.Errorf("issues = %v, want both", got)
	}
}

func TestRetention_PagesAndHourlyStats(t *testing.T) {
	db := openDB(t)
	short := projectKeeping(t, db, "Short", 30)
	forever := projectKeeping(t, db, "Forever", 0)
	ctx := context.Background()
	for _, p := range []store.Project{short, forever} {
		for i, age := range []time.Duration{40 * day, 40 * day, 2 * day} {
			id := fmt.Sprintf("%d-%d", p.ID, i)
			at := testNow.Add(-age)
			savePageEvents(t, db,
				store.PageEvent{ProjectID: p.ID, PageviewID: id, SessionID: id, WindowID: "w", UserID: "u", Route: "/", Path: "/", At: at},
				store.PageEvent{ProjectID: p.ID, Leave: true, EventID: "l" + id, PageviewID: id, SessionID: id, WindowID: "w", At: at.Add(time.Second)})
		}
	}
	agg := aggregator(db, testNow)
	if _, err := agg.run(ctx); err != nil {
		t.Fatal(err)
	}
	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }, BatchSize: 1}
	r.Run(ctx, testNow)

	views := func(p store.Project) int64 {
		return pageStats(t, db, p.ID, testNow.Add(-365*day), testNow.Add(time.Hour)).Totals.Views
	}
	if got := views(short); got != 1 {
		t.Errorf("Short keeps the summaries of %d page views, want the 1 of the last 30 days", got)
	}
	if got := views(forever); got != 3 {
		t.Errorf("Forever keeps the summaries of %d page views, want all 3", got)
	}
	// The raw rows went too: rebuilding the old hour finds nothing.
	if err := db.RecomputeHour(ctx, short.ID, testNow.Add(-40*day)); err != nil {
		t.Fatal(err)
	}
	if got := views(short); got != 1 {
		t.Errorf("after rebuilding the old hour Short has %d page views, want 1 (its raw rows are gone)", got)
	}
	if n, err := db.CountPageRows(ctx, short.ID); err != nil || n != 2 {
		t.Errorf("Short keeps %d page view and leave rows (%v), want 2", n, err)
	}
}

// Custom events and their hourly summaries follow the project's retention.
func TestRetention_CustomEvents(t *testing.T) {
	db := openDB(t)
	short := projectKeeping(t, db, "Short", 30)
	forever := projectKeeping(t, db, "Forever", 0)
	ctx := context.Background()
	for _, p := range []store.Project{short, forever} {
		var events []store.CustomEvent
		for i, age := range []time.Duration{40 * day, 40 * day, 2 * day} {
			events = append(events, store.CustomEvent{ProjectID: p.ID, EventID: fmt.Sprintf("%d-%d", p.ID, i), Name: "checkout_completed",
				UserID: "u", Path: "/checkout", Route: "/checkout", At: testNow.Add(-age), Properties: "{}"})
		}
		if _, err := db.SaveCustomEvents(ctx, events); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := aggregator(db, testNow).run(ctx); err != nil {
		t.Fatal(err)
	}
	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }, BatchSize: 1}
	r.Run(ctx, testNow)

	count := func(p store.Project) int64 {
		stats, err := db.EventStats(ctx, p.ID, testNow.Add(-365*day), testNow.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		var n int64
		for _, e := range stats.Events {
			n += e.Events
		}
		return n
	}
	if got := count(short); got != 1 {
		t.Errorf("Short keeps the summaries of %d events, want the 1 of the last 30 days", got)
	}
	if got := count(forever); got != 3 {
		t.Errorf("Forever keeps the summaries of %d events, want all 3", got)
	}
	// The raw rows went too: rebuilding the old hour finds nothing.
	if err := db.RecomputeHour(ctx, short.ID, testNow.Add(-40*day)); err != nil {
		t.Fatal(err)
	}
	if got := count(short); got != 1 {
		t.Errorf("after rebuilding the old hour Short has %d events, want 1 (its raw rows are gone)", got)
	}
}

// Source maps follow the project's retention, counted from their upload
// (not from the release or the errors they map): the files go from the
// disk and their rows from the database.
func TestRetention_SourceMapsCountFromUpload(t *testing.T) {
	db := openDB(t)
	ctx := context.Background()
	dir := t.TempDir()
	short := projectKeeping(t, db, "Short", 30)
	forever := projectKeeping(t, db, "Forever", 0)
	maps := sourcemaps.New(sourcemaps.Options{DB: db, Dir: dir, Logger: quietLogger(), Now: func() time.Time { return testNow }})

	upload := func(p store.Project, name string, age time.Duration) string {
		rel := fmt.Sprintf("%d/%s", p.ID, name)
		os.MkdirAll(filepath.Join(dir, fmt.Sprint(p.ID)), 0o750)
		if err := os.WriteFile(filepath.Join(dir, rel), []byte("{}"), 0o640); err != nil {
			t.Fatal(err)
		}
		_, err := db.SaveSourceMapFiles(ctx, []store.SourceMapFile{{ProjectID: p.ID, File: "~/" + name, Kind: store.SourceMapKind,
			Bundle: name, Path: rel, Size: 2, UploadedAt: testNow.Add(-age)}})
		if err != nil {
			t.Fatal(err)
		}
		return filepath.Join(dir, rel)
	}
	old := upload(short, "old.js.map", 40*day)
	recent := upload(short, "recent.js.map", 2*day)
	kept := upload(forever, "old.js.map", 400*day)

	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }, BatchSize: 1, SourceMaps: maps}
	r.Run(ctx, testNow)

	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the 40-day-old map is still on disk: %v", err)
	}
	for _, path := range []string{recent, kept} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was deleted: %v", path, err)
		}
	}
	paths, _ := db.SourceMapPaths(ctx)
	if len(paths) != 2 {
		t.Errorf("rows left = %v, want the recent and the kept one", paths)
	}
}

// failingMaps is a SourceMaps whose deletes fail with the errors in fail,
// one per call, then delete nothing.
type failingMaps struct {
	fail  []error
	calls int
}

func (f *failingMaps) DeleteUploadedBefore(context.Context, int64, time.Time, int) (int64, error) {
	f.calls++
	if f.calls <= len(f.fail) {
		return 0, f.fail[f.calls-1]
	}
	return 0, nil
}

// One kind of data that fails (here the source maps, which go first) is
// logged, and the project's other kinds are still deleted.
func TestRetention_AFailingKindSkipsNothingElse(t *testing.T) {
	db := openDB(t)
	short := projectKeeping(t, db, "Short", 30)
	saveEvents(t, db, short.ID, "old", 40*day, 3)
	var logs bytes.Buffer
	maps := &failingMaps{fail: []error{errors.New("disk I/O error"), errors.New("disk I/O error")}}
	r := &Retention{DB: db, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: func() time.Time { return testNow }, SourceMaps: maps}
	r.Run(context.Background(), testNow)

	if got := countEvents(t, db, short.ID); got != 0 {
		t.Errorf("%d old events left after the source maps failed, want 0", got)
	}
	if !strings.Contains(logs.String(), "retention: delete old source maps") {
		t.Errorf("the failure is not logged:\n%s", logs.String())
	}
}

// A batch Postgres ended to resolve a deadlock (40P01) or a serialization
// failure (40001) is tried again, not given up until the next day.
func TestRetention_RetriesADeadlock(t *testing.T) {
	db := openDB(t)
	projectKeeping(t, db, "Short", 30)
	var logs bytes.Buffer
	maps := &failingMaps{fail: []error{&pgconn.PgError{Code: "40P01", Message: "deadlock detected"}, &pgconn.PgError{Code: "40001"}}}
	r := &Retention{DB: db, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: func() time.Time { return testNow }, SourceMaps: maps,
		retryDelays: []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}}
	r.Run(context.Background(), testNow)

	if maps.calls != 3 || strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("%d calls, logs:\n%s\nwant two retries and no error", maps.calls, logs.String())
	}

	// Past its retries a deadlock is logged like any failure.
	logs.Reset()
	deadlock := &pgconn.PgError{Code: "40P01"}
	maps = &failingMaps{fail: []error{deadlock, deadlock, deadlock, deadlock}}
	r.SourceMaps = maps
	r.Run(context.Background(), testNow)
	if maps.calls != 4 || !strings.Contains(logs.String(), "level=ERROR") {
		t.Errorf("%d calls, logs:\n%s\nwant 4 calls and an error", maps.calls, logs.String())
	}
}

// cancellingMaps ends the run's context on its first call, as a shutdown
// in the middle of a run does.
type cancellingMaps struct {
	cancel func()
	calls  int
}

func (c *cancellingMaps) DeleteUploadedBefore(ctx context.Context, _ int64, _ time.Time, _ int) (int64, error) {
	c.calls++
	c.cancel()
	return 0, ctx.Err()
}

// A shutdown during a run stops it with one line, not an error for every
// kind of every project; the next run (after the start) goes on.
func TestRetention_ShutdownStopsTheRunQuietly(t *testing.T) {
	db := openDB(t)
	for _, name := range []string{"Short", "Shorter", "Shortest"} {
		p := projectKeeping(t, db, name, 30)
		saveEvents(t, db, p.ID, "old", 40*day, 2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	maps := &cancellingMaps{cancel: cancel}
	var logs bytes.Buffer
	r := &Retention{DB: db, Log: slog.New(slog.NewTextHandler(&logs, nil)), Now: func() time.Time { return testNow }, SourceMaps: maps}
	r.Run(ctx, testNow)

	out := logs.String()
	if strings.Contains(out, "level=ERROR") || strings.Count(out, "\n") != 1 || !strings.Contains(out, "level=INFO") ||
		!strings.Contains(out, "shutting down") {
		t.Errorf("want one INFO line about the shutdown, got:\n%s", out)
	}
	if maps.calls != 1 {
		t.Errorf("the run went on after the shutdown: %d calls", maps.calls)
	}
}
