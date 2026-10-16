package store

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"monitor/internal/config"
	"monitor/internal/store/storetest"
)

// column is one column as both databases describe it: its type family,
// whether it may be NULL, and its default.
type column struct {
	Type    string // integer, text, boolean or binary
	NotNull bool
	Default string // "" for none
}

// schema is what TestPostgres_MigrationsMatchSQLite compares.
type schema struct {
	Tables  map[string]map[string]column // table → column → description
	Unique  map[string][]string          // table → its primary and unique keys, as "a,b", sorted
	Indexes map[string]string            // index name → "table(a,b)"; the ones migrations made by name
}

// The Postgres migrations make the same tables, columns, keys and indexes
// as the SQLite ones: every SQLite migration has a Postgres twin.
func TestPostgres_MigrationsMatchSQLite(t *testing.T) {
	if !storetest.Postgres() {
		t.Skip("needs Postgres: make test-postgres")
	}
	ctx := context.Background()

	dir := t.TempDir()
	lite, err := Open(ctx, config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lite.Close() })
	pg := openTestDB(t)
	if err := lite.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	liteFiles, err := lite.migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	pgFiles, err := pg.migrationFiles()
	if err != nil {
		t.Fatal(err)
	}
	if names(liteFiles) != names(pgFiles) {
		t.Errorf("migration files differ:\nsqlite   %s\npostgres %s", names(liteFiles), names(pgFiles))
	}

	want := sqliteSchema(t, lite)
	got := postgresSchema(t, pg)
	for table, cols := range want.Tables {
		if !reflect.DeepEqual(got.Tables[table], cols) {
			t.Errorf("table %s:\nsqlite   %v\npostgres %v", table, cols, got.Tables[table])
		}
	}
	for table := range got.Tables {
		if _, ok := want.Tables[table]; !ok {
			t.Errorf("table %s is only in Postgres", table)
		}
	}
	if !reflect.DeepEqual(got.Unique, want.Unique) {
		t.Errorf("keys:\nsqlite   %v\npostgres %v", want.Unique, got.Unique)
	}
	if !reflect.DeepEqual(got.Indexes, want.Indexes) {
		t.Errorf("indexes:\nsqlite   %v\npostgres %v", want.Indexes, got.Indexes)
	}

	// Ids are identity columns: Postgres never hands one out twice.
	for table, cols := range want.Tables {
		if _, ok := cols["id"]; !ok {
			continue
		}
		var identity string
		err := pg.read.QueryRowContext(ctx, `SELECT is_identity FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = ? AND column_name = 'id'`, table).Scan(&identity)
		if err != nil || identity != "YES" {
			t.Errorf("%s.id is not an identity column (%q, %v)", table, identity, err)
		}
	}
}

func names(files []migration) string {
	var out []string
	for _, f := range files {
		out = append(out, f.name)
	}
	return strings.Join(out, " ")
}

// typeFamily maps a declared column type of either database to its family.
func typeFamily(t *testing.T, declared string) string {
	switch strings.ToLower(declared) {
	case "integer", "bigint":
		return "integer"
	case "text":
		return "text"
	case "boolean":
		return "boolean"
	case "blob", "bytea":
		return "binary"
	}
	t.Errorf("unexpected column type %q", declared)
	return declared
}

// normalDefault writes a default the same way for both databases:
// Postgres adds casts ('open'::text) and writes booleans in lowercase.
func normalDefault(d string) string {
	d = strings.ToLower(d)
	if i := strings.Index(d, "::"); i >= 0 {
		d = d[:i]
	}
	return d
}

