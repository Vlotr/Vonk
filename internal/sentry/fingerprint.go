package sentry

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// topFrames is how many frames, counted from where the exception was thrown, decide the group.
const topFrames = 5

// Limits for the issue title and message.
const (
	maxTitleRunes   = 200
	maxMessageRunes = 1000
)

// Fingerprint returns the group key of an event: 32 hex characters. Events
// with the same fingerprint in one project become one issue.
//
// By default it uses the innermost exception's type and its top five in-app
// frames (all frames when none is in-app), each as a normalized file or
// module plus the function name, without line numbers. Without a stack trace
// it uses the type and the message with numbers, hex values and UUIDs
// replaced. A fingerprint set by the SDK is used instead; "{{ default }}" in
// it stands for the computed one.
func Fingerprint(e *Event) string {
	if len(e.Fingerprint) > 0 && !onlyDefault(e.Fingerprint) {
		parts := []string{"custom"}
		for _, value := range e.Fingerprint {
			if isDefaultMarker(value) {
				value = defaultFingerprint(e)
			}
			parts = append(parts, value)
		}
		return hash(parts)
	}
	return defaultFingerprint(e)
}

func defaultFingerprint(e *Event) string {
	exc := mainException(e)
	if exc == nil {
		return hash([]string{"message", normalizeMessage(e.Message)})
	}
	frames := groupingFrames(exc.Frames)
	if len(frames) == 0 {
		return hash([]string{"exception", exc.Type, normalizeMessage(exc.Value)})
	}
	parts := []string{"stack", exc.Type}
	for _, f := range frames {
		parts = append(parts, frameKey(f))
	}
	return hash(parts)
}

// Summary is how an issue is shown in lists.
type Summary struct {
	Title   string // the exception type, or the first line of the log message
	Message string // the exception value, or the log message
	Culprit string // "PaymentGateway.php:12": the top in-app frame (the top frame when none is in-app)
}

// Summarize returns the title, message and culprit of an event, from the
// same exception that Fingerprint uses.
func Summarize(e *Event) Summary {
	exc := mainException(e)
	if exc == nil {
		title, _, _ := strings.Cut(strings.TrimSpace(e.Message), "\n")
		if title == "" {
			title = "<unlabeled event>"
		}
		return Summary{Title: shorten(title, maxTitleRunes), Message: shorten(e.Message, maxMessageRunes)}
	}

	s := Summary{Title: exc.Type, Message: shorten(exc.Value, maxMessageRunes)}
	if s.Title == "" {
		s.Title = "Error"
	}
	s.Title = shorten(s.Title, maxTitleRunes)
	if frames := groupingFrames(exc.Frames); len(frames) > 0 {
		top := frames[len(frames)-1]
		s.Culprit = fileName(top)
		if top.Lineno > 0 && s.Culprit != "" {
			s.Culprit += ":" + strconv.Itoa(top.Lineno)
		}
	}
	return s
}

// mainException is the exception an issue is named and grouped by: the
// innermost one, which Sentry lists first. It returns nil without exceptions.
func mainException(e *Event) *Exception {
	if len(e.Exceptions) == 0 {
		return nil
	}
	return &e.Exceptions[0]
}

// groupingFrames returns the top in-app frames, or the top frames when none
// is in-app, oldest first.
func groupingFrames(frames []Frame) []Frame {
	var inApp []Frame
	for _, f := range frames {
		if f.InApp {
			inApp = append(inApp, f)
		}
	}
	if len(inApp) == 0 {
		inApp = frames
	}
	if len(inApp) > topFrames {
		inApp = inApp[len(inApp)-topFrames:]
	}
	return inApp
}

// frameKey identifies a frame without its line: module or normalized file, and function.
func frameKey(f Frame) string {
	location := f.Module
	if location == "" {
		location = normalizePath(f.Filename)
	}
	if location == "" {
		location = normalizePath(f.AbsPath)
	}
	return location + "|" + phpClosureLine.ReplaceAllString(f.Function, "}")
}

