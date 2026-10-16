package store

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Migrations live per dialect in migrations/<dialect>/NNNN_name.sql
// (migrations/sqlite, migrations/postgres; every file has a twin of the
// same name in the other directory). They are embedded in the binary and
// applied in order of their number.
//
//go:embed migrations
var migrationsFS embed.FS

// migration is one SQL file.
type migration struct {
	version int    // NNNN from the file name
	name    string // the file name, for example "0001_init.sql"
	sql     string
}

// Migrate applies every migration that has not run yet. It is safe to call on
// every start and from several processes at once: each migration runs in its
// own write transaction together with its schema_migrations row.
func (db *DB) Migrate(ctx context.Context) error {
	files, err := db.migrationFiles()
	if err != nil {
		return err
	}
	return db.migrate(ctx, files)
}

// migrate applies files; tests pass fewer files than are embedded to make
// an older database.
func (db *DB) migrate(ctx context.Context, files []migration) error {
	if err := db.createSchemaMigrations(ctx); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, m := range files {
		if err := db.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

// createSchemaMigrations makes the table that records the migrations, once.
// Under the migrations lock: Postgres can fail two CREATE TABLE IF NOT
// EXISTS of one table at the same moment.
func (db *DB) createSchemaMigrations(ctx context.Context) error {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	if err := tx.lock(ctx, lockMigrations); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at BIGINT  NOT NULL
	)`)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	// Checked inside the transaction, after the lock (SQLite: the write
	// lock; Postgres: the migrations lock), so a second process that waited
	// sees the row the first one committed.
	if err := tx.lock(ctx, lockMigrations); err != nil {
		return err
	}
	var applied int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&applied)
	if err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}

	// The file as it is (several statements, no arguments): not rebound.
	if _, err := tx.tx.ExecContext(ctx, m.sql); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		m.version, m.name, time.Now().UnixMilli())
	if err != nil {
		return err
	}
	return tx.Commit()
}

// migrationFiles returns the embedded migrations of the database's
// dialect, sorted by version.
func (db *DB) migrationFiles() ([]migration, error) {
	migrationsDir := "migrations/" + string(db.d)
	entries, err := fs.ReadDir(migrationsFS, migrationsDir)
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}

	var files []migration
	seen := map[int]string{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		number, _, ok := strings.Cut(name, "_")
		version, err := strconv.Atoi(number)
		if !ok || err != nil {
			return nil, fmt.Errorf("migration %s: name must look like NNNN_name.sql", name)
		}
		if other, dup := seen[version]; dup {
			return nil, fmt.Errorf("migrations %s and %s share version %d", other, name, version)
		}
		seen[version] = name

		body, err := migrationsFS.ReadFile(path.Join(migrationsDir, name))
		if err != nil {
			return nil, err
		}
		files = append(files, migration{version: version, name: name, sql: string(body)})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	return files, nil
}
