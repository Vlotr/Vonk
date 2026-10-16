// Package store owns the database: opening it, migrating it and every query.
//
// SQLite allows one writer at a time, so a DB holds two pools on the same file:
// a single write connection and a pool of read-only connections. WAL mode lets
// the readers run while the writer commits.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite" // registers the pure Go "sqlite" driver

	"monitor/internal/config"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// busyTimeoutMillis is how long a connection waits for a lock before it gives up.
const busyTimeoutMillis = 5000

// DB is the database handle shared by the whole process.
type DB struct {
	write *sql.DB // exactly one connection: every INSERT, UPDATE and DELETE goes here
	read  *sql.DB // read-only pool for queries
}

// Open opens the database described by cfg. Run Migrate before using it.
func Open(ctx context.Context, cfg config.Config) (*DB, error) {
	if cfg.DatabaseURL != "" {
		return nil, errors.New("DATABASE_URL is set, but Postgres is not supported yet; unset it to use SQLite")
	}
	return openSQLite(ctx, cfg.SQLitePath)
}

func openSQLite(ctx context.Context, path string) (*DB, error) {
	if path == "" {
		return nil, errors.New("no SQLite path configured")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}

	// The writer sets WAL mode, which is stored in the database file, so the
	// readers inherit it. _txlock=immediate makes every write transaction take
	// the write lock at BEGIN, so two writers never deadlock halfway through.
	writeParams := url.Values{}
	writeParams.Set("_busy_timeout", fmt.Sprint(busyTimeoutMillis))
	writeParams.Set("_journal_mode", "WAL")
	writeParams.Set("_synchronous", "NORMAL")
	writeParams.Set("_foreign_keys", "1")
	writeParams.Set("_txlock", "immediate")

	write, err := sql.Open("sqlite", path+"?"+writeParams.Encode())
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	if err := write.PingContext(ctx); err != nil {
		write.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	readParams := url.Values{}
	readParams.Set("_busy_timeout", fmt.Sprint(busyTimeoutMillis))
	readParams.Set("_foreign_keys", "1")
	readParams.Set("_query_only", "1")

	read, err := sql.Open("sqlite", path+"?"+readParams.Encode())
	if err != nil {
		write.Close()
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	readers := max(4, runtime.NumCPU())
	read.SetMaxOpenConns(readers)
	read.SetMaxIdleConns(readers)
	if err := read.PingContext(ctx); err != nil {
		write.Close()
		read.Close()
		return nil, fmt.Errorf("open sqlite %s: %w", path, err)
	}

	return &DB{write: write, read: read}, nil
}

// Ping checks that the database answers a query.
func (db *DB) Ping(ctx context.Context) error {
	var one int
	return db.read.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Close closes both pools.
func (db *DB) Close() error {
	return errors.Join(db.write.Close(), db.read.Close())
}
