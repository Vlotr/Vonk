// Package httpjson writes the JSON responses every endpoint shares: a value
// with a status, or an error as {"error":"..."}. Read decodes JSON requests.
// LogPath is a request's path as every log line writes it.
package httpjson

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
	"strings"
)

// Write writes v as a JSON response with status.
func Write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": message} with status.
func Error(w http.ResponseWriter, status int, message string) {
	Write(w, status, map[string]string{"error": message})
}

// LogPath is path as a log line may show it: the user activity page
// (/projects/{id}/users/{userId}) and its API
// (/ui/api/projects/{id}/users/{userId}/activity) get ":userId" in place
// of the user id, which is often an email address. path is the decoded
// path (an encoded slash is a slash), so the id may span segments. Other
// paths stay as they are. Every log line that names a request's path uses
// it: the request log, panics, and the error helpers of the dashboard API,
// sign-in and the upload API.
func LogPath(path string) string {
	for _, page := range []struct{ prefix, suffix string }{
		{"/ui/api/projects/", "/activity"},
		{"/projects/", ""},
	} {
		rest, ok := strings.CutPrefix(path, page.prefix)
		if !ok {
			continue
		}
		project, rest, ok := strings.Cut(rest, "/")
		if !ok || project == "" {
			continue
		}
		user, ok := strings.CutPrefix(rest, "users/")
		if !ok {
			continue
		}
		if user, ok = strings.CutSuffix(user, page.suffix); ok && user != "" {
			return page.prefix + project + "/users/:userId" + page.suffix
		}
	}
	return path
}

// ClientGone reports whether the request's client went away (it closed
// the page or the connection; the dashboard drops a poll when a newer one
// starts), which ends the request's context. A query that failed then is
// no failure of the tool, so callers log it at debug instead of as an
// error. Postgres stops a running query when that happens; SQLite queries
// are seldom slow enough to notice.
func ClientGone(r *http.Request) bool {
	return r.Context().Err() != nil
}

// Read decodes a request body of at most maxBytes that is one JSON value
// sent as application/json into v. When it is not, Read answers the
// request itself (415, 413 or 400) and returns false.
//
// Taking only application/json also keeps other sites out: a page
// elsewhere can make a browser post a form or text/plain unasked, but a
// JSON request needs a CORS preflight, which the dashboard never answers.
func Read(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) bool {
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		Error(w, http.StatusUnsupportedMediaType, "send JSON with Content-Type: application/json")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	err := dec.Decode(v)
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		Error(w, http.StatusRequestEntityTooLarge, "the body is too large")
		return false
	case err != nil || dec.More():
		Error(w, http.StatusBadRequest, "the body must be one JSON object with the expected fields")
		return false
	}
	return true
}
