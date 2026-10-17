package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/config"
)

// sqliteConfig is the configuration of a SQLite database in dir.
func sqliteConfig(dir string) config.Config {
	return config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")}
}

// makeDatabase writes a migrated SQLite database with one project to
// <dir>/monitor.db and closes it.
func makeDatabase(t *testing.T, dir, project string) {
	t.Helper()
	ctx := context.Background()
	db, err := Open(ctx, sqliteConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateProject(ctx, NewProject{Name: project}); err != nil {
		t.Fatal(err)
	}
}

// makeBackup returns the path of a backup (a snapshot) of a database with one project.
func makeBackup(t *testing.T, project string) string {
	t.Helper()
	dir := t.TempDir()
	makeDatabase(t, dir, project)
	db, err := Open(context.Background(), sqliteConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f, err := db.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	path := filepath.Join(t.TempDir(), "backup.db")
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, f); err != nil {
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

// projectNames reads the project names of the SQLite file at path.
func projectNames(t *testing.T, path string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path+"?_query_only=1")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	names, err := queryStrings(context.Background(), db, "SELECT name FROM projects ORDER BY id")
	if err != nil {
		t.Fatalf("read projects of %s: %v", path, err)
	}
	return strings.Join(names, ",")
}

// changed copies the SQLite file at path and runs sql on the copy.
func changed(t *testing.T, path, statements string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "changed.db")
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", out)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(statements); err != nil {
		t.Fatal(err)
	}
	return out
}

// dataDirFiles lists the names in dir, without subdirectories.
func dataDirFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

func TestSnapshot_IsACompleteCopyAndLeavesNoFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	makeDatabase(t, dir, "Webshop")
	db, err := Open(ctx, sqliteConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// A change still in the write-ahead log is in the copy.
	if _, err := db.CreateProject(ctx, NewProject{Name: "Portal"}); err != nil {
		t.Fatal(err)
	}

	f, err := db.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		t.Fatalf("the snapshot does not start with the SQLite header: %q", data[:min(16, len(data))])
	}
	path := filepath.Join(t.TempDir(), "copy.db")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if got := projectNames(t, path); got != "Webshop,Portal" {
		t.Errorf("projects in the snapshot = %q, want Webshop,Portal", got)
	}
	if _, err := CheckBackupFile(ctx, path); err != nil {
		t.Errorf("the snapshot is no valid backup: %v", err)
	}
	for _, name := range dataDirFiles(t, dir) {
		if strings.HasPrefix(name, ".tmp-") {
			t.Errorf("the snapshot left %s in DATA_DIR", name)
		}
	}
}

