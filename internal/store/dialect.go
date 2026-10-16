package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

// The queries of this package are written once, in the SQL that SQLite and
// Postgres share, with ? placeholders. A dialect covers the few places
// where the two differ:
//
//   - placeholders: Postgres numbers them ($1, $2, ...); rebind rewrites
//     every query on its way to the driver (see pool and writeTx).
//   - locks: on SQLite a write transaction holds the whole database from
//     BEGIN (IMMEDIATE) to COMMIT, so a check and the write after it can
//     never be interleaved by another writer. Postgres runs write
//     transactions side by side (READ COMMITTED), so code that reads and
//     then writes based on what it read locks what it read: forUpdate
//     locks the rows a SELECT returns, and writeTx.lock takes a named lock
//     when there is no row to lock yet (the first account, a new slug).
//     On SQLite both are no-ops.
//   - a few expressions (asciiLower) and the database's own catalog.
//
// Upserts need no help: both understand INSERT ... ON CONFLICT (...) DO
// NOTHING / DO UPDATE SET x = excluded.x, and both accept RETURNING.
type dialect string

const (
	sqliteDialect   dialect = "sqlite"
	postgresDialect dialect = "postgres"
)

// rebind returns query with Postgres' numbered placeholders: each ? that is
// not inside a string, a quoted name or a comment becomes $1, $2 and so on.
// SQLite queries are returned as they are.
func (d dialect) rebind(query string) string {
	if d != postgresDialect || !strings.Contains(query, "?") {
		return query
	}
	var b strings.Builder
	b.Grow(len(query) + 16)
	n := 0
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case c == '\'' || c == '"': // a string or a quoted name: copy it up to its closing quote
			end := strings.IndexByte(query[i+1:], c)
			if end < 0 {
				b.WriteString(query[i:])
				return b.String()
			}
			b.WriteString(query[i : i+end+2])
			i += end + 1
		case c == '-' && i+1 < len(query) && query[i+1] == '-': // a comment, to the end of the line
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				b.WriteString(query[i:])
				return b.String()
			}
			b.WriteString(query[i : i+end])
			i += end - 1
		case c == '?':
			n++
			b.WriteByte('$')
			b.WriteString(strconv.Itoa(n))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// forUpdate is appended to a SELECT inside a write transaction whose rows
// the transaction is going to change: on Postgres the rows stay locked
// until the commit, so another transaction that wants them waits and then
// reads them as changed. "" on SQLite, where the transaction already holds
// the write lock.
func (d dialect) forUpdate() string {
	if d == postgresDialect {
		return " FOR UPDATE"
	}
	return ""
}

// forKeyShare is appended to a SELECT inside a write transaction that
// needs the rows to stay (not be deleted) until the commit, while others
// may still change them: on Postgres a deletion waits. "" on SQLite.
func (d dialect) forKeyShare() string {
	if d == postgresDialect {
		return " FOR KEY SHARE"
	}
	return ""
}

// asciiLower lowercases the ASCII letters of a text expression and leaves
// every other character as it is: what SQLite's LOWER does. Postgres'
// LOWER also lowercases É and the like, so a search for "É" would no
// longer find "É" (see likePattern).
func (d dialect) asciiLower(expr string) string {
	if d == postgresDialect {
		return "TRANSLATE(" + expr + ", 'ABCDEFGHIJKLMNOPQRSTUVWXYZ', 'abcdefghijklmnopqrstuvwxyz')"
	}
	return "LOWER(" + expr + ")"
}

// Names of the locks writeTx.lock takes. Each guards a check that reads
// rows which may not exist yet, so there is no row to lock with forUpdate.
const (
	// lockAccounts: who has an account and which role, invites and reset
	// links. These change at the speed people click, so one lock for all
	// of them keeps the reasoning as simple as on SQLite.
	lockAccounts = "accounts"
	// lockProjectSlugs: a new project's slug is checked, then inserted.
	lockProjectSlugs = "project-slugs"
	// lockSourceMapBundles: whether a project has a bundle is checked, then its files are inserted.
	lockSourceMapBundles = "source-map-bundles"
	// lockMigrations: a migration is checked, run and recorded; two
	// processes that start at once run each migration once.
	lockMigrations = "migrations"
	// lockAlertStatePrefix + name: an alert's state is read, then claimed.
	lockAlertStatePrefix = "alert-state:"
)

// pool is a database/sql pool of one dialect. It has the methods of
// *sql.DB this package uses, and rebinds every query; there is deliberately
// no way to reach a method that would skip that.
type pool struct {
	db *sql.DB
	d  dialect
}

func (p *pool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, p.d.rebind(query), args...)
}

func (p *pool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, p.d.rebind(query), args...)
}

func (p *pool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.db.QueryRowContext(ctx, p.d.rebind(query), args...)
}

// BeginTx starts a write transaction: on SQLite it takes the database's
// write lock (_txlock=immediate); on Postgres it is READ COMMITTED, and
// the code inside locks what it reads before changing it (forUpdate, lock).
func (p *pool) BeginTx(ctx context.Context) (*writeTx, error) {
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	return &writeTx{tx: tx, d: p.d}, nil
}

func (p *pool) PingContext(ctx context.Context) error { return p.db.PingContext(ctx) }
func (p *pool) Stats() sql.DBStats                    { return p.db.Stats() }
func (p *pool) Close() error                          { return p.db.Close() }

// writeTx is a transaction on the write pool. Its queries are rebound like
// the pool's.
type writeTx struct {
	tx *sql.Tx
	d  dialect
}

func (t *writeTx) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return t.tx.ExecContext(ctx, t.d.rebind(query), args...)
}

func (t *writeTx) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return t.tx.QueryContext(ctx, t.d.rebind(query), args...)
}

func (t *writeTx) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return t.tx.QueryRowContext(ctx, t.d.rebind(query), args...)
}

// PrepareContext prepares a statement for the transaction; its Exec and
// Query take only the arguments, so the query is rebound here once.
func (t *writeTx) PrepareContext(ctx context.Context, query string) (*sql.Stmt, error) {
	return t.tx.PrepareContext(ctx, t.d.rebind(query))
}

func (t *writeTx) Commit() error   { return t.tx.Commit() }
func (t *writeTx) Rollback() error { return t.tx.Rollback() }

// forUpdate and forKeyShare are the dialect's, for queries written inside the transaction.
func (t *writeTx) forUpdate() string   { return t.d.forUpdate() }
func (t *writeTx) forKeyShare() string { return t.d.forKeyShare() }

// lock takes the named lock until the transaction ends; another
// transaction that asks for the same name waits until then. On Postgres it
// is a transaction-level advisory lock (keyed by a hash of the name); on
// SQLite the transaction already holds the write lock, so nothing is done.
func (t *writeTx) lock(ctx context.Context, name string) error {
	if t.d != postgresDialect {
		return nil
	}
	_, err := t.tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", name)
	return err
}
