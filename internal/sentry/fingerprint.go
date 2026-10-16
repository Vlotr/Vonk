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
// By default it uses the main exception's type (the last one Sentry lists,
// the exception that was finally thrown) and its top five in-app frames (all
// frames when none is in-app), each as a normalized file or
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
	exc := MainException(e)
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
	// Title is the exception type ("Unhandled rejection" when the SDK caught
	// an unhandled promise rejection), or the first line of the log message.
	Title string
	// Message is the exception value, or the log message.
	Message string
	// Culprit is the top in-app frame (the top frame when none is in-app) and
	// its line: the file name for server platforms ("PaymentGateway.php:12"),
	// the URL's path for javascript ("assets/main.js:21").
	Culprit string
}

// Summarize returns the title, message and culprit of an event, from the
// same exception that Fingerprint uses.
func Summarize(e *Event) Summary {
	exc := MainException(e)
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
	if isUnhandledRejection(exc.Mechanism.Type) {
		// Named for what happened, as the designs do; the exception type
		// still shows in the stack trace and decides the fingerprint.
		s.Title = "Unhandled rejection"
	}
	s.Title = shorten(s.Title, maxTitleRunes)
	if frames := groupingFrames(exc.Frames); len(frames) > 0 {
		top := frames[len(frames)-1]
		s.Culprit = culpritFile(e, top)
		if top.Lineno > 0 && s.Culprit != "" {
			s.Culprit += ":" + strconv.Itoa(top.Lineno)
		}
	}
	return s
}

// isUnhandledRejection reports whether the SDK caught the exception as an
// unhandled promise rejection: mechanism "onunhandledrejection" (Node and
// older browser SDKs) or "auto.browser.global_handlers.onunhandledrejection"
// (@sentry/browser 8 and later).
func isUnhandledRejection(mechanism string) bool {
	return mechanism == "onunhandledrejection" || strings.HasSuffix(mechanism, ".onunhandledrejection")
}

// culpritFile names a frame's file for the culprit. Browser frames name a
// URL, and its path on the site says more than the file name alone
// ("assets/main.js"); other platforms use the file name ("CheckoutController.php").
func culpritFile(e *Event, f Frame) string {
	if strings.EqualFold(e.Platform, "javascript") {
		return sitePath(f)
	}
	return fileName(f)
}

// sitePath is a browser frame's file without origin, query, fragment and
// leading slash: "assets/main.js" for https://shop.example.com/assets/main.js?v=2.
func sitePath(f Frame) string {
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
	if strings.Contains(p, "://") {
		if u, err := url.Parse(p); err == nil {
			p = u.Path
		}
	}
	return strings.TrimLeft(p, "/")
}

// MainException is the exception an issue is named and grouped by: the last
// one in Sentry's list, which runs from the oldest cause to the exception that
// was finally thrown (Laravel's QueryException rather than the PDOException it
// wraps). It returns nil without exceptions.
func MainException(e *Event) *Exception {
	if len(e.Exceptions) == 0 {
		return nil
	}
	return &e.Exceptions[len(e.Exceptions)-1]
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
		case len(segment) >= 16 && isHex(segment), segment == ":hash":
			// ":hash" is the same directory masked before storage (routes.MaskURL).
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

// fileName is the base name of a frame's file without query, for the culprit
// of server platforms.
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
