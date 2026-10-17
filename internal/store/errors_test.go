package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

func TestErrorKinds(t *testing.T) {
	cases := []struct {
		name                         string
		err                          error
		retryable, unavailable, data bool
	}{
		{"deadlock", &pgconn.PgError{Code: "40P01"}, true, false, false},
		{"serialization failure", fmt.Errorf("issue: %w", &pgconn.PgError{Code: "40001"}), true, false, false},
		{"unique violation", &pgconn.PgError{Code: "23505"}, false, false, false},
		{"foreign key violation", &pgconn.PgError{Code: "23503"}, false, false, false},
		{"admin shutdown", &pgconn.PgError{Code: "57P01"}, false, true, false},
		{"starting up", &pgconn.PgError{Code: "57P03"}, false, true, false},
		{"connection failure", &pgconn.PgError{Code: "08006"}, false, true, false},
		{"connection refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, false, true, false},
		{"connection cut", fmt.Errorf("failed to receive message: %w", io.ErrUnexpectedEOF), false, true, false},
		{"bad connection", driver.ErrBadConn, false, true, false},
		{"closed connection", pgconn.ErrConnClosed, false, true, false},
		{"disk", errors.New("disk I/O error"), false, false, false},
		{"disk full", &pgconn.PgError{Code: "53100"}, false, false, false},
		// A value of the write itself: a NUL (22021), a character the
		// encoding lacks (22P05), an index entry too large (54000).
		{"NUL in text", fmt.Errorf("event 1: %w", &pgconn.PgError{Code: "22021"}), false, false, true},
		{"untranslatable character", &pgconn.PgError{Code: "22P05"}, false, false, true},
		{"index entry too large", &pgconn.PgError{Code: "54000"}, false, false, true},
		// The caller's own deadline or a shutdown: no outage, nothing to wait for.
		// (context.DeadlineExceeded is a net.Error: it has Timeout().)
		{"deadline", context.DeadlineExceeded, false, false, false},
		{"deadline, wrapped", fmt.Errorf("read issues: %w", context.DeadlineExceeded), false, false, false},
		{"cancelled", fmt.Errorf("save batch: %w", context.Canceled), false, false, false},
		{"none", nil, false, false, false},
	}
	for _, c := range cases {
		if got := Retryable(c.err); got != c.retryable {
			t.Errorf("%s: Retryable = %v", c.name, got)
		}
		if got := Unavailable(c.err); got != c.unavailable {
			t.Errorf("%s: Unavailable = %v", c.name, got)
		}
		if got := DataError(c.err); got != c.data {
			t.Errorf("%s: DataError = %v", c.name, got)
		}
	}
	if got := ErrorCode(fmt.Errorf("event 1: %w", &pgconn.PgError{Code: "22021", Message: "secret value"})); got != "SQLSTATE 22021" {
		t.Errorf("ErrorCode = %q, want SQLSTATE 22021 (the code only: a message can hold the value)", got)
	}
}

// SQLite refuses a string over its length limit (SQLITE_TOOBIG); that is
// a data error too. Its limit is a billion bytes, so this lowers it on
// one connection of its own.
func TestDataError_SQLiteStringTooBig(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "limit.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := sqlite.Limit(conn, sqlite3.SQLITE_LIMIT_LENGTH, 100); err != nil {
		t.Fatal(err)
	}
	_, err = conn.ExecContext(context.Background(), "SELECT ?", strings.Repeat("x", 1000))
	if !DataError(err) {
		t.Errorf("DataError(%v) = false, want true", err)
	}
	if got := ErrorCode(err); got != "SQLite 18 (SQLITE_TOOBIG)" {
		t.Errorf("ErrorCode = %q", got)
	}
}

// randomHex returns n random bytes as hex.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b) // never fails; see crypto/rand
	return hex.EncodeToString(b)
}

// The values the review reproduced on Postgres 17 are data errors there: a
// NUL in a text column (22021) and a 6 KB user id in the user index
// (54000). SQLite stores both.
func TestSaveErrorEvents_RefusedValuesAreDataErrors(t *testing.T) {
	db := openTestDB(t)
	p, err := db.CreateProject(context.Background(), NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Now()
	event := func(id, user, message string) NewErrorEvent {
		return NewErrorEvent{ProjectID: p.ID, EventID: id, Fingerprint: "f", Title: "RuntimeException", Message: message,
			Level: "error", Timestamp: now, ReceivedAt: now, UserID: user, Data: "{}"}
	}
	for name, ev := range map[string]NewErrorEvent{
		"NUL":          event("1", "42", "binding 0x\x00"),
		"6 KB user id": event("2", randomHex(3000), "failed"), // random, so Postgres cannot compress the entry
	} {
		_, err := db.SaveErrorEvents(ctx, []NewErrorEvent{ev})
		if !db.Postgres() {
			if err != nil {
				t.Errorf("%s: SQLite refused it: %v", name, err)
			}
			continue
		}
		if !DataError(err) {
			t.Errorf("%s: err = %v, want a data error", name, err)
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
