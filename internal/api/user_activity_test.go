package api

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"monitor/internal/auth"
	"monitor/internal/store"
)

type activityResponse struct {
	Project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	UserID string         `json:"userId"`
	Items  []activityItem `json:"items"`
	Next   *time.Time     `json:"next"`
}

type activityItem struct {
	Kind       string         `json:"kind"`
	At         time.Time      `json:"at"`
	Path       string         `json:"path"`
	Route      string         `json:"route"`
	DurationMs *int64         `json:"durationMs"`
	IssueID    int64          `json:"issueId"`
	Type       string         `json:"type"`
	Message    string         `json:"message"`
	Name       string         `json:"name"`
	Properties map[string]any `json:"properties"`
}

// itemsText writes items as "kind:what" for messages.
func itemsText(items []activityItem) string {
	var out []string
	for _, it := range items {
		what := it.Path
		switch it.Kind {
		case "error":
			what = it.Type
		case "event":
			what = it.Name
		}
		out = append(out, it.Kind+":"+what)
	}
	return strings.Join(out, " ")
}

func activityPath(projectID int64, userID, query string) string {
	path := fmt.Sprintf("/ui/api/projects/%d/users/%s/activity", projectID, url.PathEscape(userID))
	if query != "" {
		path += "?" + query
	}
	return path
}

// saveCustomEvents stores custom events as the ingest writer does.
func (e *testEnv) saveCustomEvents(t *testing.T, events ...store.CustomEvent) {
	t.Helper()
	if _, err := e.db.SaveCustomEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

// customEvent is a custom event of a user on a page; n makes its id unique.
func customEvent(projectID int64, n int, name, user, path string, at time.Time) store.CustomEvent {
	return store.CustomEvent{ProjectID: projectID, EventID: fmt.Sprintf("ce-%d-%d", projectID, n), Name: name, UserID: user,
		Path: path, Route: path, At: at, Properties: `{"plan":"pro"}`}
}

// A user's page views, errors and custom events, the newest first, and
// nothing of anyone else or of bots. User ids may hold dots and an @.
func TestUserActivity_NewestFirst(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	portal := env.pageProject(t, "Portal")
	anna := "anna.de.vries@example.com"
	env.visits(t, shop.ID,
		visit{user: anna, route: "/products/:slug", at: testNow.Add(-10 * time.Minute), stay: 40 * time.Second},
		visit{user: anna, route: "/checkout", at: testNow.Add(-8 * time.Minute)},
		visit{user: anna, route: "/crawled", at: testNow.Add(-7 * time.Minute), bot: true},
		visit{user: "7", route: "/someone-else", at: testNow.Add(-6 * time.Minute)},
	)
	env.visits(t, portal.ID, visit{user: anna, route: "/portal", at: testNow.Add(-5 * time.Minute)})
	env.saveCustomEvents(t,
		customEvent(shop.ID, 1, "checkout_started", anna, "/checkout", testNow.Add(-7*time.Minute)),
		customEvent(shop.ID, 2, "checkout_started", "7", "/checkout", testNow.Add(-7*time.Minute)),
	)
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), userID: anna, typ: "TypeError", message: "Cannot read properties of undefined",
			url: "https://shop.example.com/checkout?token=abc", at: testNow.Add(-6 * time.Minute)}),
		phpEvent(t, eventSpec{id: hexID(2), userID: "7", function: "other", at: testNow.Add(-6 * time.Minute)}),
	)

	got := getJSON[activityResponse](t, env, activityPath(shop.ID, anna, ""))
	if got.UserID != anna || got.Project.ID != shop.ID || got.Project.Name != "Webshop" || got.Next != nil {
		t.Errorf("answer = user %q, project %+v, next %v", got.UserID, got.Project, got.Next)
	}
	if text := itemsText(got.Items); text != "error:TypeError event:checkout_started pageview:/checkout pageview:/products/:slug" {
		t.Fatalf("items = %s", text)
	}
	errorItem, event, view := got.Items[0], got.Items[1], got.Items[3]
	if errorItem.Message != "Cannot read properties of undefined" || errorItem.Path != "/checkout" || errorItem.Route != "/checkout" ||
		!errorItem.At.Equal(testNow.Add(-6*time.Minute)) {
		t.Errorf("error = %+v", errorItem)
	}
	if issue, err := env.db.IssueByID(context.Background(), errorItem.IssueID); err != nil || issue.Title != "TypeError" {
		t.Errorf("the error's issue %d = %+v, %v; want the TypeError", errorItem.IssueID, issue, err)
	}
	if event.Path != "/checkout" || event.Properties["plan"] != "pro" {
		t.Errorf("event = %+v", event)
	}
	if view.DurationMs == nil || *view.DurationMs != 40_000 || view.Route != "/products/:slug" {
		t.Errorf("page view = %+v, want 40 s on /products/:slug", view)
	}
	if got.Items[2].DurationMs != nil {
		t.Errorf("open page view has a time: %+v", got.Items[2])
	}

	// A user id nobody has: an empty list, not a 404.
	if got := getJSON[activityResponse](t, env, activityPath(shop.ID, "nobody", "")); len(got.Items) != 0 || got.Items == nil {
		t.Errorf("unknown user = %+v, want an empty list", got.Items)
	}
}

