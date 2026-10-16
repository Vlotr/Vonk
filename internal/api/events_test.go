package api

import (
	"fmt"
	"net/http"
	"net/url"
	"testing"
	"time"

	"monitor/internal/store"
)

type eventsResponse struct {
	Project struct {
		ID              int64  `json:"id"`
		Name            string `json:"name"`
		TracksPageviews bool   `json:"tracksPageviews"`
	} `json:"project"`
	Range  string `json:"range"`
	Events []struct {
		Name  string `json:"name"`
		Count int64  `json:"count"`
		Users int    `json:"users"`
	} `json:"events"`
	Bots int64 `json:"bots"`
}

type eventSeriesResponse struct {
	Name   string `json:"name"`
	Range  string `json:"range"`
	Count  int64  `json:"count"`
	Users  int    `json:"users"`
	Series []struct {
		Start time.Time `json:"start"`
		Count int64     `json:"count"`
	} `json:"series"`
}

func eventsPath(p store.Project, query string) string {
	return fmt.Sprintf("/ui/api/projects/%d/events%s", p.ID, query)
}

func eventSeriesPath(p store.Project, name, query string) string {
	return fmt.Sprintf("/ui/api/projects/%d/events/%s%s", p.ID, url.PathEscape(name), query)
}

// The Events screen lists each custom event with how often it happened and
// by how many users, from the hourly summaries; bots apart.
func TestEvents_ListAndSeries(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	bot := customEvent(shop.ID, 6, "checkout_completed", "crawler", "/checkout", testNow.Add(-time.Hour))
	bot.IsBot = true
	env.saveCustomEvents(t,
		customEvent(shop.ID, 1, "checkout_completed", "42", "/checkout", testNow.Add(-3*time.Hour)),
		customEvent(shop.ID, 2, "checkout_completed", "42", "/checkout", testNow.Add(-time.Hour)),
		customEvent(shop.ID, 3, "checkout_completed", "7", "/checkout", testNow.Add(-time.Hour)),
		customEvent(shop.ID, 4, "product_viewed", "7", "/products/blue-shoes", testNow.Add(-2*24*time.Hour)),
		customEvent(shop.ID, 5, "old_event", "7", "/", testNow.Add(-8*24*time.Hour)),
		bot,
	)

	// Stored but not summarized: the screen reads only the hourly summaries.
	if got := getJSON[eventsResponse](t, env, eventsPath(shop, "")); len(got.Events) != 0 {
		t.Fatalf("before aggregation = %+v, want nothing", got.Events)
	}
	env.aggregate(t)

	got := getJSON[eventsResponse](t, env, eventsPath(shop, ""))
	if got.Range != "7d" || got.Project.Name != "Webshop" || got.Bots != 1 || len(got.Events) != 2 {
		t.Fatalf("7 days = %+v", got)
	}
	if e := got.Events[0]; e.Name != "checkout_completed" || e.Count != 3 || e.Users != 2 {
		t.Errorf("first event = %+v, want checkout_completed 3 times by 2 users", e)
	}
	if e := got.Events[1]; e.Name != "product_viewed" || e.Count != 1 || e.Users != 1 {
		t.Errorf("second event = %+v", e)
	}
	if got := getJSON[eventsResponse](t, env, eventsPath(shop, "?range=30d")); len(got.Events) != 3 {
		t.Errorf("30 days = %+v, want the old event too", got.Events)
	}

	// One event per hour over the last 24 hours.
	series := getJSON[eventSeriesResponse](t, env, eventSeriesPath(shop, "checkout_completed", "?range=24h"))
	if series.Name != "checkout_completed" || series.Range != "24h" || series.Count != 3 || series.Users != 2 || len(series.Series) != 24 {
		t.Fatalf("series = %+v", series)
	}
	// 12:30 now: 09:00 is bar 20, 11:00 bar 22.
	if series.Series[20].Count != 1 || series.Series[22].Count != 2 || !series.Series[22].Start.Equal(time.Date(2026, 10, 16, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("bars = %+v", series.Series)
	}
	// A week, a bar a day in the viewer's zone.
	week := getJSON[eventSeriesResponse](t, env, eventSeriesPath(shop, "checkout_completed", "?range=7d&tz=Europe/Amsterdam"))
	if len(week.Series) != 7 || week.Series[6].Count != 3 {
		t.Errorf("week = %+v", week.Series)
	}
	// An event without data in the range: zeros, not 404.
	if none := getJSON[eventSeriesResponse](t, env, eventSeriesPath(shop, "never_sent", "")); none.Count != 0 || len(none.Series) != 7 {
		t.Errorf("unknown event = %+v", none)
	}
}

func TestEvents_BadRange(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	for _, path := range []string{eventsPath(shop, "?range=1y"), eventSeriesPath(shop, "checkout_completed", "?range=1y")} {
		if rec := env.get(t, path); rec.Code != http.StatusBadRequest {
			t.Errorf("GET %s = %d, want 400", path, rec.Code)
		}
	}
	if rec := env.get(t, "/ui/api/projects/999/events"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
}

// Custom events come from every PostHog SDK with the project's token
// (posthog-node on a server too), not only from projects that track page
// views: the sidebar shows Events for a project that has any.
func TestEvents_ProjectsWithCustomEvents(t *testing.T) {
	env := newTestEnv(t)
	server := env.project(t, "Billing")   // no page views; events from posthog-node
	quiet := env.project(t, "Portal")     // nothing at all
	shop := env.pageProject(t, "Webshop") // page views, no events yet
	env.saveCustomEvents(t, customEvent(server.ID, 1, "invoice_paid", "42", "", testNow.Add(-time.Hour)))
	env.aggregate(t)

	projects := getJSON[[]struct {
		ID              int64 `json:"id"`
		TracksPageviews bool  `json:"tracksPageviews"`
		HasCustomEvents bool  `json:"hasCustomEvents"`
	}](t, env, "/ui/api/projects")
	got := map[int64][2]bool{}
	for _, p := range projects {
		got[p.ID] = [2]bool{p.TracksPageviews, p.HasCustomEvents}
	}
	want := map[int64][2]bool{server.ID: {false, true}, quiet.ID: {false, false}, shop.ID: {true, false}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("tracksPageviews, hasCustomEvents = %v, want %v", got, want)
	}

	// The Events screen says the same, so its empty state can tell "none yet" from "none in this period".
	list := getJSON[struct {
		Project struct {
			HasCustomEvents bool `json:"hasCustomEvents"`
		} `json:"project"`
	}](t, env, eventsPath(server, "?range=24h"))
	if !list.Project.HasCustomEvents {
		t.Error("the Events screen's project has no custom events")
	}
}
