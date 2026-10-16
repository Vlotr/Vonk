package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestRecorder_WritesRequestAndBody(t *testing.T) {
	dir := t.TempDir()
	rec := &recorder{dir: dir, response: `{"ok":true}`, logf: t.Logf}

	body := []byte{0x1f, 0x8b, 0x00, 0xff} // binary, as a gzip body would be
	req := httptest.NewRequest(http.MethodPost, "/api/1/envelope/?sentry_key=abc", bytes.NewReader(body))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Origin", "http://shop.test")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)

	if w.Code != http.StatusOK || w.Body.String() != `{"ok":true}` {
		t.Fatalf("response = %d %q", w.Code, w.Body)
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "http://shop.test" {
		t.Errorf("Access-Control-Allow-Origin = %q", got)
	}

	gotBody, err := os.ReadFile(filepath.Join(dir, "0001-POST-api-1-envelope.body"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Errorf("body = %x, want %x (unchanged)", gotBody, body)
	}

	metaJSON, err := os.ReadFile(filepath.Join(dir, "0001-POST-api-1-envelope.json"))
	if err != nil {
		t.Fatal(err)
	}
	var meta Request
	if err := json.Unmarshal(metaJSON, &meta); err != nil {
		t.Fatal(err)
	}
	if meta.Method != "POST" || meta.Path != "/api/1/envelope/" || meta.Query != "sentry_key=abc" {
		t.Errorf("meta = %+v", meta)
	}
	if meta.Headers.Get("Content-Encoding") != "gzip" || meta.BodySize != len(body) {
		t.Errorf("meta headers/size = %v / %d", meta.Headers, meta.BodySize)
	}
}

func TestRecorder_AnswersPreflight(t *testing.T) {
	rec := &recorder{dir: t.TempDir(), response: "{}", logf: t.Logf}

	req := httptest.NewRequest(http.MethodOptions, "/i/v0/e/", nil)
	req.Header.Set("Origin", "http://shop.test")
	req.Header.Set("Access-Control-Request-Headers", "content-type")
	w := httptest.NewRecorder()
	rec.ServeHTTP(w, req)

	if w.Code != http.StatusOK || w.Header().Get("Access-Control-Allow-Headers") != "content-type" {
		t.Errorf("preflight = %d %v", w.Code, w.Header())
	}
	entries, _ := os.ReadDir(rec.dir)
	if len(entries) != 0 {
		t.Errorf("preflight was recorded: %v", entries)
	}
}
