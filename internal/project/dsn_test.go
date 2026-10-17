package project

import (
	"testing"

	"monitor/internal/store"
)

func TestDSN(t *testing.T) {
	p := store.Project{ID: 1, SentryKey: "abc"}

	for _, baseURL := range []string{"https://monitor.example.com", "https://monitor.example.com/"} {
		if got, want := DSN(baseURL, p), "https://abc@monitor.example.com/1"; got != want {
			t.Errorf("DSN(%q) = %q, want %q", baseURL, got, want)
		}
	}

	if got, want := DSN("http://localhost:8000", store.Project{ID: 42, SentryKey: "k"}), "http://k@localhost:8000/42"; got != want {
		t.Errorf("DSN with port = %q, want %q", got, want)
	}
}
