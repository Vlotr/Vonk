package api

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

type issueJSONTest struct {
	ID        int64     `json:"id"`
	Type      string    `json:"type"`
	Message   string    `json:"message"`
	Source    string    `json:"source"`
	Culprit   string    `json:"culprit"`
	Events    int64     `json:"events"`
	Users     int       `json:"users"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`
	IsNew     bool      `json:"isNew"`
	Hourly    []int     `json:"hourly"`
	Status    string    `json:"status"`
	ProjectID int64     `json:"projectId"`
}

type issuesResponse struct {
	Project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	Counts struct {
		Open     int `json:"open"`
		Resolved int `json:"resolved"`
		Ignored  int `json:"ignored"`
	} `json:"counts"`
	OpenIssues int             `json:"openIssues"`
	New24h     int             `json:"new24h"`
	Total      int             `json:"total"`
	Issues     []issueJSONTest `json:"issues"`
}

// ids lists the issue ids of a response, in order.
func (r issuesResponse) ids() []int64 {
	ids := make([]int64, len(r.Issues))
	for i, issue := range r.Issues {
		ids[i] = issue.ID
	}
	return ids
}

type frameJSONTest struct {
	File        string   `json:"file"`
	Function    string   `json:"function"`
	Line        int      `json:"line"`
	InApp       bool     `json:"inApp"`
	PreContext  []string `json:"preContext"`
	ContextLine string   `json:"contextLine"`
	PostContext []string `json:"postContext"`
}

