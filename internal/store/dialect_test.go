package store

import "testing"

// rebind numbers the ? placeholders for Postgres and leaves every ? inside
// a string, a quoted name or a comment as it is.
func TestRebind(t *testing.T) {
	cases := []struct{ name, query, want string }{
		{"placeholders", "SELECT a FROM t WHERE b = ? AND c IN (?, ?)", "SELECT a FROM t WHERE b = $1 AND c IN ($2, $3)"},
		{"none", "SELECT 1", "SELECT 1"},
		{"in a string", "SELECT '?' FROM t WHERE b = ?", "SELECT '?' FROM t WHERE b = $1"},
		{"a doubled quote in a string", "SELECT 'it''s ?' FROM t WHERE b = ?", "SELECT 'it''s ?' FROM t WHERE b = $1"},
		{"a string of quotes", "SELECT '''' , ? , ''", "SELECT '''' , $1 , ''"},
		{"in a quoted name", `SELECT "odd?name" FROM t WHERE "b" = ?`, `SELECT "odd?name" FROM t WHERE "b" = $1`},
		{"in a comment", "SELECT a -- why? because\nFROM t WHERE b = ?", "SELECT a -- why? because\nFROM t WHERE b = $1"},
		{"a comment at the end", "SELECT ? -- the end?", "SELECT $1 -- the end?"},
		{"a minus is no comment", "SELECT ? - ?", "SELECT $1 - $2"},
		{"unterminated string", "SELECT ? WHERE a = 'open ?", "SELECT $1 WHERE a = 'open ?"},
		{"ten and more", "VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)", "VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)"},
		{"multibyte text", "SELECT 'é?' , ?", "SELECT 'é?' , $1"},
	}
	for _, c := range cases {
		if got := postgresDialect.rebind(c.query); got != c.want {
			t.Errorf("%s: rebind(%q)\n got %q\nwant %q", c.name, c.query, got, c.want)
		}
		if got := sqliteDialect.rebind(c.query); got != c.query {
			t.Errorf("%s: SQLite changed the query to %q", c.name, got)
		}
	}
}
