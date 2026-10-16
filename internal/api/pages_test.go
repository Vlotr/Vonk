package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"monitor/internal/store"
)

type pagesResponse struct {
	Project struct {
		ID              int64  `json:"id"`
		Name            string `json:"name"`
		TracksPageviews bool   `json:"tracksPageviews"`
	} `json:"project"`
	Range  string `json:"range"`
	Totals struct {
		Views     int64  `json:"views"`
		Visitors  int    `json:"visitors"`
		AvgTimeMs *int64 `json:"avgTimeMs"`
	} `json:"totals"`
	Series []struct {
		Start time.Time `json:"start"`
		Views int64     `json:"views"`
	} `json:"series"`
	Pages []struct {
		Route     string `json:"route"`
		Views     int64  `json:"views"`
		Visitors  int    `json:"visitors"`
		AvgTimeMs *int64 `json:"avgTimeMs"`
		Errors    int64  `json:"errors"`
	} `json:"pages"`
	Bots int64 `json:"bots"`
}

// visit is a test page view: who, where, when, and how long until the
// leave (0: no leave, the user left the page without one).
type visit struct {
	user, route string
	at          time.Time
	stay        time.Duration
	bot         bool
}

// visits stores page views (and their leaves) as the writer does and
// rebuilds the hours they are in, as the aggregation job does.
func (e *testEnv) visits(t *testing.T, projectID int64, vs ...visit) {
	t.Helper()
	ctx := context.Background()
	var events []store.PageEvent
	for i, v := range vs {
		id := fmt.Sprintf("pv-%d-%d-%d", projectID, v.at.UnixNano(), i)
		events = append(events, store.PageEvent{ProjectID: projectID, PageviewID: id, SessionID: id, WindowID: "w",
			UserID: v.user, Route: v.route, Path: v.route, At: v.at, IsBot: v.bot})
		if v.stay > 0 {
			events = append(events, store.PageEvent{ProjectID: projectID, Leave: true, EventID: "leave-" + id, PageviewID: id,
				SessionID: id, WindowID: "w", At: v.at.Add(v.stay)})
		}
	}
	if _, err := e.db.SavePageEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	e.aggregate(t)
}

