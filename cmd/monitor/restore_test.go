package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/store"
)

// testEnv returns the settings of a SQLite monitor in dir.
func testEnv(dir string) func(string) string {
	return envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   dir,
		"ADDR":       "127.0.0.1:0",
	})
}

// createProject runs "monitor project create" in dir.
func createProject(t *testing.T, dir, name string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"project", "create", "--name", name}, testEnv(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("project create = %d: %s", code, stderr.String())
	}
}

// backupOf writes a snapshot of the database in dir to a file and returns its path.
func backupOf(t *testing.T, dir string) string {
	t.Helper()
	cfg, err := config.Load(testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := db.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	path := filepath.Join(t.TempDir(), "monitor-backup.db")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, f); err != nil {
		t.Fatal(err)
	}
	return path
}

// projectsIn lists the project names of the database in dir.
func projectsIn(t *testing.T, path string) string {
	t.Helper()
	db, err := store.Open(context.Background(), config.Config{DataDir: filepath.Dir(path), SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	list, err := db.ListProjects(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range list {
		names = append(names, p.Name)
	}
	return strings.Join(names, ",")
}

// syncBuffer is a log the server writes from several goroutines.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// startAndStop runs the server in dir until it listens, then stops it,
// and returns its log.
func startAndStop(t *testing.T, dir string) string {
	t.Helper()
	cfg, err := config.Load(testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	var log syncBuffer
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- runServer(ctx, cfg, slog.New(slog.NewTextHandler(&log, nil))) }()
	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(log.String(), "msg=listening") {
		select {
		case err := <-done:
			t.Fatalf("the server stopped at its start: %v\n%s", err, log.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not start:\n%s", log.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	if err := <-done; err != nil {
		t.Fatalf("the server stopped with %v\n%s", err, log.String())
	}
	return log.String()
}

func TestRestore_SwapsAtStartup(t *testing.T) {
	other := t.TempDir()
	createProject(t, other, "Restored")
	backup := backupOf(t, other)

	dir := t.TempDir()
	createProject(t, dir, "Current")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"restore", backup}, testEnv(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("restore = %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, store.RestorePendingFile) || !strings.Contains(out, "Restart the server") {
		t.Errorf("restore says:\n%s", out)
	}
	// Staged, not swapped: the database is as it was until the start.
	if got := projectsIn(t, filepath.Join(dir, "monitor.db")); got != "Current" {
		t.Errorf("projects before the start = %q, want Current", got)
	}

	log := startAndStop(t, dir)
	if got := projectsIn(t, filepath.Join(dir, "monitor.db")); got != "Restored" {
		t.Errorf("projects after the start = %q, want Restored\n%s", got, log)
	}
	backups, _ := filepath.Glob(filepath.Join(dir, "backups", "pre-restore-*.db"))
	if len(backups) != 1 || projectsIn(t, backups[0]) != "Current" {
		t.Errorf("backups before the restore = %v, want one with Current", backups)
	}
	if !strings.Contains(log, "restored the database") || !strings.Contains(log, "pre-restore-") {
		t.Errorf("the log does not tell about the restore:\n%s", log)
	}
	if _, err := os.Stat(filepath.Join(dir, store.RestorePendingFile)); !os.IsNotExist(err) {
		t.Errorf("the pending restore is still there: %v", err)
	}

	// The next start has nothing to restore.
	if log := startAndStop(t, dir); strings.Contains(log, "restore") {
		t.Errorf("a second start mentions a restore:\n%s", log)
	}
}

func TestRestore_RefusedAtStartupKeepsTheDatabase(t *testing.T) {
	other := t.TempDir()
	createProject(t, other, "Restored")
	dir := t.TempDir()
	createProject(t, dir, "Current")
	var stdout, stderr bytes.Buffer
	if code := run([]string{"restore", backupOf(t, other)}, testEnv(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("restore = %d: %s", code, stderr.String())
	}
	if err := os.Truncate(filepath.Join(dir, store.RestorePendingFile), 6000); err != nil { // damaged after it was staged
		t.Fatal(err)
	}
	log := startAndStop(t, dir)
	if !strings.Contains(log, "level=ERROR") || !strings.Contains(log, "damaged") || !strings.Contains(log, "restore-rejected-") {
		t.Errorf("the log does not say the restore was refused:\n%s", log)
	}
	if got := projectsIn(t, filepath.Join(dir, "monitor.db")); got != "Current" {
		t.Errorf("projects = %q, want the database unchanged", got)
	}
}

func TestRestoreCommand_RefusesAndCancels(t *testing.T) {
	dir := t.TempDir()
	createProject(t, dir, "Current")
	junk := filepath.Join(t.TempDir(), "junk.db")
	if err := os.WriteFile(junk, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"restore", junk}, testEnv(dir), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "not a SQLite database") || !strings.Contains(stderr.String(), "Nothing was changed") {
		t.Errorf("restore of junk = %d, stderr:\n%s", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(dir, store.RestorePendingFile)); !os.IsNotExist(err) {
		t.Errorf("junk was staged: %v", err)
	}

	for _, args := range [][]string{{"restore"}, {"restore", "a.db", "b.db"}, {"restore", "--cancel", "a.db"}} {
		stderr.Reset()
		if code := run(args, testEnv(dir), &stdout, &stderr); code != 2 || !strings.Contains(stderr.String(), "monitor restore") {
			t.Errorf("%v = %d, stderr:\n%s", args, code, stderr.String())
		}
	}

	stdout.Reset()
	if code := run([]string{"restore", "--cancel"}, testEnv(dir), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "No restore was waiting") {
		t.Errorf("cancel without one = %d: %s", code, stdout.String())
	}
	if code := run([]string{"restore", backupOf(t, dir)}, testEnv(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("restore = %d: %s", code, stderr.String())
	}
	stdout.Reset()
	if code := run([]string{"restore", "--cancel"}, testEnv(dir), &stdout, &stderr); code != 0 || !strings.Contains(stdout.String(), "cancelled") {
		t.Errorf("cancel = %d: %s", code, stdout.String())
	}
	if _, err := os.Stat(filepath.Join(dir, store.RestorePendingFile)); !os.IsNotExist(err) {
		t.Errorf("still pending after cancel: %v", err)
	}
}

func TestRestoreCommand_RefusesPostgres(t *testing.T) {
	getenv := envFrom(map[string]string{
		"SECRET_KEY":   "a-long-random-secret-for-tests",
		"BASE_URL":     "https://monitor.example.com",
		"DATA_DIR":     t.TempDir(),
		"DATABASE_URL": "postgres://monitor@127.0.0.1:1/monitor",
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"restore", "backup.db"}, getenv, &stdout, &stderr); code != 1 || !strings.Contains(stderr.String(), "pg_restore") {
		t.Errorf("restore on Postgres = %d, stderr:\n%s", code, stderr.String())
	}
}

// markNewer makes the database in dir look updated by a newer version of
// the tool: a migration this binary does not have.
func markNewer(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, '9999_newer.sql', 0)"); err != nil {
		t.Fatal(err)
	}
}

// A database a newer version updated stops every command and the server
// with what to do (ruling 48); going back as docs/updating.md says (stage
// the older backup, then start this version) still works, because the
// restore is applied before the database is opened.
func TestStart_RefusesADatabaseANewerVersionUpdated(t *testing.T) {
	older := t.TempDir()
	createProject(t, older, "Before the update")
	backup := backupOf(t, older)

	dir := t.TempDir()
	createProject(t, dir, "After the update")
	markNewer(t, dir)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"project", "create", "--name", "More"}, testEnv(dir), &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "a newer version of the tool updated this database") ||
		!strings.Contains(stderr.String(), "version 9999") || !strings.Contains(stderr.String(), "monitor restore") {
		t.Errorf("project create on a newer database = %d:\n%s", code, stderr.String())
	}
	cfg, err := config.Load(testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	var log syncBuffer
	// A server that starts anyway stops after 5 s, and the test fails instead of hanging.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := runServer(ctx, cfg, slog.New(slog.NewTextHandler(&log, nil))); !errors.Is(err, store.ErrNewerDatabase) {
		t.Errorf("serve on a newer database = %v, want ErrNewerDatabase\n%s", err, log.String())
	}

	stderr.Reset()
	if code := run([]string{"restore", backup}, testEnv(dir), &stdout, &stderr); code != 0 {
		t.Fatalf("restore = %d: %s", code, stderr.String())
	}
	log2 := startAndStop(t, dir)
	if got := projectsIn(t, filepath.Join(dir, "monitor.db")); got != "Before the update" {
		t.Errorf("projects after going back = %q, want the backup's\n%s", got, log2)
	}
}
