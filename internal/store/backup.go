package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The automatic backups live in <DATA_DIR>/backups: pre-migrate-<version>-<time>.db
// is a complete SQLite copy of the database as it was at that version,
// made before a database update; pre-restore-<version>-<time>.db is the
// database a restore replaced (restore.go). The five newest of each kind
// are kept. They are made of SQLite only: a Postgres database is backed up
// with pg_dump, by whoever runs it (ErrNoBackups).

const (
	backupDir        = "backups"
	preMigratePrefix = "pre-migrate-"
	preRestorePrefix = "pre-restore-"
	keepBackups      = 5 // of each kind
	backupTimeFormat = "20060102T150405Z"
)

// The kinds of automatic backups (BackupFile.Kind).
const (
	BackupBeforeUpdate  = "pre-migrate"
	BackupBeforeRestore = "pre-restore"
)

// ErrOldBackupsKept means the new backup was made, but some of the old
// ones beyond the five newest could not be removed. The update may go on.
var ErrOldBackupsKept = errors.New("old backups could not be removed")

// ErrNoBackups means an update is about to run on a Postgres database that
// has data, without a backup: the tool makes backups of SQLite only. Back
// Postgres up with pg_dump.
var ErrNoBackups = errors.New("the tool makes no backups of a Postgres database, also not before this database update; back it up with pg_dump")

// removeBackup removes one old backup; tests make it fail.
var removeBackup = os.Remove

// BackupFile is one backup in <DATA_DIR>/backups.
type BackupFile struct {
	Name string
	Path string
	// Kind is BackupBeforeUpdate or BackupBeforeRestore.
	Kind string
	// Version is the database version the copy is at.
	Version   int
	SizeBytes int64
	CreatedAt time.Time
}

// BackupBeforeMigrate copies the database to <DATA_DIR>/backups when
// Migrate is about to change a database that already has data (migrations
// are pending and at least one ran before), and returns the copy's path;
// "" when no backup was needed. Old pre-migration backups beyond the five
// newest are removed. Run it before Migrate and stop when it fails: the
// update should not run without its backup. The one error to go on after
// is ErrOldBackupsKept (the backup is there; only old ones stay). On
// Postgres no backup is made: it returns ErrNoBackups when one would have
// been, which is a warning, not a reason to stop.
func (db *DB) BackupBeforeMigrate(ctx context.Context) (string, error) {
	files, err := db.migrationFiles()
	if err != nil {
		return "", err
	}
	return db.backupBeforeMigrate(ctx, files, time.Now())
}

func (db *DB) backupBeforeMigrate(ctx context.Context, files []migration, now time.Time) (string, error) {
	applied, err := db.appliedVersions(ctx)
	if err != nil {
		return "", fmt.Errorf("read the database version: %w", err)
	}
	if len(applied) == 0 {
		return "", nil // a new database: nothing to lose
	}
	pending := false
	for _, m := range files {
		if !applied[m.version] {
			pending = true
			break
		}
	}
	if !pending {
		return "", nil
	}
	if db.Postgres() {
		return "", ErrNoBackups
	}
	current := 0
	for v := range applied {
		current = max(current, v)
	}

	dir := filepath.Join(db.dataDir, backupDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	path, err := freeBackupPath(dir, fmt.Sprintf("%s%04d-%s", preMigratePrefix, current, now.UTC().Format(backupTimeFormat)))
	if err != nil {
		return "", err
	}
	// VACUUM INTO writes a consistent, compact copy (WAL included) while
	// the database stays open. It cannot run inside a transaction.
	if _, err := db.write.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		os.Remove(path) // a partial copy is no backup
		return "", fmt.Errorf("copy the database to %s: %w", path, err)
	}
	if err := pruneBackups(dir, BackupBeforeUpdate); err != nil {
		return path, fmt.Errorf("%w: %w", ErrOldBackupsKept, err)
	}
	return path, nil
}

