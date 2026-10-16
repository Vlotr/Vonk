// Package scrub removes sensitive values and IP addresses from SDK data
// before it is stored.
//
// Two rules apply everywhere in an event, at any depth (request headers and
// body, extra, contexts, tags, breadcrumbs, frame vars, and fields SDKs add
// later):
//
//   - a value whose key looks sensitive (IsSensitiveKey) becomes "[Filtered]",
//     also inside lists of [key, value] pairs;
//   - in a string that is a URL or path, sensitive query and fragment
//     parameters become "[Filtered]".
//
// On top of that, request cookies are dropped, and so is every IP address the
// SDKs send: user.ip_address, IP headers such as X-Forwarded-For, REMOTE_ADDR
// and friends in request.env, and the "{{auto}}" placeholder that asks the
// server to fill in the client's IP.
package scrub

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"monitor/internal/sentry"
)

// Filtered replaces every sensitive value.
const Filtered = "[Filtered]"

// sensitiveKeys are matched as substrings of a key, ignoring case and the
// characters "-", "_", "." and spaces: "X-Api-Key" matches "api_key".
var sensitiveKeys = []string{
	"password", "passwd", "pwd", "secret", "token", "api_key", "apikey", "auth", "authorization",
	"cookie", "session", "csrf", "xsrf", "credit", "card", "cvv", "iban", "ssn", "set-cookie",
}

var normalizedSensitiveKeys = func() []string {
	out := make([]string, len(sensitiveKeys))
	for i, k := range sensitiveKeys {
		out[i] = normalizeKey(k)
	}
	return out
}()

// ipKeys are headers and request.env variables that carry an IP address,
// normalized and without an "HTTP_" prefix.
var ipKeys = map[string]bool{
	"remoteaddr": true, "serveraddr": true, "xforwardedfor": true, "xrealip": true, "forwarded": true,
	"clientip": true, "xclientip": true, "cfconnectingip": true, "trueclientip": true,
	"xclusterclientip": true, "fastlyclientip": true, "xoriginalforwardedfor": true,
}

// Event scrubs ev in place, in Raw and in the typed fields.
func Event(ev *sentry.Event) {
	raw := ev.Raw
	if user, ok := raw["user"].(map[string]any); ok {
		delete(user, "ip_address")
	}
	if req, ok := raw["request"].(map[string]any); ok {
		scrubRequest(req)
	}
	Value(raw)
	ev.Refresh()
}

// IsSensitiveKey reports whether a value stored under key must be filtered.
func IsSensitiveKey(key string) bool {
	k := normalizeKey(key)
	for _, s := range normalizedSensitiveKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// Value scrubs a decoded JSON value (objects are changed in place) and
// returns the result.
func Value(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			switch {
			case isAutoIP(child):
				delete(v, k)
			case IsSensitiveKey(k):
				v[k] = Filtered
			default:
				v[k] = Value(child)
			}
		}
		return v
	case []any:
		if len(v) == 2 {
			if k, ok := v[0].(string); ok && IsSensitiveKey(k) {
				v[1] = Filtered // a [key, value] pair, such as a header
				return v
			}
		}
		out := v[:0]
		for _, child := range v {
			if !isAutoIP(child) {
				out = append(out, Value(child))
			}
		}
		return out
	case string:
		return scrubURL(v)
	}
	return v
}

// scrubRequest handles the request fields that need more than the general rules.
func scrubRequest(req map[string]any) {
	delete(req, "cookies")
	if env, ok := req["env"].(map[string]any); ok {
		for k := range env {
			if isIPKey(k) {
				delete(env, k)
			}
		}
	}
	req["headers"] = dropIPHeaders(req["headers"])
	if qs, ok := req["query_string"].(string); ok {
		req["query_string"] = scrubQuery(qs)
	}
	if body, ok := req["data"].(string); ok {
		req["data"] = scrubBody(body)
	}
}

// dropIPHeaders removes IP headers from headers sent as an object or as a list of pairs.
func dropIPHeaders(headers any) any {
	switch h := headers.(type) {
	case map[string]any:
		for k := range h {
			if isIPKey(k) {
				delete(h, k)
			}
		}
	case []any:
		out := h[:0]
		for _, pair := range h {
			if p, ok := pair.([]any); ok && len(p) > 0 {
				if name, ok := p[0].(string); ok && isIPKey(name) {
					continue
				}
			}
			out = append(out, pair)
		}
		return out
	}
	return headers
}

// scrubBody filters a request body the SDK sent as text: JSON and form
// bodies by key; other text is filtered whole when it mentions a sensitive word.
func scrubBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil {
			if out, err := encode(Value(v)); err == nil {
				return out
			}
		}
	}
	if strings.Contains(trimmed, "=") && !strings.ContainsAny(trimmed, " \t\r\n<>{}") {
		return scrubQuery(trimmed)
	}
	if IsSensitiveKey(body) {
		return Filtered
	}
	return body
}

// scrubURL filters sensitive query and fragment parameters in a string that
// looks like a URL or path (no whitespace, a "?" or "#"); other strings are
// returned as they are.
func scrubURL(s string) string {
	if !strings.ContainsAny(s, "?#") || strings.ContainsAny(s, " \t\r\n") {
		return s
	}
	rest, fragment, hasFragment := strings.Cut(s, "#")
	base, query, hasQuery := strings.Cut(rest, "?")

	var b strings.Builder
	b.WriteString(base)
	if hasQuery {
		b.WriteByte('?')
		b.WriteString(scrubQuery(query))
	}
	if hasFragment {
		b.WriteByte('#')
		b.WriteString(scrubQuery(fragment)) // OAuth puts tokens here: #access_token=...
	}
	return b.String()
}

// scrubQuery filters the values of sensitive parameters in "a=1&b=2". Keys
// are compared after percent-decoding; everything else is kept as it is.
func scrubQuery(q string) string {
	parts := strings.Split(q, "&")
	for i, part := range parts {
		key, _, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		decoded, err := url.QueryUnescape(key)
		if err != nil {
			decoded = key
		}
		if IsSensitiveKey(decoded) {
			parts[i] = key + "=" + Filtered
		}
	}
	return strings.Join(parts, "&")
}

func isIPKey(key string) bool {
	return ipKeys[strings.TrimPrefix(normalizeKey(key), "http")]
}

// isAutoIP reports whether v is "{{auto}}", the placeholder SDKs send to ask
// the server for the client's IP address.
func isAutoIP(v any) bool {
	s, ok := v.(string)
	return ok && strings.ReplaceAll(s, " ", "") == "{{auto}}"
}

// normalizeKey lowercases key and removes "-", "_", "." and spaces.
func normalizeKey(key string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '-', '_', '.', ' ':
			return -1
		}
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, key)
}

// encode writes v as compact JSON without escaping <, > and &.
func encode(v any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
