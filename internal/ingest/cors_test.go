package ingest

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnvelopeHandler_CORS(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	req := httptest.NewRequest(http.MethodOptions, envelopePath(p)+"?sentry_key="+p.SentryKey, nil)
	req.Header.Set("Origin", "https://shop.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,sentry-trace,baggage")
	rec := httptest.NewRecorder()
	env.mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("preflight status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	allowed := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers"))
	for _, h := range []string{"content-type", "x-sentry-auth", "sentry-trace", "baggage"} {
		if !strings.Contains(allowed, h) {
			t.Errorf("Access-Control-Allow-Headers %q lacks %s", allowed, h)
		}
	}
	if !strings.Contains(rec.Header().Get("Access-Control-Allow-Methods"), "POST") {
		t.Errorf("Access-Control-Allow-Methods = %q", rec.Header().Get("Access-Control-Allow-Methods"))
	}

	// Answers carry the header too, errors included, so a browser SDK can read them.
	for name, setup := range map[string]func(*http.Request){
		"accepted": authHeader(p.SentryKey),
		"refused":  authHeader("nope"),
	} {
		rec := env.post(envelopePath(p), envelope(eventIDN(1), ""), setup, func(r *http.Request) {
			r.Header.Set("Origin", "https://shop.example.com")
		})
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "*" {
			t.Errorf("%s (%d): Access-Control-Allow-Origin = %q, want *", name, rec.Code, got)
		}
		if exposed := strings.ToLower(rec.Header().Get("Access-Control-Expose-Headers")); !strings.Contains(exposed, "x-sentry-rate-limits") || !strings.Contains(exposed, "retry-after") {
			t.Errorf("%s: Access-Control-Expose-Headers = %q", name, exposed)
		}
	}

	// Other methods are refused, still as JSON.
	rec = httptest.NewRecorder()
	env.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, envelopePath(p), nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET status = %d, want 405", rec.Code)
	}
}
