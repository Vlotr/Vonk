package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/posthog"
	"monitor/internal/store"
)

const posthogFixtureToken = "phc_fixture0capture0token0for0tests0only0000000"

// posthogFixture is one request recorded from posthog-js 1.438 (see
// internal/posthog/testdata/capture), with the project's token in place
// of the recording's.
type posthogFixture struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Query   string              `json:"query"`
	Headers map[string][]string `json:"headers"`
	body    []byte
}

func loadPosthogFixture(t *testing.T, name, token string) posthogFixture {
	t.Helper()
	dir := filepath.Join("..", "posthog", "testdata")
	meta, err := os.ReadFile(filepath.Join(dir, name+".request.json"))
	if err != nil {
		t.Fatal(err)
	}
	var f posthogFixture
	if err := json.Unmarshal(meta, &f); err != nil {
		t.Fatal(err)
	}
	f.Path = strings.ReplaceAll(f.Path, posthogFixtureToken, token)
	body, err := os.ReadFile(filepath.Join(dir, name+".body"))
	if os.IsNotExist(err) {
		return f
	}
	if err != nil {
		t.Fatal(err)
	}
	// The bodies are gzip, as posthog-js sent them: swap the token inside.
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	plain, _ := io.ReadAll(zr)
	f.body = gzipBytes(t, bytes.ReplaceAll(plain, []byte(posthogFixtureToken), []byte(token)))
	return f
}

// sentAt returns the sent_at of the fixture's body.
func (f posthogFixture) sentAt(t *testing.T) time.Time {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(f.body))
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		SentAt time.Time `json:"sent_at"`
	}
	if err := json.NewDecoder(zr).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body.SentAt
}

