package ingest

import (
	"net/http"
	"strings"
	"testing"
)

func TestAuth_Sources(t *testing.T) {
	env := newTestEnv(t, 100)
	p1 := env.project(t, "Webshop", 0)
	p2 := env.project(t, "Admin", 0)

	dsn := func(key string, id int64) string {
		return `,"dsn":"https://` + key + `@monitor.example.com/` + map[int64]string{p1.ID: "1", p2.ID: "2"}[id] + `"`
	}
	cases := []struct {
		name string
		path string
		body []byte
		auth func(*http.Request)
		want int
	}{
		{"X-Sentry-Auth header", envelopePath(p1), envelope(eventIDN(1), ""), authHeader(p1.SentryKey), http.StatusOK},
		{"sentry_key query", envelopePath(p1) + "?sentry_version=7&sentry_key=" + p1.SentryKey, envelope(eventIDN(2), ""), nil, http.StatusOK},
		{"dsn in the envelope header", envelopePath(p1), envelope(eventIDN(3), dsn(p1.SentryKey, p1.ID)), nil, http.StatusOK},
		{"header without the Sentry prefix", envelopePath(p1), envelope(eventIDN(4), ""), func(r *http.Request) {
			r.Header.Set("X-Sentry-Auth", "sentry_key="+p1.SentryKey+",sentry_version=7")
		}, http.StatusOK},
		{"wrong key in the header", envelopePath(p1), envelope(eventIDN(5), ""), authHeader("0123456789abcdef0123456789abcdef"), http.StatusUnauthorized},
		{"wrong key in the query", envelopePath(p1) + "?sentry_key=nope", envelope(eventIDN(6), ""), nil, http.StatusUnauthorized},
		{"wrong key in the dsn", envelopePath(p1), envelope(eventIDN(7), dsn("0123456789abcdef0123456789abcdef", p1.ID)), nil, http.StatusUnauthorized},
		{"no key at all", envelopePath(p1), envelope(eventIDN(8), ""), nil, http.StatusUnauthorized},
		{"key of project 2 on project 1", envelopePath(p1), envelope(eventIDN(9), ""), authHeader(p2.SentryKey), http.StatusUnauthorized},
		{"dsn of project 2 on project 1", envelopePath(p1), envelope(eventIDN(10), dsn(p2.SentryKey, p2.ID)), nil, http.StatusUnauthorized},
		{"project id that is not a number", "/api/webshop/envelope/", envelope(eventIDN(11), ""), authHeader(p1.SentryKey), http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var setup []func(*http.Request)
			if tc.auth != nil {
				setup = append(setup, tc.auth)
			}
			rec := env.post(tc.path, tc.body, setup...)
			if rec.Code != tc.want {
				t.Errorf("status = %d, want %d; body %s", rec.Code, tc.want, rec.Body)
			}
			if !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
				t.Errorf("Content-Type = %q, want JSON", rec.Header().Get("Content-Type"))
			}
		})
	}

	items := env.drain()
	if len(items) != 4 {
		t.Fatalf("queued %d items, want 4 (the accepted envelopes)", len(items))
	}
	for _, item := range items {
		if item.ProjectID != p1.ID {
			t.Errorf("item for project %d, want %d", item.ProjectID, p1.ID)
		}
	}
}

func TestSentryKeyFromAuthHeader(t *testing.T) {
	cases := map[string]string{
		"Sentry sentry_version=7, sentry_client=sentry.php/4.34.0, sentry_key=abc": "abc",
		"Sentry sentry_key=abc,sentry_version=7":                                   "abc",
		`sentry sentry_key="abc", sentry_secret=xyz`:                               "abc",
		"Sentry sentry_version=7":                                                  "",
		"Bearer abc":                                                               "",
	}
	for header, want := range cases {
		if got := authField(header, "sentry_key"); got != want {
			t.Errorf("authField(%q) = %q, want %q", header, got, want)
		}
	}
	if got := keyFromDSN("https://abc@monitor.example.com/1"); got != "abc" {
		t.Errorf("keyFromDSN = %q", got)
	}
	if got := keyFromDSN("not a url"); got != "" {
		t.Errorf("keyFromDSN(junk) = %q", got)
	}
}
