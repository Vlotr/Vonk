// Package store owns the database: opening it, migrating it and every query.
//
// The database is SQLite (the default, a file in DATA_DIR) or Postgres
// (DATABASE_URL). Either way a DB holds two pools: one for writes and one
// of read-only connections for queries. SQLite allows one writer at a
// time, so its write pool is a single connection and WAL mode lets the
// readers run while it commits. Postgres takes several writers at once;
// the code that reads and then writes locks what it read (see dialect.go).
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
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite" // registers the pure Go "sqlite" driver

	"monitor/internal/config"
)

// ErrNotFound is returned when a lookup matches no row.
var ErrNotFound = errors.New("not found")

// busyTimeoutMillis is how long a connection waits for a lock before it gives up.
const busyTimeoutMillis = 5000

// Postgres pool sizes. The write pool is for the ingest writer, the jobs
// and the dashboard's changes, which mostly wait on the network and the
// disk, not on each other; queries get as many connections as SQLite's
// read pool. Together they stay far below Postgres' default limit of 100
// connections.
const (
	postgresWriteConns = 4
	postgresIdleTime   = 5 * time.Minute
)

// DB is the database handle shared by the whole process.
type DB struct {
	write *pool // every INSERT, UPDATE and DELETE goes here; one connection on SQLite
	read  *pool // read-only pool for queries

	d       dialect
	path    string // the SQLite file; "" for Postgres
	dataDir string // DATA_DIR, where backups/ lives

	sourceMapsVersion atomic.Int64 // see SourceMapsVersion
}

// Open opens the database described by cfg: Postgres when DatabaseURL is
// set (it must start with postgres:// or postgresql://), else SQLite at
// SQLitePath. Run Migrate before using it.
func Open(ctx context.Context, cfg config.Config) (*DB, error) {
	var (
		db  *DB
		err error
	)
	switch {
	case cfg.DatabaseURL == "":
		db, err = openSQLite(ctx, cfg.SQLitePath)
	case strings.HasPrefix(cfg.DatabaseURL, "postgres://"), strings.HasPrefix(cfg.DatabaseURL, "postgresql://"):
		db, err = openPostgres(ctx, cfg.DatabaseURL)
	default:
		return nil, errors.New("DATABASE_URL must start with postgres:// or postgresql://; leave it empty to use SQLite")
	}
	if err != nil {
		return nil, err
	}
	db.dataDir = cfg.DataDir
	if db.dataDir == "" && cfg.SQLitePath != "" {
		db.dataDir = filepath.Dir(cfg.SQLitePath)
	}
	return db, nil
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

	return &DB{write: &pool{write, sqliteDialect}, read: &pool{read, sqliteDialect}, d: sqliteDialect, path: path}, nil
}

// openPostgres opens both pools on the database at rawURL through pgx's
// database/sql driver (pure Go). The read pool's sessions are read-only
// (default_transaction_read_only), as SQLite's are (_query_only). Errors
// never repeat the URL, which holds the password.
func openPostgres(ctx context.Context, rawURL string) (*DB, error) {
	writeConfig, err := postgresConfig(rawURL)
	if err != nil {
		return nil, err
	}
	readConfig := writeConfig.Copy()
	readConfig.RuntimeParams["default_transaction_read_only"] = "on"

	write := stdlib.OpenDB(*writeConfig)
	write.SetMaxOpenConns(postgresWriteConns)
	write.SetMaxIdleConns(postgresWriteConns)
	write.SetConnMaxIdleTime(postgresIdleTime)
	if err := write.PingContext(ctx); err != nil {
		write.Close()
		return nil, fmt.Errorf("connect to Postgres (DATABASE_URL): %w", err)
	}

	readers := max(4, runtime.NumCPU())
	read := stdlib.OpenDB(*readConfig)
	read.SetMaxOpenConns(readers)
	read.SetMaxIdleConns(readers)
	read.SetConnMaxIdleTime(postgresIdleTime)
	if err := read.PingContext(ctx); err != nil {
		write.Close()
		read.Close()
		return nil, fmt.Errorf("connect to Postgres (DATABASE_URL): %w", err)
	}
	return &DB{write: &pool{write, postgresDialect}, read: &pool{read, postgresDialect}, d: postgresDialect}, nil
}

// DataDir is DATA_DIR: where backups/, a pending restore and the SQLite file live.
func (db *DB) DataDir() string {
	return db.dataDir
}

// defaultConnectTimeout bounds a new Postgres connection when DATABASE_URL
// sets no connect_timeout (pgx would wait for the system's TCP timeout,
// minutes, on a host that does not answer): during a failover the writer
// then learns quickly that the database cannot be reached, and tries again.
const defaultConnectTimeout = 5 * time.Second

// postgresConfig parses DATABASE_URL, with defaultConnectTimeout when it
// names none. The error never repeats the URL, which holds the password.
func postgresConfig(rawURL string) (*pgx.ConnConfig, error) {
	cfg, err := pgx.ParseConfig(rawURL)
	if err != nil {
		return nil, errors.New("DATABASE_URL is not a valid Postgres URL (postgres://user:password@host:5432/database)")
	}
	if cfg.ConnectTimeout == 0 {
		cfg.ConnectTimeout = defaultConnectTimeout
	}
	return cfg, nil
}

// Postgres reports whether the database is Postgres (DATABASE_URL) rather than SQLite.
func (db *DB) Postgres() bool {
	return db.d == postgresDialect
}

// Ping checks that both the write connection and the read pool answer a query.
func (db *DB) Ping(ctx context.Context) error {
	var one int
	if err := db.write.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("write connection: %w", err)
	}
	if err := db.read.QueryRowContext(ctx, "SELECT 1").Scan(&one); err != nil {
		return fmt.Errorf("read pool: %w", err)
	}
	return nil
}

// SizeBytes returns the size of the database on disk: the SQLite file and
// its write-ahead log, or what Postgres reports for the database. 0 when
// it cannot be read.
func (db *DB) SizeBytes(ctx context.Context) int64 {
	if db.Postgres() {
		var size int64
		if err := db.read.QueryRowContext(ctx, "SELECT pg_database_size(current_database())").Scan(&size); err != nil {
			return 0
		}
		return size
	}
	var total int64
	for _, name := range []string{db.path, db.path + "-wal"} {
		if info, err := os.Stat(name); err == nil {
			total += info.Size()
		}
	}
	return total
}

// Close closes both pools.
func (db *DB) Close() error {
	return errors.Join(db.write.Close(), db.read.Close())
}
