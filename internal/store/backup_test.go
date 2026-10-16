package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/config"
)

// openUnmigrated opens a fresh database in a temp directory without migrating it.
func openUnmigrated(t *testing.T) (*DB, string) {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dir
}

func backupFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "backups"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestBackup_OnlyWhenMigrationsPending(t *testing.T) {
	ctx := context.Background()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) < 2 {
		t.Fatal("this test needs at least two migrations")
	}
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// A fresh database has nothing to lose: no backup.
	db, dir := openUnmigrated(t)
	path, err := db.backupBeforeMigrate(ctx, files, now)
	if err != nil || path != "" {
		t.Fatalf("fresh database: backup %q, %v; want none", path, err)
	}
	if names := backupFiles(t, dir); len(names) != 0 {
		t.Fatalf("fresh database: backups %v", names)
	}

	// An older database with data, and a migration waiting: a backup of it
	// as it was, named after its version.
	older := files[:len(files)-1]
	if err := db.migrate(ctx, older); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject(ctx, NewProject{Name: "Webshop"}); err != nil {
		t.Fatal(err)
	}
	path, err = db.backupBeforeMigrate(ctx, files, now)
	if err != nil {
		t.Fatal(err)
	}
	wantName := fmt.Sprintf("pre-migrate-%04d-20261016T120000Z.db", older[len(older)-1].version)
	if filepath.Base(path) != wantName || filepath.Dir(path) != filepath.Join(dir, "backups") {
		t.Errorf("backup = %s, want backups/%s", path, wantName)
	}
	copyDB, err := sql.Open("sqlite", path+"?_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	var name string
	if err := copyDB.QueryRow("SELECT name FROM projects").Scan(&name); err != nil || name != "Webshop" {
		t.Errorf("the backup holds project %q, %v; want Webshop", name, err)
	}
	var version int
	copyDB.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&version)
	if version != older[len(older)-1].version {
		t.Errorf("the backup is at version %d, want %d (before the update)", version, older[len(older)-1].version)
	}

	// Up to date: nothing pending, no new backup.
	if err := db.migrate(ctx, files); err != nil {
		t.Fatal(err)
	}
	path, err = db.backupBeforeMigrate(ctx, files, now.Add(time.Hour))
	if err != nil || path != "" {
		t.Errorf("up to date: backup %q, %v; want none", path, err)
	}
	if names := backupFiles(t, dir); len(names) != 1 {
		t.Errorf("backups = %v, want the one from before the update", names)
	}
}

func TestBackup_KeepsFive(t *testing.T) {
	ctx := context.Background()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	db, dir := openUnmigrated(t)
	if err := db.migrate(ctx, files[:1]); err != nil {
		t.Fatal(err)
	}

	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	var made []string
	for i := range 7 {
		path, err := db.backupBeforeMigrate(ctx, files, start.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		made = append(made, filepath.Base(path))
	}
	got := backupFiles(t, dir)
	want := made[2:] // the five newest; ReadDir sorts by name, which sorts by time here
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("kept %v, want the five newest %v", got, want)
	}

	// A file that is not one of these backups is never touched.
	other := filepath.Join(dir, "backups", "keep-me.db")
	if err := os.WriteFile(other, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.backupBeforeMigrate(ctx, files, start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Errorf("another file in backups/ was removed: %v", err)
	}
	if n := len(backupFiles(t, dir)); n != 6 {
		t.Errorf("%d files in backups/, want 5 backups and keep-me.db", n)
	}
}

func TestBackup_FailsClearly(t *testing.T) {
	ctx := context.Background()
	files, err := migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	db, dir := openUnmigrated(t)
	if err := db.migrate(ctx, files[:1]); err != nil {
		t.Fatal(err)
	}
	// backups/ cannot be made: a file has its name.
	if err := os.WriteFile(filepath.Join(dir, "backups"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.backupBeforeMigrate(ctx, files, time.Now()); err == nil {
		t.Fatal("want an error when the backup cannot be written")
	}
}