// aggregate rebuilds every hour that has new data, as the job does each minute.
func (e *testEnv) aggregate(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	hours, err := e.db.DirtyHours(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hours {
		if err := e.db.RecomputeHour(ctx, h.ProjectID, h.Hour); err != nil {
			t.Fatal(err)
		}
	}
}

// pageProject makes a project that tracks page views.
func (e *testEnv) pageProject(t *testing.T, name string) store.Project {
	t.Helper()
	p, err := e.db.CreateProject(context.Background(), store.NewProject{Name: name, TracksLaravel: true, TracksPageviews: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func pagesPath(p store.Project, query string) string {
	return fmt.Sprintf("/ui/api/projects/%d/pages%s", p.ID, query)
}

// The pages screen reads only the hourly summaries: page views that no
// aggregation run has seen yet do not show, and the numbers stay when the
// raw page views are gone.
func TestPages_TotalsFromHourlyOnly(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	ctx := context.Background()

	// Stored but not aggregated: nothing on the screen yet.
	if _, err := env.db.SavePageEvents(ctx, []store.PageEvent{{ProjectID: shop.ID, PageviewID: "a", SessionID: "s", WindowID: "w",
		UserID: "u1", Route: "/cart", Path: "/cart", At: testNow.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	if got := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=24h")); got.Totals.Views != 0 || len(got.Pages) != 0 {
		t.Fatalf("before aggregation = %+v, want nothing (the raw page views are not read)", got)
	}

	env.aggregate(t)
	env.visits(t, shop.ID,
		visit{user: "u2", route: "/", at: testNow.Add(-2 * time.Hour), stay: 30 * time.Second},
		visit{user: "u1", route: "/", at: testNow.Add(-3 * time.Hour), stay: 90 * time.Second},
	)
	want := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=24h"))
	if want.Totals.Views != 3 || want.Totals.Visitors != 2 || want.Totals.AvgTimeMs == nil || *want.Totals.AvgTimeMs != 60_000 {
		t.Errorf("totals = %+v, want 3 views, 2 visitors, 60 s average", want.Totals)
	}
	before := env.get(t, pagesPath(shop, "?range=24h")).Body.String()

	// The raw page views and leaves go; the screen stays the same.
	future := testNow.Add(24 * time.Hour)
	if _, err := env.db.DeletePageviewsBefore(ctx, shop.ID, future, 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.DeletePageleavesBefore(ctx, shop.ID, future, 1000); err != nil {
		t.Fatal(err)
	}
	if left, err := env.db.CountPageRows(ctx, shop.ID); err != nil || left != 0 {
		t.Fatalf("%d page views and leaves left, %v", left, err)
	}
	if after := env.get(t, pagesPath(shop, "?range=24h")).Body.String(); after != before {
		t.Errorf("without raw page views:\n%s\nwant\n%s", after, before)
	}
	got := want
	if got.Project.Name != "Webshop" || !got.Project.TracksPageviews || got.Range != "24h" {
		t.Errorf("project %+v, range %q", got.Project, got.Range)
	}
}

// The average time on a page counts only page views that were closed: one
// the user left without a page leave is not in it.
func TestPages_AvgExcludesUnclosed(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	env.visits(t, shop.ID,
		visit{user: "u1", route: "/checkout", at: testNow.Add(-5 * time.Hour), stay: 2 * time.Minute},
		visit{user: "u2", route: "/checkout", at: testNow.Add(-4 * time.Hour), stay: 4 * time.Minute},
		visit{user: "u3", route: "/checkout", at: testNow.Add(-3 * time.Hour)}, // left without a page leave
		visit{user: "u4", route: "/cart", at: testNow.Add(-3 * time.Hour)},     // only unclosed views
	)
	got := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=7d"))
	if got.Totals.Views != 4 || got.Totals.AvgTimeMs == nil || *got.Totals.AvgTimeMs != 180_000 {
		t.Errorf("totals = %+v, want 4 views and an average of 3 min over the two closed ones", got.Totals)
	}
	if len(got.Pages) != 2 {
		t.Fatalf("pages = %+v", got.Pages)
	}
	checkout, cart := got.Pages[0], got.Pages[1]
	if checkout.Route != "/checkout" || checkout.Views != 3 || checkout.AvgTimeMs == nil || *checkout.AvgTimeMs != 180_000 {
		t.Errorf("/checkout = %+v", checkout)
	}
	if cart.Route != "/cart" || cart.Views != 1 || cart.AvgTimeMs != nil {
		t.Errorf("/cart = %+v, want 1 view and no average (null)", cart)
	}
}

// Bots are counted apart: not in views, visitors, times or the busiest
// pages. A page only bots saw is listed with 0 views; a page with errors
// and no visits at all is not (its errors are on the issues side).
func TestPages_BotsSeparate(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	env.visits(t, shop.ID,
		visit{user: "u1", route: "/", at: testNow.Add(-time.Hour), stay: 20 * time.Second},
		visit{user: "u2", route: "/products", at: testNow.Add(-2 * time.Hour), stay: 40 * time.Second},
		visit{user: "u2", route: "/products", at: testNow.Add(-90 * time.Minute), stay: 40 * time.Second},
		visit{user: "crawler", route: "/", at: testNow.Add(-time.Hour), stay: 5 * time.Second, bot: true},
		visit{user: "crawler", route: "/robots-only", at: testNow.Add(-time.Hour), bot: true},
		visit{user: "crawler", route: "/robots-only", at: testNow.Add(-time.Hour), bot: true},
	)
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1), url: "https://shop.example.com/admin/broken", at: testNow.Add(-time.Hour)}))
	env.aggregate(t)

	got := getJSON[pagesResponse](t, env, pagesPath(shop, ""))
	if got.Range != "7d" {
		t.Errorf("default range = %q, want 7d", got.Range)
	}
	if got.Bots != 3 {
		t.Errorf("bots = %d, want 3", got.Bots)
	}
	if got.Totals.Views != 3 || got.Totals.Visitors != 2 || got.Totals.AvgTimeMs == nil || *got.Totals.AvgTimeMs != 33_333 {
		t.Errorf("totals = %+v, want the 3 views of people: 2 visitors, 33 s on average", got.Totals)
	}
	var routes []string
	for _, p := range got.Pages {
		routes = append(routes, fmt.Sprintf("%s:%d", p.Route, p.Views))
	}
	if fmt.Sprint(routes) != "[/products:2 /:1 /robots-only:0]" {
		t.Errorf("pages = %v, want by views, the bot-only page last, the error-only page left out", routes)
	}
	var seriesViews int64
	for _, s := range got.Series {
		seriesViews += s.Views
	}
	if seriesViews != 3 {
		t.Errorf("the chart counts %d views, want 3 (no bots)", seriesViews)
	}
}

// 7 and 30 days have a bar per day, today last, in the viewer's time zone;
// 24 hours a bar per clock hour.
func TestPages_SeriesPerDay(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	// testNow is Oct 16, 12:30 UTC (14:30 in Amsterdam).
	env.visits(t, shop.ID,
		visit{user: "u1", route: "/", at: time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)},   // Oct 10, six days ago
		visit{user: "u1", route: "/", at: time.Date(2026, 10, 9, 23, 30, 0, 0, time.UTC)},  // Oct 10 in Amsterdam, Oct 9 in UTC
		visit{user: "u2", route: "/", at: time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)},  // yesterday
		visit{user: "u3", route: "/", at: time.Date(2026, 10, 16, 11, 59, 0, 0, time.UTC)}, // today, the hour before
		visit{user: "u3", route: "/", at: time.Date(2026, 10, 16, 12, 10, 0, 0, time.UTC)}, // today, this hour
		visit{user: "u4", route: "/", at: time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)},   // 26 days ago
		visit{user: "u5", route: "/", at: time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)},   // 36 days ago: in no range
	)

	week := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=7d"))
	if len(week.Series) != 7 {
		t.Fatalf("7 days = %d bars, want 7", len(week.Series))
	}
	views := func(r pagesResponse) []int64 {
		var v []int64
		for _, s := range r.Series {
			v = append(v, s.Views)
		}
		return v
	}
	if fmt.Sprint(views(week)) != "[1 0 0 0 0 1 2]" {
		t.Errorf("7 days in UTC = %v, want [1 0 0 0 0 1 2]", views(week))
	}
	if first := week.Series[0].Start; !first.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("first day starts %v, want Oct 10 00:00 UTC", first)
	}
	if week.Totals.Views != 4 {
		t.Errorf("7 days totals = %d views, want 4 (the bars' sum)", week.Totals.Views)
	}

	amsterdam := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=7d&tz=Europe/Amsterdam"))
	if fmt.Sprint(views(amsterdam)) != "[2 0 0 0 0 1 2]" {
		t.Errorf("7 days in Amsterdam = %v, want [2 0 0 0 0 1 2]", views(amsterdam))
	}
	loc, _ := time.LoadLocation("Europe/Amsterdam")
	if first := amsterdam.Series[0].Start; !first.Equal(time.Date(2026, 10, 10, 0, 0, 0, 0, loc)) {
		t.Errorf("first Amsterdam day starts %v, want Oct 10 00:00 there", first)
	}

	month := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=30d"))
	if len(month.Series) != 30 || month.Totals.Views != 6 {
		t.Errorf("30 days = %d bars, %d views; want 30 and 6", len(month.Series), month.Totals.Views)
	}

	day := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=24h"))
	if len(day.Series) != 24 {
		t.Fatalf("24 hours = %d bars, want 24", len(day.Series))
	}
	last, before := day.Series[23], day.Series[22]
	if !last.Start.Equal(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)) || last.Views != 1 || before.Views != 1 {
		t.Errorf("last hours = %+v, %+v; want 11:00 and 12:00 with a view each", before, last)
	}
	if day.Totals.Views != 2 { // yesterday 12:00 is not: the first of the 24 clock hours is 13:00 yesterday
		t.Errorf("24 hours = %d views, want 2", day.Totals.Views)
	}

	for _, q := range []string{"?range=1y", "?range=7", "?range=24H"} {
		if rec := env.get(t, pagesPath(shop, q)); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", q, rec.Code)
		}
	}
	// An unknown zone is UTC.
	if got := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=7d&tz=Mars/Olympus")); fmt.Sprint(views(got)) != "[1 0 0 0 0 1 2]" {
		t.Errorf("unknown zone = %v, want the UTC days", views(got))
	}
}