type detailResponse struct {
	Issue       issueJSONTest `json:"issue"`
	Hourly      []int         `json:"hourly"`
	LatestEvent *struct {
		EventID   string    `json:"eventId"`
		Timestamp time.Time `json:"timestamp"`
		Exception *struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		} `json:"exception"`
		Frames []frameJSONTest `json:"frames"`
		User   *struct {
			ID string `json:"id"`
		} `json:"user"`
		Browser     string `json:"browser"`
		OS          string `json:"os"`
		Environment string `json:"environment"`
		Server      string `json:"server"`
		Runtime     *struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"runtime"`
		URL     string `json:"url"`
		Release string `json:"release"`
		Country string `json:"country"`
	} `json:"latestEvent"`
}

func issuesPath(projectID int64, query string) string {
	path := fmt.Sprintf("/ui/api/projects/%d/issues", projectID)
	if query != "" {
		path += "?" + query
	}
	return path
}

func TestListIssues_OpenOnlyByDefault(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), function: "open", at: testNow.Add(-time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), function: "resolved"}),
		phpEvent(t, eventSpec{id: hexID(3), function: "ignored"}),
	)
	all, _ := env.db.ListIssues(context.Background(), p.ID) // newest first: ignored, resolved, open
	env.setStatus(t, all[0].ID, store.IssueIgnored)
	env.setStatus(t, all[1].ID, store.IssueResolved)

	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, ""))
	if got.Project.ID != p.ID || got.Project.Name != "Webshop" {
		t.Errorf("project = %+v", got.Project)
	}
	if len(got.Issues) != 1 || got.Issues[0].ID != all[2].ID {
		t.Fatalf("issues = %+v, want only the open one (%d)", got.Issues, all[2].ID)
	}
	if got.Counts.Open != 1 || got.Counts.Resolved != 1 || got.Counts.Ignored != 1 || got.Total != 1 {
		t.Errorf("counts = %+v, total %d; want 1 of each and total 1", got.Counts, got.Total)
	}

	resolved := getJSON[issuesResponse](t, env, issuesPath(p.ID, "status=resolved"))
	if len(resolved.Issues) != 1 || resolved.Issues[0].ID != all[1].ID {
		t.Errorf("status=resolved: issues = %+v, want %d", resolved.Issues, all[1].ID)
	}
}

func TestListIssues_Fields(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	old := testNow.Add(-30 * time.Hour)
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), function: "old", at: old}),
		phpEvent(t, eventSpec{id: hexID(2), function: "old", at: testNow.Add(-2 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(3), typ: `Illuminate\Database\QueryException`, function: "fresh", at: testNow.Add(-time.Hour)}),
	)

	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, ""))
	if len(got.Issues) != 2 || got.Total != 2 {
		t.Fatalf("issues = %+v, total %d", got.Issues, got.Total)
	}
	// Newest last_seen first: the old issue had an event two minutes ago.
	oldIssue, fresh := got.Issues[0], got.Issues[1]
	if oldIssue.Type != "RuntimeException" || oldIssue.Message != "Checkout failed" || oldIssue.Source != "laravel" ||
		oldIssue.Culprit != "PaymentGateway.php:12" || oldIssue.Events != 2 {
		t.Errorf("old issue = %+v", oldIssue)
	}
	if !oldIssue.FirstSeen.Equal(old) || !oldIssue.LastSeen.Equal(testNow.Add(-2*time.Minute)) {
		t.Errorf("old issue seen %v .. %v", oldIssue.FirstSeen, oldIssue.LastSeen)
	}
	if oldIssue.IsNew {
		t.Error("an issue first seen 30 h ago is not new")
	}
	if fresh.Type != `Illuminate\Database\QueryException` || !fresh.IsNew {
		t.Errorf("fresh issue: type %q (the API keeps the full class), isNew %v", fresh.Type, fresh.IsNew)
	}
	if got.New24h != 1 {
		t.Errorf("new24h = %d, want 1", got.New24h)
	}
}

func TestListIssues_HourlyBuckets(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), at: testNow.Add(-3 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), at: testNow.Add(-3*time.Hour - 20*time.Minute)}),  // 09:10, same clock hour
		phpEvent(t, eventSpec{id: hexID(3), at: testNow.Add(-25 * time.Minute)}),              // 12:05, the current hour
		phpEvent(t, eventSpec{id: hexID(4), at: testNow.Add(-23*time.Hour - 30*time.Minute)}), // 13:00 yesterday, the oldest bucket
		phpEvent(t, eventSpec{id: hexID(5), at: testNow.Add(-24 * time.Hour)}),                // 12:30 yesterday, outside the window
	)

	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, ""))
	if len(got.Issues) != 1 {
		t.Fatalf("issues = %+v", got.Issues)
	}
	hourly := got.Issues[0].Hourly
	want := make([]int, 24)
	want[0], want[20], want[23] = 1, 2, 1
	if fmt.Sprint(hourly) != fmt.Sprint(want) {
		t.Errorf("hourly = %v\nwant     %v (oldest first; an event 3 h ago is in bucket 20)", hourly, want)
	}
	if got.Issues[0].Events != 5 {
		t.Errorf("events = %d, want 5: the counter covers all time, the buckets 24 h", got.Issues[0].Events)
	}
}

func TestListIssues_UsersDistinct(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), userID: "user_1"}),
		phpEvent(t, eventSpec{id: hexID(2), userID: "user_1"}),
		phpEvent(t, eventSpec{id: hexID(3), userID: "user_2"}),
		phpEvent(t, eventSpec{id: hexID(4)}), // no user
	)
	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, ""))
	if len(got.Issues) != 1 || got.Issues[0].Users != 2 || got.Issues[0].Events != 4 {
		t.Fatalf("issues = %+v, want one issue with 4 events and 2 users", got.Issues)
	}
}

func TestListIssues_LimitAndOffset(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	var events []*sentry.Event
	for i := 1; i <= 5; i++ {
		events = append(events, phpEvent(t, eventSpec{id: hexID(i), function: fmt.Sprint("f", i), at: testNow.Add(-time.Duration(i) * time.Minute)}))
	}
	env.save(t, p.ID, events...)

	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "limit=2&offset=2"))
	if got.Total != 5 || len(got.Issues) != 2 {
		t.Fatalf("total %d, %d issues; want 5 and 2", got.Total, len(got.Issues))
	}
	// Newest first, so the third and fourth newest.
	all, _ := env.db.ListIssues(context.Background(), p.ID)
	if got.Issues[0].ID != all[2].ID || got.Issues[1].ID != all[3].ID {
		t.Errorf("page = %d, %d; want %d, %d", got.Issues[0].ID, got.Issues[1].ID, all[2].ID, all[3].ID)
	}
}

func TestListIssues_EmptyProject(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	rec := env.get(t, issuesPath(p.ID, ""))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"issues":[]`) {
		t.Errorf("GET = %d %s, want 200 with an empty issues list", rec.Code, rec.Body)
	}
}

