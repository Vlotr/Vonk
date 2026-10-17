package httpjson

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestRead(t *testing.T) {
	tests := []struct {
		name, contentType, body string
		code                    int // 0: Read succeeds
	}{
		{"object", "application/json", `{"name":"Webshop"}`, 0},
		{"charset", "application/json; charset=utf-8", `{"name":"Webshop"}`, 0},
		{"form", "application/x-www-form-urlencoded", `{"name":"Webshop"}`, http.StatusUnsupportedMediaType},
		{"text", "text/plain", `{"name":"Webshop"}`, http.StatusUnsupportedMediaType},
		{"no type", "", `{"name":"Webshop"}`, http.StatusUnsupportedMediaType},
		{"not JSON", "application/json", `name=Webshop`, http.StatusBadRequest},
		{"two values", "application/json", `{"name":"a"}{"name":"b"}`, http.StatusBadRequest},
		{"wrong type", "application/json", `{"name":1}`, http.StatusBadRequest},
		{"too large", "application/json", `{"name":"` + strings.Repeat("a", 100) + `"}`, http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.body))
		if tt.contentType != "" {
			req.Header.Set("Content-Type", tt.contentType)
		}
		rec := httptest.NewRecorder()
		var v struct {
			Name string `json:"name"`
		}
		ok := Read(rec, req, 64, &v)
		if tt.code == 0 {
			if !ok || v.Name != "Webshop" {
				t.Errorf("%s: Read = %v, %+v; answered %d %s", tt.name, ok, v, rec.Code, rec.Body)
			}
			continue
		}
		if ok || rec.Code != tt.code || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
			t.Errorf("%s: Read = %v, answered %d %s; want %d with a JSON error", tt.name, ok, rec.Code, rec.Body, tt.code)
		}
	}
}

func TestClientGone(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if ClientGone(r) {
		t.Error("a request in progress counts as gone")
	}
	ctx, cancel := context.WithCancel(r.Context())
	cancel()
	if !ClientGone(r.WithContext(ctx)) {
		t.Error("a request whose client went away does not count as gone")
	}
}

func TestLogPath(t *testing.T) {
	for path, want := range map[string]string{
		"/ui/api/projects/1/users/jan@example.com/activity": "/ui/api/projects/1/users/:userId/activity",
		"/ui/api/projects/12/users/team/jan/activity":       "/ui/api/projects/12/users/:userId/activity", // %2F decoded
		"/projects/1/users/jan@example.com":                 "/projects/1/users/:userId",
		"/projects/1/users/team/jan":                        "/projects/1/users/:userId",
		"/projects/1/users/":                                "/projects/1/users/",
		"/ui/api/projects/1/users//activity":                "/ui/api/projects/1/users//activity",
		"/ui/api/projects/1/issues":                         "/ui/api/projects/1/issues",
		"/ui/api/settings/users/3":                          "/ui/api/settings/users/3",
		"/api/1/envelope/":                                  "/api/1/envelope/",
	} {
		if got := LogPath(path); got != want {
			t.Errorf("LogPath(%q) = %q, want %q", path, got, want)
		}
	}
}