// Pages of the activity follow each other through next (before=), and
// items that share a time at a page's edge are neither lost nor repeated.
func TestUserActivity_Paging(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	user := "team/7" // a slash, encoded in the path
	same := testNow.Add(-5 * time.Minute)
	env.visits(t, shop.ID,
		visit{user: user, route: "/a", at: testNow.Add(-1 * time.Minute)},
		visit{user: user, route: "/b", at: testNow.Add(-2 * time.Minute)},
		visit{user: user, route: "/c", at: same},
		visit{user: user, route: "/d", at: same},
		visit{user: user, route: "/e", at: testNow.Add(-9 * time.Minute)},
	)
	env.saveCustomEvents(t, customEvent(shop.ID, 1, "signed_in", user, "/c", same))

	var seen []string
	query := "limit=3"
	for page := 0; page < 5; page++ {
		got := getJSON[activityResponse](t, env, activityPath(shop.ID, user, query))
		for _, it := range got.Items {
			seen = append(seen, it.Path)
		}
		if got.Next == nil {
			break
		}
		query = "limit=3&before=" + url.QueryEscape(got.Next.Format(time.RFC3339Nano))
	}
	// The three items at the same time come on one page, after /a and /b.
	if text := strings.Join(seen, " "); len(seen) != 6 || !strings.HasPrefix(text, "/a /b ") || !strings.HasSuffix(text, " /e") ||
		!strings.Contains(text, "/c") || !strings.Contains(text, "/d") {
		t.Errorf("pages together = %s, want /a /b, the three at the same time, /e", text)
	}
}

func TestUserActivity_BadQuery(t *testing.T) {
	env := newTestEnv(t)
	shop := env.pageProject(t, "Webshop")
	for _, q := range []string{"limit=0", "limit=101", "limit=x", "before=yesterday"} {
		if rec := env.get(t, activityPath(shop.ID, "42", q)); rec.Code != http.StatusBadRequest {
			t.Errorf("?%s = %d %s, want 400", q, rec.Code, rec.Body)
		}
	}
	if rec := env.get(t, activityPath(999, "42", "")); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
}

// The user activity route carries a user id, often an email address, in
// its path. When a request to it fails, every log line names the route with
// the id masked, as the request log does: the session check's when the
// browser went away before it, the activity query's when the browser went
// away after it (debug), and the query's when the database fails (error).
func TestUserActivity_FailuresLogNoUserID(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	env := newTestEnv(t, func(o *auth.Options) { o.Logger = logger })
	env.api.log = logger
	shop := env.project(t, "Webshop")
	path := fmt.Sprintf("/ui/api/projects/%d/users/jan.de.vries%%40example.com/activity", shop.ID)
	signedIn := env.handler
	cancelled := func(r *http.Request) *http.Request {
		ctx, cancel := context.WithCancel(r.Context())
		cancel()
		return r.WithContext(ctx)
	}

	// Gone before the session check.
	env.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { signedIn.ServeHTTP(w, cancelled(r)) })
	env.as(t, env.admin, "GET", path, "")
	// Gone after it, during the query.
	env.handler = env.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.guarded.ServeHTTP(w, cancelled(r))
	}))
	env.as(t, env.admin, "GET", path, "")
	// The database fails after the session check.
	env.handler = env.auth.RequireUser(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		env.db.Close()
		env.guarded.ServeHTTP(w, r)
	}))
	if rec := env.as(t, env.admin, "GET", path, ""); rec.Code != http.StatusInternalServerError {
		t.Errorf("activity with the database closed = %d, want 500", rec.Code)
	}

	lines := logs.String()
	for _, want := range []string{"auth: the client went away", "dashboard api: the client went away", "level=ERROR msg=\"dashboard api\""} {
		if !strings.Contains(lines, want) {
			t.Errorf("logs lack %q:\n%s", want, lines)
		}
	}
	if masked := fmt.Sprintf("path=/ui/api/projects/%d/users/:userId/activity", shop.ID); strings.Count(lines, masked) != 3 {
		t.Errorf("want 3 lines with %s:\n%s", masked, lines)
	}
	if strings.Contains(lines, "jan") {
		t.Errorf("a log line names the user id:\n%s", lines)
	}
}
