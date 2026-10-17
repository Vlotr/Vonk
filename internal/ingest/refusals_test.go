package ingest

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// During a database outage every request's project lookup fails: each
// gets 503 with Retry-After, and the log gets one ERROR line per 30
// seconds that counts the others, not one line per request.
func TestFailedLookups_LogOncePer30Seconds(t *testing.T) {
	var logs bytes.Buffer
	now := time.Date(2026, 10, 17, 9, 0, 0, 0, time.UTC)
	f := &failedLookups{logger: slog.New(slog.NewTextHandler(&logs, nil)), prefix: "sentry", now: func() time.Time { return now }}
	down := errors.New("dial tcp 10.0.0.5:5432: connect: connection refused")
	answer := func(err error) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		f.answer(rec, httptest.NewRequest(http.MethodPost, "/api/1/envelope/", nil), err)
		return rec
	}

	for range 5 {
		if rec := answer(down); rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != dbErrorRetryAfter {
			t.Fatalf("lookup failed = %d (Retry-After %q), want 503", rec.Code, rec.Header().Get("Retry-After"))
		}
		now = now.Add(time.Second)
	}
	if n := strings.Count(logs.String(), "level=ERROR"); n != 1 {
		t.Errorf("%d ERROR lines for 5 refused requests in 5 s, want 1:\n%s", n, logs.String())
	}

	now = now.Add(30 * time.Second)
	answer(down)
	lines := strings.Split(strings.TrimSpace(logs.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[1], "refused_since_last=4") {
		t.Errorf("after 30 s: %q, want a second line counting the 4 in between", lines)
	}

	// Unknown keys are the client's mistake: 401, nothing logged.
	logs.Reset()
	if rec := answer(errUnauthorized); rec.Code != http.StatusUnauthorized || logs.Len() != 0 {
		t.Errorf("unknown key = %d, logs %q", rec.Code, logs.String())
	}
}
