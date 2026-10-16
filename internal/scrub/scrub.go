// Package scrub removes sensitive values and IP addresses from SDK data
// before it is stored.
//
// Two rules apply everywhere in an event, at any depth (request headers and
// body, extra, contexts, tags, breadcrumbs, frame vars, and fields SDKs add
// later):
//
//   - a value whose key looks sensitive (IsSensitiveKey) becomes "[Filtered]";
//   - in a string that is a URL or path, sensitive query and fragment
//     parameters become "[Filtered]".
//
// The fields SDKs may send as lists of [key, value] pairs (request headers,
// query string, env and body, and tags) get the key rule per pair. Two things
// are never changed: the SDK's fingerprint, which decides grouping, and the
// source lines around a frame, which are code.
//
// On top of that, request cookies are dropped, and so is every IP address the
// SDKs send: user.ip_address, IP headers such as X-Forwarded-For, REMOTE_ADDR
// and friends in request.env, and the "{{auto}}" placeholder that asks the
// server to fill in the client's IP. The page's URL (request.url and the
// "url" tag) loses the secrets in its path: segments that look like a UUID,
// a hash or a token are masked (routes.MaskURL, ruling 38). PostHog event
// properties (Properties) get the same key and URL rules, and lose "$ip"
// and other IP fields.
package scrub

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"

	"monitor/internal/routes"
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

// sourceContextKeys hold a frame's source lines, which are left as they are.
var sourceContextKeys = map[string]bool{"pre_context": true, "context_line": true, "post_context": true}

// Event scrubs ev in place, in Raw and in the typed fields.
func Event(ev *sentry.Event) {
	raw := ev.Raw
	if user, ok := raw["user"].(map[string]any); ok {
		delete(user, "ip_address")
	}
	if req, ok := raw["request"].(map[string]any); ok {
		scrubRequest(req)
		if u, ok := req["url"].(string); ok {
			req["url"] = routes.MaskURL(u)
		}
	}
	if tags, ok := raw["tags"]; ok {
		raw["tags"] = scrubPairs(tags)
		maskURLTag(raw["tags"])
	}

	// The fingerprint is set by the developer to group events; scrubbing it
	// would merge issues ("auth-failure" contains "auth").
	fingerprint, hasFingerprint := raw["fingerprint"]
	delete(raw, "fingerprint")
	Value(raw)
	if hasFingerprint {
		raw["fingerprint"] = fingerprint
	}
	ev.Refresh()
}

// maskURLTag masks the secrets in the path of the "url" tag (an object or
// a list of pairs), which names the page when the request does not: page
// URLs are stored without the tokens and ids in their path (ruling 38).
func maskURLTag(tags any) {
	switch tags := tags.(type) {
	case map[string]any:
		if u, ok := tags["url"].(string); ok {
			tags["url"] = routes.MaskURL(u)
		}
	case []any:
		for _, item := range tags {
			if pair, ok := item.([]any); ok && len(pair) == 2 && pair[0] == "url" {
				if u, ok := pair[1].(string); ok {
					pair[1] = routes.MaskURL(u)
				}
			}
		}
	}
}

// Properties scrubs the properties of a PostHog event in place: IP
// addresses go ("$ip", "ip" and the names of IP headers, at any depth,
// and "{{auto}}" values), then the key and URL rules apply as they do to
// a Sentry event.
func Properties(props map[string]any) {
	dropPropertyIPs(props)
	Value(props)
}

// dropPropertyIPs removes the IP fields of PostHog properties, at any depth.
func dropPropertyIPs(v any) {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			if key := strings.TrimLeft(normalizeKey(k), "$"); key == "ip" || key == "ipaddress" || isIPKey(key) {
				delete(v, k)
				continue
			}
			dropPropertyIPs(child)
		}
	case []any:
		for _, child := range v {
			dropPropertyIPs(child)
		}
	}
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

