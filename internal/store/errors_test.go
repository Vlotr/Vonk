package store

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"syscall"
	"testing"
	"time"

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
		// The caller's own deadline or a shutdown: no outage, nothing to wait for.
		// (context.DeadlineExceeded is a net.Error: it has Timeout().)
		{"deadline", context.DeadlineExceeded, false, false},
		{"deadline, wrapped", fmt.Errorf("read issues: %w", context.DeadlineExceeded), false, false},
		{"cancelled", fmt.Errorf("save batch: %w", context.Canceled), false, false},
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

// A DATABASE_URL without connect_timeout gets 5 seconds, so a dead host
// fails fast (the ingest writer retries) instead of after the system's
// TCP timeout; one that names it keeps it.
func TestPostgresConfig_ConnectTimeout(t *testing.T) {
	for url, want := range map[string]time.Duration{
		"postgres://monitor:secret@db.example.com:5432/monitor":                   5 * time.Second,
		"postgres://monitor:secret@db.example.com:5432/monitor?connect_timeout=2": 2 * time.Second,
		"postgresql://monitor@db.example.com/monitor?sslmode=require":             5 * time.Second,
	} {
		cfg, err := postgresConfig(url)
		if err != nil {
			t.Fatalf("%s: %v", url, err)
		}
		if cfg.ConnectTimeout != want {
			t.Errorf("%s: ConnectTimeout = %v, want %v", url, cfg.ConnectTimeout, want)
		}
	}
	if _, err := postgresConfig("postgres://monitor:se cret@[db/x"); err == nil || strings.Contains(err.Error(), "se cret") {
		t.Errorf("a broken URL: %v (must not repeat the password)", err)
	}
}
