package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

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

// testEnv is a migrated database and the API on a mux, with a fixed clock.
type testEnv struct {
	db     *store.DB
	dbPath string
	mux    *http.ServeMux
}

func newTestEnv(t *testing.T) *testEnv {
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

	h := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.now = func() time.Time { return testNow }
	mux := http.NewServeMux()
	h.Register(mux)
	return &testEnv{db: db, dbPath: cfg.SQLitePath, mux: mux}
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

// setStatus changes an issue's status directly in the database; the status
// API arrives in Task 4.
func (e *testEnv) setStatus(t *testing.T, issueID int64, status string) {
	t.Helper()
	conn, err := sql.Open("sqlite", e.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec("UPDATE issues SET status = ? WHERE id = ?", status, issueID); err != nil {
		t.Fatal(err)
	}
}

// get calls the API and returns the response.
func (e *testEnv) get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
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
	function string    // function of the top frame, default charge
	userID   string    // empty: no user
	at       time.Time // default testNow
}

// phpEvent builds a sentry-laravel style event from spec.
func phpEvent(t *testing.T, spec eventSpec) *sentry.Event {
	t.Helper()
	if spec.typ == "" {
		spec.typ = "RuntimeException"
	}
	if spec.function == "" {
		spec.function = "App\\Services\\PaymentGateway::charge"
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
			"type": spec.typ, "value": "Checkout failed",
			"stacktrace": map[string]any{"frames": []any{
				map[string]any{"filename": "/app/Http/Controllers/CheckoutController.php", "function": "App\\Http\\Controllers\\CheckoutController::store", "lineno": 16, "in_app": true},
				map[string]any{"filename": "/app/Services/PaymentGateway.php", "function": spec.function, "lineno": 12, "in_app": true},
			}},
		}}},
	}
	if spec.userID != "" {
		raw["user"] = map[string]any{"id": spec.userID}
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
