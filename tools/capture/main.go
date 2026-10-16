// Command capture is a tiny HTTP recorder for real SDK traffic. Point an SDK
// (Sentry, PostHog, sentry-cli, ...) at it, and it writes every request it
// receives to two files in the output directory:
//
//	0001-POST-api-1-envelope.json  method, path, query, host and headers
//	0001-POST-api-1-envelope.body  the body exactly as sent (still compressed)
//
// It answers every request with 200 and a small JSON body, and answers CORS
// preflights (which it does not record) for any origin, so browser SDKs can
// talk to it too.
//
// Usage:
//
//	go run ./tools/capture -addr 127.0.0.1:9999 -out /tmp/captured
//	go run ./tools/capture -response '{"flags":{}}'   # a different reply body
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Request is the metadata written next to each body.
type Request struct {
	Method   string      `json:"method"`
	Path     string      `json:"path"`
	Query    string      `json:"query"`
	Host     string      `json:"host"`
	Headers  http.Header `json:"headers"`
	BodySize int         `json:"bodySize"`
	Received time.Time   `json:"received"`
}

func main() {
	addr := flag.String("addr", "127.0.0.1:9999", "listen address")
	out := flag.String("out", "captured", "directory for the recorded requests")
	response := flag.String("response", "{}", "JSON body sent back for every request")
	flag.Parse()

	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}
	rec := &recorder{dir: *out, response: *response, logf: log.Printf}
	log.Printf("capture: listening on http://%s, writing to %s", *addr, *out)
	log.Fatal(http.ListenAndServe(*addr, rec))
}

// recorder writes each request to files and answers it.
type recorder struct {
	dir      string
	response string
	logf     func(format string, args ...any)

	mu sync.Mutex
	n  int
}

func (rec *recorder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	allowCORS(w, r)
	if r.Method == http.MethodOptions {
		rec.logf("capture: preflight %s (not recorded)", r.URL.Path)
		w.WriteHeader(http.StatusOK)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
		return
	}
	name, err := rec.save(r, body)
	if err != nil {
		rec.logf("capture: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rec.logf("capture: %s %s (%d bytes) -> %s", r.Method, r.URL.Path, len(body), name)

	w.Header().Set("Content-Type", "application/json")
	io.WriteString(w, rec.response)
}

// save writes NNNN-METHOD-path.json and .body and returns the base name.
func (rec *recorder) save(r *http.Request, body []byte) (string, error) {
	rec.mu.Lock()
	rec.n++
	n := rec.n
	rec.mu.Unlock()

	base := fmt.Sprintf("%04d-%s-%s", n, r.Method, slug(r.URL.Path))
	meta := Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		Query:    r.URL.RawQuery,
		Host:     r.Host,
		Headers:  r.Header,
		BodySize: len(body),
		Received: time.Now().UTC(),
	}
	metaJSON, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(rec.dir, base+".json"), append(metaJSON, '\n'), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(rec.dir, base+".body"), body, 0o644); err != nil {
		return "", err
	}
	return base, nil
}

// allowCORS lets browser SDKs on any origin send to the recorder and read its answers.
func allowCORS(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Credentials", "true")
	h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
	if requested := r.Header.Get("Access-Control-Request-Headers"); requested != "" {
		h.Set("Access-Control-Allow-Headers", requested)
	}
	h.Set("Access-Control-Expose-Headers", "*")
}

// slug turns a URL path into a file-name-safe word: "/api/1/envelope/" becomes "api-1-envelope".
func slug(path string) string {
	var b strings.Builder
	for _, r := range path {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				b.WriteByte('-')
			}
		}
	}
	s := strings.TrimSuffix(b.String(), "-")
	if s == "" {
		return "root"
	}
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}