// phpClosureLine matches the line in PHP closure names such as "{closure:/routes/web.php:42}".
var phpClosureLine = regexp.MustCompile(`:\d+\}$`)

// normalizePath makes a file path or URL the same across hosts, deploys and
// builds: only the path of a URL, forward slashes, numbered directories as
// "<n>", and build hashes in file names (index-D8xk2Lp1.js) removed.
func normalizePath(p string) string {
	if p == "" {
		return ""
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	p = strings.ReplaceAll(p, `\`, "/")

	segments := strings.Split(p, "/")
	for i, segment := range segments {
		switch {
		case i == len(segments)-1:
			segments[i] = withoutBuildHash(segment)
		case isDigits(segment):
			segments[i] = "<n>"
		case len(segment) >= 16 && isHex(segment):
			segments[i] = "<hash>"
		}
	}
	return strings.Join(segments, "/")
}

// withoutBuildHash removes hash parts from a file name: "index-D8xk2Lp1.js"
// and "main.3f2a1b4c.chunk.js" become "index.js" and "main.chunk.js". The
// first part (the name) and the extension are always kept.
func withoutBuildHash(name string) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)

	var kept strings.Builder
	start := 0
	for i := 0; i <= len(stem); i++ {
		if i < len(stem) && stem[i] != '-' && stem[i] != '.' {
			continue
		}
		part := stem[start:i]
		last := i == len(stem)
		if start == 0 || !looksLikeBuildHash(part, last) {
			if start > 0 {
				kept.WriteByte(stem[start-1]) // the separator before this part
			}
			kept.WriteString(part)
		}
		start = i + 1
	}
	return kept.String() + ext
}

// looksLikeBuildHash reports whether part is a content hash a bundler put in a
// file name: 8 or more letters, digits or underscores with at least one digit,
// or, as the last part, exactly 8 of them in mixed case (Vite hashes without digits).
func looksLikeBuildHash(part string, last bool) bool {
	if len(part) < 8 {
		return false
	}
	var digit, upper, lower bool
	for _, r := range part {
		switch {
		case r >= '0' && r <= '9':
			digit = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= 'a' && r <= 'z':
			lower = true
		case r == '_':
		default:
			return false
		}
	}
	return digit || (last && len(part) == 8 && upper && lower)
}

var (
	uuidPattern  = regexp.MustCompile(`(?i)\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	hex0xPattern = regexp.MustCompile(`(?i)\b0x[0-9a-f]+\b`)
	hexPattern   = regexp.MustCompile(`(?i)\b[0-9a-f]{8,}\b`)
	digitPattern = regexp.MustCompile(`\d+`)
)

// normalizeMessage replaces the parts of a message that differ between
// occurrences of the same error: UUIDs, hex values and numbers.
func normalizeMessage(s string) string {
	s = uuidPattern.ReplaceAllString(s, "<uuid>")
	s = hex0xPattern.ReplaceAllString(s, "<hex>")
	s = hexPattern.ReplaceAllStringFunc(s, func(m string) string {
		if strings.IndexFunc(m, unicode.IsLetter) < 0 {
			return m // only digits: a number, handled below
		}
		return "<hex>"
	})
	s = digitPattern.ReplaceAllString(s, "<n>")
	return strings.TrimSpace(s)
}

// fileName is the base name of a frame's file without query, for the culprit.
func fileName(f Frame) string {
	p := f.Filename
	if p == "" {
		p = f.AbsPath
	}
	if p == "" {
		return f.Module
	}
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	return path.Base(strings.ReplaceAll(p, `\`, "/"))
}

func onlyDefault(values []string) bool {
	for _, v := range values {
		if !isDefaultMarker(v) {
			return false
		}
	}
	return true
}

// isDefaultMarker reports whether v is "{{ default }}", spaces and case aside.
func isDefaultMarker(v string) bool {
	return strings.EqualFold(strings.Join(strings.Fields(v), ""), "{{default}}")
}

func hash(parts []string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:16])
}

// shorten cuts s to at most n runes, ending with "…" when it was cut.
func shorten(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	runes := []rune(s)
	return string(runes[:n-1]) + "…"
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func isHex(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}
