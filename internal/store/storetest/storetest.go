// Package storetest gives each test a database of its own. Without
// TEST_DATABASE_URL that is SQLite in the test's temp directory, as the
// tool runs by default. With it (make test-postgres), it is a new, empty
// schema in that Postgres database, dropped when the test ends, so tests
// never see each other's rows.
//
// Only tests import this package.
package storetest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"sync"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
)

// EnvVar names the Postgres database the tests run against, such as
// postgres://monitor:monitor@127.0.0.1:5432/monitor?sslmode=disable.
const EnvVar = "TEST_DATABASE_URL"

// Postgres reports whether the tests run against Postgres.
func Postgres() bool {
	return os.Getenv(EnvVar) != ""
}

// DatabaseURL returns the DATABASE_URL a test opens the store with: ""
// (SQLite) when TEST_DATABASE_URL is not set; else TEST_DATABASE_URL with
// search_path set to a new schema, which is dropped when the test ends.
// Call it before store.Open, so the schema is dropped after the test's
// own cleanup closed the store.
func DatabaseURL(t testing.TB) string {
	t.Helper()
	base := os.Getenv(EnvVar)
	if base == "" {
		return ""
	}
	db, err := adminDB(base)
	if err != nil {
		t.Fatalf("connect to %s: %v", EnvVar, err)
	}
	b := make([]byte, 8)
	rand.Read(b) // never fails; see crypto/rand
	schema := "test_" + hex.EncodeToString(b)
	if _, err := db.ExecContext(context.Background(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create test schema: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Errorf("drop test schema %s: %v", schema, err)
		}
	})

	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("%s: %v", EnvVar, err)
	}
	q := u.Query()
	q.Set("search_path", schema) // pgx sends unknown parameters to the server as settings
	u.RawQuery = q.Encode()
	return u.String()
}

var (
	adminOnce sync.Once
	admin     *sql.DB
	adminErr  error
)

// adminDB is the connection that makes and drops the schemas, shared by
// the tests of one package.
func adminDB(base string) (*sql.DB, error) {
	adminOnce.Do(func() {
		admin, adminErr = sql.Open("pgx", base)
		if adminErr == nil {
			adminErr = admin.PingContext(context.Background())
		}
	})
	return admin, adminErr
}