func TestRestore_RejectsCorruptOrNewerSchema(t *testing.T) {
	ctx := context.Background()
	good := makeBackup(t, "Restored")
	data, err := os.ReadFile(good)
	if err != nil {
		t.Fatal(err)
	}
	// Every page after the first gets a garbled page header.
	damaged := append([]byte(nil), data...)
	for page := 4096; page+8 < len(damaged); page += 4096 {
		copy(damaged[page+1:page+8], bytes.Repeat([]byte{0xff}, 7))
	}
	other := filepath.Join(t.TempDir(), "other.db")
	otherDB, err := sql.Open("sqlite", other)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherDB.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT)"); err != nil {
		t.Fatal(err)
	}
	otherDB.Close()
	file := func(path string) []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	cases := []struct {
		name string
		body []byte
		says string
	}{
		{"not SQLite", []byte("PK\x03\x04 this is a zip file, not a database"), "not a SQLite database"},
		{"empty", nil, "not a SQLite database"},
		{"damaged", damaged, "damaged"},
		{"cut short", data[:len(data)/2], "damaged"},
		{"another program's database", file(other), "not a backup of this tool"},
		{"newer schema", file(changed(t, good, "INSERT INTO schema_migrations VALUES (9999, '9999_future.sql', 0)")), "newer version"},
		{"another schema", file(changed(t, good, "UPDATE schema_migrations SET name = '0001_other.sql' WHERE version = 1")), "not a backup of this tool"},
		{"triggers", file(changed(t, good, "CREATE TRIGGER t AFTER INSERT ON projects BEGIN DELETE FROM users; END")), "not a backup of this tool"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			_, err := StageRestore(ctx, dir, bytes.NewReader(tc.body))
			var refused *BackupError
			if !errors.As(err, &refused) {
				t.Fatalf("StageRestore = %v, want a BackupError", err)
			}
			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("error %q does not say %q", err, tc.says)
			}
			if names := dataDirFiles(t, dir); len(names) != 0 {
				t.Errorf("a refused restore left %v in DATA_DIR", names)
			}
			if p, err := PendingRestoreIn(dir); p != nil || err != nil {
				t.Errorf("pending restore after a refusal = %+v, %v", p, err)
			}
		})
	}

	// A good one is staged as restore-pending.db, and can be cancelled.
	dir := t.TempDir()
	staged, err := StageRestore(ctx, dir, bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if names := dataDirFiles(t, dir); strings.Join(names, ",") != RestorePendingFile {
		t.Errorf("DATA_DIR holds %v, want only %s", names, RestorePendingFile)
	}
	pending, err := PendingRestoreIn(dir)
	if err != nil || pending == nil || pending.SizeBytes != int64(len(data)) || staged.SizeBytes != pending.SizeBytes {
		t.Errorf("pending = %+v, %v; staged %+v; want %d bytes", pending, err, staged, len(data))
	}
	if got := projectNames(t, filepath.Join(dir, RestorePendingFile)); got != "Restored" {
		t.Errorf("staged projects = %q", got)
	}
	if removed, err := CancelRestore(dir); err != nil || !removed {
		t.Errorf("CancelRestore = %v, %v", removed, err)
	}
	if p, _ := PendingRestoreIn(dir); p != nil {
		t.Errorf("still pending after cancelling: %+v", p)
	}
}

// failingReader fails after a few bytes, as a cut-off upload does.
type failingReader struct{ n int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.n <= 0 {
		return 0, io.ErrUnexpectedEOF
	}
	r.n--
	p[0] = 'S'
	return 1, nil
}

func TestStageRestore_ReadErrorLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	_, err := StageRestore(context.Background(), dir, &failingReader{n: 10})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("err = %v, want the reader's error", err)
	}
	if names := dataDirFiles(t, dir); len(names) != 0 {
		t.Errorf("a failed upload left %v", names)
	}
}

