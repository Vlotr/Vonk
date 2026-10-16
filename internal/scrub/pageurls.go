package scrub

import (
	"strings"

	"monitor/internal/routes"
)

// maskPageURLs masks the secrets in the paths of the page addresses an
// event carries (ruling 38): request.url, the Referer header (a Laravel
// error during POST /reset-password, document.referrer in the browser),
// the "url" tag, and the URLs of breadcrumbs (a navigation's from and to,
// a request's url). So a password reset link is never stored with its
// token; digits and words stay (routes.MaskURL).
func maskPageURLs(raw map[string]any) {
	if req, ok := raw["request"].(map[string]any); ok {
		if u, ok := req["url"]; ok {
			req["url"] = maskURLs(u)
		}
		maskReferer(req["headers"])
	}
	maskURLTag(raw["tags"])
	for _, crumb := range breadcrumbList(raw["breadcrumbs"]) {
		c, _ := crumb.(map[string]any)
		data, _ := c["data"].(map[string]any)
		for _, key := range []string{"url", "from", "to"} {
			if u, ok := data[key]; ok {
				data[key] = maskURLs(u)
			}
		}
	}
}

// maskURLs masks a URL, or each URL of a list of them (sentry-php sends
// header values as lists); anything else stays as it is.
func maskURLs(v any) any {
	switch v := v.(type) {
	case string:
		return routes.MaskURL(v)
	case []any:
		for i, item := range v {
			if s, ok := item.(string); ok {
				v[i] = routes.MaskURL(s)
			}
		}
	}
	return v
}

// maskReferer masks the Referer header, in headers sent as an object or as
// a list of [name, value] pairs; header names in any case.
func maskReferer(headers any) {
	switch h := headers.(type) {
	case map[string]any:
		for name, value := range h {
			if strings.EqualFold(name, "referer") {
				h[name] = maskURLs(value)
			}
		}
	case []any:
		for _, item := range h {
			if pair, ok := item.([]any); ok && len(pair) == 2 {
				if name, ok := pair[0].(string); ok && strings.EqualFold(name, "referer") {
					pair[1] = maskURLs(pair[1])
				}
			}
		}
	}
}

// maskURLTag masks the "url" tag (tags as an object or as a list of
// pairs), which names the page when the request does not.
func maskURLTag(tags any) {
	switch t := tags.(type) {
	case map[string]any:
		if u, ok := t["url"]; ok {
			t["url"] = maskURLs(u)
		}
	case []any:
		for _, item := range t {
			if pair, ok := item.([]any); ok && len(pair) == 2 && pair[0] == "url" {
				pair[1] = maskURLs(pair[1])
			}
		}
	}
}

// breadcrumbList returns an event's breadcrumbs, sent as a list or as
// {"values": [...]}.
func breadcrumbList(v any) []any {
	switch v := v.(type) {
	case []any:
		return v
	case map[string]any:
		list, _ := v["values"].([]any)
		return list
	}
	return nil
}
