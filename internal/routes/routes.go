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
package routes

import "strings"

// MaxLength is the longest route, in bytes; longer ones are cut.
const MaxLength = 300

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

// split returns the segments of a path or URL, without the scheme and
// host, the query, the fragment and empty segments.
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
			segments = append(segments, s)
		}
	}
	return segments
}

func join(segments []string) string {
	route := "/" + strings.Join(segments, "/")
	if len(route) > MaxLength {
		route = route[:MaxLength]
	}
	return route
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
	switch {
	case isDigits(s):
		return ":id"
	case isUUID(s):
		return ":uuid"
	case len(s) >= 16 && isHex(s):
		return ":hash"
	case isToken(s):
		return ":token"
	}
	return s
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