// send replays a fixture as the browser sent it.
func (e *testEnv) send(f posthogFixture, setup ...func(*http.Request)) *httptest.ResponseRecorder {
	target := f.Path
	if f.Query != "" {
		target += "?" + f.Query
	}
	req := httptest.NewRequest(f.Method, target, bytes.NewReader(f.body))
	for name, values := range f.Headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	for _, s := range setup {
		s(req)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// capture posts a JSON body to a PostHog capture endpoint.
func (e *testEnv) capture(path, body string, setup ...func(*http.Request)) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36")
	for _, s := range setup {
		s(req)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

func TestPosthog_Fixtures(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)

	// Loading the page: the remote config as a script, the surveys
	// script it asks for, and the first page view (sent to /e/ before the
	// config named /i/v0/e/).
	rec := env.send(loadPosthogFixture(t, "01-config-js", p.PosthogToken))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "window._POSTHOG_REMOTE_CONFIG[\""+p.PosthogToken+"\"]") ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/javascript") {
		t.Errorf("config.js = %d %s %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	if rec := env.send(loadPosthogFixture(t, "03-surveys-js", p.PosthogToken)); rec.Code != http.StatusOK ||
		!strings.HasPrefix(rec.Header().Get("Content-Type"), "application/javascript") {
		t.Errorf("surveys.js = %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	for _, name := range []string{"02-pageview-load", "04-pageview-history", "05-identify", "06-flags", "07-custom", "08-pageleave"} {
		f := loadPosthogFixture(t, name, p.PosthogToken)
		env.clock.Set(f.sentAt(t)) // received when it was sent, as the browser's requests were
		rec := env.send(f)
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: no CORS header", name)
		}
	}

	// Two page views and the leave reach the queue; identify and the
	// custom event are accepted and left for later (Task 9).
	items := env.drain()
	var names []string
	for _, it := range items {
		if it.ProjectID != p.ID || it.Page == nil {
			t.Fatalf("item = %+v", it)
		}
		names = append(names, it.Page.Name)
	}
	if strings.Join(names, " ") != "$pageview $pageview $pageleave" {
		t.Fatalf("queued %v", names)
	}
	if items[0].Route != "/products/blue-shoes" || items[0].Path != "/products/blue-shoes" || items[1].Route != "/checkout" || items[0].IsBot {
		t.Errorf("routes = %q %q, path %q, bot %v", items[0].Route, items[1].Route, items[0].Path, items[0].IsBot)
	}
	if strings.Contains(fmt.Sprint(items[0].Page.Properties), "tok_abc123") {
		t.Error("the token in the page's query string was not filtered")
	}

	// Through the writer: the first page view lasted until the second (a
	// page change in the app), the second until the leave (12.025 s), the
	// same times posthog-js reports itself.
	for _, it := range items {
		env.queue.TryEnqueue(it)
	}
	NewWriter(env.db, env.queue, &recordingHooks{}, env.logger).Close()
	views := pageviewRows(t, env, p)
	if len(views) != 2 {
		t.Fatalf("page views = %+v", views)
	}
	// posthog-js reports the time on the page it left as $prev_pageview_duration (seconds).
	reported := func(it Item) int64 {
		f, _ := it.Page.Properties["$prev_pageview_duration"].(json.Number).Float64()
		return int64(math.Round(f * 1000))
	}
	if views[0].route != "/products/blue-shoes" || views[0].durationMs != reported(items[1]) || views[0].left {
		t.Errorf("first page view = %+v, want %d ms on /products/blue-shoes", views[0], reported(items[1]))
	}
	if views[1].route != "/checkout" || views[1].durationMs != 12025 || reported(items[2]) != 12025 || views[1].left {
		t.Errorf("second page view = %+v, want 12025 ms on /checkout", views[1])
	}
	// Both came before identify: the anonymous id posthog-js made.
	if views[0].user != "01a145eb-0946-74da-93bd-343eec6f4faa" || views[1].user != views[0].user {
		t.Errorf("users = %q, %q; want the anonymous distinct_id", views[0].user, views[1].user)
	}
}

type pageviewRow struct {
	route, path, user string
	durationMs        int64
	left, bot         bool
}

func pageviewRows(t *testing.T, env *testEnv, p store.Project) []pageviewRow {
	t.Helper()
	db, err := sql.Open("sqlite", env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT route, path, user_id, COALESCE(duration_ms, -1), left_without_pageleave, is_bot
		FROM pageviews WHERE project_id = ? ORDER BY start, id`, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []pageviewRow
	for rows.Next() {
		var r pageviewRow
		if err := rows.Scan(&r.route, &r.path, &r.user, &r.durationMs, &r.left, &r.bot); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

func TestPosthog_UnknownToken401(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	cases := map[string]string{
		"unknown api_key":   `{"api_key":"phc_unknown","batch":[{"event":"$pageview","properties":{}}]}`,
		"unknown token":     `{"token":"phc_unknown","event":"$pageview","properties":{}}`,
		"unknown in event":  `[{"event":"$pageview","properties":{"token":"phc_unknown"}}]`,
		"no token":          `{"batch":[{"event":"$pageview","properties":{}}]}`,
		"one of two events": `{"api_key":"` + p.PosthogToken + `","batch":[{"event":"$pageview"},{"event":"$pageview","properties":{"token":"phc_unknown"}}]}`,
	}
	for name, body := range cases {
		rec := env.capture("/i/v0/e/", body)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s, want 401", name, rec.Code, rec.Body)
		}
		if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Errorf("%s: the 401 lacks CORS, so the browser cannot read it", name)
		}
	}
	if items := env.drain(); len(items) != 0 {
		t.Errorf("queued %d items for unknown tokens", len(items))
	}
	// Tokens are accepted whether or not the project shows page views.
	if p.TracksPageviews {
		t.Fatal("test project tracks page views")
	}
	if rec := env.capture("/i/v0/e/", `{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview","properties":{"$current_url":"https://shop.example.com/"}}]}`); rec.Code != http.StatusOK {
		t.Errorf("known token: %d %s", rec.Code, rec.Body)
	}
}

func TestPosthog_BatchMixedTokens(t *testing.T) {
	env := newTestEnv(t, 100)
	shop := env.project(t, "Webshop", 1000)
	portal := env.project(t, "Portal", 1000)
	// A batch names one project; an event with its own token belongs to that project.
	body := `{"api_key":"` + shop.PosthogToken + `","batch":[
		{"event":"$pageview","uuid":"a","properties":{"$current_url":"https://shop.example.com/cart"}},
		{"event":"$pageview","uuid":"b","properties":{"token":"` + portal.PosthogToken + `","$current_url":"https://portal.example.com/orders/12"}},
		{"event":"$pageview","uuid":"c","properties":{"token":"` + shop.PosthogToken + `","$current_url":"https://shop.example.com/"}}]}`
	if rec := env.capture("/batch/", body); rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	got := map[string]int64{}
	for _, it := range env.drain() {
		got[it.Page.UUID+" "+it.Route] = it.ProjectID
	}
	want := map[string]int64{"a /cart": shop.ID, "b /orders/:id": portal.ID, "c /": shop.ID}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("queued %v, want %v", got, want)
	}
}

func TestPosthog_Endpoints(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	get := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		rec := httptest.NewRecorder()
		env.mux.ServeHTTP(rec, req)
		return rec
	}
	// Flags, with GET and POST, with and without the slash.
	for _, target := range []string{"/flags/?v=2", "/flags?v=2"} {
		for _, method := range []string{"GET", "POST"} {
			rec := get(method, target)
			var flags posthog.FlagsAnswer
			if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &flags) != nil || flags.Flags == nil || flags.RequestID == "" {
				t.Errorf("%s %s = %d %s", method, target, rec.Code, rec.Body)
			}
		}
	}
	if rec := get("POST", "/decide/?v=4"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sessionRecording":false`) {
		t.Errorf("decide = %d %s", rec.Code, rec.Body)
	}
	if rec := get("GET", "/array/"+p.PosthogToken+"/config"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"supportedCompression":["gzip","gzip-js"]`) {
		t.Errorf("config = %d %s", rec.Code, rec.Body)
	}
	if rec := get("GET", "/array/bad%22token/config.js"); rec.Code != http.StatusNotFound {
		t.Errorf("config.js for a malformed token = %d", rec.Code)
	}
	// Session recordings are accepted and dropped.
	if rec := env.capture("/s/", `{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$snapshot"}]}`); rec.Code != http.StatusOK {
		t.Errorf("/s/ = %d", rec.Code)
	}
	// Unknown paths under the PostHog prefixes get 200 and a debug line.
	if rec := get("POST", "/i/v1/logs"); rec.Code != http.StatusOK {
		t.Errorf("unknown path = %d", rec.Code)
	}
	if !strings.Contains(env.logs.String(), "posthog: unknown path") || !strings.Contains(env.logs.String(), "level=DEBUG") {
		t.Errorf("unknown path not logged at debug:\n%s", env.logs.String())
	}
	// The preflight allows what posthog-js sends.
	req := httptest.NewRequest(http.MethodOptions, "/i/v0/e/", nil)
	req.Header.Set("Origin", "https://shop.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type, x-custom")
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)
	h := rec.Header()
	if rec.Code != http.StatusOK || h.Get("Access-Control-Allow-Origin") != "*" || !strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.Contains(h.Get("Access-Control-Allow-Headers"), "x-custom") {
		t.Errorf("preflight = %d %v", rec.Code, h)
	}
	// Other methods get 405.
	if rec := get("DELETE", "/batch/"); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d", rec.Code)
	}
	// A GET pixel with ?data=.
	data := `{"event":"$pageview","properties":{"token":"` + p.PosthogToken + `","$current_url":"https://shop.example.com/pixel"}}`
	if rec := get("GET", "/e/?ip=0&data="+url.QueryEscape(data)); rec.Code != http.StatusOK {
		t.Errorf("GET pixel = %d %s", rec.Code, rec.Body)
	}
	if items := env.drain(); len(items) != 1 || items[0].Route != "/pixel" {
		t.Errorf("GET pixel queued %+v", items)
	}
}

