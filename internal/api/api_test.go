package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/auth"
	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/issues"
	"monitor/internal/scrub"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

// testNow is the fixed clock of every API test: half past noon, so the
// current hour (index 23 of the hourly buckets) started 30 minutes ago.
var testNow = time.Date(2026, 10, 16, 12, 30, 0, 0, time.UTC)

// testEnv is a migrated database and the API behind auth.RequireUser, as
// the server mounts it, with a fixed clock. Requests go out as a signed-in
// admin unless a test picks another user.
type testEnv struct {
	db      *store.DB
	auth    *auth.Service
	handler http.Handler
	// admin is the first account, signed in.
	admin *session
}

// session is a signed-in browser: its cookie and CSRF token.
type session struct {
	userID int64
	cookie *http.Cookie
	csrf   string
}

// envOption changes the auth service of a test environment.
type envOption func(*auth.Options)

func newTestEnv(t *testing.T, opts ...envOption) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	o := auth.Options{
		DB:        db,
		SecretKey: "a-long-random-secret-for-tests",
		BaseURL:   "https://monitor.example.com",
		Logger:    logger,
		Now:       func() time.Time { return testNow },
	}
	for _, opt := range opts {
		opt(&o)
	}
	svc := auth.New(o)

	h := New(db, svc, "https://monitor.example.com", logger)
	h.now = func() time.Time { return testNow }
	guarded := http.NewServeMux()
	svc.Register(guarded)
	h.Register(guarded)
	guarded.HandleFunc("/ui/api/", func(w http.ResponseWriter, r *http.Request) { notFound(w) })
	public := http.NewServeMux()
	svc.RegisterPublic(public)
	public.Handle("/", svc.RequireUser(guarded))

	env := &testEnv{db: db, auth: svc, handler: public}
	admin, err := db.CreateFirstAdmin(context.Background(), "admin@example.com", "not-a-real-hash", testNow)
	if err != nil {
		t.Fatal(err)
	}
	env.admin = env.signIn(t, admin.ID)
	return env
}

// signIn starts a session for a user, as signing in does.
func (e *testEnv) signIn(t *testing.T, userID int64) *session {
	t.Helper()
	cookie, s, err := e.auth.NewSession(context.Background(), userID)
	if err != nil {
		t.Fatal(err)
	}
	return &session{userID: userID, cookie: cookie, csrf: s.CSRFToken}
}

