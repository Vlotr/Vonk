package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"monitor/internal/store"
)

// TestIngest_ConcurrentLoad sends 50 × 200 envelopes at once through the HTTP
// handler, with dashboard-like reads running next to it, and checks that every
// event is stored and that no request failed or logged a busy database.
func TestIngest_ConcurrentLoad(t *testing.T) {
	const (
		senders   = 50
		perSender = 200
		total     = senders * perSender
	)
	env := newTestEnv(t, DefaultQueueCapacity)
	var projects []store.Project
	for i := range 5 {
		projects = append(projects, env.project(t, fmt.Sprintf("Project %d", i), 10_000))
	}
	hooks := &recordingHooks{}
	w := startWriter(t, env, hooks)

	server := httptest.NewServer(env.mux)
	defer server.Close()
	client := server.Client()
	client.Transport.(*http.Transport).MaxIdleConnsPerHost = senders

	// Readers, as the dashboard would run them, while the writer works.
	stopReading := make(chan struct{})
	var readers sync.WaitGroup
	var readErrors atomic.Int64
	for range 4 {
		readers.Add(1)
		go func() {
			defer readers.Done()
			for {
				select {
				case <-stopReading:
					return
				default:
				}
				for _, p := range projects {
					if _, err := env.db.ListIssues(context.Background(), p.ID); err != nil {
						readErrors.Add(1)
					}
				}
			}
		}()
	}

	var failures atomic.Int64
	var firstFailure atomic.Value
	var senderGroup sync.WaitGroup
	start := time.Now()
	for s := range senders {
		senderGroup.Add(1)
		go func() {
			defer senderGroup.Done()
			p := projects[s%len(projects)]
			for i := range perSender {
				n := s*perSender + i
				// 20 different stacks, so issues are shared between senders.
				body := bytes.Replace(envelope(eventIDN(n), ""), []byte(`"function":"charge"`),
					[]byte(fmt.Sprintf(`"function":"charge%d"`, n%20)), 1)
				req, _ := http.NewRequest(http.MethodPost, server.URL+envelopePath(p), bytes.NewReader(body))
				authHeader(p.SentryKey)(req)
				resp, err := client.Do(req)
				if err != nil {
					failures.Add(1)
					firstFailure.CompareAndSwap(nil, err.Error())
					continue
				}
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					failures.Add(1)
					firstFailure.CompareAndSwap(nil, fmt.Sprintf("status %d", resp.StatusCode))
				}
			}
		}()
	}
	senderGroup.Wait()
	sent := time.Since(start)
	w.Close()
	close(stopReading)
	readers.Wait()

	if n := failures.Load(); n > 0 {
		t.Fatalf("%d of %d requests failed; first: %v", n, total, firstFailure.Load())
	}
	stored, timesSeen, issueCount := 0, int64(0), 0
	for _, p := range projects {
		n, err := env.db.CountErrorEvents(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		stored += n
		issues, err := env.db.ListIssues(context.Background(), p.ID)
		if err != nil {
			t.Fatal(err)
		}
		issueCount += len(issues)
		for _, issue := range issues {
			timesSeen += issue.TimesSeen
		}
	}
	if stored != total || timesSeen != total {
		t.Errorf("stored %d events, times_seen adds up to %d; want %d", stored, timesSeen, total)
	}
	if issueCount != len(projects)*20 || hooks.createdCount() != issueCount {
		t.Errorf("%d issues and %d created hooks, want %d of each", issueCount, hooks.createdCount(), len(projects)*20)
	}
	if n := readErrors.Load(); n > 0 {
		t.Errorf("%d dashboard reads failed during the load", n)
	}
	logs := env.logs.String()
	for _, bad := range []string{"database is locked", "SQLITE_BUSY", "level=ERROR", "level=WARN"} {
		if strings.Contains(logs, bad) {
			t.Errorf("log contains %q", bad)
		}
	}
	t.Logf("%d envelopes over HTTP in %v (%.0f/s)", total, sent.Round(time.Millisecond), float64(total)/sent.Seconds())
}