func TestPosthog_BadBodies(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	cases := map[string]func(*http.Request){
		"brotli": func(r *http.Request) { r.Header.Set("Content-Encoding", "br") },
		"lz64":   func(r *http.Request) { r.URL.RawQuery = "compression=lz64" },
	}
	for name, setup := range cases {
		if rec := env.capture("/i/v0/e/", `{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview"}]}`, setup); rec.Code != http.StatusBadRequest {
			t.Errorf("%s = %d", name, rec.Code)
		}
	}
	for _, body := range []string{"", "not json", `{"api_key":"x"`, `{"batch":"x"}`} {
		if rec := env.capture("/batch/", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%q = %d %s", body, rec.Code, rec.Body)
		}
	}
	// Content-Encoding gzip, as server SDKs send it.
	body := gzipBytes(t, []byte(`{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview","properties":{"$current_url":"/"}}]}`))
	if rec := env.capture("/batch/", string(body), func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }); rec.Code != http.StatusOK {
		t.Errorf("Content-Encoding gzip = %d %s", rec.Code, rec.Body)
	}
	// Too large after decompression.
	huge := gzipBytes(t, append([]byte(`{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview","properties":{"x":"`), bytes.Repeat([]byte("a"), MaxBodyBytes+10)...))
	if rec := env.capture("/batch/", string(huge)); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("huge body = %d", rec.Code)
	}
}

