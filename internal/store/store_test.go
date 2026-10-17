package store

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"monitor/internal/config"
	"monitor/internal/store/storetest"
)

// openTestDB opens and migrates a fresh database (see openUnmigrated).
func openTestDB(t *testing.T) *DB {
	t.Helper()
	db, _ := openUnmigrated(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db
}

func TestOpen_SQLitePragmas(t *testing.T) {
	if storetest.Postgres() {
		t.Skip("SQLite only")
	}
	db := openTestDB(t)
	ctx := context.Background()

	pools := map[string]*pool{"write": db.write, "read": db.read}
	for name, pool := range pools {
		t.Run(name, func(t *testing.T) {
			var journalMode string
			if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
				t.Fatal(err)
			}
			if journalMode != "wal" {
				t.Errorf("journal_mode = %q, want wal", journalMode)
			}

			var foreignKeys int
			if err := pool.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
				t.Fatal(err)
			}
			if foreignKeys != 1 {
				t.Errorf("foreign_keys = %d, want 1", foreignKeys)
			}

			var busyTimeout int
			if err := pool.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
				t.Fatal(err)
			}
			if busyTimeout < 5000 {
				t.Errorf("busy_timeout = %d, want >= 5000", busyTimeout)
			}
		})
	}

	if got := db.write.Stats().MaxOpenConnections; got != 1 {
		t.Errorf("write pool MaxOpenConnections = %d, want 1", got)
	}

	// The writer commits with synchronous=NORMAL (1): safe in WAL mode and much
	// faster than FULL. Readers never write, so only the writer matters.
	var synchronous int
	if err := db.write.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if synchronous != 1 {
		t.Errorf("write pool synchronous = %d, want 1 (NORMAL)", synchronous)
	}
}

func TestPing_ChecksBothPools(t *testing.T) {
	ctx := context.Background()

	if err := openTestDB(t).Ping(ctx); err != nil {
		t.Fatalf("Ping on a working database: %v", err)
	}

	broken := openTestDB(t)
	broken.write.Close()
	if err := broken.Ping(ctx); err == nil {
		t.Error("Ping with a closed write connection: want error, got nil")
	}

	broken = openTestDB(t)
	broken.read.Close()
	if err := broken.Ping(ctx); err == nil {
		t.Error("Ping with a closed read pool: want error, got nil")
	}
}

func TestOpen_ReadPoolCannotWrite(t *testing.T) {
	db := openTestDB(t)

	_, err := db.read.ExecContext(context.Background(), "CREATE TABLE should_fail (id INTEGER)")
	if err == nil {
		t.Fatal("write through the read pool succeeded, want an error")
	}
}

// A DATABASE_URL of another kind is refused before anything is opened
// (config.Load refuses it too), and a Postgres URL that cannot be parsed
// or reached gives an error without its password.
func TestOpen_RejectsOtherURLs(t *testing.T) {
	ctx := context.Background()
	for _, raw := range []string{"mysql://monitor:secret@db/monitor", "sqlite:///data/monitor.db"} {
		if _, err := Open(ctx, config.Config{DatabaseURL: raw}); err == nil || !strings.Contains(err.Error(), "postgres://") {
			t.Errorf("Open(%q) = %v, want the postgres:// hint", raw, err)
		}
	}
	for _, raw := range []string{"postgres://monitor:s3cret-pw@127.0.0.1:1/monitor?connect_timeout=1", "postgres://monitor:s3cret-pw@[::1/monitor"} {
		_, err := Open(ctx, config.Config{DatabaseURL: raw})
		if err == nil || strings.Contains(err.Error(), "s3cret-pw") {
			t.Errorf("Open(%q) = %v, want an error without the password", raw, err)
		}
	}
}

// On Postgres the write pool has a few connections (writers lock what they
// read, see dialect.go) and the read pool's sessions are read-only.
func TestOpen_PostgresPools(t *testing.T) {
	if !storetest.Postgres() {
		t.Skip("needs Postgres: make test-postgres")
	}
	db := openTestDB(t)
	ctx := context.Background()
	if !db.Postgres() || db.d != postgresDialect {
		t.Fatal("Postgres() = false for a Postgres DATABASE_URL")
	}
	if got := db.write.Stats().MaxOpenConnections; got != postgresWriteConns {
		t.Errorf("write pool MaxOpenConnections = %d, want %d", got, postgresWriteConns)
	}
	var readOnly string
	if err := db.read.QueryRowContext(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil || readOnly != "on" {
		t.Errorf("read pool default_transaction_read_only = %q, %v; want on", readOnly, err)
	}
	if err := db.write.QueryRowContext(ctx, "SHOW default_transaction_read_only").Scan(&readOnly); err != nil || readOnly != "off" {
		t.Errorf("write pool default_transaction_read_only = %q, %v; want off", readOnly, err)
	}
	if db.SizeBytes(ctx) <= 0 {
		t.Error("SizeBytes = 0 for a Postgres database")
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	db := openTestDB(t) // migrated once
	ctx := context.Background()

	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}

	files, err := db.migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no migration files embedded")
	}

	var rows int
	if err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != len(files) {
		t.Errorf("schema_migrations has %d rows, want %d (one per file)", rows, len(files))
	}

	for _, table := range []string{"projects", "issues", "error_events", "users", "project_access", "sessions", "invites", "invite_projects", "password_resets",
		"alerts", "alert_state", "settings", "pageviews", "pageleaves", "hourly_stats", "stats_dirty_hours", "custom_events", "event_hourly"} {
		query := "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?"
		if db.Postgres() {
			query = "SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = ?"
		}
		var name string
		if err := db.read.QueryRowContext(ctx, query, table).Scan(&name); err != nil {
			t.Errorf("table %s missing after migrate: %v", table, err)
		}
	}
}

