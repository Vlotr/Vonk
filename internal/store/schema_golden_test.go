package store

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// updateGolden rewrites the golden schema files: go test ./internal/store/ -run TestReferenceSchema_Golden -update
var updateGolden = flag.Bool("update", false, "rewrite testdata/reference-schema/*.txt from this binary's migrations")

// renderSchema writes a schema as text, one object after the other, with
// its SQL exactly as SQLite keeps it.
func renderSchema(rows []schemaRow) string {
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "== %s %s (table %s)\n", r.typ, r.name, r.table)
		if r.noSQL {
			b.WriteString("(no SQL: SQLite made it for a UNIQUE or PRIMARY KEY)\n")
		} else {
			b.WriteString(r.sql + "\n")
		}
	}
	return b.String()
}

// The schema a restore compares a backup with (referenceSchema), for every
// database version, is pinned as text: SQLite keeps CREATE statements as
// written, but rewrites them for ALTER TABLE ... RENAME and ADD COLUMN, so
// an update of the SQLite driver can change the text it writes. Then a
// backup that an older build of the tool made would be refused by this
// one (the comparison is byte for byte, ruling 47), so this test fails
// first. Rewrite the files with -update only when the change is understood
// and the restore of older backups is taken care of.
func TestReferenceSchema_Golden(t *testing.T) {
	ctx := context.Background()
	files := mustMigrations(t)
	dir := filepath.Join("testdata", "reference-schema")
	if *updateGolden {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for k := 1; k <= len(files); k++ {
		version := files[k-1].version
		rows, err := referenceSchema(ctx, files[:k])
		if err != nil {
			t.Fatal(err)
		}
		got := renderSchema(rows)
		path := filepath.Join(dir, fmt.Sprintf("%04d.txt", version))
		if *updateGolden {
			if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("version %d: %v (a new migration? run with -update and commit the file)", version, err)
		}
		if got != string(want) {
			t.Errorf("version %d: the reference schema is not %s any more; the first difference:\n%s", version, path, firstDifference(got, string(want)))
		}
	}
	if !*updateGolden {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != len(files) {
			t.Errorf("%s has %d files for %d migrations", dir, len(entries), len(files))
		}
	}
}

// firstDifference names the first line where got and want differ.
func firstDifference(got, want string) string {
	g, w := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(w); i++ {
		var gl, wl string
		if i < len(g) {
			gl = g[i]
		}
		if i < len(w) {
			wl = w[i]
		}
		if gl != wl {
			return fmt.Sprintf("line %d:\n  now:  %q\n  file: %q", i+1, gl, wl)
		}
	}
	return "(none)"
}
