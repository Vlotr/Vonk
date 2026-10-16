package httpjson

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteAndError(t *testing.T) {
	rec := httptest.NewRecorder()
	Write(rec, http.StatusCreated, map[string]int{"id": 7})
	if rec.Code != http.StatusCreated || rec.Header().Get("Content-Type") != "application/json" || rec.Body.String() != "{\"id\":7}\n" {
		t.Errorf("Write = %d %q %q", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}

	rec = httptest.NewRecorder()
	Error(rec, http.StatusNotFound, "not found")
	if rec.Code != http.StatusNotFound || rec.Body.String() != "{\"error\":\"not found\"}\n" {
		t.Errorf("Error = %d %q", rec.Code, rec.Body)
	}
}
