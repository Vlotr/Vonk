//go:build perf

// The performance check of page statistics (go test -tags perf ./internal/jobs -run TestPerf -v):
// 1 000 000 page views across 3 projects, 200 routes and 30 days go
// through the ingest writer into a fresh database in a temp directory
// (deleted afterwards), the aggregation job rebuilds every hour, and the
// dashboard's queries read the result. docs/perf.md records the numbers
// and the targets: writer at least 5 000 events/s, aggregation under
// 60 s, the 30-day pages query and the overview under 300 ms.
package jobs

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/issues"
	"monitor/internal/posthog"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

const (
	perfPageviews = 1_000_000
	perfProjects  = 3
	perfRoutes    = 200
	perfDays      = 30
	perfVisitors  = 40_000 // per project
	perfErrors    = 20_000 // error events with a route, across the projects
)

// perfSession is one visit: its page views follow each other in one tab.
type perfSession struct {
	project int
	start   time.Time
	views   int
	user    int
	bot     bool
	leave   bool // the last page view gets a $pageleave
}

func TestPerf(t *testing.T) {
	ctx := context.Background()
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir() // deleted when the test ends, with the database
	path := filepath.Join(dir, "monitor.db")
	db, err := store.Open(ctx, config.Config{DataDir: dir, SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)
	from := now.Add(-perfDays * 24 * time.Hour)

	var projects []store.Project
	for i := range perfProjects {
		projects = append(projects, projectKeeping(t, db, fmt.Sprintf("Project %d", i+1), 90))
	}
	routes := make([]string, perfRoutes)
	for i := range routes {
		routes[i] = fmt.Sprintf("/section-%d/page-%d", i%12, i)
	}

	// Visits: 1 to 8 page views, a few seconds to two minutes apart; 3 %
	// bots; 80 % end with a page leave. Routes follow a Zipf curve (a few
	// pages get most views), visit starts are spread over 30 days and
	// arrive in time order, as they would.
	rng := rand.New(rand.NewPCG(42, 7))
	zipf := rand.NewZipf(rng, 1.1, 4, perfRoutes-1)
	var sessions []perfSession
	for total := 0; total < perfPageviews; {
		s := perfSession{
			project: rng.IntN(perfProjects),
			start:   from.Add(time.Duration(rng.Int64N(int64(perfDays * 24 * time.Hour)))),
			views:   min(1+rng.IntN(8), perfPageviews-total),
			user:    rng.IntN(perfVisitors),
			bot:     rng.IntN(100) < 3,
			leave:   rng.IntN(100) < 80,
		}
		sessions = append(sessions, s)
		total += s.views
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].start.Before(sessions[j].start) })

	// The writer, fed from one goroutine as fast as the queue allows.
	queue := ingest.NewQueue(ingest.DefaultQueueCapacity)
	writer := ingest.NewWriter(db, queue, issues.NoHooks{}, quiet)
	done := make(chan struct{})
	go func() {
		writer.Run(ctx)
		close(done)
	}()
	enqueue := func(item ingest.Item) {
		for !queue.TryEnqueue(item) {
			time.Sleep(200 * time.Microsecond) // the SDK would retry after a 429
		}
	}

	start := time.Now()
	events, views := 0, 0
	for n, s := range sessions {
		at := s.start
		sessionID := fmt.Sprintf("s%d", n)
		var last string
		for v := range s.views {
			id := fmt.Sprintf("pv%d-%d", n, v)
			route := routes[zipf.Uint64()]
			ev := &posthog.Event{UUID: id, Name: posthog.EventPageview, PageviewID: id, SessionID: sessionID, WindowID: "w",
				DistinctID: fmt.Sprintf("p%d-u%d", s.project, s.user), Timestamp: at, Properties: map[string]any{}, Size: 1200}
			enqueue(ingest.Item{ProjectID: projects[s.project].ID, ReceivedAt: at.Add(time.Second), Page: ev, Route: route, Path: route, IsBot: s.bot})
			events++
			views++
			last = id
			at = at.Add(time.Duration(3+rng.IntN(120)) * time.Second)
		}
		if s.leave {
			ev := &posthog.Event{UUID: "l" + last, Name: posthog.EventPageleave, PageviewID: last, SessionID: sessionID, WindowID: "w",
				Timestamp: at, Properties: map[string]any{}, Size: 1200}
			enqueue(ingest.Item{ProjectID: projects[s.project].ID, ReceivedAt: at.Add(time.Second), Page: ev})
			events++
		}
	}
	for i := range perfErrors {
		p := projects[i%perfProjects]
		at := from.Add(time.Duration(rng.Int64N(int64(perfDays * 24 * time.Hour))))
		ev, err := sentry.DecodeEvent([]byte(fmt.Sprintf(`{"event_id":"%032x","platform":"php","message":"error %d"}`, i, i%40)))
		if err != nil {
			t.Fatal(err)
		}
		ev.Timestamp = at
		enqueue(ingest.Item{ProjectID: p.ID, ReceivedAt: at.Add(time.Second), Event: ev, Route: routes[zipf.Uint64()]})
		events++
	}
	writer.Close()
	<-done
	writeTook := time.Since(start)
	if stats := writer.Stats(); stats.LostEvents > 0 {
		t.Fatalf("the writer lost %d events: %s", stats.LostEvents, stats.LastError)
	}
	rate := float64(events) / writeTook.Seconds()
	t.Logf("writer: %d events (%d page views, %d page leaves, %d errors) in %s: %.0f events/s",
		events, views, events-views-perfErrors, perfErrors, writeTook.Round(time.Millisecond), rate)

	// Every hour of the 30 days is dirty now: the first run rebuilds them all.
	agg := &Aggregate{DB: db, Log: quiet, Now: func() time.Time { return now }}
	start = time.Now()
	hours, err := agg.run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	aggTook := time.Since(start)
	t.Logf("aggregation: %d project hours in %s", hours, aggTook.Round(time.Millisecond))

	// The dashboard's reads, each the median of five runs.
	ids := make([]int64, len(projects))
	for i, p := range projects {
		ids[i] = p.ID
	}
	var pagesTook, overviewTook, overview30Took time.Duration
	var stats store.PageStats
	for i, p := range projects {
		took := median(func() {
			if stats, err = db.PageStats(ctx, p.ID, from, now.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		})
		pagesTook = max(pagesTook, took)
		t.Logf("pages query, 30 days, project %d: %s (%d views, %d visitors, %d routes, %d bots, %d errors)",
			i+1, took.Round(time.Microsecond), stats.Totals.Views, stats.Totals.Visitors, len(stats.Routes), stats.Totals.Bots, stats.Totals.Errors)
	}
	overviewTook = median(func() {
		if _, err := db.PageviewTotals(ctx, ids, now.Add(-24*time.Hour), now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	overview30Took = median(func() {
		if _, err := db.PageviewTotals(ctx, ids, from, now.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("overview page columns, 3 projects: 24 h %s, 30 days %s", overviewTook.Round(time.Microsecond), overview30Took.Round(time.Microsecond))

	sketchBytes := sketchSizes(t, path)
	t.Logf("database: %d MB; %d hourly rows, median sketch %d bytes, largest %d bytes",
		db.SizeBytes(ctx)>>20, len(sketchBytes), sketchBytes[len(sketchBytes)/2], sketchBytes[len(sketchBytes)-1])

	if rate < 5000 {
		t.Errorf("writer: %.0f events/s, target at least 5 000", rate)
	}
	if aggTook > time.Minute {
		t.Errorf("aggregation took %s, target under 60 s", aggTook)
	}
	if pagesTook > 300*time.Millisecond || overviewTook > 300*time.Millisecond || overview30Took > 300*time.Millisecond {
		t.Errorf("pages query %s, overview %s (24 h) and %s (30 days); target under 300 ms", pagesTook, overviewTook, overview30Took)
	}
}

// sketchSizes returns the sizes of the stored visitor sketches, smallest first.
func sketchSizes(t *testing.T, path string) []int {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query("SELECT length(visitors_hll) FROM hourly_stats ORDER BY 1")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sizes []int
	for rows.Next() {
		var n int
		rows.Scan(&n)
		sizes = append(sizes, n)
	}
	return sizes
}

// median runs f five times and returns the median duration.
func median(f func()) time.Duration {
	var runs []time.Duration
	for range 5 {
		start := time.Now()
		f()
		runs = append(runs, time.Since(start))
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i] < runs[j] })
	return runs[2]
}
