package testutil

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// SlackServer is a fake Slack incoming webhook: it records the text of
// every message posted to it and answers "ok", or 500 while told to fail.
type SlackServer struct {
	// URL is the webhook to post to, with a secret-looking path as Slack's have.
	URL string

	mu       sync.Mutex
	messages []string
	requests int
	failing  int
}

// NewSlackServer starts a fake webhook that stops when the test ends.
func NewSlackServer(t *testing.T) *SlackServer {
	t.Helper()
	s := &SlackServer{}
	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	t.Cleanup(srv.Close)
	s.URL = srv.URL + "/services/T0000TEST/B0000TEST/s3cr3tW3bh00kT0k3n"
	return s
}

func (s *SlackServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests++
	if s.failing > 0 {
		s.failing--
		http.Error(w, "internal_error", http.StatusInternalServerError)
		return
	}
	var msg struct {
		Text string `json:"text"`
	}
	if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || json.Unmarshal(body, &msg) != nil || msg.Text == "" {
		http.Error(w, "invalid_payload", http.StatusBadRequest)
		return
	}
	s.messages = append(s.messages, msg.Text)
	io.WriteString(w, "ok")
}

// Messages returns the texts received so far.
func (s *SlackServer) Messages() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.messages...)
}

// Requests returns how many posts arrived, failed ones included.
func (s *SlackServer) Requests() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

// Fail makes the next n posts fail with 500.
func (s *SlackServer) Fail(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failing = n
}
