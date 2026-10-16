package store

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

var pageT0 = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// view is a $pageview in session s and tab "w1", at t0 plus seconds.
func view(p Project, id, session string, seconds float64) PageEvent {
	return PageEvent{ProjectID: p.ID, PageviewID: id, SessionID: session, WindowID: "w1", UserID: "u-" + session,
		Route: "/r/" + id, Path: "/r/" + id, At: pageT0.Add(time.Duration(seconds * float64(time.Second)))}
}

// leave is a $pageleave of page view id, at t0 plus seconds.
func leave(p Project, id, session string, seconds float64) PageEvent {
	return PageEvent{ProjectID: p.ID, Leave: true, EventID: "leave-" + id + fmt.Sprint(seconds), PageviewID: id,
		SessionID: session, WindowID: "w1", At: pageT0.Add(time.Duration(seconds * float64(time.Second)))}
}

func savePages(t *testing.T, db *DB, events ...PageEvent) PageSaveResult {
	t.Helper()
	res, err := db.SavePageEvents(context.Background(), events)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// durations returns each page view's duration in ms (-1 for none) and
// whether it is marked as left without a page leave.
func durations(t *testing.T, db *DB, p Project) map[string]string {
	t.Helper()
	rows, err := db.read.QueryContext(context.Background(), "SELECT pageview_id, duration_ms, left_without_pageleave FROM pageviews WHERE project_id = ?", p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var (
			id   string
			dur  *int64
			left bool
		)
		if err := rows.Scan(&id, &dur, &left); err != nil {
			t.Fatal(err)
		}
		switch {
		case left:
			got[id] = "left"
		case dur == nil:
			got[id] = "dropped"
		default:
			got[id] = fmt.Sprintf("%dms", *dur)
		}
	}
	return got
}

func assertDurations(t *testing.T, got, want map[string]string) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("durations = %v, want %v", got, want)
	}
}

func TestDurations_PageleavePairs(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	savePages(t, db, view(p, "a", "s1", 0))
	savePages(t, db, leave(p, "a", "s1", 12.025))
	// A second visit in another session, with its leave in the same batch.
	savePages(t, db, view(p, "b", "s2", 5), leave(p, "b", "s2", 65))
	assertDurations(t, durations(t, db, p), map[string]string{"a": "12025ms", "b": "60000ms"})

	// An SDK retry of the same events changes nothing.
	res := savePages(t, db, view(p, "a", "s1", 0), leave(p, "a", "s1", 12.025))
	if res.Stored != 0 || res.Duplicates != 2 {
		t.Errorf("retry: %+v, want 2 duplicates", res)
	}
	assertDurations(t, durations(t, db, p), map[string]string{"a": "12025ms", "b": "60000ms"})
}

func TestDurations_NextPageviewCloses(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	// A single-page app: page views follow each other in one tab; only the last one gets a leave.
	savePages(t, db, view(p, "a", "s1", 0), view(p, "b", "s1", 7.5))
	savePages(t, db, view(p, "c", "s1", 20), leave(p, "c", "s1", 30))
	// Another tab of the same session does not close this tab's page views.
	other := view(p, "x", "s1", 3)
	other.WindowID = "w2"
	savePages(t, db, other)
	assertDurations(t, durations(t, db, p), map[string]string{"a": "7500ms", "b": "12500ms", "c": "10000ms", "x": "left"})
}

func TestDurations_OutOfOrder(t *testing.T) {
	// The same visit arriving in every order gives the same durations.
	want := map[string]string{"a": "7000ms", "b": "5000ms", "c": "4000ms"}
	events := func(p Project) []PageEvent {
		return []PageEvent{
			view(p, "a", "s1", 0), view(p, "b", "s1", 7), leave(p, "b", "s1", 12), view(p, "c", "s1", 12), leave(p, "c", "s1", 16),
		}
	}
	orders := [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {2, 4, 0, 3, 1}, {3, 0, 4, 1, 2}, {1, 2, 3, 4, 0}}
	for _, order := range orders {
		t.Run(fmt.Sprint(order), func(t *testing.T) {
			db := openTestDB(t)
			p := addProject(t, db, "Webshop")
			all := events(p)
			for _, i := range order {
				savePages(t, db, all[i]) // one batch each: the worst case
			}
			assertDurations(t, durations(t, db, p), want)
		})
	}
	// And all in one batch, backwards.
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	all := events(p)
	for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
		all[i], all[j] = all[j], all[i]
	}
	savePages(t, db, all...)
	assertDurations(t, durations(t, db, p), want)
}

