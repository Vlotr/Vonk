// Package text holds the string helpers several packages share: cutting
// a string to a size without splitting a character, and cleaning text so
// that both databases store it.
package text

import (
	"strings"
	"unicode/utf8"
)

// Cut shortens s to at most n bytes, at a character boundary: a UTF-8
// character is never split, so the result can be shorter than n.
func Cut(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Shorten shortens s to at most n characters (not bytes), ending in "…"
// when it was longer.
func Shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

// Clean returns s without NUL characters and with invalid UTF-8 replaced
// by U+FFFD. Postgres refuses both in a text column (SQLSTATE 22021), and
// one refused value would fail the whole write it is in; SQLite would
// store them, so cleaning keeps the two databases alike. Text decoded from
// JSON is valid UTF-8 already, but "\u0000" decodes to a NUL; decoding a
// URL's percent-escapes ("%00", "%ff") can give either.
func Clean(s string) string {
	if strings.IndexByte(s, 0) >= 0 {
		s = strings.ReplaceAll(s, "\x00", "")
	}
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	return s
}

// CleanJSON cleans every string in a decoded JSON value with Clean, object
// keys too, and returns the value: objects and lists are changed in
// place. When a cleaned key equals a key the object already has ("a\x00b"
// next to "ab"), the value of the key that needed no cleaning stays.
func CleanJSON(v any) any {
	switch v := v.(type) {
	case string:
		return Clean(v)
	case map[string]any:
		for key, value := range v {
			if clean := Clean(key); clean != key {
				delete(v, key)
				if _, taken := v[clean]; taken {
					continue
				}
				key = clean
			}
			v[key] = CleanJSON(value)
		}
	case []any:
		for i, value := range v {
			v[i] = CleanJSON(value)
		}
	}
	return v
}
