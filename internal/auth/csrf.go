package auth

import (
	"crypto/subtle"
	"net/http"

	"monitor/internal/store"
)

// CSRFHeader carries the session's CSRF token on every change the dashboard
// makes. Another site can make a browser send the session cookie along
// with a request (a form post, for one), but it cannot read the token, which
// the dashboard gets from GET /ui/api/me.
//
// Two more layers keep other sites out: the session cookie is SameSite=Lax,
// and requests that change something must be JSON (httpjson.Read), which a
// browser only sends cross-site after a CORS preflight this server never
// allows. The sign-in endpoints have no session yet and rely on those two.
const CSRFHeader = "X-CSRF-Token"

// safeMethod reports whether a method only reads.
func safeMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// validCSRF reports whether r carries the session's CSRF token.
func validCSRF(r *http.Request, session store.Session) bool {
	got := r.Header.Get(CSRFHeader)
	return got != "" && session.CSRFToken != "" &&
		subtle.ConstantTimeCompare([]byte(got), []byte(session.CSRFToken)) == 1
}