func TestListIssues_BadRequests(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	tests := []struct {
		path string
		code int
	}{
		{issuesPath(999, ""), http.StatusNotFound},
		{"/ui/api/projects/abc/issues", http.StatusNotFound},
		{issuesPath(p.ID, "status=deleted"), http.StatusBadRequest},
		{issuesPath(p.ID, "limit=0"), http.StatusBadRequest},
		{issuesPath(p.ID, "limit=101"), http.StatusBadRequest},
		{issuesPath(p.ID, "limit=ten"), http.StatusBadRequest},
		{issuesPath(p.ID, "offset=-1"), http.StatusBadRequest},
	}
	for _, tt := range tests {
		rec := env.get(t, tt.path)
		if rec.Code != tt.code || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
			t.Errorf("GET %s = %d %s, want %d with a JSON error", tt.path, rec.Code, rec.Body, tt.code)
		}
	}
}

// savePHPFixture stores the real sentry-php event from the Task 2 fixtures.
func savePHPFixture(t *testing.T, env *testEnv, projectID int64) {
	t.Helper()
	data, err := os.ReadFile("../sentry/testdata/php-exception.envelope.gz")
	if err != nil {
		t.Fatal(err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sentry.ParseEnvelope(zr, 1<<20)
	if err != nil || len(envelope.Events) != 1 {
		t.Fatalf("ParseEnvelope: %v, %+v", err, envelope)
	}
	ev := envelope.Events[0]
	if ev.EventID == "" {
		ev.SetEventID(envelope.Header.EventID) // sentry-php puts the id only in the envelope header
	}
	// The fixture was recorded on 2026-10-16; keep it inside the 24 h window.
	ev.Raw["timestamp"] = json.Number(strconv.FormatInt(testNow.Add(-10*time.Minute).Unix(), 10))
	ev.Refresh()
	env.save(t, projectID, ev)
}

func TestIssueDetail_FramesWithContext(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	savePHPFixture(t, env, p.ID)
	list, _ := env.db.ListIssues(context.Background(), p.ID)
	if len(list) != 1 {
		t.Fatalf("issues = %+v", list)
	}

	got := getJSON[detailResponse](t, env, fmt.Sprintf("/ui/api/issues/%d", list[0].ID))
	if got.Issue.ID != list[0].ID || got.Issue.ProjectID != p.ID || got.Issue.Status != "open" || got.Issue.Type != "RuntimeException" {
		t.Errorf("issue = %+v", got.Issue)
	}
	if len(got.Hourly) != 24 || got.Hourly[23] != 1 {
		t.Errorf("hourly = %v, want 24 buckets with the event in the last", got.Hourly)
	}
	ev := got.LatestEvent
	if ev == nil || ev.Exception == nil {
		t.Fatalf("latestEvent = %+v", ev)
	}
	if ev.Exception.Type != "RuntimeException" || ev.Exception.Value != "Checkout failed" {
		t.Errorf("exception = %+v", ev.Exception)
	}
	if len(ev.Frames) != 6 {
		t.Fatalf("got %d frames, want the fixture's 6", len(ev.Frames))
	}

	// Most recent call first: the frame that threw.
	top := ev.Frames[0]
	if top.File != "/app/Services/PaymentGateway.php" || top.Function != `App\Services\PaymentGateway::charge` || top.Line != 12 || !top.InApp {
		t.Errorf("top frame = %+v", top)
	}
	if top.ContextLine != `            throw new \RuntimeException('Checkout failed');` {
		t.Errorf("contextLine = %q", top.ContextLine)
	}
	// The SDK sends five lines around the error; the API keeps the three nearest it.
	if len(top.PreContext) != 3 || len(top.PostContext) != 3 {
		t.Fatalf("context = %d before, %d after; want 3 and 3", len(top.PreContext), len(top.PostContext))
	}
	if top.PreContext[0] != "    public function charge(int $orderId, string $email): string" ||
		top.PreContext[2] != "        if ($orderId > 0) {" || top.PostContext[0] != "        }" || top.PostContext[2] != "        return 'ok';" {
		t.Errorf("pre = %q, post = %q", top.PreContext, top.PostContext)
	}

	// Vendor code is not in-app; the outermost frame (the script) comes last.
	if ev.Frames[3].InApp || ev.Frames[3].File != "/vendor/symfony/options-resolver/OptionsResolver.php" {
		t.Errorf("frame 3 = %+v, want a vendor frame", ev.Frames[3])
	}
	if last := ev.Frames[5]; last.File != "/public/index.php" || last.Line != 45 {
		t.Errorf("last frame = %+v", last)
	}
}

func TestIssueDetail_EventDetails(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	savePHPFixture(t, env, p.ID)
	list, _ := env.db.ListIssues(context.Background(), p.ID)

	ev := getJSON[detailResponse](t, env, fmt.Sprintf("/ui/api/issues/%d", list[0].ID)).LatestEvent
	if ev == nil {
		t.Fatal("no latestEvent")
	}
	if ev.EventID == "" || !ev.Timestamp.Equal(testNow.Add(-10*time.Minute)) {
		t.Errorf("eventId %q, timestamp %v", ev.EventID, ev.Timestamp)
	}
	if ev.User == nil || ev.User.ID != "42" {
		t.Errorf("user = %+v, want id 42", ev.User)
	}
	// From the visitor's User-Agent header, not from contexts.os (the server).
	if ev.Browser != "Chrome 141" || ev.OS != "macOS" {
		t.Errorf("browser %q, os %q; want Chrome 141 and macOS", ev.Browser, ev.OS)
	}
	if ev.Environment != "production" || ev.Server != "web-1" || ev.Release != "webshop@1.4.2" {
		t.Errorf("environment %q, server %q, release %q", ev.Environment, ev.Server, ev.Release)
	}
	if ev.Runtime == nil || ev.Runtime.Name != "php" || ev.Runtime.Version != "8.4.1" {
		t.Errorf("runtime = %+v", ev.Runtime)
	}
	// Scrubbed before it was stored.
	if ev.URL != "http://localhost:18091/checkout?coupon=SPRING&token=[Filtered]" {
		t.Errorf("url = %q", ev.URL)
	}
	if ev.Country != "" {
		t.Errorf("country = %q, want none: the event has no geo data", ev.Country)
	}
}

func TestIssueDetail_OnlyFieldsPresent(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1)}))
	list, _ := env.db.ListIssues(context.Background(), p.ID)

	rec := env.get(t, fmt.Sprintf("/ui/api/issues/%d", list[0].ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET = %d %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, field := range []string{`"user"`, `"browser"`, `"os"`, `"environment"`, `"server"`, `"runtime"`, `"url"`, `"release"`, `"country"`} {
		if strings.Contains(body, field) {
			t.Errorf("body has %s although the event has no such data: %s", field, body)
		}
	}
}

func TestIssueDetail_UnknownID404(t *testing.T) {
	env := newTestEnv(t)
	for _, path := range []string{"/ui/api/issues/999", "/ui/api/issues/abc", "/ui/api/issues/-1"} {
		rec := env.get(t, path)
		if rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
			t.Errorf("GET %s = %d %q, want 404 {\"error\":\"not found\"}", path, rec.Code, rec.Body)
		}
	}
}

