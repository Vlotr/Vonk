package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"monitor/internal/config"
)

// Restoring a backup (SQLite only). The live database is never swapped
// while the tool runs: StageRestore checks the file and puts it in
// <DATA_DIR>/restore-pending.db, and the next start of the server
// (ApplyPendingRestore, before the migrations) checks it again, moves the
// current database to backups/pre-restore-<version>-<time>.db and puts
// the pending file in its place. Then the migrations bring it up to date,
// as after an update. A file is accepted when SQLite finds it intact
// (PRAGMA integrity_check) and its schema is this tool's at a version no
// newer than this binary's.

// RestorePendingFile is the backup waiting in DATA_DIR for the next start.
const RestorePendingFile = "restore-pending.db"

const (
	// rejectedPrefix names a pending restore the start refused, kept so
	// nothing is lost: restore-rejected-<time>.db in DATA_DIR.
	rejectedPrefix = "restore-rejected-"
	// tempPrefix names files under way in DATA_DIR: snapshots being
	// downloaded, uploads being checked. Left over after a crash, the next
	// start removes them (RemoveTempFiles).
	tempPrefix = ".tmp-"
)

// sqliteHeader starts every SQLite database file.
var sqliteHeader = []byte("SQLite format 3\x00")

var (
	// ErrRestoreRefused means the start found the pending restore unusable:
	// it was moved aside (RestoreResult.Rejected) and the current database
	// stays.
	ErrRestoreRefused = errors.New("the restore was refused")
	// ErrRestoreOnPostgres means a restore is pending while the database
	// is Postgres; it is left alone.
	ErrRestoreOnPostgres = errors.New(RestorePendingFile + " is ignored: the database is Postgres (DATABASE_URL), " +
		"which the tool does not restore; restore Postgres with pg_restore")
)

// BackupError says why a file cannot be restored, in words for people.
type BackupError struct {
	Reason string
}

func (e *BackupError) Error() string { return e.Reason }

func notABackup(format string, args ...any) error {
	return &BackupError{Reason: fmt.Sprintf(format, args...)}
}

// BackupInfo is what CheckBackupFile found.
type BackupInfo struct {
	// Version is the database version of the backup; the migrations
	// bring it up to date after the restore.
	Version int
}