func TestPosthog_RateLimitAndFullQueue(t *testing.T) {
	env := newTestEnv(t, 3)
	p := env.project(t, "Webshop", 4)
	batch := func(n int) string {
		events := make([]string, n)
		for i := range events {
			events[i] = `{"event":"$pageview","properties":{"$current_url":"/"}}`
		}
		return `{"api_key":"` + p.PosthogToken + `","batch":[` + strings.Join(events, ",") + `]}`
	}
	if rec := env.capture("/batch/", batch(3)); rec.Code != http.StatusOK {
		t.Fatalf("first batch = %d", rec.Code)
	}
	// The minute's budget of 4 has room for one more, not two.
	rec := env.capture("/batch/", batch(2))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "50" {
		t.Errorf("over the limit = %d, Retry-After %q; want 429 and 50 s to the next minute", rec.Code, rec.Header().Get("Retry-After"))
	}
	// The queue holds 3 and is full: 429 with Retry-After 5.
	rec = env.capture("/batch/", batch(1))
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("full queue = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestPosthog_BotsMarked(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	body := `{"api_key":"` + p.PosthogToken + `","batch":[{"event":"$pageview","properties":{"$current_url":"/"}}]}`
	env.capture("/i/v0/e/", body, func(r *http.Request) {
		r.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)")
	})
	// posthog-js told to send bots marks them itself.
	env.capture("/i/v0/e/", `{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview","properties":{"$browser_type":"bot"}}]}`)
	env.capture("/i/v0/e/", body)
	items := env.drain()
	if len(items) != 3 || !items[0].IsBot || !items[1].IsBot || items[2].IsBot {
		t.Errorf("bots = %v %v %v", items[0].IsBot, items[1].IsBot, items[2].IsBot)
	}
}

// The writer stores page views next to error events, and the route of an
// error event comes from its request URL.
func TestWriter_PageEventsAndErrorRoutes(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 1000)
	if _, err := env.db.SetRoutePatterns(context.Background(), p.ID, []string{"/checkout/:step"}); err != nil {
		t.Fatal(err)
	}
	p, _ = env.db.ProjectByID(context.Background(), p.ID)

	// The envelope's event has request.url https://shop.example.com/checkout?token=abc.
	if rec := env.post(envelopePath(p), envelope(eventIDN(1), ""), authHeader(p.SentryKey)); rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	env.capture("/i/v0/e/", `{"api_key":"`+p.PosthogToken+`","batch":[{"event":"$pageview","uuid":"v1","timestamp":"2026-10-16T12:00:00Z",
		"properties":{"$current_url":"https://shop.example.com/checkout/payment?token=abc","$session_id":"s","$window_id":"w","distinct_id":"42"}}]}`)
	items := env.drain()
	if len(items) != 2 || items[0].Route != "/checkout" || items[1].Route != "/checkout/:step" {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		env.queue.TryEnqueue(it)
	}
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	w.Close()
	ev, err := env.db.ErrorEventByEventID(context.Background(), p.ID, eventIDN(1))
	if err != nil || ev.Route != "/checkout" {
		t.Errorf("error event route = %q, %v", ev.Route, err)
	}
	views := pageviewRows(t, env, p)
	if len(views) != 1 || views[0].path != "/checkout/payment" || views[0].route != "/checkout/:step" || views[0].user != "42" {
		t.Errorf("page views = %+v", views)
	}
	if got := w.StoredLastHour(); got != 2 {
		t.Errorf("stored in the last hour = %d, want 2 (the error and the page view)", got)
	}
	dirty, _ := env.db.DirtyHours(context.Background())
	if len(dirty) != 1 || !dirty[0].Hour.Equal(time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("dirty hours = %v, want 12:00 once", dirty)
	}
}
