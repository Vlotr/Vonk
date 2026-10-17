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
	"regexp"
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
// is this tool's (checkBackup). A refusal is a *BackupError. The file is
// opened read-only and left as it is.
func CheckBackupFile(ctx context.Context, path string) (BackupInfo, error) {
	return checkBackup(ctx, path, "integrity_check")
}

// checkBackup checks the file at path with the PRAGMA check (integrity_check,
// or quick_check, which skips the comparison of indexes with their tables:
// at the start, after the full check ran when the file was staged), and
// that it is this tool's database:
//
//   - schema_migrations lists exactly this binary's migrations 1..v, with
//     their names, and v is no newer than the binary's newest;
//   - every table, index, view and trigger (sqlite_master) is the one
//     those migrations make (referenceSchema): no extra objects (virtual
//     tables, triggers, views), none missing (a unique index), none
//     changed (a column, a CHECK, a default);
//   - every source map row names a file the tool made (storedFileName), as
//     the server reads and removes those files.
//
// The file is opened immutable (no locks, no -wal or -shm files next to
// it), in defensive mode, with trusted_schema off: nothing in its schema
// may call functions while it is read.
func checkBackup(ctx context.Context, path, check string) (BackupInfo, error) {
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

	dsn := "file:" + (&url.URL{Path: path}).EscapedPath() + "?mode=ro&immutable=1&_defensive=1&_pragma=trusted_schema(0)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return BackupInfo{}, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA "+check+"(1)").Scan(&result); err != nil {
		if ctx.Err() != nil {
			return BackupInfo{}, ctx.Err()
		}
		return BackupInfo{}, notABackup("The file is damaged: SQLite cannot read it (%v).", err)
	}
	if result != "ok" {
		return BackupInfo{}, notABackup("The file is damaged: SQLite's integrity check says %q.", result)
	}

	var tables int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'").Scan(&tables)
	if err != nil {
		return BackupInfo{}, notABackup("The file is damaged: SQLite cannot read its tables (%v).", err)
	}
	if tables == 0 {
		return BackupInfo{}, notABackup("The file is a SQLite database, but not a backup of this tool: it has no schema_migrations table.")
	}

	files, err := migrationFilesOf(sqliteDialect)
	if err != nil {
		return BackupInfo{}, err
	}
	version, err := backupVersion(ctx, db, files)
	if err != nil {
		return BackupInfo{}, err
	}
	var upTo []migration
	for _, m := range files {
		if m.version <= version {
			upTo = append(upTo, m)
		}
	}
	want, err := referenceSchema(ctx, upTo)
	if err != nil {
		return BackupInfo{}, fmt.Errorf("the schema of version %d: %w", version, err)
	}
	got, err := schemaOf(ctx, db)
	if err != nil {
		return BackupInfo{}, notABackup("The file is damaged: SQLite cannot read its tables (%v).", err)
	}
	if diff := schemaDifference(got, want); diff != "" {
		return BackupInfo{}, notABackup("The file is not a backup of this tool: its database is not the tool's at version %d (%s).", version, diff)
	}
	if _, ok := want["source_maps"]; ok {
		if err := checkSourceMapPaths(ctx, db); err != nil {
			return BackupInfo{}, err
		}
	}
	return BackupInfo{Version: version}, nil
}

// backupVersion reads the backup's schema_migrations: exactly this
// binary's migrations from the first up to some version, by name. It
// returns that version, or why the file is refused.
func backupVersion(ctx context.Context, db *sql.DB, files []migration) (int, error) {
	unreadable := func(err error) error {
		return notABackup("The file is not a backup of this tool: its schema_migrations cannot be read (%v).", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT version, name FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, unreadable(err)
	}
	defer rows.Close()
	latest := files[len(files)-1].version
	i := 0 // the next of files the backup must have
	version := 0
	for rows.Next() {
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return 0, unreadable(err)
		}
		if version > latest {
			return 0, notABackup("The backup is from a newer version of the tool: its database is at version %d, and this version "+
				"knows up to %d. Update the tool first, then restore the backup.", version, latest)
		}
		if i == len(files) || files[i].version != version {
			missing := latest
			if i < len(files) {
				missing = files[i].version
			}
			return 0, notABackup("The file is not a backup of this tool: its database lacks database update %d.", missing)
		}
		if files[i].name != name {
			return 0, notABackup("The file is not a backup of this tool: its database update %d is %q, this tool's is %q.",
				version, name, files[i].name)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		return 0, unreadable(err)
	}
	if i == 0 {
		return 0, notABackup("The file is a SQLite database, but not a backup of this tool: it has no database updates.")
	}
	return version, nil
}

// schemaObject is one row of sqlite_master: a table, index, view or trigger.
type schemaObject struct {
	typ, table, sql string // sql normalized (normalSQL)
}

// referenceSchema returns the schema a database of this tool has after the
// migrations files: they run, as Migrate runs them, on an empty database
// in memory.
func referenceSchema(ctx context.Context, files []migration) (map[string]schemaObject, error) {
	raw, err := sql.Open("sqlite", ":memory:?_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	defer raw.Close()
	raw.SetMaxOpenConns(1) // one connection: the database lives in it
	raw.SetConnMaxLifetime(0)
	ref := &DB{write: &pool{raw, sqliteDialect}, read: &pool{raw, sqliteDialect}, d: sqliteDialect}
	if err := ref.migrate(ctx, files); err != nil {
		return nil, err
	}
	return schemaOf(ctx, raw)
}

// schemaOf reads a SQLite database's sqlite_master by name.
func schemaOf(ctx context.Context, db *sql.DB) (map[string]schemaObject, error) {
	rows, err := db.QueryContext(ctx, "SELECT type, name, tbl_name, COALESCE(sql, '') FROM sqlite_master")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	objects := map[string]schemaObject{}
	for rows.Next() {
		var name string
		var o schemaObject
		if err := rows.Scan(&o.typ, &name, &o.table, &o.sql); err != nil {
			return nil, err
		}
		o.sql = normalSQL(o.sql)
		objects[name] = o
	}
	return objects, rows.Err()
}

// normalSQL writes a CREATE statement the same way however it was spaced
// and quoted: SQLite keeps the text as written, and quotes names it
// rewrote itself (ALTER TABLE ... RENAME TO).
func normalSQL(statement string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(statement, `"`, "")), " ")
}

