package scrub

import (
	"strings"

	"monitor/internal/routes"
)

// maskPageURLs masks the secrets in the paths of the page addresses an
// event carries (ruling 38): request.url, the Referer header (a Laravel
// error during POST /reset-password, document.referrer in the browser),
// the "url" tag, the URLs of breadcrumbs (a navigation's from and to, a
// request's url), frames' http(s) files (an inline <script> is the page
// itself) with the debug images that name them, and a transaction that is
// a path. So a password reset link is never stored with its token; digits
// and words stay (routes.MaskURL). Symbolication runs before this, while
// the frames still have the URLs their source maps were uploaded for.
func maskPageURLs(raw map[string]any) {
	if req, ok := raw["request"].(map[string]any); ok {
		if u, ok := req["url"]; ok {
			req["url"] = maskURLs(u)
		}
		maskReferer(req["headers"])
	}
	maskURLTag(raw["tags"])
	maskFrameURLs(raw)
	if tx, ok := raw["transaction"].(string); ok && looksLikeURL(tx) {
		raw["transaction"] = routes.MaskURL(tx)
	}
	for _, crumb := range valuesList(raw["breadcrumbs"]) {
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

// maskFrameURLs masks the files of frames that are http(s) URLs: in the
// stack traces of exceptions (also the minified frames symbolication keeps
// in raw_stacktrace), of threads and of the event itself, and the debug
// images' code_file, which names the same files.
func maskFrameURLs(raw map[string]any) {
	var traces []any
	for _, key := range []string{"exception", "threads"} {
		for _, value := range valuesList(raw[key]) {
			if v, ok := value.(map[string]any); ok {
				traces = append(traces, v["stacktrace"], v["raw_stacktrace"])
			}
		}
	}
	traces = append(traces, raw["stacktrace"])
	for _, trace := range traces {
		st, _ := trace.(map[string]any)
		frames, _ := st["frames"].([]any)
		for _, f := range frames {
			frame, _ := f.(map[string]any)
			for _, key := range []string{"filename", "abs_path"} {
				if file, ok := frame[key].(string); ok && isHTTPURL(file) {
					frame[key] = routes.MaskURL(file)
				}
			}
		}
	}
	debugMeta, _ := raw["debug_meta"].(map[string]any)
	images, _ := debugMeta["images"].([]any)
	for _, img := range images {
		image, _ := img.(map[string]any)
		if file, ok := image["code_file"].(string); ok && isHTTPURL(file) {
			image["code_file"] = routes.MaskURL(file)
		}
	}
}

// valuesList returns a list SDKs send bare or as {"values": [...]}
// (breadcrumbs, exceptions, threads).
func valuesList(v any) []any {
	switch v := v.(type) {
	case []any:
		return v
	case map[string]any:
		list, _ := v["values"].([]any)
		return list
	}
	return nil
}

func isHTTPURL(s string) bool {
	lower := strings.ToLower(s)
	return strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://")
}

// looksLikeURL reports whether a transaction names a page (a path, or an
// http(s) URL) rather than a route or a controller.
func looksLikeURL(s string) bool {
	return strings.HasPrefix(s, "/") || isHTTPURL(s)
}