var (
	sentryKeyPattern    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	posthogTokenPattern = regexp.MustCompile(`^phc_[A-Za-z0-9_-]{32,}$`)
)

func TestCreateProject_GeneratesKeys(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	p, err := db.CreateProject(ctx, NewProject{Name: "Customer portal", TracksLaravel: true})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	if p.ID == 0 {
		t.Error("ID is 0")
	}
	if p.Name != "Customer portal" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Slug != "customer-portal" {
		t.Errorf("Slug = %q, want customer-portal", p.Slug)
	}
	if !sentryKeyPattern.MatchString(p.SentryKey) {
		t.Errorf("SentryKey = %q, want 32 lowercase hex characters", p.SentryKey)
	}
	if !posthogTokenPattern.MatchString(p.PosthogToken) {
		t.Errorf("PosthogToken = %q, want phc_ + at least 32 url-safe characters", p.PosthogToken)
	}
	if p.RetentionDays == nil || *p.RetentionDays != 90 {
		t.Errorf("RetentionDays = %v, want 90", p.RetentionDays)
	}
	if !p.TracksLaravel || p.TracksBrowser || p.TracksPageviews {
		t.Errorf("tracking flags = %v/%v/%v, want true/false/false", p.TracksLaravel, p.TracksBrowser, p.TracksPageviews)
	}
	if p.RateLimitPerMinute != 1000 {
		t.Errorf("RateLimitPerMinute = %d, want 1000", p.RateLimitPerMinute)
	}
	if p.CreatedAt.IsZero() {
		t.Error("CreatedAt is zero")
	}

	// Same name again: the slug gets a suffix, the keys are new.
	p2, err := db.CreateProject(ctx, NewProject{Name: "Customer portal"})
	if err != nil {
		t.Fatalf("second CreateProject: %v", err)
	}
	if p2.Slug != "customer-portal-2" {
		t.Errorf("second Slug = %q, want customer-portal-2", p2.Slug)
	}
	if p2.SentryKey == p.SentryKey || p2.PosthogToken == p.PosthogToken {
		t.Error("second project reuses the keys of the first")
	}

	p3, err := db.CreateProject(ctx, NewProject{Name: "Customer  Portal!"})
	if err != nil {
		t.Fatalf("third CreateProject: %v", err)
	}
	if p3.Slug != "customer-portal-3" {
		t.Errorf("third Slug = %q, want customer-portal-3", p3.Slug)
	}
}

