package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/server"
	"monitor/internal/store"
)

func TestCLI_AdminResetPassword(t *testing.T) {
	vars := map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   t.TempDir(),
	}
	getenv := envFrom(vars)
	cfg, err := config.Load(getenv)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateFirstAdmin(ctx, "jan@example.com", "a forgotten password's hash", testNowCLI()); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	if code := run([]string{"admin", "reset-password", "--email", "JAN@example.com"}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr %s", code, stderr.String())
	}
	m := regexp.MustCompile(`(?m)^Link:\s+(https://monitor\.example\.com/reset-password#token=([A-Za-z0-9_-]{43}))$`).FindStringSubmatch(stdout.String())
	if m == nil {
		t.Fatalf("output has no reset link:\n%s", stdout.String())
	}
	if !strings.Contains(stdout.String(), "jan@example.com") || !regexp.MustCompile(`(?m)^Expires:\s+\S`).MatchString(stdout.String()) {
		t.Errorf("output does not name the account and the expiry:\n%s", stdout.String())
	}

	// The link works in the running server, once.
	h := server.New(server.Deps{Config: cfg, DB: db, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Queue: ingest.NewQueue(1), Limiter: ingest.NewRateLimiter()})
	post := func(path, body string) int {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	reset := fmt.Sprintf(`{"token":%q,"password":"my new admin password"}`, m[2])
	if code := post("/ui/api/password/reset", reset); code != http.StatusOK {
		t.Fatalf("use the link = %d", code)
	}
	if code := post("/ui/api/password/reset", reset); code != http.StatusGone {
		t.Errorf("use it again = %d, want 410", code)
	}
	if code := post("/ui/api/login", `{"email":"jan@example.com","password":"my new admin password"}`); code != http.StatusOK {
		t.Errorf("login with the new password = %d", code)
	}
}

func TestCLI_AdminResetPassword_Errors(t *testing.T) {
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   t.TempDir(),
	})
	tests := []struct {
		args []string
		code int
		says string
	}{
		{[]string{"admin"}, 2, "reset-password"},
		{[]string{"admin", "reset-password"}, 2, "--email"},
		{[]string{"admin", "reset-password", "--email", "a@example.com", "extra"}, 2, "extra"},
		{[]string{"admin", "reset-password", "--email", "nobody@example.com"}, 1, "nobody@example.com"},
	}
	for _, tt := range tests {
		var stdout, stderr bytes.Buffer
		code := run(tt.args, getenv, &stdout, &stderr)
		if code != tt.code || !strings.Contains(stderr.String(), tt.says) {
			t.Errorf("monitor %v: exit %d, stderr %q; want %d mentioning %q", tt.args, code, stderr.String(), tt.code, tt.says)
		}
		if strings.Contains(stdout.String(), "token=") {
			t.Errorf("monitor %v printed a link: %q", tt.args, stdout.String())
		}
	}
}

// testNowCLI is when the test's account was made; any time works.
func testNowCLI() time.Time {
	return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
}
