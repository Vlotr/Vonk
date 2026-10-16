package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"monitor/internal/store"
)

// olderDatabase writes a database that ran only the first migration (and
// so waits for the others) into dir.
func olderDatabase(t *testing.T, dir string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL, applied_at BIGINT NOT NULL);
		INSERT INTO schema_migrations VALUES (1, '0001_init.sql', 0)`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestStartup_AbortsWhenTheBackupFails(t *testing.T) {
	dir := t.TempDir()
	olderDatabase(t, dir)
	// backups/ cannot be made: a file has its name.
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   dir,
		"ADDR":       "127.0.0.1:0",
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"serve"}, getenv, &stdout, &stderr); code != 1 {
		t.Fatalf("exit code %d, want 1; stderr:\n%s", code, stderr.String())
	}
	out := stderr.String()
	if !strings.Contains(out, "the backup before the database update failed") || !strings.Contains(out, "the update did not run") {
		t.Errorf("stderr does not say the backup failed and the update did not run:\n%s", out)
	}

	// The update did not run: still at version 1.
	db, err := sql.Open("sqlite", filepath.Join(dir, "monitor.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n)
	if n != 1 {
		t.Errorf("%d migrations applied after a failed backup, want 1", n)
	}

	// project create aborts the same way.
	stderr.Reset()
	if code := run([]string{"project", "create", "--name", "Webshop"}, getenv, &stdout, &stderr); code != 1 ||
		!strings.Contains(stderr.String(), "the backup before the database update failed") {
		t.Errorf("project create = %d, stderr:\n%s", code, stderr.String())
	}
}

func TestStartup_BacksUpBeforeTheUpdate(t *testing.T) {
	dir := t.TempDir()
	olderDatabase(t, dir)
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   dir,
	})
	var stdout, stderr bytes.Buffer
	// Version 1 here has no tables, so the next migration fails; the
	// backup is made before that.
	run([]string{"project", "create", "--name", "Webshop"}, getenv, &stdout, &stderr)
	entries, _ := os.ReadDir(filepath.Join(dir, "backups"))
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "pre-migrate-0001-") {
		t.Errorf("backups = %v, want one pre-migrate-0001-… file; stderr:\n%s", entries, stderr.String())
	}
	if !strings.Contains(stderr.String(), "backed up the database") {
		t.Errorf("stderr does not name the backup:\n%s", stderr.String())
	}
}

// Old backups that cannot be removed are a warning: the command goes on,
// updates the database and does its work.
func TestStartup_WarnsAndContinuesWhenOldBackupsStay(t *testing.T) {
	dir := t.TempDir()
	backupBeforeMigrate = func(*store.DB, context.Context) (string, error) {
		return filepath.Join(dir, "backups", "pre-migrate-0004-20261016T120000Z.db"),
			fmt.Errorf("%w: remove pre-migrate-0001-20261001T120000Z.db: permission denied", store.ErrOldBackupsKept)
	}
	t.Cleanup(func() { backupBeforeMigrate = (*store.DB).BackupBeforeMigrate })
	getenv := envFrom(map[string]string{
		"SECRET_KEY": "a-long-random-secret-for-tests",
		"BASE_URL":   "https://monitor.example.com",
		"DATA_DIR":   dir,
	})
	var stdout, stderr bytes.Buffer
	if code := run([]string{"project", "create", "--name", "Webshop"}, getenv, &stdout, &stderr); code != 0 {
		t.Fatalf("project create = %d; stderr:\n%s", code, stderr.String())
	}
	if out := stderr.String(); !strings.Contains(out, "backed up the database to") ||
		!strings.Contains(out, "warning: old backups could not be removed: remove pre-migrate-0001") {
		t.Errorf("stderr does not name the backup and warn about the old ones:\n%s", out)
	}
	if !strings.Contains(stdout.String(), "Webshop") {
		t.Errorf("the project was not created after the warning:\n%s", stdout.String())
	}
}