// CheckBackupFile checks that the SQLite file at path can be restored: it
// is a SQLite database, PRAGMA integrity_check finds nothing wrong, and it
// is this tool's: its schema_migrations match the binary's migrations, up
// to a version no newer than the binary's, and it has no triggers or
// views (the tool makes none, and they would run in the tool). A refusal
// is a *BackupError. The file is opened read-only and left as it is.
func CheckBackupFile(ctx context.Context, path string) (BackupInfo, error) {
	head := make([]byte, len(sqliteHeader))
	f, err := os.Open(path)
	if err != nil {
		return BackupInfo{}, err
	}
	n, _ := io.ReadFull(f, head)
	f.Close()
	if n < len(head) || !bytes.Equal(head, sqliteHeader) {
		return BackupInfo{}, notABackup("The file is not a SQLite database, so it is not a backup of this tool.")
	}

	// immutable: SQLite reads the file as it is, with no locks and no
	// -wal or -shm files next to it. trusted_schema off: nothing in the
	// file's schema may call functions while it is read.
	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&immutable=1&_pragma=trusted_schema(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return BackupInfo{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA integrity_check(1)").Scan(&result); err != nil {
		if ctx.Err() != nil {
			return BackupInfo{}, ctx.Err()
		}
		return BackupInfo{}, notABackup("The file is damaged: SQLite cannot read it (%v).", err)
	}
	if result != "ok" {
		return BackupInfo{}, notABackup("The file is damaged: SQLite's integrity check says %q.", result)
	}

	var tables, extras int
	err = db.QueryRowContext(ctx, `SELECT
			(SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'),
			(SELECT COUNT(*) FROM sqlite_master WHERE type IN ('trigger', 'view'))`).Scan(&tables, &extras)
	if err != nil {
		return BackupInfo{}, notABackup("The file is damaged: SQLite cannot read its tables (%v).", err)
	}
	if tables == 0 {
		return BackupInfo{}, notABackup("The file is a SQLite database, but not a backup of this tool: it has no schema_migrations table.")
	}
	if extras > 0 {
		return BackupInfo{}, notABackup("The file is not a backup of this tool: it has triggers or views, which the tool never makes.")
	}

	files, err := migrationFilesOf(sqliteDialect)
	if err != nil {
		return BackupInfo{}, err
	}
	known := map[int]string{}
	latest := 0
	for _, m := range files {
		known[m.version] = m.name
		latest = max(latest, m.version)
	}
	rows, err := db.QueryContext(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		return BackupInfo{}, notABackup("The file is not a backup of this tool: its schema_migrations cannot be read (%v).", err)
	}
	defer rows.Close()
	info := BackupInfo{}
	for rows.Next() {
		var (
			version int
			name    string
		)
		if err := rows.Scan(&version, &name); err != nil {
			return BackupInfo{}, notABackup("The file is not a backup of this tool: its schema_migrations cannot be read (%v).", err)
		}
		info.Version = max(info.Version, version)
		if version <= latest && known[version] != name {
			return BackupInfo{}, notABackup("The file is not a backup of this tool: its database update %d is %q, this tool's is %q.",
				version, name, known[version])
		}
	}
	if err := rows.Err(); err != nil {
		return BackupInfo{}, notABackup("The file is not a backup of this tool: its schema_migrations cannot be read (%v).", err)
	}
	if info.Version == 0 {
		return BackupInfo{}, notABackup("The file is a SQLite database, but not a backup of this tool: it has no database updates.")
	}
	if info.Version > latest {
		return BackupInfo{}, notABackup("The backup is from a newer version of the tool: its database is at version %d, and this version "+
			"knows up to %d. Update the tool first, then restore the backup.", info.Version, latest)
	}
	return info, nil
}

// PendingRestore is a backup waiting for the next start.
type PendingRestore struct {
	SizeBytes int64
	StagedAt  time.Time
	// Version is the backup's database version (StageRestore only).
	Version int
}

// StageRestore copies a backup from src into DATA_DIR, checks it
// (CheckBackupFile) and makes it the pending restore, replacing one that
// was pending. A refusal (*BackupError) or a failing src (an upload over
// its size limit, a connection that broke) leaves nothing behind. The
// caller limits src; nothing of the live database changes.
func StageRestore(ctx context.Context, dataDir string, src io.Reader) (PendingRestore, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return PendingRestore{}, err
	}
	tmp, err := os.CreateTemp(dataDir, tempPrefix+"restore-*.db")
	if err != nil {
		return PendingRestore{}, err
	}
	path := tmp.Name()
	keep := false
	defer func() {
		if !keep {
			os.Remove(path)
		}
	}()
	size, err := io.Copy(tmp, src)
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return PendingRestore{}, err
	}

	info, err := CheckBackupFile(ctx, path)
	if err != nil {
		return PendingRestore{}, err
	}
	if err := os.Rename(path, filepath.Join(dataDir, RestorePendingFile)); err != nil {
		return PendingRestore{}, err
	}
	keep = true
	syncDir(dataDir)
	return PendingRestore{SizeBytes: size, StagedAt: time.Now(), Version: info.Version}, nil
}

// PendingRestoreIn returns the restore waiting in dataDir, or nil.
func PendingRestoreIn(dataDir string) (*PendingRestore, error) {
	info, err := os.Stat(filepath.Join(dataDir, RestorePendingFile))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &PendingRestore{SizeBytes: info.Size(), StagedAt: info.ModTime()}, nil
}

// RejectedRestore is a pending restore a start refused, kept in DATA_DIR.
type RejectedRestore struct {
	Name       string
	RejectedAt time.Time
}

// RejectedRestores lists the refused restores in dataDir, the newest first.
func RejectedRestores(dataDir string) ([]RejectedRestore, error) {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return []RejectedRestore{}, nil
	}
	if err != nil {
		return nil, err
	}
	list := []RejectedRestore{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, rejectedPrefix) || !strings.HasSuffix(name, ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		list = append(list, RejectedRestore{Name: name, RejectedAt: info.ModTime()})
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name > list[j].Name })
	return list, nil
}

// CancelRestore removes the pending restore and the refused ones, and
// reports whether there was anything to remove.
func CancelRestore(dataDir string) (bool, error) {
	rejected, err := RejectedRestores(dataDir)
	if err != nil {
		return false, err
	}
	names := []string{RestorePendingFile}
	for _, r := range rejected {
		names = append(names, r.Name)
	}
	removed := false
	var errs []error
	for _, name := range names {
		err := os.Remove(filepath.Join(dataDir, name))
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, os.ErrNotExist):
			errs = append(errs, err)
		}
	}
	return removed, errors.Join(errs...)
}

