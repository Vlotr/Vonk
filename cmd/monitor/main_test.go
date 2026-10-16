package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

func envFrom(vars map[string]string) func(string) string {
	return func(key string) string { return vars[key] }
}

func TestProjectCreate_PrintsKeys(t *testing.T) {
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   t.TempDir(),
	})
	var stdout, stderr bytes.Buffer

	code := run([]string{"project", "create", "--name", "Webshop"}, getenv, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("exit code %d, stderr: %s", code, stderr.String())
	}

	out := stdout.String()
	for _, pattern := range []string{
		`(?m)^ID:\s+1$`,
		`(?m)^Slug:\s+webshop$`,
		`(?m)^DSN:\s+https://[0-9a-f]{32}@monitor\.example\.com/1$`,
		`(?m)^PostHog token:\s+phc_[A-Za-z0-9_-]{32,}$`,
	} {
		if !regexp.MustCompile(pattern).MatchString(out) {
			t.Errorf("output does not match %s:\n%s", pattern, out)
		}
	}
}

func TestProjectCreate_RequiresName(t *testing.T) {
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   t.TempDir(),
	})
	var stdout, stderr bytes.Buffer

	if code := run([]string{"project", "create"}, getenv, &stdout, &stderr); code == 0 {
		t.Fatal("project create without --name: exit code 0, want non-zero")
	}
	if !strings.Contains(stderr.String(), "--name") {
		t.Errorf("stderr %q does not mention --name", stderr.String())
	}
}

func TestHealthcheck_ExitCodes(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer healthy.Close()

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer failing.Close()

	down := httptest.NewServer(http.NotFoundHandler())
	downAddr := down.Listener.Addr().String()
	down.Close()

	cases := []struct {
		name string
		addr string
		want int
	}{
		{"healthy", healthy.Listener.Addr().String(), 0},
		{"503", failing.Listener.Addr().String(), 1},
		{"nothing listening", downAddr, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			getenv := envFrom(map[string]string{"ADDR": tc.addr})

			if code := run([]string{"healthcheck"}, getenv, &stdout, &stderr); code != tc.want {
				t.Errorf("exit code %d, want %d (stderr %q)", code, tc.want, stderr.String())
			}
		})
	}
}

func TestHealthURL(t *testing.T) {
	cases := map[string]string{
		":8000":          "http://127.0.0.1:8000/health",
		"0.0.0.0:8000":   "http://127.0.0.1:8000/health",
		"[::]:9000":      "http://127.0.0.1:9000/health",
		"10.0.0.5:8000":  "http://10.0.0.5:8000/health",
		"[::1]:8000":     "http://[::1]:8000/health",
		"localhost:8080": "http://localhost:8080/health",
	}
	for addr, want := range cases {
		got, err := healthURL(addr)
		if err != nil {
			t.Errorf("healthURL(%q): %v", addr, err)
			continue
		}
		if got != want {
			t.Errorf("healthURL(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestRun_UnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"frobnicate"}, envFrom(nil), &stdout, &stderr); code != 2 {
		t.Errorf("exit code %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "Usage") {
		t.Errorf("stderr %q does not show usage", stderr.String())
	}
}
