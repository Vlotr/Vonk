package ingest

import "net/http"

// Browser SDKs send from the visitor's page, so every origin may post
// envelopes. They send no cookies, which is why "*" is enough.
const (
	corsAllowMethods = "POST, OPTIONS"
	corsAllowHeaders = "content-type, content-encoding, x-sentry-auth, authorization, sentry-trace, baggage"
	// The browser SDK reads these to back off.
	corsExposeHeaders = "x-sentry-error, x-sentry-rate-limits, retry-after"
	corsMaxAgeSeconds = "86400"
)

// allowCORS adds the headers every answer to a browser needs, errors included.
func allowCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", corsExposeHeaders)
}

// preflight answers the browser's OPTIONS request before a cross-origin POST.
func preflight(w http.ResponseWriter) {
	h := w.Header()
	allowCORS(h)
	h.Set("Access-Control-Allow-Methods", corsAllowMethods)
	h.Set("Access-Control-Allow-Headers", corsAllowHeaders)
	h.Set("Access-Control-Max-Age", corsMaxAgeSeconds)
	w.WriteHeader(http.StatusOK)
}

// PostHog SDKs read flags and configuration with GET and send events with
// POST; posthog-js can add headers of the site's choosing (request_headers).
const (
	posthogAllowMethods  = "GET, POST, OPTIONS"
	posthogAllowHeaders  = "content-type, content-encoding"
	posthogExposeHeaders = "retry-after"
)

// allowPosthogCORS adds the headers every answer to posthog-js needs, errors included.
func allowPosthogCORS(h http.Header) {
	h.Set("Access-Control-Allow-Origin", "*")
	h.Set("Access-Control-Expose-Headers", posthogExposeHeaders)
}

// posthogPreflight answers the OPTIONS request before a cross-origin
// request. It allows the headers the browser asks for: the answers carry
// no cookies and need no credentials, so any header is harmless.
func posthogPreflight(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	allowPosthogCORS(h)
	h.Set("Access-Control-Allow-Methods", posthogAllowMethods)
	allowed := posthogAllowHeaders
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" && isHeaderList(requested) {
		allowed = requested
	}
	h.Set("Access-Control-Allow-Headers", allowed)
	h.Set("Access-Control-Max-Age", corsMaxAgeSeconds)
	w.WriteHeader(http.StatusOK)
}

// isHeaderList reports whether s is a comma-separated list of header names.
func isHeaderList(s string) bool {
	if len(s) > 1000 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == ',' || c == ' ') {
			return false
		}
	}
	return true
}
