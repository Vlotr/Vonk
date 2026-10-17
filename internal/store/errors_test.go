package store

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"syscall"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestErrorKinds(t *testing.T) {
	cases := []struct {
		name                   string
		err                    error
		retryable, unavailable bool
	}{
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true, false},
		{"serialization failure", fmt.Errorf("issue: %w", &pgconn.PgError{Code: "40001"}), true, false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false, false},
		{"foreign key violation", &pgconn.PgError{Code: "23503"}, false, false},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, false, true},
		{"starting up", &pgconn.PgError{Code: "57P03"}, false, true},
		{"connection failure", &pgconn.PgError{Code: "08006"}, false, true},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, false, true},
		{"connection cut", fmt.Errorf("failed to receive message: %w", io.ErrUnexpectedEOF), false, true},
		{"bad connection", driver.ErrBadConn, false, true},
		{"closed connection", pgconn.ErrConnClosed, false, true},
		{"disk", errors.New("disk I/O error"), false, false},
		{"none", nil, false, false},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.retryable {
			t.Errorf("%s: Retryable = %v", c.name, got)
		}
		if got := Unavailable(c.err); got != c.unavailable {
			t.Errorf("%s: Unavailable = %v", c.name, got)
		}
	}
}