// issueIDs returns the ids of a project's issues by title, for readable assertions.
func issueIDs(t *testing.T, env *testEnv, projectID int64) map[string]int64 {
	t.Helper()
	list, err := env.db.ListIssues(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]int64{}
	for _, issue := range list {
		ids[issue.Title] = issue.ID
	}
	return ids
}

func TestListIssues_FilterSource(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), typ: "QueryException"}),
		phpEvent(t, eventSpec{id: hexID(2), typ: "TypeError", browser: true, file: "https://shop.example.com/assets/cart.js"}),
		phpEvent(t, eventSpec{id: hexID(3), typ: "ChunkLoadError", browser: true, file: "https://shop.example.com/assets/app.js"}),
	)
	ids := issueIDs(t, env, p.ID)
	env.setStatus(t, ids["ChunkLoadError"], store.IssueResolved)

	browser := getJSON[issuesResponse](t, env, issuesPath(p.ID, "source=browser"))
	if fmt.Sprint(browser.ids()) != fmt.Sprint([]int64{ids["TypeError"]}) || browser.Issues[0].Source != "browser" {
		t.Errorf("source=browser: issues = %+v", browser.Issues)
	}
	// The tabs count with the filter: one open and one resolved browser issue.
	if browser.Counts.Open != 1 || browser.Counts.Resolved != 1 || browser.Total != 1 {
		t.Errorf("source=browser: counts %+v, total %d", browser.Counts, browser.Total)
	}
	// The headline counts the whole project.
	if browser.OpenIssues != 2 || browser.New24h != 2 {
		t.Errorf("source=browser: openIssues %d, new24h %d; want the project's 2 and 2", browser.OpenIssues, browser.New24h)
	}

	laravel := getJSON[issuesResponse](t, env, issuesPath(p.ID, "source=laravel"))
	if fmt.Sprint(laravel.ids()) != fmt.Sprint([]int64{ids["QueryException"]}) || laravel.Counts.Resolved != 0 {
		t.Errorf("source=laravel: issues %+v, counts %+v", laravel.Issues, laravel.Counts)
	}
	if rec := env.get(t, issuesPath(p.ID, "source=python")); rec.Code != http.StatusBadRequest {
		t.Errorf("source=python = %d, want 400", rec.Code)
	}
}

