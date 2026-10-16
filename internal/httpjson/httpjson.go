// Package httpjson writes the JSON responses every endpoint shares: a value
// with a status, or an error as {"error":"..."}.
package httpjson

import (
	"encoding/json"
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
