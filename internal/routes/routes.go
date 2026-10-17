// Package routes turns the path of a page into its route: the page
// without the parts that differ per visit, so "/orders/1234" and
// "/orders/5678" count as one page, "/orders/:id".
//
// Normalize strips the query and fragment, a trailing slash and empty
// segments, then tries the project's route patterns in order, then
// replaces segments that look like ids:
//
//	digits                                   :id     /orders/1234
//	a UUID                                   :uuid   /users/550e8400-e29b-41d4-a716-446655440000
//	16 or more hex characters                :hash   /assets/0123456789abcdef
//	20 or more letters, digits, - and _,     :token  /reset/eyJhbGciOiJIUzI1NiJ9xQ
//	with a part of 8 or more characters
//	that mixes letters and digits
//
// Slugs such as "summer-sale-2026" stay: no part between dashes mixes
// letters and digits. A project fixes what the rules get wrong with a
// pattern ("/products/:slug"). Patterns and rules apply when data comes
// in; changing a pattern leaves the routes already stored as they are.
//
// Segments are compared decoded, so "/zoeken/caf%C3%A9" (as Sentry SDKs
// send a URL) and "/zoeken/café" (as browsers show it) are one route; an
// encoded "/" and "%" stay encoded, so they never add a segment.
//
// Path and MaskURL keep a page's address without the secrets in it (ruling
// 38): segments that look like a UUID, a hash or a token are masked before
// anything is stored, so a password reset link never keeps its token.
// Digits (however many) and words stay.
package routes

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"monitor/internal/text"
)

// MaxLength is the longest route, in bytes; longer ones are cut.
const MaxLength = 300

// MaxPathLength is the longest stored path, in bytes; longer ones are cut.
const MaxPathLength = 1000

// Normalize returns the route of path (a path, or a whole URL) for a
// project with these route patterns, such as "/products/:slug", in which
// ":name" stands for one segment. The first matching pattern wins; a
// pattern is read like a path ("shop/:id/" is "/shop/:id"), and blank
// patterns are skipped.
func Normalize(path string, patterns []string) string {
	segments := split(path)
	for _, pattern := range patterns {
		if p := split(pattern); len(p) > 0 && matches(p, segments) {
			return join(p)
		}
	}
	for i, s := range segments {
		segments[i] = replace(s)
	}
	return join(segments)
}

// Path returns the path of a page (a path, or a whole URL) as it is
// stored: without scheme, host, query, fragment and empty segments, each
// segment decoded, and segments that look like secrets masked (:uuid,
// :hash, :token); digits and words stay, so it still names the page. It
// is cut at MaxPathLength bytes. Normalize(Path(p)) is the route of p.
func Path(s string) string {
	segments := split(s)
	for i, seg := range segments {
		if mask := secret(seg); mask != "" {
			segments[i] = mask
		}
	}
	return text.Cut("/"+strings.Join(segments, "/"), MaxPathLength)
}

// MaskURL returns a URL (or a path) as it was sent, but with the segments
// of its path that look like secrets masked (:uuid, :hash, :token). The
// scheme, host, query and fragment stay; the query's values are scrubbed
// by key elsewhere.
func MaskURL(s string) string {
	start := 0 // where the path begins
	if i := strings.Index(s, "://"); i >= 0 && !strings.ContainsAny(s[:i], "/?#") {
		start = i + 3
		start += strings.IndexAny(s[start:]+"/", "/?#")
	} else if strings.HasPrefix(s, "//") {
		start = 2 + strings.IndexAny(s[2:]+"/", "/?#")
	}
	end := len(s)
	if i := strings.IndexAny(s[start:], "?#"); i >= 0 {
		end = start + i
	}
	if start >= end {
		return s
	}
	segments := strings.Split(s[start:end], "/")
	for i, seg := range segments {
		if mask := secret(decode(seg)); mask != "" {
			segments[i] = mask
		}
	}
	return s[:start] + strings.Join(segments, "/") + s[end:]
}

// split returns the segments of a path or URL, decoded, without the scheme
// and host, the query, the fragment and empty segments.
func split(path string) []string {
	path = strings.TrimSpace(path)
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if i := strings.Index(path, "://"); i >= 0 && !strings.Contains(path[:i], "/") {
		path = path[i+3:]
		path = path[strings.IndexByte(path+"/", '/'):] // drop the host
	} else if strings.HasPrefix(path, "//") {
		path = path[2:]
		path = path[strings.IndexByte(path+"/", '/'):]
	}
	var segments []string
	for s := range strings.SplitSeq(path, "/") {
		if s != "" {
			segments = append(segments, decode(s))
		}
	}
	return segments
}

// decode returns a segment with its percent-encoding undone, except for
// "%" and "/", which stay encoded: so decoding never adds a segment, and a
// decoded segment decodes to itself. A segment with an invalid escape
// ("100%"), or one whose escapes decode to a NUL or to invalid UTF-8
// ("a%00b", "caf%C3"), stays as it is: Postgres refuses both in text.
func decode(segment string) string {
	if !strings.Contains(segment, "%") {
		return segment
	}
	decoded, err := url.PathUnescape(segment)
	if err != nil || strings.IndexByte(decoded, 0) >= 0 || !utf8.ValidString(decoded) {
		return segment
	}
	return strings.NewReplacer("%", "%25", "/", "%2F").Replace(decoded)
}

func join(segments []string) string {
	return text.Cut("/"+strings.Join(segments, "/"), MaxLength)
}

// matches reports whether a pattern's segments match a path's: the same
// number, literal segments equal, ":name" segments anything.
func matches(pattern, segments []string) bool {
	if len(pattern) != len(segments) {
		return false
	}
	for i, p := range pattern {
		if !strings.HasPrefix(p, ":") && p != segments[i] {
			return false
		}
	}
	return true
}

// replace returns the placeholder for a segment that looks like an id, or
// the segment itself.
func replace(s string) string {
	if isDigits(s) {
		return ":id"
	}
	if mask := secret(s); mask != "" {
		return mask
	}
	return s
}

// secret returns the placeholder for a segment that may be a secret (a
// UUID, a hash, a token), or "". Digits are never a secret, however many:
// an order number of 16 or more digits is hex too, but stays.
func secret(s string) string {
	switch {
	case isDigits(s):
		return ""
	case isUUID(s):
		return ":uuid"
	case len(s) >= 16 && isHex(s):
		return ":hash"
	case isToken(s):
		return ":token"
	}
	return ""
}

func isDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return false
		}
	}
	return s != ""
}

// isUUID reports whether s is 8-4-4-4-12 hex characters.
func isUUID(s string) bool {
	parts := strings.Split(s, "-")
	if len(parts) != 5 {
		return false
	}
	for i, n := range []int{8, 4, 4, 4, 12} {
		if len(parts[i]) != n || !isHex(parts[i]) {
			return false
		}
	}
	return true
}

// isToken reports whether s looks like a random token: at least 20
// letters, digits, "-" and "_", with a part between them of at least 8
// characters that has both letters and digits.
func isToken(s string) bool {
	if len(s) < 20 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c == '-' || c == '_') {
			return false
		}
	}
	for part := range strings.FieldsFuncSeq(s, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(part) >= 8 && strings.ContainsAny(part, "0123456789") && strings.ContainsFunc(part, isLetter) {
			return true
		}
	}
	return false
}

func isLetter(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
}
