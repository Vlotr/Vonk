// Package httpjson writes the JSON responses every endpoint shares: a value
// with a status, or an error as {"error":"..."}. Read decodes JSON requests.
package httpjson

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"
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
