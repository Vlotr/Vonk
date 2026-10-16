package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"monitor/internal/config"
	"monitor/internal/health"
	"monitor/internal/ingest"
	"monitor/internal/store"
	"monitor/internal/web"
)

const indexHTML = `<!doctype html><html><body><div id="root">dashboard-index</div></body></html>`

// testFiles stands in for a built dashboard.
var testFiles = fstest.MapFS{
	"index.html":    {Data: []byte(indexHTML)},
	"assets/app.js": {Data: []byte(`console.log("app")`)},
	".keep":         {Data: nil},
}

// newTestServer builds the full handler on a fresh database.
func newTestServer(t *testing.T) http.Handler {
	t.Helper()
	return newTestServerWithLog(t, io.Discard)
}

func newTestServerWithLog(t *testing.T, logOutput io.Writer) http.Handler {
	t.Helper()
	return New(newTestDeps(t, logOutput))
}

// newTestDeps opens a fresh, migrated database and returns the server's dependencies.
func newTestDeps(t *testing.T, logOutput io.Writer) Deps {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		SecretKey:  "a-long-random-secret-for-tests",
		BaseURL:    "https://monitor.example.com",
		DataDir:    dir,
		SQLitePath: filepath.Join(dir, "monitor.db"),
	}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	logger := slog.New(slog.NewTextHandler(logOutput, nil))
	queue := ingest.NewQueue(100)
	return Deps{
		Config:  cfg,
		DB:      db,
		Logger:  logger,
		Version: "test",
		SPA:     web.NewHandler(testFiles),
		Queue:   queue,
		Limiter: ingest.NewRateLimiter(),
		// A roomy disk, whatever the machine running the tests has.
		Health: &health.Checker{DB: db, DataDir: dir, Queue: queue, Version: "test", Log: logger,
			DiskUsage: func(string) (uint64, uint64, error) { return 50 << 30, 100 << 30, nil }},
	}
}

// signIn creates the first account through the setup page's API and
// returns a request option that sends its session cookie and CSRF token.
func signIn(t *testing.T, h http.Handler) func(*http.Request) {
	t.Helper()
	rec := do(h, http.MethodPost, "/ui/api/setup", "", func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"email":"admin@example.com","password":"a good password"}`))
		r.Header.Set("Content-Type", "application/json")
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("setup = %d %s", rec.Code, rec.Body)
	}
	var me struct {
		CSRFToken string `json:"csrfToken"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil || len(rec.Result().Cookies()) != 1 {
		t.Fatalf("setup answer %s, cookies %v", rec.Body, rec.Result().Cookies())
	}
	cookie := rec.Result().Cookies()[0]
	return func(r *http.Request) {
		r.AddCookie(cookie)
		r.Header.Set("X-CSRF-Token", me.CSRFToken)
	}
}

// sentryEnvelope is a minimal envelope with one event.
const sentryEnvelope = `{"event_id":"9ec79c33ec9942ab8353589fcb2e04dc"}
{"type":"event"}
{"platform":"php","exception":{"values":[{"type":"RuntimeException","value":"Checkout failed"}]}}
`