func TestDurations_MissingLeaveFlagged(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	// The tab was closed and the leave never came; the other one left properly.
	savePages(t, db, view(p, "a", "s1", 0), view(p, "b", "s2", 0), leave(p, "b", "s2", 30))
	assertDurations(t, durations(t, db, p), map[string]string{"a": "left", "b": "30000ms"})

	// Without a session the next page view cannot be told; a leave still pairs by id.
	lone := view(p, "c", "", 0)
	lone.WindowID = ""
	savePages(t, db, lone, view(p, "d", "", 9))
	savePages(t, db, leave(p, "d", "", 19))
	assertDurations(t, durations(t, db, p), map[string]string{"a": "left", "b": "30000ms", "c": "left", "d": "10000ms"})
}

func TestDurations_OutlierDropped(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	savePages(t, db,
		view(p, "long", "s1", 0), leave(p, "long", "s1", 7*3600), // a tab left open for 7 hours
		view(p, "back", "s2", 100), leave(p, "back", "s2", 40), // a leave before its page view (a clock jump)
		view(p, "edge", "s3", 0), leave(p, "edge", "s3", 6*3600), // exactly 6 hours still counts
	)
	assertDurations(t, durations(t, db, p), map[string]string{"long": "dropped", "back": "dropped", "edge": "21600000ms"})

	// A later, plausible closer still gives the long one its time.
	savePages(t, db, view(p, "next", "s1", 600))
	assertDurations(t, durations(t, db, p), map[string]string{"long": "600000ms", "back": "dropped", "edge": "21600000ms", "next": "left"})
}

func TestPageEvents_LeaveWithoutIDClosesLatest(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	// Other SDKs send $pageleave without $pageview_id: it closes the
	// latest page view of its tab before it, whatever arrives first.
	l := leave(p, "", "s1", 20)
	savePages(t, db, l)
	savePages(t, db, view(p, "b", "s1", 10), view(p, "a", "s1", 0))
	assertDurations(t, durations(t, db, p), map[string]string{"a": "10000ms", "b": "10000ms"})
}

func TestPageEvents_DeletedProjectSkipped(t *testing.T) {
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")
	if err := db.DeleteProject(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}
	if res := savePages(t, db, view(p, "a", "s1", 0), leave(p, "a", "s1", 1)); res.Orphaned != 2 || res.Stored != 0 {
		t.Errorf("events of a deleted project: %+v", res)
	}
}

// TestDurations_AnyOrderMatchesReference sends random visits (several
// tabs, page views closed by leaves, by the next page view, or not at all,
// some leaves without an id, some resent) in random orders and batches,
// and compares every duration with one computed from the whole visit.
func TestDurations_AnyOrderMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 11))
	db := openTestDB(t)
	p := addProject(t, db, "Webshop")

	var events []PageEvent
	want := map[string]string{}
	for session := range 30 {
		s := fmt.Sprintf("s%d", session)
		for tab := range 1 + rng.IntN(2) {
			w := fmt.Sprintf("w%d", tab)
			at := rng.IntN(1000) * 1000
			var views []PageEvent
			var leaves []PageEvent
			for v := range 1 + rng.IntN(5) {
				ev := PageEvent{ProjectID: p.ID, PageviewID: fmt.Sprintf("%s-%s-%d", s, w, v), SessionID: s, WindowID: w,
					Route: "/x", At: pageT0.Add(time.Duration(at) * time.Millisecond)}
				views = append(views, ev)
				at += 1 + rng.IntN(60_000)
				switch rng.IntN(4) {
				case 0: // a leave naming the page view
					leaves = append(leaves, PageEvent{ProjectID: p.ID, Leave: true, EventID: "l-" + ev.PageviewID, PageviewID: ev.PageviewID,
						SessionID: s, WindowID: w, At: pageT0.Add(time.Duration(at-rng.IntN(500)) * time.Millisecond)})
				case 1: // a leave without an id
					leaves = append(leaves, PageEvent{ProjectID: p.ID, Leave: true, EventID: "n-" + ev.PageviewID,
						SessionID: s, WindowID: w, At: pageT0.Add(time.Duration(at-1) * time.Millisecond)})
				}
			}
			for id, d := range referenceDurations(views, leaves) {
				want[id] = d
			}
			events = append(events, views...)
			events = append(events, leaves...)
		}
	}
	// Some events are sent twice.
	for range 20 {
		events = append(events, events[rng.IntN(len(events))])
	}
	rng.Shuffle(len(events), func(i, j int) { events[i], events[j] = events[j], events[i] })
	for len(events) > 0 {
		n := min(len(events), 1+rng.IntN(40))
		savePages(t, db, events[:n]...)
		events = events[n:]
	}
	assertDurations(t, durations(t, db, p), want)
}

