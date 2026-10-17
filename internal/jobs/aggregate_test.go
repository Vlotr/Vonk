package jobs

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"reflect"
	"sort"
	"testing"
	"time"

	"monitor/internal/store"
)

func aggregator(db *store.DB, now time.Time) *Aggregate {
	return &Aggregate{DB: db, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return now }}
}

func savePageEvents(t *testing.T, db *store.DB, events ...store.PageEvent) {
	t.Helper()
	if _, err := db.SavePageEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

func saveRouteErrors(t *testing.T, db *store.DB, projectID int64, route string, at time.Time, n int) {
	t.Helper()
	events := make([]store.NewErrorEvent, n)
	for i := range events {
		events[i] = store.NewErrorEvent{ProjectID: projectID, EventID: fmt.Sprintf("%s-%d-%d", route, at.UnixNano(), i),
			Fingerprint: "f" + route, Title: "Error", Route: route, Timestamp: at, ReceivedAt: at, Data: "{}"}
	}
	if _, err := db.SaveErrorEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

func pageStats(t *testing.T, db *store.DB, projectID int64, from, to time.Time) store.PageStats {
	t.Helper()
	stats, err := db.PageStats(context.Background(), projectID, from, to)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

// rawRoute is what the raw rows say about one route.
type rawRoute struct {
	views, bots, errors int64
	visitors            map[string]bool
	totalMs, timed      int64
}

func TestAggregate_MatchesRawCounts(t *testing.T) {
	db := openDB(t)
	p := projectKeeping(t, db, "Webshop", 90)
	other := projectKeeping(t, db, "Portal", 90)
	rng := rand.New(rand.NewPCG(3, 4))
	routes := []string{"/", "/products/:slug", "/checkout"}
	raw := map[string]*rawRoute{}
	for _, r := range routes {
		raw[r] = &rawRoute{visitors: map[string]bool{}}
	}
	from := testNow.Add(-6 * time.Hour)

	// 400 page views over six hours by 60 visitors and some bots; most
	// left with a leave after up to two minutes, some without one.
	var events []store.PageEvent
	for i := range 400 {
		route := routes[rng.IntN(len(routes))]
		user := fmt.Sprintf("u%d", rng.IntN(60))
		bot := rng.IntN(10) == 0
		at := from.Add(time.Duration(rng.IntN(6*3600)) * time.Second)
		id := fmt.Sprintf("pv%d", i)
		events = append(events, store.PageEvent{ProjectID: p.ID, PageviewID: id, SessionID: id, WindowID: "w", UserID: user,
			Route: route, Path: route, At: at, IsBot: bot})
		r := raw[route]
		if bot {
			r.bots++
			continue
		}
		r.views++
		r.visitors[user] = true
		if rng.IntN(5) > 0 {
			ms := int64(1000 + rng.IntN(120_000))
			events = append(events, store.PageEvent{ProjectID: p.ID, Leave: true, EventID: "l" + id, PageviewID: id, SessionID: id,
				WindowID: "w", At: at.Add(time.Duration(ms) * time.Millisecond)})
			r.totalMs += ms
			r.timed++
		}
	}
	savePageEvents(t, db, events...)
	for i, route := range routes {
		n := 2 + i
		saveRouteErrors(t, db, p.ID, route, from.Add(time.Duration(i)*time.Hour), n)
		raw[route].errors += int64(n)
	}
	// The other project's data stays its own.
	savePageEvents(t, db, store.PageEvent{ProjectID: other.ID, PageviewID: "x", UserID: "u1", Route: "/", Path: "/", At: from})

	aggregator(db, testNow).Run(context.Background(), testNow)

	stats := pageStats(t, db, p.ID, from, testNow.Add(time.Hour))
	var total rawRoute
	allVisitors := map[string]bool{}
	for _, route := range routes {
		want := raw[route]
		var got *store.RouteStats
		for i := range stats.Routes {
			if stats.Routes[i].Route == route {
				got = &stats.Routes[i]
			}
		}
		if got == nil {
			t.Fatalf("no stats for %s: %+v", route, stats.Routes)
		}
		if got.Views != want.views || got.Bots != want.bots || got.Errors != want.errors || got.Visitors != len(want.visitors) ||
			got.TotalDurationMs != want.totalMs || got.DurationCount != want.timed {
			t.Errorf("%s: got %+v, want views %d bots %d errors %d visitors %d time %d/%d", route, *got,
				want.views, want.bots, want.errors, len(want.visitors), want.totalMs, want.timed)
		}
		total.views += want.views
		total.bots += want.bots
		total.errors += want.errors
		for u := range want.visitors {
			allVisitors[u] = true
		}
	}
	if stats.Totals.Views != total.views || stats.Totals.Bots != total.bots || stats.Totals.Errors != total.errors ||
		stats.Totals.Visitors != len(allVisitors) {
		t.Errorf("totals = %+v, want views %d bots %d errors %d visitors %d", stats.Totals, total.views, total.bots, total.errors, len(allVisitors))
	}
	// Sorted by views, the busiest first.
	if !sort.SliceIsSorted(stats.Routes, func(i, j int) bool { return stats.Routes[i].Views > stats.Routes[j].Views }) {
		t.Errorf("routes not sorted by views: %+v", stats.Routes)
	}
	// Hours add up to the views.
	var hourly int64
	for _, h := range stats.Hours {
		hourly += h.Views
	}
	if hourly != total.views {
		t.Errorf("hourly views add up to %d, want %d", hourly, total.views)
	}
	if o := pageStats(t, db, other.ID, from, testNow); o.Totals.Views != 1 {
		t.Errorf("other project = %+v", o.Totals)
	}
	totals, err := db.PageviewTotals(context.Background(), []int64{p.ID, other.ID}, from, testNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if totals[p.ID].Views != total.views || totals[other.ID].Views != 1 || totals[p.ID].DurationCount != stats.Totals.DurationCount {
		t.Errorf("overview totals = %+v", totals)
	}
}

// hourlyRows returns every hourly_stats row of the database as text.
func hourlyRows(t *testing.T, db *store.DB, projectID int64) []string {
	t.Helper()
	stats := pageStats(t, db, projectID, testNow.Add(-48*time.Hour), testNow.Add(time.Hour))
	var rows []string
	for _, h := range stats.Hours {
		rows = append(rows, fmt.Sprintf("%s %d", h.Hour.Format(time.RFC3339), h.Views))
	}
	for _, r := range stats.Routes {
		rows = append(rows, fmt.Sprintf("%+v", r))
	}
	rows = append(rows, fmt.Sprintf("%+v", stats.Totals))
	return rows
}

func TestAggregate_Idempotent(t *testing.T) {
	db := openDB(t)
	p := projectKeeping(t, db, "Webshop", 90)
	at := testNow.Add(-90 * time.Minute)
	savePageEvents(t, db,
		store.PageEvent{ProjectID: p.ID, PageviewID: "a", SessionID: "s", WindowID: "w", UserID: "u1", Route: "/", Path: "/", At: at},
		store.PageEvent{ProjectID: p.ID, PageviewID: "b", SessionID: "s", WindowID: "w", UserID: "u1", Route: "/cart", Path: "/cart", At: at.Add(time.Minute)},
		store.PageEvent{ProjectID: p.ID, PageviewID: "c", SessionID: "t", WindowID: "w", UserID: "u2", Route: "/", Path: "/", At: testNow.Add(-time.Minute)},
	)
	saveRouteErrors(t, db, p.ID, "/cart", at, 3)

	agg := aggregator(db, testNow)
	agg.Run(context.Background(), testNow)
	first := hourlyRows(t, db, p.ID)
	if got := pageStats(t, db, p.ID, at.Add(-time.Hour), testNow.Add(time.Hour)).Totals; got.Views != 3 || got.Errors != 3 {
		t.Fatalf("first run = %+v, want 3 views and 3 errors", got)
	}
	// Running again (nothing new, or every hour forced dirty) gives the same rows.
	agg.Run(context.Background(), testNow)
	if again := hourlyRows(t, db, p.ID); !reflect.DeepEqual(again, first) {
		t.Errorf("second run:\n%v\nfirst run:\n%v", again, first)
	}
	for _, h := range []time.Time{at, testNow} {
		if err := db.RecomputeHour(context.Background(), p.ID, h); err != nil {
			t.Fatal(err)
		}
	}
	if again := hourlyRows(t, db, p.ID); !reflect.DeepEqual(again, first) {
		t.Errorf("recomputed by hand:\n%v\nfirst run:\n%v", again, first)
	}
}

func TestAggregate_LateEventRecomputesHour(t *testing.T) {
	db := openDB(t)
	p := projectKeeping(t, db, "Webshop", 90)
	old := testNow.Add(-5 * time.Hour)
	savePageEvents(t, db, store.PageEvent{ProjectID: p.ID, PageviewID: "a", SessionID: "s", WindowID: "w", UserID: "u1", Route: "/", Path: "/", At: old})
	agg := aggregator(db, testNow)
	agg.Run(context.Background(), testNow)
	before := pageStats(t, db, p.ID, old.Add(-time.Hour), testNow).Totals
	if before.Views != 1 || before.DurationCount != 0 {
		t.Fatalf("before = %+v", before)
	}

	// Hours later: another page view of that hour (a browser that was
	// offline), its leave, and an error on that page, all late.
	later := testNow.Add(10 * time.Minute)
	savePageEvents(t, db,
		store.PageEvent{ProjectID: p.ID, PageviewID: "b", SessionID: "s2", WindowID: "w", UserID: "u2", Route: "/", Path: "/", At: old.Add(time.Minute)},
		store.PageEvent{ProjectID: p.ID, Leave: true, EventID: "la", PageviewID: "a", SessionID: "s", WindowID: "w", At: old.Add(40 * time.Second)},
	)
	saveRouteErrors(t, db, p.ID, "/", old.Add(2*time.Minute), 1)
	aggregator(db, later).Run(context.Background(), later)

	after := pageStats(t, db, p.ID, old.Add(-time.Hour), testNow).Totals
	if after.Views != 2 || after.Visitors != 2 || after.DurationCount != 1 || after.TotalDurationMs != 40_000 || after.Errors != 1 {
		t.Errorf("after the late events = %+v, want 2 views, 2 visitors, one of 40 s, 1 error", after)
	}
	dirty, err := db.DirtyHours(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(dirty) != 0 {
		t.Errorf("dirty hours left after the run: %v", dirty)
	}
}

// A page view at 10:59 closed by its leave at 11:01: the leave marks the
// page view's hour (a leave counts nowhere else), so a run that rebuilds
// only the dirty hours (the current hour is long past both) gives 10:00
// its time on the page.
func TestAggregate_CloserInALaterHour(t *testing.T) {
	db := openDB(t)
	p := projectKeeping(t, db, "Webshop", 90)
	view := time.Date(2026, 10, 16, 10, 59, 0, 0, time.UTC)
	savePageEvents(t, db, store.PageEvent{ProjectID: p.ID, PageviewID: "a", SessionID: "s", WindowID: "w", UserID: "u1", Route: "/cart", Path: "/cart", At: view})
	aggregator(db, testNow).Run(context.Background(), testNow)
	if before := pageStats(t, db, p.ID, view.Truncate(time.Hour), testNow).Totals; before.Views != 1 || before.DurationCount != 0 {
		t.Fatalf("before the leave = %+v", before)
	}

	savePageEvents(t, db, store.PageEvent{ProjectID: p.ID, Leave: true, EventID: "l", PageviewID: "a", SessionID: "s", WindowID: "w", At: view.Add(2 * time.Minute)})
	dirty, err := db.DirtyHours(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var hours []string
	for _, d := range dirty {
		hours = append(hours, d.Hour.Format("15:04"))
	}
	sort.Strings(hours)
	if fmt.Sprint(hours) != "[10:00]" {
		t.Errorf("dirty hours after the leave = %v, want [10:00], the page view's hour", hours)
	}
	aggregator(db, testNow).Run(context.Background(), testNow) // 12:00: neither hour is the current one

	ten := pageStats(t, db, p.ID, view.Truncate(time.Hour), view.Truncate(time.Hour).Add(time.Hour)).Totals
	if ten.Views != 1 || ten.DurationCount != 1 || ten.TotalDurationMs != 120_000 {
		t.Errorf("10:00 = %+v, want 1 view of 2 minutes", ten)
	}
}