func TestListIssues_SortEventsDesc(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	// one: 1 event, seen last; two: 3 events from 1 user, first seen first;
	// three: 2 events from 2 users.
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), typ: "One", at: testNow.Add(-time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(2), typ: "Two", userID: "u1", at: testNow.Add(-5 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(3), typ: "Two", userID: "u1", at: testNow.Add(-4 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(4), typ: "Two", userID: "u1", at: testNow.Add(-3 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(5), typ: "Three", userID: "u2", at: testNow.Add(-2 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(6), typ: "Three", userID: "u3", at: testNow.Add(-90 * time.Minute)}),
	)
	ids := issueIDs(t, env, p.ID)
	order := func(titles ...string) string {
		var want []int64
		for _, title := range titles {
			want = append(want, ids[title])
		}
		return fmt.Sprint(want)
	}

	tests := []struct {
		sort string
		want string
	}{
		{"", order("One", "Three", "Two")}, // last seen, the default
		{"last_seen", order("One", "Three", "Two")},
		{"events", order("Two", "Three", "One")},
		{"users", order("Three", "Two", "One")},
		{"first_seen", order("One", "Three", "Two")}, // newest first
	}
	for _, tt := range tests {
		got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "sort="+tt.sort))
		if fmt.Sprint(got.ids()) != tt.want {
			t.Errorf("sort=%s: ids %v, want %s", tt.sort, got.ids(), tt.want)
		}
	}

	// Ties keep a stable order: the most recently seen first.
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(7), typ: "Four", at: testNow.Add(-30 * time.Second)}))
	ids = issueIDs(t, env, p.ID)
	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "sort=events"))
	if fmt.Sprint(got.ids()) != order("Two", "Three", "Four", "One") {
		t.Errorf("sort=events with a tie: ids %v", got.ids())
	}
	if rec := env.get(t, issuesPath(p.ID, "sort=title")); rec.Code != http.StatusBadRequest {
		t.Errorf("sort=title = %d, want 400", rec.Code)
	}
}