func TestCreateProject_RetentionChoices(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	forever, err := db.CreateProject(ctx, NewProject{Name: "Archive", KeepForever: true})
	if err != nil {
		t.Fatal(err)
	}
	if forever.RetentionDays != nil {
		t.Errorf("KeepForever: RetentionDays = %d, want nil", *forever.RetentionDays)
	}
	stored, err := db.ProjectByID(ctx, forever.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.RetentionDays != nil {
		t.Errorf("KeepForever after reload: RetentionDays = %d, want nil", *stored.RetentionDays)
	}

	short, err := db.CreateProject(ctx, NewProject{Name: "Short", RetentionDays: 30})
	if err != nil {
		t.Fatal(err)
	}
	if short.RetentionDays == nil || *short.RetentionDays != 30 {
		t.Errorf("RetentionDays = %v, want 30", short.RetentionDays)
	}
}

func TestCreateProject_StoresTrackingFlags(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	created, err := db.CreateProject(ctx, NewProject{
		Name:               "Everything",
		TracksLaravel:      true,
		TracksBrowser:      true,
		TracksPageviews:    true,
		RateLimitPerMinute: 250,
	})
	if err != nil {
		t.Fatal(err)
	}

	stored, err := db.ProjectByID(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.TracksLaravel || !stored.TracksBrowser || !stored.TracksPageviews {
		t.Errorf("tracking flags after reload = %v/%v/%v, want true/true/true",
			stored.TracksLaravel, stored.TracksBrowser, stored.TracksPageviews)
	}
	if stored.RateLimitPerMinute != 250 {
		t.Errorf("RateLimitPerMinute after reload = %d, want 250", stored.RateLimitPerMinute)
	}
	if !reflect.DeepEqual(stored, created) {
		t.Errorf("reloaded project = %+v, want %+v", stored, created)
	}
}

func TestCreateProject_RequiresName(t *testing.T) {
	db := openTestDB(t)

	if _, err := db.CreateProject(context.Background(), NewProject{Name: "   "}); err == nil {
		t.Fatal("CreateProject with a blank name: want error, got nil")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Customer portal":   "customer-portal",
		"  Webshop  ":       "webshop",
		"API v2 / Backend":  "api-v2-backend",
		"Café Noir":         "caf-noir",
		"!!!":               "project",
		"already-a-slug_ok": "already-a-slug-ok",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProjectLookups(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	webshop, err := db.CreateProject(ctx, NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := db.CreateProject(ctx, NewProject{Name: "Admin"})
	if err != nil {
		t.Fatal(err)
	}

	byKey, err := db.ProjectBySentryKey(ctx, webshop.SentryKey)
	if err != nil {
		t.Fatalf("ProjectBySentryKey: %v", err)
	}
	if !reflect.DeepEqual(byKey, webshop) {
		t.Errorf("ProjectBySentryKey = %+v, want %+v", byKey, webshop)
	}

	byID, err := db.ProjectByID(ctx, admin.ID)
	if err != nil {
		t.Fatalf("ProjectByID: %v", err)
	}
	if byID.Name != "Admin" {
		t.Errorf("ProjectByID name = %q", byID.Name)
	}

	if _, err := db.ProjectBySentryKey(ctx, "unknown"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown key: err = %v, want ErrNotFound", err)
	}
	if _, err := db.ProjectByID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown id: err = %v, want ErrNotFound", err)
	}

	list, err := db.ListProjects(ctx)
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 2 || list[0].Name != "Admin" || list[1].Name != "Webshop" {
		t.Errorf("ListProjects = %+v, want Admin then Webshop", list)
	}
}

// Both pools are in defensive mode with an untrusted schema, also for a
// database that came from a restore.
func TestOpen_SQLiteDefensive(t *testing.T) {
	if storetest.Postgres() {
		t.Skip("SQLite only")
	}
	db := openTestDB(t)
	ctx := context.Background()
	for name, pool := range map[string]*pool{"write": db.write, "read": db.read} {
		var trusted int
		if err := pool.QueryRowContext(ctx, "PRAGMA trusted_schema").Scan(&trusted); err != nil || trusted != 0 {
			t.Errorf("%s pool: trusted_schema = %d, %v; want 0", name, trusted, err)
		}
	}
	// Defensive mode refuses writable_schema, the way to rewrite sqlite_master by hand.
	if _, err := db.write.ExecContext(ctx, "PRAGMA writable_schema = ON"); err == nil {
		var on int
		db.write.QueryRowContext(ctx, "PRAGMA writable_schema").Scan(&on)
		if on != 0 {
			t.Error("writable_schema could be turned on: the write pool is not in defensive mode")
		}
	}
}

// A database that a newer version of the tool updated is refused, with
// what to do, before anything is written: this version does not know the
// newer tables and could damage them (ruling 48).
func TestMigrate_RefusesADatabaseANewerVersionUpdated(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	files := mustMigrations(t)
	newest := files[len(files)-1].version
	if _, err := db.write.ExecContext(ctx, "INSERT INTO schema_migrations (version, name, applied_at) VALUES (?, ?, ?)",
		newest+1, "9999_from_a_newer_version.sql", 0); err != nil {
		t.Fatal(err)
	}

	err := db.Migrate(ctx)
	if !errors.Is(err, ErrNewerDatabase) {
		t.Fatalf("Migrate on a newer database = %v, want ErrNewerDatabase", err)
	}
	wayBack := "pre-migrate" // SQLite's automatic backup; Postgres has the pg_dump the operator made
	if db.Postgres() {
		wayBack = "pg_dump"
	}
	for _, want := range []string{fmt.Sprintf("version %d", newest+1), fmt.Sprintf("up to %d", newest), wayBack} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not say %q", err, want)
		}
	}
	var rows int
	if err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&rows); err != nil || rows != newest+1 {
		t.Errorf("schema_migrations has %d rows (%v), want %d: nothing written", rows, err, newest+1)
	}
	// The pre-migrate backup has nothing to do either.
	if path, err := db.BackupBeforeMigrate(ctx); path != "" || (err != nil && !errors.Is(err, ErrNoBackups)) {
		t.Errorf("BackupBeforeMigrate on a newer database = %q, %v; want no backup", path, err)
	}
}