// Value scrubs a decoded JSON value with the key and URL rules (objects are
// changed in place) and returns the result. Source lines (pre_context,
// context_line, post_context) are left alone.
func Value(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, child := range v {
			switch {
			case isAutoIP(child):
				delete(v, k)
			case IsSensitiveKey(k):
				v[k] = Filtered
			case sourceContextKeys[k]:
				// code, shown as it is
			default:
				v[k] = Value(child)
			}
		}
		return v
	case []any:
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
	for _, field := range []string{"headers", "env"} {
		if v, ok := req[field]; ok {
			req[field] = dropIPKeys(v)
		}
	}
	if qs, ok := req["query_string"].(string); ok {
		req["query_string"] = scrubQuery(qs)
	}
	if body, ok := req["data"].(string); ok {
		req["data"] = scrubBody(body)
	}
	for _, field := range []string{"headers", "query_string", "env", "data"} {
		if v, ok := req[field]; ok {
			req[field] = scrubPairs(v)
		}
	}
}

// scrubPairs filters the values of sensitive [key, value] pairs in a list.
// Only fields that SDKs send as pair lists go through it: elsewhere a list
// of two strings ("card-declined", "checkout") is just a list.
func scrubPairs(v any) any {
	list, ok := v.([]any)
	if !ok {
		return v
	}
	for _, item := range list {
		if pair, ok := item.([]any); ok && len(pair) == 2 {
			if key, ok := pair[0].(string); ok && IsSensitiveKey(key) {
				pair[1] = Filtered
			}
		}
	}
	return list
}

// dropIPKeys removes IP headers or env variables from an object or a list of pairs.
func dropIPKeys(v any) any {
	switch m := v.(type) {
	case map[string]any:
		for k := range m {
			if isIPKey(k) {
				delete(m, k)
			}
		}
	case []any:
		out := m[:0]
		for _, pair := range m {
			if p, ok := pair.([]any); ok && len(p) > 0 {
				if name, ok := p[0].(string); ok && isIPKey(name) {
					continue
				}
			}
			out = append(out, pair)
		}
		return out
	}
	return v
}

// scrubBody filters a request body the SDK sent as text: JSON and form
// bodies by key (a JSON list of [key, value] pairs by the pair rule); other
// text is filtered whole when it mentions a sensitive word.
func scrubBody(body string) string {
	trimmed := strings.TrimSpace(body)
	if strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[") {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		dec.UseNumber()
		var v any
		if err := dec.Decode(&v); err == nil {
			if out, err := encode(scrubPairs(Value(v))); err == nil {
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

// scrubURL filters the password of every URL in s ("mysql://root:secret@db"
// anywhere in the text), then, when s itself looks like a URL or path (no
// whitespace, a "?" or "#"), its sensitive query and fragment parameters.
func scrubURL(s string) string {
	s = scrubUserInfo(s)
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

// scrubUserInfo replaces the password of each "scheme://user:password@host"
// in s with Filtered, keeping the user name ("https://key@host", a DSN, has
// no password and stays). The host part runs to the next "/" or whitespace
// and the last "@" in it ends the user info, so a password with an
// unencoded "#", "?" or "@" is filtered whole. When the host part has no
// "@" but a ":" followed by something other than a port, the password
// holds an unencoded "/" ("postgres://app:pa/ss@db/x"), and the last "@"
// before whitespace ends it. An IPv6 host in brackets is never taken for
// a password.
func scrubUserInfo(s string) string {
	if !strings.Contains(s, "://") || !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	for {
		i := strings.Index(s, "://")
		if i < 0 {
			break
		}
		start := i + 3
		b.WriteString(s[:start])
		s = s[start:]

		end := strings.IndexAny(s, "/ \t\r\n")
		if end < 0 {
			end = len(s)
		}
		at := strings.LastIndex(s[:end], "@")
		// A host part that starts with "[" is an IPv6 address
		// ("http://[::1]:8080/"): its colons are no password.
		if colon := strings.IndexByte(s[:end], ':'); at < 0 && colon >= 0 && !strings.HasPrefix(s, "[") && !isPort(s[colon+1:end]) {
			token := strings.IndexAny(s, " \t\r\n")
			if token < 0 {
				token = len(s)
			}
			at = strings.LastIndex(s[:token], "@")
		}
		user, _, hasPassword := strings.Cut(s[:max(at, 0)], ":")
		if at < 0 || !hasPassword {
			continue
		}
		b.WriteString(user + ":" + Filtered)
		s = s[at:]
	}
	b.WriteString(s)
	return b.String()
}

// isPort reports whether s is what follows "host:" in a URL with a port:
// digits only (or nothing).
func isPort(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
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
