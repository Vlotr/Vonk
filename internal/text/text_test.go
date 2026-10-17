package text

import (
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestCut(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"checkout", 20, "checkout"},
		{"checkout", 8, "checkout"},
		{"checkout", 5, "check"},
		{"café", 4, "caf"}, // é is two bytes: never half of it
		{"café", 5, "café"},
		{"日本", 4, "日"},
		{"日本", 2, ""},
		{"", 3, ""},
		{"abc", 0, ""},
	} {
		if got := Cut(c.in, c.n); got != c.want {
			t.Errorf("Cut(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
	long := strings.Repeat("é", 150) // 300 bytes
	if got := Cut(long, 200); len(got) != 200 || !utf8.ValidString(got) {
		t.Errorf("Cut of 150 é to 200 bytes = %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

func TestShorten(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"Checkout failed", 20, "Checkout failed"},
		{"Checkout failed", 15, "Checkout failed"},
		{"Checkout failed", 9, "Checkout…"}, // nine characters with the "…"
		{"één twee", 4, "één…"},
	} {
		if got := Shorten(c.in, c.n); got != c.want {
			t.Errorf("Shorten(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestClean(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Checkout failed", "Checkout failed"},
		{"binding 0x\x00\x11é", "binding 0x\x11é"},
		{"\x00\x00", ""},
		{"caf\xc3", "caf�"},     // half a character
		{"a\xffb\x00c", "a�bc"}, // both at once
	} {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCleanJSON(t *testing.T) {
	v := map[string]any{
		"value":     "a\x00b",
		"user":      map[string]any{"id": "anna\x00", "n\x00ame": "Anna"},
		"list":      []any{"x\x00", map[string]any{"k": "\x00v"}, 42, nil, true},
		"ke\x00y":   "dropped: the clean key below needs no cleaning",
		"key":       "kept",
		"untouched": "plain",
	}
	want := map[string]any{
		"value":     "ab",
		"user":      map[string]any{"id": "anna", "name": "Anna"},
		"list":      []any{"x", map[string]any{"k": "v"}, 42, nil, true},
		"key":       "kept",
		"untouched": "plain",
	}
	if got := CleanJSON(v); !reflect.DeepEqual(got, want) {
		t.Errorf("CleanJSON = %#v,\nwant %#v", got, want)
	}
	if got := CleanJSON("\x00"); got != "" {
		t.Errorf("CleanJSON of a string = %q", got)
	}
}