// RemoveTempFiles removes files a stopped process left under way in
// dataDir (snapshots, uploads). Run it at the start, before anything else.
func RemoveTempFiles(dataDir string) error {
	entries, err := os.ReadDir(dataDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), tempPrefix) {
			if err := os.Remove(filepath.Join(dataDir, e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

// RestoreResult tells what ApplyPendingRestore did.
type RestoreResult struct {
	// Restored is true when the pending backup is now the database.
	Restored bool
	// Version is the restored database's version, before the migrations.
	Version int
	// Backup is where the database before the restore went; "" when
	// there was none (a new DATA_DIR).
	Backup string
	// Rejected is where a refused pending restore was moved.
	Rejected string
}

// ApplyPendingRestore makes the pending restore the database. Run it at
// the start of the server, before the database is opened: nothing may
// have the database open meanwhile. It checks the file again
// (CheckBackupFile): a refusal moves it aside to restore-rejected-<time>.db
// and returns ErrRestoreRefused, leaving the current database as it was.
// Otherwise the current database (and the write-ahead log it could not
// fold in, if any) moves to backups/pre-restore-<version>-<time>.db, and
// the pending file takes its place. On Postgres a pending file is left
// alone (ErrRestoreOnPostgres).
func ApplyPendingRestore(ctx context.Context, cfg config.Config, now time.Time) (RestoreResult, error) {
	pending := filepath.Join(cfg.DataDir, RestorePendingFile)
	if _, err := os.Stat(pending); errors.Is(err, os.ErrNotExist) {
		return RestoreResult{}, nil
	} else if err != nil {
		return RestoreResult{}, err
	}
	if cfg.DatabaseURL != "" {
		return RestoreResult{}, ErrRestoreOnPostgres
	}

	info, err := CheckBackupFile(ctx, pending)
	var refused *BackupError
	if errors.As(err, &refused) {
		rejected, err := freeBackupPath(cfg.DataDir, rejectedPrefix+now.UTC().Format(backupTimeFormat))
		if err != nil {
			return RestoreResult{}, err
		}
		if err := os.Rename(pending, rejected); err != nil {
			return RestoreResult{}, err
		}
		return RestoreResult{Rejected: rejected}, fmt.Errorf("%w: %w", ErrRestoreRefused, refused)
	}
	if err != nil {
		return RestoreResult{}, fmt.Errorf("check %s: %w", pending, err)
	}

	result := RestoreResult{Version: info.Version}
	if result.Backup, err = moveAside(ctx, cfg.SQLitePath, filepath.Join(cfg.DataDir, backupDir), now); err != nil {
		return RestoreResult{}, fmt.Errorf("the database could not be moved to backups before the restore: %w", err)
	}
	if err := os.Rename(pending, cfg.SQLitePath); err != nil {
		return result, fmt.Errorf("put the restored database in place: %w (the database from before is in %s)", err, result.Backup)
	}
	syncDir(filepath.Dir(cfg.SQLitePath))
	result.Restored = true
	if err := pruneBackups(filepath.Join(cfg.DataDir, backupDir), BackupBeforeRestore); err != nil {
		return result, fmt.Errorf("%w: %w", ErrOldBackupsKept, err)
	}
	return result, nil
}

// moveAside moves the SQLite database at path into dir as
// pre-restore-<version>-<time>.db and returns its new path; "" when there
// is no database. First the write-ahead log is folded into the file
// (closing the last connection does that); a log that could not be folded
// in (a damaged database) moves along as <name>-wal, so nothing is lost.
func moveAside(ctx context.Context, path, dir string, now time.Time) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	version := 0
	if db, err := sql.Open("sqlite", path+"?_busy_timeout="+strconv.Itoa(busyTimeoutMillis)); err == nil {
		db.QueryRowContext(ctx, "SELECT MAX(version) FROM schema_migrations").Scan(&version) // 0 when it cannot be read
		db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
		db.Close()
	}

	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	backup, err := freeBackupPath(dir, fmt.Sprintf("%s%04d-%s", preRestorePrefix, version, now.UTC().Format(backupTimeFormat)))
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, backup); err != nil {
		return "", err
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		if err := os.Rename(path+"-wal", backup+"-wal"); err != nil {
			return backup, err
		}
	}
	// What is left belongs to the old database and must not meet the new one.
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, os.ErrNotExist) {
			return backup, err
		}
	}
	syncDir(dir)
	return backup, nil
}

// syncDir flushes a directory's entries (a rename) to disk; errors are
// ignored, as some file systems cannot.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
