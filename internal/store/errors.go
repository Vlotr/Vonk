package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Kinds of database errors that callers handle by trying again. Both are
// Postgres': SQLite holds the database for one writer at a time (and
// waits up to busyTimeoutMillis for it), and it is a file on this machine.
// A data error (DataError) is the opposite: the same write fails again.

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
// succeed unchanged once it is back. Constraint errors, a full disk, the
// caller's own deadline and the like are not.
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
	// The caller's own context ended (its deadline, a shutdown): nothing
	// says the database is away. context.DeadlineExceeded would pass for a
	// net.Error below (it has Timeout()). A connection attempt that timed
	// out is a ConnectError, caught above.
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var netErr net.Error
	return errors.As(err, &netErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

// DataError reports whether err means the database refused a value of the
// write itself: one of Postgres' data exceptions (SQLSTATE class 22, such
// as a NUL in text, 22021) or program limits (class 54, such as an index
// entry over 2704 bytes, 54000), or SQLite's "string or BLOB exceeds size
// limit". The same write fails every time, so trying it again is no use;
// the ingest writer writes the events of such a batch in smaller parts
// instead, to find the one that holds the value.
func DataError(err error) bool {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "54")
	}
	var lite *sqlite.Error
	if errors.As(err, &lite) {
		return lite.Code()&0xff == sqlite3.SQLITE_TOOBIG
	}
	return false
}

// ErrorCode names a database error by its code only ("SQLSTATE 22021",
// "SQLite 18 (SQLITE_TOOBIG)"), for a log line or an admin alert about a
// refused value: the message of a data error can quote the value, which
// may be personal data. It returns "" for an error without a code.
func ErrorCode(err error) string {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		return "SQLSTATE " + pg.Code
	}
	var lite *sqlite.Error
	if errors.As(err, &lite) {
		code := lite.Code()
		if name, ok := sqlite.ErrorCodeString[code]; ok {
			if i := strings.LastIndex(name, "("); i >= 0 {
				return fmt.Sprintf("SQLite %d %s", code, name[i:])
			}
		}
		return fmt.Sprintf("SQLite %d", code)
	}
	return ""
}