// Errors per page come with the pages, and at most 100 pages are listed.
func TestPages_ErrorsAndLimit(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	var vs []visit
	for i := range 105 {
		for range 105 - i { // page i has 105-i views
			vs = append(vs, visit{user: "u", route: fmt.Sprintf("/p%03d", i), at: testNow.Add(-time.Hour)})
		}
	}
	env.visits(t, shop.ID, vs...)
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), url: "https://shop.example.com/p001", at: testNow.Add(-time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), url: "https://shop.example.com/p001?x=1", at: testNow.Add(-2 * time.Hour)}),
	)
	env.aggregate(t)
	got := getJSON[pagesResponse](t, env, pagesPath(shop, "?range=24h"))
	if len(got.Pages) != 100 || got.Pages[0].Route != "/p000" || got.Pages[99].Route != "/p099" {
		t.Fatalf("%d pages, from %s to %s; want the 100 busiest", len(got.Pages), got.Pages[0].Route, got.Pages[len(got.Pages)-1].Route)
	}
	if got.Pages[1].Errors != 2 || got.Pages[0].Errors != 0 {
		t.Errorf("errors on /p001 = %d, on /p000 = %d; want 2 and 0", got.Pages[1].Errors, got.Pages[0].Errors)
	}
}

func TestPages_UnknownProject(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/ui/api/projects/999/pages", "/ui/api/projects/abc/pages"} {
		if rec := env.get(t, path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rec.Code)
		}
	}
}