func TestListIssues_SearchCulpritAndURL(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), typ: `Illuminate\Database\QueryException`, message: "Deadlock found", file: "/app/Http/Controllers/CartController.php"}),
		phpEvent(t, eventSpec{id: hexID(2), typ: "TypeError", message: "Cannot read 'price'", browser: true,
			file: "https://shop.example.com/resources/js/stock.js", url: "https://shop.example.com/checkout/pay?step=2"}),
		phpEvent(t, eventSpec{id: hexID(3), typ: "DiscountError", message: "100% off is not allowed", file: "/app/Services/Discounts.php"}),
	)
	ids := issueIDs(t, env, p.ID)

	tests := []struct {
		q    string
		want []int64
	}{
		{"cartcontroller", []int64{ids[`Illuminate\Database\QueryException`]}}, // culprit, any case
		{"/checkout/pay", []int64{ids["TypeError"]}},                           // the URL of an event
		{"js/STOCK.js", []int64{ids["TypeError"]}},                             // a browser culprit
		{"queryexception", []int64{ids[`Illuminate\Database\QueryException`]}}, // title
		{`database\query`, []int64{ids[`Illuminate\Database\QueryException`]}}, // a backslash is literal
		{"deadlock", []int64{ids[`Illuminate\Database\QueryException`]}},       // message
		{"100%", []int64{ids["DiscountError"]}},                                // "%" is literal, not a wildcard
		{"_", nil},                                                             // so is "_"
		{"  deadlock  ", []int64{ids[`Illuminate\Database\QueryException`]}},   // trimmed
		{"nothing like this", nil},
	}
	for _, tt := range tests {
		got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "q="+url.QueryEscape(tt.q)))
		if fmt.Sprint(got.ids()) != fmt.Sprint(append([]int64{}, tt.want...)) {
			t.Errorf("q=%q: ids %v, want %v", tt.q, got.ids(), tt.want)
		}
		if got.Counts.Open != len(tt.want) || got.Total != len(tt.want) {
			t.Errorf("q=%q: counts %+v, total %d; want %d", tt.q, got.Counts, got.Total, len(tt.want))
		}
	}
	if rec := env.get(t, issuesPath(p.ID, "q="+strings.Repeat("a", 201))); rec.Code != http.StatusBadRequest {
		t.Errorf("a 201-character search = %d, want 400", rec.Code)
	}
}

func TestListIssues_FilterRoute(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	// Task 7 fills route from the URL; until then only stored events have one.
	base := store.NewErrorEvent{ProjectID: p.ID, Title: "E", Level: "error", Timestamp: testNow, ReceivedAt: testNow, Data: "{}"}
	product, cart, other := base, base, base
	product.EventID, product.Fingerprint, product.Route = hexID(1), "a", "/products/{slug}"
	cart.EventID, cart.Fingerprint, cart.Route = hexID(2), "b", "/cart"
	other.EventID, other.Fingerprint, other.Route = hexID(3), "a", "/cart"
	if _, err := env.db.SaveErrorEvents(context.Background(), []store.NewErrorEvent{product, cart, other}); err != nil {
		t.Fatal(err)
	}
	list, _ := env.db.ListIssues(context.Background(), p.ID)
	byFingerprint := map[string]int64{}
	for _, issue := range list {
		byFingerprint[issue.Fingerprint] = issue.ID
	}

	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "route="+url.QueryEscape("/products/{slug}")))
	if fmt.Sprint(got.ids()) != fmt.Sprint([]int64{byFingerprint["a"]}) {
		t.Errorf("route=/products/{slug}: ids %v, want only issue a", got.ids())
	}
	// Both issues had an event on /cart.
	if got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "route=/cart")); len(got.Issues) != 2 || got.Counts.Open != 2 {
		t.Errorf("route=/cart: %d issues, counts %+v; want 2", len(got.Issues), got.Counts)
	}
	// Exact, not a prefix.
	if got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "route=/products")); len(got.Issues) != 0 {
		t.Errorf("route=/products: %d issues, want 0", len(got.Issues))
	}
}