// schemaDifference names the first object (by name) that got has and want
// does not, that want has and got does not, or that differs; "" when they
// are the same.
func schemaDifference(got, want map[string]schemaObject) string {
	names := map[string]bool{}
	for name := range got {
		names[name] = true
	}
	for name := range want {
		names[name] = true
	}
	sorted := make([]string, 0, len(names))
	for name := range names {
		sorted = append(sorted, name)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		g, inGot := got[name]
		w, inWant := want[name]
		switch {
		case !inWant:
			return fmt.Sprintf("it has the %s %q, which the tool never makes", g.typ, name)
		case !inGot:
			return fmt.Sprintf("it lacks the %s %q", w.typ, name)
		case g != w:
			return fmt.Sprintf("its %s %q is not defined as the tool's", g.typ, name)
		}
	}
	return ""
}

// storedFileName is the shape of source_maps.path for a file the tool
// stored: the project's id and a random name (sourcemaps.extract), relative
// to <DATA_DIR>/sourcemaps.
var storedFileName = regexp.MustCompile(`^([0-9]+)/[0-9a-f]{24}$`)

// checkSourceMapPaths refuses a backup whose source map rows name files
// the tool would not have made: the server reads those files and removes
// them (retention), so a path such as "../monitor.db" must never come in.
func checkSourceMapPaths(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "SELECT project_id, path FROM source_maps")
	if err != nil {
		return notABackup("The file is damaged: SQLite cannot read its source maps (%v).", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			projectID int64
			path      string
		)
		if err := rows.Scan(&projectID, &path); err != nil {
			return notABackup("The file is damaged: SQLite cannot read its source maps (%v).", err)
		}
		if m := storedFileName.FindStringSubmatch(path); m == nil || m[1] != strconv.FormatInt(projectID, 10) {
			return notABackup("The file is not a backup of this tool: a source map row names %q, which is not a file the tool made.", path)
		}
	}
	return rows.Err()
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

	// quick_check: the full integrity check ran when the file was staged;
	// the schema and the paths are checked again in full.
	info, err := checkBackup(ctx, pending, "quick_check")
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
	dir := filepath.Join(cfg.DataDir, backupDir)
	if result.Backup, err = moveAside(ctx, cfg.SQLitePath, dir, now); err != nil {
		return RestoreResult{}, fmt.Errorf("the database could not be moved to backups before the restore: %w", err)
	}
	// Last before the new file arrives: no write-ahead log or shared
	// memory of the old database may stay next to it, or SQLite would
	// replay that log onto the restored database. Also after a start that
	// stopped between moving the database and its log.
	if err := clearSideFiles(cfg.SQLitePath, dir, result.Backup, now); err != nil {
		if result.Backup != "" {
			return RestoreResult{}, fmt.Errorf("the database before the restore is in %s, but the files next to it could not be moved: %w", result.Backup, err)
		}
		return RestoreResult{}, fmt.Errorf("the files next to the database could not be moved before the restore: %w", err)
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
// in (a damaged database) stays for clearSideFiles, which moves it along.
func moveAside(ctx context.Context, path, dir string, now time.Time) (string, error) {
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	version := 0
	if db, err := sql.Open("sqlite", path+"?_busy_timeout="+strconv.Itoa(busyTimeoutMillis)+"&_defensive=1&_pragma=trusted_schema(0)"); err == nil {
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
	syncDir(dir)
	return backup, nil
}

// clearSideFiles makes sure no -wal or -shm file is left at path: a log
// that is not empty moves into dir, next to backup (the database it
// belongs to) as <backup>-wal, or, when this start moved no database (one
// that stopped halfway already did), as orphaned-<time>.db-wal, so nothing
// is lost; an empty log and the shared memory are removed.
func clearSideFiles(path, dir, backup string, now time.Time) error {
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() > 0 {
		target := backup + "-wal"
		if backup == "" {
			if err := os.MkdirAll(dir, 0o750); err != nil {
				return err
			}
			orphan, err := freeBackupPath(dir, "orphaned-"+now.UTC().Format(backupTimeFormat))
			if err != nil {
				return err
			}
			target = orphan + "-wal"
		}
		if err := os.Rename(path+"-wal", target); err != nil {
			return err
		}
		syncDir(dir)
	}
	for _, side := range []string{path + "-wal", path + "-shm"} {
		if err := os.Remove(side); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// syncDir flushes a directory's entries (a rename) to disk; errors are
// ignored, as some file systems cannot.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
}