func sqliteSchema(t *testing.T, db *DB) schema {
	t.Helper()
	ctx := context.Background()
	s := schema{Tables: map[string]map[string]column{}, Unique: map[string][]string{}, Indexes: map[string]string{}}
	tables := queryList(t, db, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'")
	for _, table := range tables {
		s.Tables[table] = map[string]column{}
		rows, err := db.read.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%q)", table))
		if err != nil {
			t.Fatal(err)
		}
		pk := map[int]string{}
		for rows.Next() {
			var (
				cid, notNull, pkPos int
				name, typ           string
				dflt                *string
			)
			if err := rows.Scan(&cid, &name, &typ, &notNull, &dflt, &pkPos); err != nil {
				t.Fatal(err)
			}
			c := column{Type: typeFamily(t, typ), NotNull: notNull == 1 || pkPos > 0}
			if dflt != nil {
				c.Default = normalDefault(*dflt)
			}
			s.Tables[table][name] = c
			if pkPos > 0 {
				pk[pkPos] = name
			}
		}
		rows.Close()
		var pkCols []string
		for i := 1; i <= len(pk); i++ {
			pkCols = append(pkCols, pk[i])
		}
		keys := []string{strings.Join(pkCols, ",")}
		for _, index := range queryList(t, db, fmt.Sprintf("SELECT name FROM pragma_index_list(%q) WHERE \"unique\" = 1 AND origin <> 'pk'", table)) {
			keys = append(keys, strings.Join(queryList(t, db, fmt.Sprintf("SELECT name FROM pragma_index_info(%q) ORDER BY seqno", index)), ","))
		}
		sort.Strings(keys)
		s.Unique[table] = keys
	}
	rows, err := db.read.QueryContext(ctx, "SELECT name, tbl_name FROM sqlite_master WHERE type = 'index' AND sql IS NOT NULL")
	if err != nil {
		t.Fatal(err)
	}
	indexTables := map[string]string{}
	for rows.Next() {
		var name, table string
		if err := rows.Scan(&name, &table); err != nil {
			t.Fatal(err)
		}
		indexTables[name] = table
	}
	rows.Close()
	for name, table := range indexTables {
		cols := queryList(t, db, fmt.Sprintf("SELECT name FROM pragma_index_info(%q) ORDER BY seqno", name))
		s.Indexes[name] = table + "(" + strings.Join(cols, ",") + ")"
	}
	return s
}

func postgresSchema(t *testing.T, db *DB) schema {
	t.Helper()
	ctx := context.Background()
	s := schema{Tables: map[string]map[string]column{}, Unique: map[string][]string{}, Indexes: map[string]string{}}
	rows, err := db.read.QueryContext(ctx, `SELECT table_name, column_name, data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns WHERE table_schema = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, name, typ, nullable, dflt string
		if err := rows.Scan(&table, &name, &typ, &nullable, &dflt); err != nil {
			t.Fatal(err)
		}
		if s.Tables[table] == nil {
			s.Tables[table] = map[string]column{}
		}
		s.Tables[table][name] = column{Type: typeFamily(t, typ), NotNull: nullable == "NO", Default: normalDefault(dflt)}
	}
	rows.Close()

	// Primary and unique keys, and the indexes that are not one.
	keyColumns := `(SELECT string_agg(a.attname, ',' ORDER BY k.ord) FROM unnest(x.indkey) WITH ORDINALITY AS k(attnum, ord)
		JOIN pg_attribute a ON a.attrelid = x.indrelid AND a.attnum = k.attnum)`
	rows, err = db.read.QueryContext(ctx, `SELECT i.relname, t.relname, `+keyColumns+`,
			EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conindid = x.indexrelid AND c.contype IN ('p', 'u'))
		FROM pg_index x JOIN pg_class i ON i.oid = x.indexrelid JOIN pg_class t ON t.oid = x.indrelid
		WHERE t.relnamespace = current_schema()::regnamespace`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var index, table, cols string
		var key bool
		if err := rows.Scan(&index, &table, &cols, &key); err != nil {
			t.Fatal(err)
		}
		if key {
			s.Unique[table] = append(s.Unique[table], cols)
		} else {
			s.Indexes[index] = table + "(" + cols + ")"
		}
	}
	rows.Close()
	for table := range s.Unique {
		sort.Strings(s.Unique[table])
	}
	return s
}

func queryList(t *testing.T, db *DB, query string) []string {
	t.Helper()
	list, err := queryStrings(context.Background(), db.read, query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return list
}