func TestApplyPendingRestore_SwapsAndKeepsTheOldDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := sqliteConfig(dir)
	now := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)

	// Nothing pending: nothing happens.
	if res, err := ApplyPendingRestore(ctx, cfg, now); err != nil || res != (RestoreResult{}) {
		t.Fatalf("nothing pending: %+v, %v", res, err)
	}

	makeDatabase(t, dir, "Current")
	backup, err := os.Open(makeBackup(t, "Restored"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if _, err := StageRestore(ctx, dir, backup); err != nil {
		t.Fatal(err)
	}

	res, err := ApplyPendingRestore(ctx, cfg, now)
	if err != nil {
		t.Fatal(err)
	}
	files := mustMigrations(t)
	latest := files[len(files)-1].version
	wantBackup := filepath.Join(dir, "backups", fmt.Sprintf("pre-restore-%04d-20261017T093000Z.db", latest))
	if !res.Restored || res.Backup != wantBackup || res.Version != latest {
		t.Errorf("result = %+v, want restored at version %d with the old database in %s", res, latest, wantBackup)
	}
	if got := projectNames(t, cfg.SQLitePath); got != "Restored" {
		t.Errorf("the database holds %q after the restore, want Restored", got)
	}
	if got := projectNames(t, res.Backup); got != "Current" {
		t.Errorf("the backup holds %q, want the database from before (Current)", got)
	}
	for _, name := range dataDirFiles(t, dir) {
		if name != "monitor.db" {
			t.Errorf("left in DATA_DIR after the restore: %s", name)
		}
	}
	// It opens and migrates as the tool's database.
	db, err := Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	list, err := db.Backups()
	if err != nil || len(list) != 1 || list[0].Kind != BackupBeforeRestore || list[0].Version != latest {
		t.Errorf("Backups() = %+v, %v; want the one from before the restore", list, err)
	}
}

func TestApplyPendingRestore_OnANewDataDir(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	backup, err := os.Open(makeBackup(t, "Restored"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if _, err := StageRestore(ctx, dir, backup); err != nil {
		t.Fatal(err)
	}
	res, err := ApplyPendingRestore(ctx, sqliteConfig(dir), time.Now())
	if err != nil || !res.Restored || res.Backup != "" {
		t.Fatalf("result = %+v, %v; want restored without a backup (there was no database)", res, err)
	}
	if got := projectNames(t, filepath.Join(dir, "monitor.db")); got != "Restored" {
		t.Errorf("projects = %q", got)
	}
}

func TestApplyPendingRestore_RefusesADamagedFileAndKeepsTheDatabase(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := sqliteConfig(dir)
	makeDatabase(t, dir, "Current")
	backup, err := os.Open(makeBackup(t, "Restored"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if _, err := StageRestore(ctx, dir, backup); err != nil {
		t.Fatal(err)
	}
	// The staged file is damaged afterwards (a disk error).
	pending := filepath.Join(dir, RestorePendingFile)
	if err := os.Truncate(pending, 4096+512); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 10, 17, 9, 30, 0, 0, time.UTC)
	res, err := ApplyPendingRestore(ctx, cfg, now)
	if !errors.Is(err, ErrRestoreRefused) || !strings.Contains(err.Error(), "damaged") {
		t.Fatalf("err = %v, want ErrRestoreRefused saying it is damaged", err)
	}
	if res.Restored || res.Rejected != filepath.Join(dir, "restore-rejected-20261017T093000Z.db") {
		t.Errorf("result = %+v", res)
	}
	if got := projectNames(t, cfg.SQLitePath); got != "Current" {
		t.Errorf("the database holds %q, want it unchanged", got)
	}
	if _, err := os.Stat(pending); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refused file is still pending: %v", err)
	}
	rejected, err := RejectedRestores(dir)
	if err != nil || len(rejected) != 1 || rejected[0].Name != "restore-rejected-20261017T093000Z.db" {
		t.Errorf("RejectedRestores = %+v, %v", rejected, err)
	}
	// Cancelling clears the refused file too.
	if removed, err := CancelRestore(dir); err != nil || !removed {
		t.Errorf("CancelRestore = %v, %v", removed, err)
	}
	if rejected, _ := RejectedRestores(dir); len(rejected) != 0 {
		t.Errorf("refused files after cancelling: %+v", rejected)
	}
}

func TestApplyPendingRestore_PostgresLeavesItAlone(t *testing.T) {
	dir := t.TempDir()
	backup, err := os.Open(makeBackup(t, "Restored"))
	if err != nil {
		t.Fatal(err)
	}
	defer backup.Close()
	if _, err := StageRestore(context.Background(), dir, backup); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataDir: dir, DatabaseURL: "postgres://monitor@127.0.0.1:1/monitor"}
	res, err := ApplyPendingRestore(context.Background(), cfg, time.Now())
	if !errors.Is(err, ErrRestoreOnPostgres) || res.Restored {
		t.Errorf("Postgres: %+v, %v; want ErrRestoreOnPostgres", res, err)
	}
	if p, _ := PendingRestoreIn(dir); p == nil {
		t.Error("the pending file was removed")
	}
}

func TestRemoveTempFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".tmp-snapshot-1.db", ".tmp-restore-2.db", "monitor.db", RestorePendingFile} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := RemoveTempFiles(dir); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(dataDirFiles(t, dir), ","); got != "monitor.db,"+RestorePendingFile {
		t.Errorf("left %s, want the database and the pending restore", got)
	}
}

func mustMigrations(t *testing.T) []migration {
	t.Helper()
	files, err := migrationFilesOf(sqliteDialect)
	if err != nil {
		t.Fatal(err)
	}
	return files
}
