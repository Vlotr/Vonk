package store

import (
	"database/sql/driver"
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// Kinds of database errors that callers handle by trying again. Both are
// Postgres': SQLite holds the database for one writer at a time (and
// waits up to busyTimeoutMillis for it), and it is a file on this machine.

// Retryable reports whether err means Postgres ended the transaction to
// get two transactions out of each other's way (a deadlock, 40P01, or a
// serialization failure, 40001): nothing was written, and the same
// transaction again normally succeeds.
func Retryable(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && (pg.Code == "40P01" || pg.Code == "40001")
}

// Unavailable reports whether err means the database could not be
// reached at all (Postgres restarting or failing over, the network to it
// cut) rather than that it refused this statement: the same write can
// succeed unchanged once it is back. Constraint errors, a full disk and
// the like are not.
func Unavailable(err error) bool {
	if err == nil {
		return false
	}
	var connect *pgconn.ConnectError
	if errors.As(err, &connect) || errors.Is(err, driver.ErrBadConn) || errors.Is(err, pgconn.ErrConnClosed) {
		return true
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		// 08: connection exceptions; 57P01-57P03: the server shuts down, crashed, or is starting up.
		return strings.HasPrefix(pg.Code, "08") || pg.Code == "57P01" || pg.Code == "57P02" || pg.Code == "57P03"
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}
