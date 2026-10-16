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

// newTestServer builds the full handler on a fresh database. devBasicAuth is
// the DEV_BASIC_AUTH value ("" for none).
func newTestServer(t *testing.T, devBasicAuth string) http.Handler {
	t.Helper()
	return newTestServerWithLog(t, devBasicAuth, io.Discard)
}

func newTestServerWithLog(t *testing.T, devBasicAuth string, logOutput io.Writer) http.Handler {
	t.Helper()
	return New(newTestDeps(t, devBasicAuth, logOutput))
}

// newTestDeps opens a fresh, migrated database and returns the server's dependencies.
func newTestDeps(t *testing.T, devBasicAuth string, logOutput io.Writer) Deps {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		BaseURL:      "https://monitor.example.com",
		DataDir:      dir,
		SQLitePath:   filepath.Join(dir, "monitor.db"),
		DevBasicAuth: devBasicAuth,
	}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	return Deps{
		Config:  cfg,
		DB:      db,
		Logger:  slog.New(slog.NewTextHandler(logOutput, nil)),
		Version: "test",
		SPA:     web.NewHandler(testFiles),
		Queue:   ingest.NewQueue(100),
		Limiter: ingest.NewRateLimiter(),
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
	h := newTestServer(t, "")

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
	deps := newTestDeps(t, "", io.Discard)
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
	h := newTestServer(t, "")

	t.Run("client route gets index.html", func(t *testing.T) {
		for _, path := range []string{"/", "/projects/1/issues", "/index.html"} {
			rec := do(h, http.MethodGet, path, "")
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
		rec := do(h, http.MethodGet, "/ui/api/nope", "")
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
			rec := do(h, http.MethodGet, path, "")
			if rec.Code != http.StatusOK || !isHTML(rec) || !strings.Contains(rec.Body.String(), "dashboard-index") {
				t.Errorf("GET %s = %d %q, want 200 index.html", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
	})

	t.Run("missing files are 404, not HTML", func(t *testing.T) {
		for _, path := range []string{"/assets/missing.js", "/assets/v1.2.3", "/.keep"} {
			rec := do(h, http.MethodGet, path, "")
			if rec.Code != http.StatusNotFound || isHTML(rec) {
				t.Errorf("GET %s = %d %q, want a 404 that is not HTML", path, rec.Code, rec.Header().Get("Content-Type"))
			}
		}
	})

	t.Run("other methods are not answered with HTML", func(t *testing.T) {
		rec := do(h, http.MethodPost, "/projects", "")
		if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
			t.Errorf("POST /projects = %d, want 405 or 404", rec.Code)
		}
		if isHTML(rec) {
			t.Errorf("POST /projects answered with HTML")
		}
	})
}

func TestDashboardAPI_Mounted(t *testing.T) {
	h := newTestServer(t, "dev:secret-password")

	rec := do(h, http.MethodGet, "/ui/api/projects", "", func(r *http.Request) { r.SetBasicAuth("dev", "secret-password") })
	if rec.Code != http.StatusOK || rec.Body.String() != "[]\n" {
		t.Errorf("GET /ui/api/projects = %d %q, want 200 []", rec.Code, rec.Body)
	}
	// Behind the guard like every dashboard route.
	if rec := do(h, http.MethodGet, "/ui/api/projects", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("GET /ui/api/projects without credentials = %d, want 401", rec.Code)
	}
	// A wrong method falls through to the /ui/api/ catch-all: a JSON 404, not Go's plain-text 405.
	rec = do(h, http.MethodDelete, "/ui/api/projects", "", func(r *http.Request) { r.SetBasicAuth("dev", "secret-password") })
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

func TestDashboardGuard(t *testing.T) {
	const remote = "203.0.113.5:40000"

	t.Run("without DEV_BASIC_AUTH only loopback reaches the dashboard", func(t *testing.T) {
		h := newTestServer(t, "")

		for _, path := range []string{"/", "/projects/1/issues", "/ui/api/projects"} {
			if rec := do(h, http.MethodGet, path, remote); rec.Code != http.StatusForbidden {
				t.Errorf("GET %s from %s = %d, want 403", path, remote, rec.Code)
			}
		}
		if rec := do(h, http.MethodGet, "/ui/api/projects", remote); !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("403 on /ui/api has Content-Type %q, want JSON", rec.Header().Get("Content-Type"))
		}

		for _, addr := range []string{"127.0.0.1:50000", "[::1]:50000"} {
			if rec := do(h, http.MethodGet, "/", addr); rec.Code != http.StatusOK {
				t.Errorf("GET / from %s = %d, want 200", addr, rec.Code)
			}
			if rec := do(h, http.MethodGet, "/ui/api/nope", addr); rec.Code != http.StatusNotFound {
				t.Errorf("GET /ui/api/nope from %s = %d, want 404 (allowed through)", addr, rec.Code)
			}
		}
	})

	t.Run("a loopback peer carrying forwarding headers is a proxy and is refused", func(t *testing.T) {
		h := newTestServer(t, "")

		headers := map[string]string{
			"Forwarded":       "for=203.0.113.5",
			"X-Forwarded-For": "203.0.113.5",
			"X-Real-IP":       "203.0.113.5",
		}
		for name, value := range headers {
			for _, addr := range []string{"127.0.0.1:50000", "[::1]:50000"} {
				rec := do(h, http.MethodGet, "/", addr, func(r *http.Request) { r.Header.Set(name, value) })
				if rec.Code != http.StatusForbidden {
					t.Errorf("GET / from %s with %s = %d, want 403", addr, name, rec.Code)
				}
			}
		}
	})

	t.Run("with DEV_BASIC_AUTH everyone needs the credentials", func(t *testing.T) {
		h := newTestServer(t, "user:pass")

		cases := []struct {
			name, addr string
			setup      func(*http.Request)
			want       int
		}{
			{"no credentials", remote, nil, http.StatusUnauthorized},
			{"wrong password", remote, func(r *http.Request) { r.SetBasicAuth("user", "nope") }, http.StatusUnauthorized},
			{"wrong user", remote, func(r *http.Request) { r.SetBasicAuth("admin", "pass") }, http.StatusUnauthorized},
			{"loopback without credentials", "127.0.0.1:50000", nil, http.StatusUnauthorized},
			{"right credentials", remote, func(r *http.Request) { r.SetBasicAuth("user", "pass") }, http.StatusOK},
		}
		for _, tc := range cases {
			for _, path := range []string{"/", "/ui/api/nope"} {
				var setup []func(*http.Request)
				if tc.setup != nil {
					setup = append(setup, tc.setup)
				}
				rec := do(h, http.MethodGet, path, tc.addr, setup...)

				want := tc.want
				if want == http.StatusOK && path == "/ui/api/nope" {
					want = http.StatusNotFound // allowed through to the API's 404
				}
				if rec.Code != want {
					t.Errorf("%s: GET %s = %d, want %d", tc.name, path, rec.Code, want)
				}
				if want == http.StatusUnauthorized && !strings.HasPrefix(rec.Header().Get("WWW-Authenticate"), "Basic ") {
					t.Errorf("%s: GET %s has no Basic WWW-Authenticate header", tc.name, path)
				}
			}
		}
	})

	t.Run("health and ingest are never guarded", func(t *testing.T) {
		for _, auth := range []string{"", "user:pass"} {
			deps := newTestDeps(t, auth, io.Discard)
			h := New(deps)
			p, err := deps.DB.CreateProject(context.Background(), store.NewProject{Name: "Webshop"})
			if err != nil {
				t.Fatal(err)
			}

			if rec := do(h, http.MethodGet, "/health", remote); rec.Code != http.StatusOK {
				t.Errorf("DEV_BASIC_AUTH=%q: GET /health from %s = %d, want 200", auth, remote, rec.Code)
			}
			rec := postEnvelope(h, p, remote, func(r *http.Request) { r.Header.Set("X-Forwarded-For", "198.51.100.7") })
			if rec.Code != http.StatusOK {
				t.Errorf("DEV_BASIC_AUTH=%q: POST envelope from %s = %d, want 200; body %s", auth, remote, rec.Code, rec.Body)
			}
			if rec.Header().Get("WWW-Authenticate") != "" {
				t.Errorf("DEV_BASIC_AUTH=%q: POST envelope asks for credentials", auth)
			}
		}
	})
}

func TestSentryRoute(t *testing.T) {
	deps := newTestDeps(t, "", io.Discard)
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
	deps := newTestDeps(t, "", &logs) // the text handler logs info and up
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
	h := newTestServerWithLog(t, "", &logs)

	do(h, http.MethodGet, "/ui/api/nope?sentry_key=secret123", "")

	line := logs.String()
	for _, want := range []string{"method=GET", "path=/ui/api/nope", "status=404"} {
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