// referenceDurations closes the page views of one tab from the whole
// visit at once: each is closed by its own leaves, by the next page view,
// and by leaves without an id from its start until the next page view.
func referenceDurations(views, leaves []PageEvent) map[string]string {
	out := map[string]string{}
	for i, v := range views {
		c := closing{start: v.At.UnixMilli()}
		end := int64(1 << 62)
		if i+1 < len(views) {
			end = views[i+1].At.UnixMilli()
			c.close(end)
		}
		for _, l := range leaves {
			at := l.At.UnixMilli()
			if l.PageviewID == v.PageviewID || (l.PageviewID == "" && at >= c.start && at < end) {
				c.close(at)
			}
		}
		switch {
		case !c.closed:
			out[v.PageviewID] = "left"
		case !c.duration.Valid:
			out[v.PageviewID] = "dropped"
		default:
			out[v.PageviewID] = fmt.Sprintf("%dms", c.duration.Int64)
		}
	}
	return out
}

func TestProjects_RoutePatterns(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	if len(p.RoutePatterns) != 0 {
		t.Errorf("new project has patterns %v", p.RoutePatterns)
	}
	setPatterns := func(id int64, patterns ...string) (Project, error) {
		return db.UpdateProject(ctx, id, ProjectSettings{Name: p.Name, RetentionDays: p.RetentionDays,
			TracksLaravel: p.TracksLaravel, RoutePatterns: &patterns})
	}
	got, err := setPatterns(p.ID, " /products/:slug ", "", "/blog/:post")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := db.ProjectByPosthogToken(ctx, p.PosthogToken)
	if fmt.Sprint(got.RoutePatterns) != "[/products/:slug /blog/:post]" || fmt.Sprint(again.RoutePatterns) != fmt.Sprint(got.RoutePatterns) {
		t.Errorf("patterns = %v, reloaded %v", got.RoutePatterns, again.RoutePatterns)
	}
	if _, err := setPatterns(p.ID + 99); err != ErrNotFound {
		t.Errorf("unknown project: %v", err)
	}
	if _, err := setPatterns(p.ID, "/"+string(make([]byte, 400))); err == nil {
		t.Error("a 400-character pattern was accepted")
	}
}

// Settings and route patterns change in one write: when the patterns are
// refused, the other settings stay as they were too. Pattern lengths count
// characters, not bytes.
func TestUpdateProject_SettingsAndPatternsInOneWrite(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	patterns := []string{" /products/:slug ", ""}
	got, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "Web shop", RoutePatterns: &patterns})
	if err != nil || got.Name != "Web shop" || fmt.Sprint(got.RoutePatterns) != "[/products/:slug]" {
		t.Fatalf("update = %+v, %v", got, err)
	}
	// Without patterns they stay.
	if got, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "Shop"}); err != nil || len(got.RoutePatterns) != 1 {
		t.Errorf("update without patterns = %+v, %v", got, err)
	}

	tooMany := make([]string, MaxRoutePatterns+1)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("/p%d/:id", i)
	}
	tooLong := []string{"/" + strings.Repeat("é", MaxRoutePatternChars)}
	for _, refused := range [][]string{tooMany, tooLong} {
		if _, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "Renamed", RoutePatterns: &refused}); err == nil {
			t.Errorf("patterns %.40v were accepted", refused)
		}
	}
	after, err := db.ProjectByID(ctx, p.ID)
	if err != nil || after.Name != "Shop" || len(after.RoutePatterns) != 1 {
		t.Errorf("after refused updates = %+v, %v; want the name Shop and one pattern", after, err)
	}

	// 300 characters of two bytes each are fine.
	long := []string{"/" + strings.Repeat("é", MaxRoutePatternChars-1)}
	if _, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "Shop", RoutePatterns: &long}); err != nil {
		t.Errorf("a pattern of 300 characters (599 bytes): %v", err)
	}
}