func TestListIssues_CountsPerStatus(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	other := env.project(t, "Customer portal")
	var events []*sentry.Event
	for i := 1; i <= 6; i++ {
		at := testNow.Add(-time.Duration(i) * time.Hour)
		if i > 4 {
			at = testNow.Add(-time.Duration(i) * 24 * time.Hour) // not new
		}
		events = append(events, phpEvent(t, eventSpec{id: hexID(i), typ: fmt.Sprint("E", i), browser: i%2 == 0, at: at}))
	}
	env.save(t, p.ID, events...)
	env.save(t, other.ID, phpEvent(t, eventSpec{id: hexID(99), typ: "Elsewhere"}))
	ids := issueIDs(t, env, p.ID)
	env.setStatus(t, ids["E1"], store.IssueResolved)
	env.setStatus(t, ids["E2"], store.IssueResolved)
	env.setStatus(t, ids["E3"], store.IssueIgnored)
	// Open: E4 (new, browser), E5 (old), E6 (old, browser).

	for _, status := range []string{"open", "resolved", "ignored"} {
		got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "status="+status))
		if got.Counts.Open != 3 || got.Counts.Resolved != 2 || got.Counts.Ignored != 1 {
			t.Errorf("status=%s: counts %+v, want 3 open, 2 resolved, 1 ignored", status, got.Counts)
		}
		// Whatever the tab, the headline is about the project's open issues.
		if got.OpenIssues != 3 || got.New24h != 1 {
			t.Errorf("status=%s: openIssues %d, new24h %d; want 3 and 1 (E4)", status, got.OpenIssues, got.New24h)
		}
	}
	if got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "status=resolved")); got.Total != 2 || len(got.Issues) != 2 {
		t.Errorf("status=resolved: total %d, %d issues", got.Total, len(got.Issues))
	}

	// With a filter the tabs count what matches it.
	browser := getJSON[issuesResponse](t, env, issuesPath(p.ID, "source=browser&status=ignored"))
	if browser.Counts.Open != 2 || browser.Counts.Resolved != 1 || browser.Counts.Ignored != 0 || browser.Total != 0 {
		t.Errorf("source=browser: counts %+v, total %d; want 2 open (E4, E6), 1 resolved (E2), 0 ignored", browser.Counts, browser.Total)
	}
}

func TestListIssues_Pagination(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	var events []*sentry.Event
	for i := 1; i <= 30; i++ {
		events = append(events, phpEvent(t, eventSpec{id: hexID(i), typ: fmt.Sprint("E", i), at: testNow.Add(-time.Duration(i) * time.Minute)}))
	}
	env.save(t, p.ID, events...)

	first := getJSON[issuesResponse](t, env, issuesPath(p.ID, "")) // the default page of 25
	second := getJSON[issuesResponse](t, env, issuesPath(p.ID, "limit=25&offset=25"))
	if len(first.Issues) != 25 || len(second.Issues) != 5 || first.Total != 30 || second.Total != 30 {
		t.Fatalf("pages of %d and %d, totals %d and %d; want 25 and 5 of 30", len(first.Issues), len(second.Issues), first.Total, second.Total)
	}
	// Together, every issue once, newest first.
	seen := map[int64]bool{}
	for i, issue := range append(first.Issues, second.Issues...) {
		if seen[issue.ID] || issue.Type != fmt.Sprint("E", i+1) {
			t.Fatalf("issue %d is %s (seen before: %v), want E%d", i, issue.Type, seen[issue.ID], i+1)
		}
		seen[issue.ID] = true
	}
	// Past the end: no issues, the same total.
	if past := getJSON[issuesResponse](t, env, issuesPath(p.ID, "offset=40")); len(past.Issues) != 0 || past.Total != 30 {
		t.Errorf("offset=40: %d issues, total %d", len(past.Issues), past.Total)
	}
	// The total follows the filters.
	if got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "q=E1&limit=5")); got.Total != 11 || len(got.Issues) != 5 {
		t.Errorf("q=E1: total %d, %d issues; want 11 (E1, E10..E19) and 5", got.Total, len(got.Issues))
	}
}