// postEnvelope sends sentryEnvelope for project p from remoteAddr.
func postEnvelope(h http.Handler, p store.Project, remoteAddr string, setup ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/api/%d/envelope/", p.ID), strings.NewReader(sentryEnvelope))
	req.RemoteAddr = remoteAddr
	req.Header.Set("X-Sentry-Auth", "Sentry sentry_version=7, sentry_key="+p.SentryKey)
	for _, f := range setup {
		f(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// do sends one request from remoteAddr ("" means loopback) and returns the recorded response.
func do(h http.Handler, method, target, remoteAddr string, setup ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = "127.0.0.1:50000"
	if remoteAddr != "" {
		req.RemoteAddr = remoteAddr
	}
	for _, f := range setup {
		f(req)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func isHTML(rec *httptest.ResponseRecorder) bool {
	return strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html")
}

func TestHealth_OK(t *testing.T) {
	h := newTestServer(t)

	rec := do(h, http.MethodGet, "/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v; %s", err, rec.Body)
	}
	if body["status"] != "ok" {
		t.Errorf(`status field = %v, want "ok"`, body["status"])
	}
	if body["version"] != "test" {
		t.Errorf(`version field = %v, want "test"`, body["version"])
	}
}

func TestHealth_503WhenDatabaseFails(t *testing.T) {
	deps := newTestDeps(t, io.Discard)
	h := New(deps)
	deps.DB.Close()

	rec := do(h, http.MethodGet, "/health", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with a closed database; body %s", rec.Code, rec.Body)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v; %s", err, rec.Body)
	}
	if body["status"] != "error" || body["db"] != "error" {
		t.Errorf("body = %v, want status and db \"error\"", body)
	}
}

func TestSPA_Fallback(t *testing.T) {
	h := newTestServer(t)
	session := signIn(t, h)

	t.Run("client route gets index.html", func(t *testing.T) {
		for _, path := range []string{"/", "/projects/1/issues", "/index.html"} {
			rec := do(h, http.MethodGet, path, "", session)
			if rec.Code != http.StatusOK || !isHTML(rec) || !strings.Contains(rec.Body.String(), "dashboard-index") {
				t.Errorf("GET %s = %d %q %q, want 200 index.html", path, rec.Code, rec.Header().Get("Content-Type"), rec.Body)
			}
			if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
				t.Errorf("GET %s Cache-Control = %q, want no-cache", path, cc)
			}
		}
	})

	t.Run("built asset is served and cached", func(t *testing.T) {
		rec := do(h, http.MethodGet, "/assets/app.js", "")
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `console.log("app")`) {
			t.Fatalf("GET /assets/app.js = %d %q", rec.Code, rec.Body)
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
			t.Errorf("Cache-Control = %q, want immutable", cc)
		}
	})

	t.Run("unknown dashboard API is a JSON 404", func(t *testing.T) {
		rec := do(h, http.MethodGet, "/ui/api/nope", "", session)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Errorf(`body = %q, want {"error":"..."}`, rec.Body)
		}
	})

	t.Run("client routes with a dot in the last segment get index.html", func(t *testing.T) {
		for _, path := range []string{"/projects/1/users/jan@example.com", "/releases/v1.2.3", "/favicon.ico"} {
			rec := do(h, http.MethodGet, path, "", session)
			if rec.Code != http.StatusOK || !isHTML(rec) || !strings.Contains(rec.Body.String(), "dashboard-index") {
				t.Errorf("GET %s = %d %q, want 200 index.html", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
	})

	t.Run("missing files are 404, not HTML", func(t *testing.T) {
		for _, path := range []string{"/assets/missing.js", "/assets/v1.2.3", "/.keep"} {
			rec := do(h, http.MethodGet, path, "", session)
			if rec.Code != http.StatusNotFound || isHTML(rec) {
				t.Errorf("GET %s = %d %q, want a 404 that is not HTML", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
	})

	t.Run("other methods are not answered with HTML", func(t *testing.T) {
		rec := do(h, http.MethodPost, "/projects", "", session)
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST /projects = %d, want 405 or 404", rec.Code)
		}
		if isHTML(rec) {
			t.Errorf("POST /projects answered with HTML")
		}
	})
}

func TestDashboardAPI_Mounted(t *testing.T) {
	h := newTestServer(t)
	// Behind RequireUser like every dashboard route.
	if rec := do(h, http.MethodGet, "/ui/api/projects", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /ui/api/projects without a session = %d, want 401", rec.Code)
	}
	session := signIn(t, h)

	rec := do(h, http.MethodGet, "/ui/api/projects", "", session)
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("GET /ui/api/projects = %d %q, want 200 []", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/ui/api/me", "", session); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "admin@example.com") {
		t.Errorf("GET /ui/api/me = %d %s", rec.Code, rec.Body)
	}
	if rec := do(h, http.MethodGet, "/ui/api/settings/projects", "", session); rec.Code != http.StatusOK {
		t.Errorf("GET /ui/api/settings/projects = %d, want 200", rec.Code)
	}
	// A wrong method falls through to the /ui/api/ catch-all: a JSON 404, not Go's plain-text 405.
	rec = do(h, http.MethodDelete, "/ui/api/projects", "", session)
	if rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Errorf("DELETE /ui/api/projects = %d %q, want 404 {\"error\":\"not found\"}", rec.Code, rec.Body)
	}
}

func TestSPA_NotBuilt(t *testing.T) {
	h := web.NewHandler(fstest.MapFS{".keep": {Data: nil}})

	rec := do(h, http.MethodGet, "/", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 when the dashboard is not built", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "make build") {
		t.Errorf("body %q does not say how to build the dashboard", rec.Body)
	}
}

func TestDashboard_SignInRequired(t *testing.T) {
	h := newTestServer(t)
	const remote = "203.0.113.5:40000"

	// Before the first account exists, pages lead to the setup page.
	if rec := do(h, http.MethodGet, "/projects/1/issues", remote); rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Errorf("GET /projects/1/issues before setup = %d %q, want 303 /setup", rec.Code, rec.Header().Get("Location"))
	}
	session := signIn(t, h)

	// The sign-in pages, their API and the app's files are open...
	for _, path := range []string{"/setup", "/login", "/invite", "/forgot-password", "/reset-password"} {
		if rec := do(h, http.MethodGet, path, remote); rec.Code != http.StatusOK || !isHTML(rec) {
			t.Errorf("GET %s = %d, want the app's index.html", path, rec.Code)
		}
	}
	if rec := do(h, http.MethodGet, "/assets/app.js", remote); rec.Code != http.StatusOK {
		t.Errorf("GET /assets/app.js = %d, want 200", rec.Code)
	}
	if rec := do(h, http.MethodGet, "/ui/api/auth/status", remote); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"setupNeeded":false`) {
		t.Errorf("GET /ui/api/auth/status = %d %s", rec.Code, rec.Body)
	}

	// ...everything else needs a session, from anywhere: this machine
	// (loopback) gets no exception any more.
	for _, addr := range []string{remote, "127.0.0.1:50000", "[::1]:50000"} {
		rec := do(h, http.MethodGet, "/", addr)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login" {
			t.Errorf("GET / from %s without a session = %d %q, want 303 /login", addr, rec.Code, rec.Header().Get("Location"))
		}
		rec = do(h, http.MethodGet, "/ui/api/overview", addr)
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("GET /ui/api/overview from %s without a session = %d %q, want a JSON 401", addr, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	if rec := do(h, http.MethodGet, "/", remote, session); rec.Code != http.StatusOK || !isHTML(rec) {
		t.Errorf("GET / signed in = %d, want 200", rec.Code)
	}
	if rec := do(h, http.MethodPost, "/ui/api/setup", remote, func(r *http.Request) {
		r.Body = io.NopCloser(strings.NewReader(`{"email":"second@example.com","password":"a good password"}`))
		r.Header.Set("Content-Type", "application/json")
	}); rec.Code != http.StatusNotFound {
		t.Errorf("second setup = %d, want 404", rec.Code)
	}
}

func TestDashboard_HealthAndIngestNeedNoSession(t *testing.T) {
	deps := newTestDeps(t, io.Discard)
	h := New(deps)
	signIn(t, h)
	p, err := deps.DB.CreateProject(context.Background(), store.NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}
	const remote = "203.0.113.5:40000"

	if rec := do(h, http.MethodGet, "/health", remote); rec.Code != http.StatusOK {
		t.Errorf("GET /health = %d, want 200", rec.Code)
	}
	// Browser SDKs send from the sites they run on: ingest takes cross-site requests.
	rec := postEnvelope(h, p, remote, func(r *http.Request) {
		r.Header.Set("Origin", "https://shop.example.com")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
	})
	if rec.Code != http.StatusOK {
		t.Errorf("POST envelope from another site = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestDashboard_ChangesFromOtherSitesRefused(t *testing.T) {
	h := newTestServer(t)
	session := signIn(t, h)
	crossSite := func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.Header.Set("Origin", "https://evil.example.com")
	}
	jsonBody := func(body string) func(*http.Request) {
		return func(r *http.Request) {
			r.Body = io.NopCloser(strings.NewReader(body))
			r.Header.Set("Content-Type", "application/json")
		}
	}

	// Even with the session and its CSRF token, a change sent from another site is refused...
	rec := do(h, http.MethodPost, "/ui/api/settings/projects", "", session, jsonBody(`{"name":"Evil"}`), crossSite)
	if rec.Code != http.StatusForbidden || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		t.Errorf("cross-site POST = %d %q, want a JSON 403", rec.Code, rec.Header().Get("Content-Type"))
	}
	rec = do(h, http.MethodPost, "/ui/api/login", "", jsonBody(`{"email":"admin@example.com","password":"a good password"}`), crossSite)
	if rec.Code != http.StatusForbidden {
		t.Errorf("cross-site login = %d, want 403", rec.Code)
	}
	// ...reading is not a change...
	if rec := do(h, http.MethodGet, "/ui/api/projects", "", session, crossSite); rec.Code != http.StatusOK {
		t.Errorf("cross-site GET = %d, want 200 (SameSite=Lax keeps the cookie out of real cross-site requests)", rec.Code)
	}
	// ...and the same change from the dashboard itself goes through.
	rec = do(h, http.MethodPost, "/ui/api/settings/projects", "", session, jsonBody(`{"name":"Fine"}`), func(r *http.Request) {
		r.Header.Set("Sec-Fetch-Site", "same-origin")
	})
	if rec.Code != http.StatusCreated {
		t.Errorf("same-origin POST = %d %s, want 201", rec.Code, rec.Body)
	}
}

func TestDashboard_SecurityHeaders(t *testing.T) {
	h := newTestServer(t)
	session := signIn(t, h)
	for _, path := range []string{"/", "/login", "/ui/api/projects"} {
		rec := do(h, http.MethodGet, path, "", session)
		if rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Content-Security-Policy") != "frame-ancestors 'none'" ||
			rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("GET %s headers = %v", path, rec.Header())
		}
	}
	if cc := do(h, http.MethodGet, "/ui/api/projects", "", session).Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("API Cache-Control = %q, want no-store", cc)
	}
}

func TestSentryRoute(t *testing.T) {
	deps := newTestDeps(t, io.Discard)
	h := New(deps)
	p, err := deps.DB.CreateProject(context.Background(), store.NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}
	const remote = "203.0.113.5:40000"

	if rec := postEnvelope(h, p, remote); rec.Code != http.StatusOK {
		t.Fatalf("POST envelope = %d; body %s", rec.Code, rec.Body)
	}
	if deps.Queue.Len() != 1 {
		t.Errorf("queue holds %d items, want the accepted event", deps.Queue.Len())
	}

	rec := do(h, http.MethodOptions, fmt.Sprintf("/api/%d/envelope/", p.ID), remote)
	if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Errorf("preflight = %d %v", rec.Code, rec.Header())
	}

	// Without the trailing slash, Go's mux redirects with 307, which keeps the method and body.
	rec = do(h, http.MethodPost, fmt.Sprintf("/api/%d/envelope", p.ID), remote)
	if rec.Code != http.StatusTemporaryRedirect || rec.Header().Get("Location") != fmt.Sprintf("/api/%d/envelope/", p.ID) {
		t.Errorf("POST without trailing slash = %d, Location %q", rec.Code, rec.Header().Get("Location"))
	}

	// Other /api/ paths stay a JSON 404.
	for _, path := range []string{"/api/1/store/", "/api/0/projects/", fmt.Sprintf("/api/%d/envelope/extra", p.ID)} {
		rec := do(h, http.MethodPost, path, remote)
		if rec.Code != http.StatusNotFound || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("POST %s = %d %q, want a JSON 404", path, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

func TestRequestLog_IngestSuccessAtDebug(t *testing.T) {
	var logs bytes.Buffer
	deps := newTestDeps(t, &logs) // the text handler logs info and up
	h := New(deps)
	p, err := deps.DB.CreateProject(context.Background(), store.NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}

	postEnvelope(h, p, "203.0.113.5:40000")
	if strings.Contains(logs.String(), "/envelope/") {
		t.Errorf("an accepted envelope was logged at info: %s", logs.String())
	}
	postEnvelope(h, p, "203.0.113.5:40000", func(r *http.Request) { r.Header.Set("X-Sentry-Auth", "sentry_key=wrong") })
	if !strings.Contains(logs.String(), "status=401") {
		t.Errorf("a refused envelope was not logged: %s", logs.String())
	}
}

func TestRequestLog_OmitsQueryAndIP(t *testing.T) {
	var logs bytes.Buffer
	h := newTestServerWithLog(t, &logs)

	do(h, http.MethodGet, "/ui/api/nope?sentry_key=secret123", "")

	line := logs.String()
	for _, want := range []string{"method=GET", "path=/ui/api/nope", "status=401"} {
		if !strings.Contains(line, want) {
			t.Errorf("log %q does not contain %q", line, want)
		}
	}
	for _, leak := range []string{"secret123", "127.0.0.1"} {
		if strings.Contains(line, leak) {
			t.Errorf("log %q contains %q", line, leak)
		}
	}
}

func TestRecoverPanics(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	h := recoverPanics(logger, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	rec := do(h, http.MethodGet, "/x", "")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(logs.String(), "boom") {
		t.Errorf("panic not logged: %q", logs.String())
	}
}