// user makes an account with a role (and, for a member, projects) the way
// accepting an invite does, and signs it in.
func (e *testEnv) user(t *testing.T, email, role string, projectIDs ...int64) *session {
	t.Helper()
	ctx := context.Background()
	_, err := e.db.CreateInvite(ctx, store.NewInvite{Email: email, Role: role, ProjectIDs: projectIDs,
		TokenHash: "test-" + email, Now: testNow, ExpiresAt: testNow.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	u, err := e.db.AcceptInvite(ctx, "test-"+email, "not-a-real-hash", testNow)
	if err != nil {
		t.Fatal(err)
	}
	return e.signIn(t, u.ID)
}

// member makes a member who sees projectIDs, signed in.
func (e *testEnv) member(t *testing.T, email string, projectIDs ...int64) *session {
	t.Helper()
	return e.user(t, email, store.RoleMember, projectIDs...)
}

func (e *testEnv) project(t *testing.T, name string) store.Project {
	t.Helper()
	p, err := e.db.CreateProject(context.Background(), store.NewProject{Name: name, TracksLaravel: true, TracksBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// save stores events the way ingest does: scrubbed, through the queue and
// the batch writer, received at testNow.
func (e *testEnv) save(t *testing.T, projectID int64, events ...*sentry.Event) {
	t.Helper()
	queue := ingest.NewQueue(len(events))
	for _, ev := range events {
		scrub.Event(ev)
		if !queue.TryEnqueue(ingest.Item{ProjectID: projectID, ReceivedAt: testNow, Event: ev}) {
			t.Fatal("queue full")
		}
	}
	// Close without Run writes the whole queue and returns when it is stored.
	ingest.NewWriter(e.db, queue, issues.NoHooks{}, slog.New(slog.NewTextHandler(io.Discard, nil))).Close()
}

// setStatus changes an issue's status in the database, as the status API does.
func (e *testEnv) setStatus(t *testing.T, issueID int64, status string) {
	t.Helper()
	if _, err := e.db.SetIssueStatus(context.Background(), issueID, status, testNow); err != nil {
		t.Fatal(err)
	}
}

// post calls the API as the admin with a body and returns the response.
func (e *testEnv) post(t *testing.T, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	return e.send(t, e.admin, http.MethodPost, path, contentType, body)
}

// get calls the API as the admin and returns the response.
func (e *testEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	return e.send(t, e.admin, http.MethodGet, path, "", "")
}

// as calls the API as someone, with a JSON body when body is not empty.
func (e *testEnv) as(t *testing.T, who *session, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	contentType := ""
	if body != "" {
		contentType = "application/json"
	}
	return e.send(t, who, method, path, contentType, body)
}

// send makes one request with who's cookie and CSRF token.
func (e *testEnv) send(t *testing.T, who *session, method, path, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, r)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if who != nil {
		req.AddCookie(who.cookie)
		req.Header.Set(auth.CSRFHeader, who.csrf)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

// getJSON calls the API, expects 200 and decodes the body into a T.
func getJSON[T any](t *testing.T, e *testEnv, path string) T {
	t.Helper()
	rec := e.get(t, path)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("GET %s: decode %s: %v", path, rec.Body, err)
	}
	return v
}

// eventSpec describes a test event; zero fields get defaults.
type eventSpec struct {
	id       string    // 32 hex characters
	typ      string    // exception type, default RuntimeException
	message  string    // exception value, default "Checkout failed"
	function string    // function of the top frame, default charge
	file     string    // file of the top frame, default /app/Services/PaymentGateway.php
	userID   string    // empty: no user
	url      string    // the request URL; empty: none
	browser  bool      // sent by @sentry/browser instead of sentry-laravel
	at       time.Time // default testNow
}

// phpEvent builds a sentry-laravel style event from spec, or a
// @sentry/browser one when spec.browser is set.
func phpEvent(t *testing.T, spec eventSpec) *sentry.Event {
	t.Helper()
	if spec.typ == "" {
		spec.typ = "RuntimeException"
	}
	if spec.message == "" {
		spec.message = "Checkout failed"
	}
	if spec.function == "" {
		spec.function = "App\\Services\\PaymentGateway::charge"
	}
	if spec.file == "" {
		spec.file = "/app/Services/PaymentGateway.php"
	}
	if spec.at.IsZero() {
		spec.at = testNow
	}
	raw := map[string]any{
		"event_id":  spec.id,
		"timestamp": float64(spec.at.UnixMilli()) / 1000,
		"platform":  "php",
		"sdk":       map[string]any{"name": "sentry.php.laravel", "version": "4.20.0"},
		"exception": map[string]any{"values": []any{map[string]any{
			"type": spec.typ, "value": spec.message,
			"stacktrace": map[string]any{"frames": []any{
				map[string]any{"filename": "/app/Http/Controllers/CheckoutController.php", "function": "App\\Http\\Controllers\\CheckoutController::store", "lineno": 16, "in_app": true},
				map[string]any{"filename": spec.file, "function": spec.function, "lineno": 12, "in_app": true},
			}},
		}}},
	}
	if spec.browser {
		raw["platform"] = "javascript"
		raw["sdk"] = map[string]any{"name": "sentry.javascript.browser", "version": "11.8.0"}
	}
	if spec.userID != "" {
		raw["user"] = map[string]any{"id": spec.userID}
	}
	if spec.url != "" {
		raw["request"] = map[string]any{"url": spec.url}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := sentry.DecodeEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// hexID returns a valid event id that differs per n.
func hexID(n int) string {
	const digits = "0123456789abcdef"
	id := []byte("00000000000000000000000000000000")
	for i := len(id) - 1; n > 0; i-- {
		id[i] = digits[n%16]
		n /= 16
	}
	return string(id)
}

type projectsResponse []struct {
	ID         int64    `json:"id"`
	Name       string   `json:"name"`
	Slug       string   `json:"slug"`
	Sources    []string `json:"sources"`
	OpenIssues int      `json:"openIssues"`
}

func TestListProjects_SourcesAndOpenIssues(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	portal, err := env.db.CreateProject(context.Background(), store.NewProject{Name: "Customer portal", TracksLaravel: true})
	if err != nil {
		t.Fatal(err)
	}
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), function: "a"}),
		phpEvent(t, eventSpec{id: hexID(2), function: "b"}),
		phpEvent(t, eventSpec{id: hexID(3), function: "c"}),
	)
	list, _ := env.db.ListIssues(context.Background(), shop.ID)
	env.setStatus(t, list[0].ID, store.IssueResolved)

	got := getJSON[projectsResponse](t, env, "/ui/api/projects")
	if len(got) != 2 {
		t.Fatalf("got %d projects, want 2: %+v", len(got), got)
	}
	// Sorted by name: Customer portal, Webshop.
	if got[0].ID != portal.ID || got[0].Name != "Customer portal" || got[0].Slug != "customer-portal" {
		t.Errorf("first project = %+v", got[0])
	}
	if len(got[0].Sources) != 1 || got[0].Sources[0] != "laravel" || got[0].OpenIssues != 0 {
		t.Errorf("Customer portal sources %v, open %d; want [laravel], 0", got[0].Sources, got[0].OpenIssues)
	}
	if len(got[1].Sources) != 2 || got[1].Sources[0] != "laravel" || got[1].Sources[1] != "browser" {
		t.Errorf("Webshop sources = %v, want [laravel browser]", got[1].Sources)
	}
	if got[1].OpenIssues != 2 {
		t.Errorf("Webshop open issues = %d, want 2 (one of three is resolved)", got[1].OpenIssues)
	}
}

func TestListProjects_EmptyIsAList(t *testing.T) {
	env := newTestEnv(t)
	rec := env.get(t, "/ui/api/projects")
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("GET /ui/api/projects = %d %q, want 200 []", rec.Code, rec.Body)
	}
}
