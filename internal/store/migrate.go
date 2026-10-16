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

// Migrations live per dialect in migrations/<dialect>/NNNN_name.sql. They are
// embedded in the binary and applied in order of their number.
//
//go:embed migrations
var migrationsFS embed.FS

const migrationsDir = "migrations/sqlite"

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
	_, err := db.write.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		name       TEXT    NOT NULL,
		applied_at BIGINT  NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	files, err := migrationFiles()
	if err != nil {
		return err
	}
	for _, m := range files {
		if err := db.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("migration %s: %w", m.name, err)
		}
	}
	return nil
}

func (db *DB) applyMigration(ctx context.Context, m migration) error {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	// Checked inside the transaction, so a second process that waited for the
	// write lock sees the row the first one committed.
	var applied int
	err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = ?", m.version).Scan(&applied)
	if err != nil {
		return err
	}
	if applied > 0 {
		return nil
	}

	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
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

// migrationFiles returns the embedded migrations, sorted by version.
func migrationFiles() ([]migration, error) {
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