type releaseResponse struct {
	Issue struct {
		ID               int64  `json:"id"`
		Release          string `json:"release"`
		RegressedRelease string `json:"regressedRelease"`
	} `json:"issue"`
}

// An issue's release is the one it was first seen in, whatever later
// events say; when it comes back after a resolve, the detail also names
// the release it came back in.
func TestRelease_FirstReleaseKept(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1), release: "v2.8.1", at: testNow.Add(-3 * time.Hour)}))
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(2), release: "v2.9.0", at: testNow.Add(-2 * time.Hour)}))
	// An event that arrives late, from an older release, does not change it either.
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(3), release: "v2.7.0", at: testNow.Add(-4 * time.Hour)}))
	issueID := firstIssue(t, env, p.ID)
	path := fmt.Sprintf("/ui/api/issues/%d", issueID)

	got := getJSON[releaseResponse](t, env, path)
	if got.Issue.Release != "v2.8.1" || got.Issue.RegressedRelease != "" {
		t.Errorf("release %q, came back in %q; want v2.8.1 and nothing", got.Issue.Release, got.Issue.RegressedRelease)
	}

	// Resolved, then an event of v2.9.1 opens it again.
	env.setStatus(t, issueID, store.IssueResolved)
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(4), release: "v2.9.1", at: testNow.Add(-time.Minute)}))
	got = getJSON[releaseResponse](t, env, path)
	if got.Issue.Release != "v2.8.1" || got.Issue.RegressedRelease != "v2.9.1" {
		t.Errorf("after coming back: release %q, came back in %q; want v2.8.1 and v2.9.1", got.Issue.Release, got.Issue.RegressedRelease)
	}
	// Later events of the open issue change neither.
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(5), release: "v3.0.0"}))
	got = getJSON[releaseResponse](t, env, path)
	if got.Issue.Release != "v2.8.1" || got.Issue.RegressedRelease != "v2.9.1" {
		t.Errorf("after another event: release %q, came back in %q", got.Issue.Release, got.Issue.RegressedRelease)
	}
}

// release keeps the issues with an event in that release.
func TestListIssues_ReleaseFilter(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID,
		phpEvent(t, eventSpec{id: hexID(1), function: "old", release: "v1.0.0", at: testNow.Add(-2 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), function: "both", release: "v1.0.0", at: testNow.Add(-2 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(3), function: "both", release: "v2.0.0", at: testNow.Add(-time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(4), function: "none"}),
	)
	got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "release=v2.0.0"))
	if len(got.Issues) != 1 || got.Total != 1 || got.Counts.Open != 1 || got.OpenIssues != 3 {
		t.Errorf("release=v2.0.0: %d issues, total %d, open %d of %d; want 1, 1, 1 of 3", len(got.Issues), got.Total, got.Counts.Open, got.OpenIssues)
	}
	if got := getJSON[issuesResponse](t, env, issuesPath(p.ID, "release=v1.0.0")); got.Total != 2 {
		t.Errorf("release=v1.0.0: total %d, want 2", got.Total)
	}
	if rec := env.get(t, issuesPath(p.ID, "release="+strings.Repeat("x", 201))); rec.Code != http.StatusBadRequest {
		t.Errorf("a release of 201 characters = %d, want 400", rec.Code)
	}
}
