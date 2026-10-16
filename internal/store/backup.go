package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Backups made before a database update live in <DATA_DIR>/backups as
// pre-migrate-<version>-<time>.db: a complete SQLite copy of the database
// as it was at that version. The five newest are kept. They are made of
// SQLite only: a Postgres database is backed up with pg_dump, by whoever
// runs it (ErrNoBackups).

const (
	backupDir        = "backups"
	preMigratePrefix = "pre-migrate-"
	keepPreMigrate   = 5
	backupTimeFormat = "20060102T150405Z"
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
	Name      string
	Path      string
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
	if err := prunePreMigrate(dir); err != nil {
		return path, fmt.Errorf("%w: %w", ErrOldBackupsKept, err)
	}
	return path, nil
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
	for n := 1; n < 100; n++ {
		name := base + ".db"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.db", base, n)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		}
	}
	return "", fmt.Errorf("no free backup name for %s in %s", base, dir)
}

// prunePreMigrate removes pre-migration backups beyond the five newest.
// Other files in the directory are left alone.
func prunePreMigrate(dir string) error {
	backups, err := listBackups(dir)
	if err != nil {
		return err
	}
	var errs []error
	for i, b := range backups {
		if i >= keepPreMigrate {
			if err := removeBackup(b.Path); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// Backups returns the pre-migration backups, the newest first; none on
// Postgres, which the tool does not back up.
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

// listBackups reads the pre-migration backups in dir, the newest first (by
// the time in their name, then the name).
func listBackups(dir string) ([]BackupFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	backups := []BackupFile{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, preMigratePrefix) || !strings.HasSuffix(name, ".db") {
			continue
		}
		// pre-migrate-0003-20261016T120000Z.db or ...Z-2.db
		_, rest, ok := strings.Cut(strings.TrimPrefix(name, preMigratePrefix), "-")
		if !ok || len(rest) < len(backupTimeFormat) {
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
		backups = append(backups, BackupFile{Name: name, Path: filepath.Join(dir, name), SizeBytes: info.Size(), CreatedAt: created})
	}
	sort.Slice(backups, func(i, j int) bool {
		if !backups[i].CreatedAt.Equal(backups[j].CreatedAt) {
			return backups[i].CreatedAt.After(backups[j].CreatedAt)
		}
		return backups[i].Name > backups[j].Name
	})
	return backups, nil
}