// Snapshot writes a consistent copy of the whole database (VACUUM INTO,
// compact, the write-ahead log included) to a new file in DATA_DIR and
// returns it open for reading, already removed from the directory: the
// copy disappears when the file is closed, also when the process stops
// halfway. It reads through its own connection, so the ingest writer goes
// on meanwhile. It needs free disk space for a copy of the database. On
// Postgres it returns ErrNoBackups.
func (db *DB) Snapshot(ctx context.Context) (*os.File, error) {
	if db.Postgres() {
		return nil, ErrNoBackups
	}
	tmp, err := os.CreateTemp(db.dataDir, tempPrefix+"snapshot-*.db")
	if err != nil {
		return nil, err
	}
	path := tmp.Name()
	tmp.Close() // VACUUM INTO writes into an empty file
	defer os.Remove(path)

	if err := db.vacuumInto(ctx, path); err != nil {
		return nil, fmt.Errorf("copy the database: %w", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return f, nil // removed by the deferred call; the open file stays readable
}

// vacuumInto copies the database to path on a connection of its own: the
// read pool's connections are query-only, and the write connection is the
// ingest writer's.
func (db *DB) vacuumInto(ctx context.Context, path string) error {
	conn, err := sql.Open("sqlite", db.path+"?_busy_timeout="+strconv.Itoa(busyTimeoutMillis)+"&_defensive=1&_pragma=trusted_schema(0)")
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.ExecContext(ctx, "VACUUM INTO ?", path)
	return err
}

// appliedVersions returns the migrations that ran; none for a new database.
func (db *DB) appliedVersions(ctx context.Context) (map[int]bool, error) {
	query := "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'"
	if db.Postgres() {
		query = "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'schema_migrations'"
	}
	var tables int
	err := db.write.QueryRowContext(ctx, query).Scan(&tables)
	if err != nil || tables == 0 {
		return nil, err
	}
	rows, err := db.write.QueryContext(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	applied := map[int]bool{}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		applied[v] = true
	}
	return applied, rows.Err()
}

// freeBackupPath returns dir/base.db, or dir/base-2.db and so on when that
// name is taken (two starts in one second).
func freeBackupPath(dir, base string) (string, error) {
	return freeName(dir, base, ".db")
}

// freeName returns dir/base+ext, or dir/base-2+ext and so on when that
// name is taken: the name checked is the name the caller writes.
func freeName(dir, base, ext string) (string, error) {
	for n := 1; n < 100; n++ {
		name := base + ext
		if n > 1 {
			name = fmt.Sprintf("%s-%d%s", base, n, ext)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free backup name for %s in %s", base, dir)
}

// pruneBackups removes backups of one kind beyond the five newest (with
// the write-ahead log a restore may have kept next to one). Other files in
// the directory are left alone.
func pruneBackups(dir, kind string) error {
	backups, err := listBackups(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	kept := 0
	for _, b := range backups {
		if b.Kind != kind {
			continue
		}
		if kept++; kept <= keepBackups {
			continue
		}
		if err := removeBackup(b.Path); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(b.Path + "-wal"); err != nil && !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Backups returns the automatic backups (before updates and before
// restores), the newest first; none on Postgres, which the tool does not
// back up.
func (db *DB) Backups() ([]BackupFile, error) {
	if db.Postgres() {
		return []BackupFile{}, nil
	}
	backups, err := listBackups(filepath.Join(db.dataDir, backupDir))
	if errors.Is(err, os.ErrNotExist) {
		return []BackupFile{}, nil
	}
	return backups, err
}

// listBackups reads the automatic backups in dir, the newest first (by
// the time in their name, then the name).
func listBackups(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	backups := []BackupFile{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".db") {
			continue
		}
		var kind, rest string
		switch {
		case strings.HasPrefix(name, preMigratePrefix):
			kind, rest = BackupBeforeUpdate, strings.TrimPrefix(name, preMigratePrefix)
		case strings.HasPrefix(name, preRestorePrefix):
			kind, rest = BackupBeforeRestore, strings.TrimPrefix(name, preRestorePrefix)
		default:
			continue
		}
		// 0003-20261016T120000Z.db or ...Z-2.db
		number, rest, ok := strings.Cut(rest, "-")
		version, err := strconv.Atoi(number)
		if !ok || err != nil || len(rest) < len(backupTimeFormat) {
			continue
		}
		created, err := time.Parse(backupTimeFormat, rest[:len(backupTimeFormat)])
		if err != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		backups = append(backups, BackupFile{Name: name, Path: filepath.Join(dir, name), Kind: kind, Version: version,
			SizeBytes: info.Size(), CreatedAt: created})
	}
	sort.Slice(backups, func(i, j int) bool {
		if !backups[i].CreatedAt.Equal(backups[j].CreatedAt) {
			return backups[i].CreatedAt.After(backups[j].CreatedAt)
		}
		return backups[i].Name > backups[j].Name
	})
	return backups, nil
}
