package api

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

type overviewResponse struct {
	UpdatedAt time.Time `json:"updatedAt"`
	Projects  []struct {
		ID              int64    `json:"id"`
		Name            string   `json:"name"`
		Sources         []string `json:"sources"`
		NewIssues24h    int      `json:"newIssues24h"`
		Errors24h       int      `json:"errors24h"`
		Hourly          []int    `json:"hourly"`
		Pageviews24h    *int     `json:"pageviews24h"`
		AvgTimeOnPageMs *int     `json:"avgTimeOnPageMs"`
	} `json:"projects"`
	NewestIssues []struct {
		ID          int64     `json:"id"`
		ProjectID   int64     `json:"projectId"`
		ProjectName string    `json:"projectName"`
		Type        string    `json:"type"`
		Message     string    `json:"message"`
		LastSeen    time.Time `json:"lastSeen"`
	} `json:"newestIssues"`
}

func TestOverview_NewIssuesAndErrors24h(t *testing.T) {
	env := newTestEnv(t)
	alpha := env.project(t, "Alpha")
	beta, err := env.db.CreateProject(context.Background(), store.NewProject{Name: "Beta", TracksBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	shop := env.project(t, "Webshop")

	ago := func(d time.Duration) time.Time { return testNow.Add(-d) }
	env.save(t, shop.ID,
		// New: first seen 2 h ago, three events.
		phpEvent(t, eventSpec{id: hexID(1), typ: "New", at: ago(2 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), typ: "New", at: ago(time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(3), typ: "New", at: ago(10 * time.Minute)}),
		// Old: first seen 30 h ago; 23 h 45 min ago is inside 24 h but
		// before the oldest bar (13:00 yesterday, testNow being 12:30).
		phpEvent(t, eventSpec{id: hexID(4), typ: "Old", at: ago(30 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(5), typ: "Old", at: ago(23*time.Hour + 45*time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(6), typ: "Old", at: ago(time.Hour)}),
		// New but resolved, and new but ignored: their events count, they are not new issues.
		phpEvent(t, eventSpec{id: hexID(7), typ: "Resolved", at: ago(3 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(8), typ: "Ignored", at: ago(20 * time.Minute)}),
	)
	env.save(t, beta.ID,
		phpEvent(t, eventSpec{id: hexID(9), typ: "Old", browser: true, at: ago(30 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(10), typ: "Old", browser: true, at: ago(time.Hour)}),
	)
	ids := issueIDs(t, env, shop.ID)
	env.setStatus(t, ids["Resolved"], store.IssueResolved)
	env.setStatus(t, ids["Ignored"], store.IssueIgnored)

	rec := env.get(t, "/ui/api/overview")
	if !strings.Contains(rec.Body.String(), `"pageviews24h":null,"avgTimeOnPageMs":null`) {
		t.Errorf("page view columns are not null until page tracking exists: %s", rec.Body)
	}
	got := getJSON[overviewResponse](t, env, "/ui/api/overview")
	if !got.UpdatedAt.Equal(testNow) {
		t.Errorf("updatedAt = %v, want %v", got.UpdatedAt, testNow)
	}
	// New issues first, then errors, then by name.
	var order []string
	for _, p := range got.Projects {
		order = append(order, p.Name)
	}
	if fmt.Sprint(order) != "[Webshop Beta Alpha]" {
		t.Fatalf("projects in order %v, want [Webshop Beta Alpha]", order)
	}

	ws := got.Projects[0]
	if ws.ID != shop.ID || fmt.Sprint(ws.Sources) != "[laravel browser]" {
		t.Errorf("Webshop: id %d, sources %v", ws.ID, ws.Sources)
	}
	if ws.NewIssues24h != 1 {
		t.Errorf("Webshop newIssues24h = %d, want 1 (resolved and ignored ones are not shown as new)", ws.NewIssues24h)
	}
	if ws.Errors24h != 7 {
		t.Errorf("Webshop errors24h = %d, want 7 (every event of the last 24 h, whatever its issue's status)", ws.Errors24h)
	}
	want := make([]int, 24)
	want[20], want[21], want[22], want[23] = 1, 1, 2, 2
	if fmt.Sprint(ws.Hourly) != fmt.Sprint(want) {
		t.Errorf("Webshop hourly = %v\nwant              %v", ws.Hourly, want)
	}
	if ws.Pageviews24h != nil || ws.AvgTimeOnPageMs != nil {
		t.Errorf("page view columns = %v, %v; want null", ws.Pageviews24h, ws.AvgTimeOnPageMs)
	}

	if b := got.Projects[1]; b.NewIssues24h != 0 || b.Errors24h != 1 || fmt.Sprint(b.Sources) != "[browser]" {
		t.Errorf("Beta = %+v, want no new issues, 1 error, sources [browser]", b)
	}
	if a := got.Projects[2]; a.ID != alpha.ID || a.NewIssues24h != 0 || a.Errors24h != 0 || fmt.Sprint(a.Hourly) != fmt.Sprint(make([]int, 24)) {
		t.Errorf("Alpha = %+v, want nothing", a)
	}
}

func TestOverview_NewestIssuesAcrossProjects(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	portal := env.project(t, "Customer portal")
	ago := func(d time.Duration) time.Time { return testNow.Add(-d) }

	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), typ: `Illuminate\Database\QueryException`, message: "Deadlock found", at: ago(time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(2), typ: "N2", at: ago(3 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(3), typ: "N3", at: ago(5 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(4), typ: "N4", at: ago(7 * time.Minute)}),
		// Busy right now, but not new: first seen 30 h ago.
		phpEvent(t, eventSpec{id: hexID(5), typ: "Old", at: ago(30 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(6), typ: "Old", at: ago(30 * time.Second)}),
	)
	env.save(t, portal.ID,
		phpEvent(t, eventSpec{id: hexID(7), typ: "P1", at: ago(2 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(8), typ: "P2", at: ago(6 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(9), typ: "Fixed", at: ago(10 * time.Second)}),
	)
	env.setStatus(t, issueIDs(t, env, portal.ID)["Fixed"], store.IssueResolved)

	got := getJSON[overviewResponse](t, env, "/ui/api/overview").NewestIssues
	var titles []string
	for _, issue := range got {
		titles = append(titles, issue.Type)
	}
	// At most five new open issues, the most recently seen first.
	if fmt.Sprint(titles) != `[Illuminate\Database\QueryException P1 N2 N3 P2]` {
		t.Fatalf("newest issues = %v", titles)
	}
	first := got[0]
	if first.ProjectID != shop.ID || first.ProjectName != "Webshop" || first.Message != "Deadlock found" || !first.LastSeen.Equal(ago(time.Minute)) {
		t.Errorf("first = %+v", first)
	}
	if got[1].ProjectID != portal.ID || got[1].ProjectName != "Customer portal" {
		t.Errorf("second = %+v, want Customer portal's P1", got[1])
	}
}

func TestOverview_Empty(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/ui/api/overview")
	if !strings.Contains(rec.Body.String(), `"projects":[],"newestIssues":[]`) {
		t.Errorf("GET /ui/api/overview without projects = %d %s, want empty lists", rec.Code, rec.Body)
	}
}

// Projects that track page views get their page views and average time on
// a page of the last 24 hours, from the hourly summaries; others keep null
// (the design's "Add page tracking").
func TestOverview_PageviewColumns(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	env.pageProject(t, "Quiet site")
	tools := env.project(t, "Internal tools") // errors only
	env.visits(t, shop.ID,
		visit{user: "u1", route: "/", at: testNow.Add(-time.Hour), stay: 60 * time.Second},
		visit{user: "u2", route: "/cart", at: testNow.Add(-2 * time.Hour), stay: 84 * time.Second},
		visit{user: "u3", route: "/cart", at: testNow.Add(-3 * time.Hour)}, // no leave: not in the average
		visit{user: "bot", route: "/", at: testNow.Add(-time.Hour), stay: time.Second, bot: true},
		visit{user: "u4", route: "/", at: testNow.Add(-30 * time.Hour), stay: time.Hour}, // before the 24 hours
	)
	// Page views of a project that does not track them (its token is accepted anyway) stay hidden.
	env.visits(t, tools.ID, visit{user: "u1", route: "/", at: testNow.Add(-time.Hour), stay: time.Minute})

	got := getJSON[overviewResponse](t, env, "/ui/api/overview")
	byName := map[string]int{}
	for i, p := range got.Projects {
		byName[p.Name] = i
	}
	ws := got.Projects[byName["Webshop"]]
	if ws.Pageviews24h == nil || *ws.Pageviews24h != 3 || ws.AvgTimeOnPageMs == nil || *ws.AvgTimeOnPageMs != 72_000 {
		t.Errorf("Webshop page columns = %v, %v; want 3 page views, 72 s", deref(ws.Pageviews24h), deref(ws.AvgTimeOnPageMs))
	}
	q := got.Projects[byName["Quiet site"]]
	if q.Pageviews24h == nil || *q.Pageviews24h != 0 || q.AvgTimeOnPageMs != nil {
		t.Errorf("Quiet site = %v, %v; want 0 page views and no average (null)", deref(q.Pageviews24h), deref(q.AvgTimeOnPageMs))
	}
	if it := got.Projects[byName["Internal tools"]]; it.Pageviews24h != nil || it.AvgTimeOnPageMs != nil {
		t.Errorf("Internal tools = %v, %v; want null (no page tracking)", deref(it.Pageviews24h), deref(it.AvgTimeOnPageMs))
	}
}

func deref(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
