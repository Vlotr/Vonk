package health

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/store"
)

const gib = 1 << 30

func testChecker(t *testing.T, free, total uint64, diskErr error) *Checker {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Checker{
		DB:        db,
		DataDir:   dir,
		Queue:     ingest.NewQueue(10),
		Version:   "1.2.3",
		DiskUsage: func(string) (uint64, uint64, error) { return free, total, diskErr },
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

type healthBody struct {
	Status         string   `json:"status"`
	Version        string   `json:"version"`
	DB             string   `json:"db"`
	Disk           string   `json:"disk"`
	DiskFreeBytes  int64    `json:"diskFreeBytes"`
	DiskTotalBytes int64    `json:"diskTotalBytes"`
	QueueDepth     int      `json:"queueDepth"`
	Failing        []string `json:"failing"`
}

func get(t *testing.T, c *Checker) (int, healthBody) {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler(c).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	var body healthBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body %s: %v", rec.Body, err)
	}
	return rec.Code, body
}

func TestHealth_OK(t *testing.T) {
	c := testChecker(t, 50*gib, 100*gib, nil)
	c.Queue.TryEnqueue(ingest.Item{ProjectID: 1})
	code, body := get(t, c)
	if code != http.StatusOK || body.Status != "ok" || body.DB != "ok" || body.Disk != "ok" || len(body.Failing) != 0 {
		t.Errorf("healthy = %d %+v", code, body)
	}
	if body.Version != "1.2.3" || body.DiskFreeBytes != 50*gib || body.DiskTotalBytes != 100*gib || body.QueueDepth != 1 {
		t.Errorf("numbers = %+v", body)
	}
}

func TestHealth_503WhenDiskCheckFails(t *testing.T) {
	cases := []struct {
		name        string
		free, total uint64
		err         error
		disk        string
	}{
		{"under 10 percent free", 9 * gib, 100 * gib, nil, "low"},
		{"under 1 GiB free", gib - 1, 5 * gib, nil, "low"},
		{"cannot be read", 0, 0, errors.New("statfs: permission denied"), "error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, body := get(t, testChecker(t, tc.free, tc.total, tc.err))
			if code != http.StatusServiceUnavailable || body.Status != "error" || body.Disk != tc.disk || body.DB != "ok" {
				t.Errorf("= %d %+v, want 503 with disk %q", code, body, tc.disk)
			}
			if len(body.Failing) != 1 || body.Failing[0] != "disk" {
				t.Errorf("failing = %v, want [disk]", body.Failing)
			}
		})
	}
	// Exactly 10 percent and more than 1 GiB is fine.
	if code, _ := get(t, testChecker(t, 10*gib, 100*gib, nil)); code != http.StatusOK {
		t.Errorf("10 percent free = %d, want 200", code)
	}
}

func TestHealth_503WhenDatabaseFails(t *testing.T) {
	c := testChecker(t, 50*gib, 100*gib, nil)
	c.DB.Close()
	code, body := get(t, c)
	if code != http.StatusServiceUnavailable || body.DB != "error" || len(body.Failing) != 1 || body.Failing[0] != "db" {
		t.Errorf("= %d %+v, want 503 failing db", code, body)
	}
}
