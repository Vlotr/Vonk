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
