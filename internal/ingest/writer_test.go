package ingest

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

// recordingHooks remembers the hook calls.
type recordingHooks struct {
	mu        sync.Mutex
	created   []store.Issue
	regressed []store.Issue
}

func (h *recordingHooks) OnIssueCreated(_ context.Context, issue store.Issue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.created = append(h.created, issue)
}

func (h *recordingHooks) OnIssueRegressed(_ context.Context, issue store.Issue) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.regressed = append(h.regressed, issue)
}

func (h *recordingHooks) createdCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.created)
}

// startWriter runs a writer on env's queue. Close is called at cleanup too.
func startWriter(t *testing.T, env *testEnv, hooks *recordingHooks, setup ...func(*Writer)) *Writer {
	t.Helper()
	w := NewWriter(env.db, env.queue, hooks, env.logger)
	for _, f := range setup {
		f(w)
	}
	go w.Run(context.Background())
	t.Cleanup(w.Close)
	return w
}

// errorItem builds a queued event. function names the top frame; ts is the event time.
func errorItem(t *testing.T, projectID int64, eventID, function string, ts, received time.Time) Item {
	t.Helper()
	event := map[string]any{
		"event_id": eventID,
		"platform": "php",
		"level":    "error",
		"release":  "webshop@1.4.2",
		"sdk":      map[string]any{"name": "sentry.php.laravel", "version": "4.20.0"},
		"user":     map[string]any{"id": "42"},
		"request":  map[string]any{"url": "https://shop.example.com/checkout"},
		"exception": map[string]any{"values": []any{map[string]any{
			"type": "RuntimeException", "value": "Checkout failed",
			"stacktrace": map[string]any{"frames": []any{
				map[string]any{"filename": "/app/Http/Controllers/CheckoutController.php", "function": "store", "lineno": 16, "in_app": true},
				map[string]any{"filename": "/app/Services/PaymentGateway.php", "function": function, "lineno": 12, "in_app": true},
			}},
		}}},
	}
	if !ts.IsZero() {
		event["timestamp"] = float64(ts.UnixMilli()) / 1000
	}
	data, _ := json.Marshal(event)
	ev, err := sentry.DecodeEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	return Item{ProjectID: projectID, ReceivedAt: received, Event: ev}
}

func enqueue(t *testing.T, env *testEnv, items ...Item) {
	t.Helper()
	for _, item := range items {
		if !env.queue.TryEnqueue(item) {
			t.Fatal("queue full")
		}
	}
}

// eventually waits up to 5 s for cond.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var t0 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

func TestWriter_GroupsIntoIssues(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{})

	received := t0.Add(2 * time.Hour)
	enqueue(t, env,
		errorItem(t, p.ID, eventIDN(1), "charge", t0.Add(time.Hour), received), // newest first, on purpose
		errorItem(t, p.ID, eventIDN(2), "charge", t0, received),
		errorItem(t, p.ID, eventIDN(3), "refund", t0.Add(time.Minute), received),
	)
	w.Close()

	issues, err := env.db.ListIssues(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2 (same fingerprint twice, one different)", len(issues))
	}
	if got := w.StoredLastHour(); got != 3 {
		t.Errorf("StoredLastHour = %d, want 3 (the status page shows it)", got)
	}
	charge := issues[0]
	if charge.TimesSeen != 2 || !charge.LastSeen.Equal(t0.Add(time.Hour)) || !charge.FirstSeen.Equal(t0) {
		t.Errorf("issue = times_seen %d, first %v, last %v; want 2, %v, %v", charge.TimesSeen, charge.FirstSeen, charge.LastSeen, t0, t0.Add(time.Hour))
	}
	if charge.Title != "RuntimeException" || charge.Message != "Checkout failed" || charge.Culprit != "PaymentGateway.php:12" || charge.Source != "laravel" {
		t.Errorf("issue fields = %+v", charge)
	}
	if issues[1].TimesSeen != 1 || issues[1].Fingerprint == charge.Fingerprint {
		t.Errorf("second issue = %+v", issues[1])
	}

	stored, err := env.db.ErrorEventByEventID(context.Background(), p.ID, eventIDN(1))
	if err != nil {
		t.Fatal(err)
	}
	if stored.IssueID != charge.ID || stored.UserID != "42" || stored.Release != "webshop@1.4.2" ||
		stored.URL != "https://shop.example.com/checkout" || stored.Platform != "php" || !stored.ReceivedAt.Equal(received) {
		t.Errorf("stored event = %+v", stored)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(stored.Data), &data); err != nil || data["event_id"] != eventIDN(1) || data["exception"] == nil {
		t.Errorf("stored data = %s", stored.Data)
	}
}

func TestWriter_DedupesEventID(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{}, func(w *Writer) { w.flushInterval = 20 * time.Millisecond })

	item := errorItem(t, p.ID, eventIDN(1), "charge", t0, t0)
	enqueue(t, env, item, item) // a retry in the same batch
	eventually(t, "the first batch", func() bool {
		n, _ := env.db.CountErrorEvents(context.Background(), p.ID)
		return n == 1
	})
	enqueue(t, env, item) // a retry in a later batch
	w.Close()

	if n, _ := env.db.CountErrorEvents(context.Background(), p.ID); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
	issues, _ := env.db.ListIssues(context.Background(), p.ID)
	if len(issues) != 1 || issues[0].TimesSeen != 1 {
		t.Errorf("issues = %+v, want one with times_seen 1", issues)
	}
}

func TestWriter_ClampsFutureTimestamp(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{})

	received := t0
	enqueue(t, env,
		errorItem(t, p.ID, eventIDN(1), "charge", received.Add(2*time.Hour), received),    // clock far ahead
		errorItem(t, p.ID, eventIDN(2), "charge", received.Add(30*time.Minute), received), // within an hour: kept
		errorItem(t, p.ID, eventIDN(3), "charge", time.Time{}, received),                  // no timestamp
		errorItem(t, p.ID, eventIDN(4), "charge", received.Add(-48*time.Hour), received),  // late: kept
	)
	w.Close()

	want := map[string]time.Time{
		eventIDN(1): received,
		eventIDN(2): received.Add(30 * time.Minute),
		eventIDN(3): received,
		eventIDN(4): received.Add(-48 * time.Hour),
	}
	for id, ts := range want {
		ev, err := env.db.ErrorEventByEventID(context.Background(), p.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ev.Timestamp.Equal(ts) {
			t.Errorf("event %s: timestamp %v, want %v", id, ev.Timestamp, ts)
		}
	}
	issues, _ := env.db.ListIssues(context.Background(), p.ID)
	if len(issues) != 1 || !issues[0].LastSeen.Equal(received.Add(30*time.Minute)) {
		t.Errorf("issue last_seen = %v, want the clamped maximum", issues[0].LastSeen)
	}
}

func TestWriter_ClampsOldTimestamp(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{})

	received := t0
	enqueue(t, env,
		errorItem(t, p.ID, eventIDN(1), "charge", received.Add(-29*24*time.Hour), received), // late: kept
		errorItem(t, p.ID, eventIDN(2), "charge", received.Add(-31*24*time.Hour), received), // too old
		errorItem(t, p.ID, eventIDN(3), "charge", time.Unix(1, 0), received),                // "timestamp": 1
	)
	w.Close()

	want := map[string]time.Time{
		eventIDN(1): received.Add(-29 * 24 * time.Hour),
		eventIDN(2): received,
		eventIDN(3): received,
	}
	for id, ts := range want {
		ev, err := env.db.ErrorEventByEventID(context.Background(), p.ID, id)
		if err != nil {
			t.Fatal(err)
		}
		if !ev.Timestamp.Equal(ts) {
			t.Errorf("event %s: timestamp %v, want %v", id, ev.Timestamp, ts)
		}
	}
	issues, _ := env.db.ListIssues(context.Background(), p.ID)
	if len(issues) != 1 || !issues[0].FirstSeen.Equal(received.Add(-29*24*time.Hour)) {
		t.Errorf("issues = %+v, want first_seen at the oldest kept timestamp, not 1970", issues)
	}
}

func TestWriter_FlushOnClose(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{}, func(w *Writer) { w.flushInterval = time.Hour })

	for i := range 3 {
		enqueue(t, env, errorItem(t, p.ID, eventIDN(i), "charge", t0, t0))
	}
	time.Sleep(50 * time.Millisecond) // the writer has picked them up but not flushed
	if n, _ := env.db.CountErrorEvents(context.Background(), p.ID); n != 0 {
		t.Fatalf("stored %d events before Close, want 0 with a 1 h interval", n)
	}
	w.Close()

	if n, _ := env.db.CountErrorEvents(context.Background(), p.ID); n != 3 {
		t.Errorf("stored %d events after Close, want 3", n)
	}
	if env.queue.Len() != 0 {
		t.Errorf("queue still holds %d items", env.queue.Len())
	}
	w.Close() // a second Close is harmless
}

func TestWriter_CloseWithoutRun(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)

	enqueue(t, env, errorItem(t, p.ID, eventIDN(1), "charge", t0, t0))
	w.Close()

	if n, _ := env.db.CountErrorEvents(context.Background(), p.ID); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
}

func TestWriter_FlushesEverySecondOrFullBatch(t *testing.T) {
	if defaultFlushInterval != time.Second || defaultBatchSize != 500 {
		t.Fatalf("flush interval %v, batch size %d; want 1s and 500", defaultFlushInterval, defaultBatchSize)
	}
	env := newTestEnv(t, 1000)
	p := env.project(t, "Webshop", 0)

	t.Run("a second after the first item", func(t *testing.T) {
		startWriter(t, env, &recordingHooks{})
		enqueue(t, env, errorItem(t, p.ID, eventIDN(1), "charge", t0, t0))
		start := time.Now()
		eventually(t, "the timed flush", func() bool {
			n, _ := env.db.CountErrorEvents(context.Background(), p.ID)
			return n == 1
		})
		if d := time.Since(start); d > 2*time.Second {
			t.Errorf("flushed after %v, want about 1 s", d)
		}
	})

	t.Run("at 500 items", func(t *testing.T) {
		startWriter(t, env, &recordingHooks{}, func(w *Writer) { w.flushInterval = time.Hour })
		for i := range defaultBatchSize {
			enqueue(t, env, errorItem(t, p.ID, eventIDN(1000+i), "charge", t0, t0))
		}
		eventually(t, "the full batch", func() bool {
			n, _ := env.db.CountErrorEvents(context.Background(), p.ID)
			return n == 1+defaultBatchSize
		})
	})
}

func TestWriter_CallsHooks(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	hooks := &recordingHooks{}
	w := startWriter(t, env, hooks, func(w *Writer) { w.flushInterval = 20 * time.Millisecond })

	enqueue(t, env,
		errorItem(t, p.ID, eventIDN(1), "charge", t0, t0),
		errorItem(t, p.ID, eventIDN(2), "charge", t0, t0),
		errorItem(t, p.ID, eventIDN(3), "refund", t0, t0),
	)
	eventually(t, "the first batch", func() bool { return hooks.createdCount() == 2 })
	enqueue(t, env, errorItem(t, p.ID, eventIDN(4), "charge", t0, t0)) // an existing issue
	w.Close()

	if n := hooks.createdCount(); n != 2 {
		t.Errorf("OnIssueCreated called %d times, want once per new issue (2)", n)
	}
	seen := map[string]bool{}
	for _, issue := range hooks.created {
		if issue.ID == 0 || issue.ProjectID != p.ID || issue.Status != store.IssueOpen || seen[issue.Fingerprint] {
			t.Errorf("hook got issue %+v", issue)
		}
		seen[issue.Fingerprint] = true
	}
	if len(hooks.regressed) != 0 {
		t.Errorf("OnIssueRegressed called %d times", len(hooks.regressed))
	}
}

func TestWriter_WaitsForBusyDatabase(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	// Another process (say "monitor project create") holds the write lock for a moment.
	other, err := sql.Open("sqlite", env.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	conn, err := other.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}

	w := startWriter(t, env, &recordingHooks{}, func(w *Writer) { w.flushInterval = 20 * time.Millisecond })
	enqueue(t, env, errorItem(t, p.ID, eventIDN(1), "charge", t0, t0))
	time.Sleep(time.Second)
	if _, err := conn.ExecContext(context.Background(), "COMMIT"); err != nil {
		t.Fatal(err)
	}
	w.Close()

	if n, _ := env.db.CountErrorEvents(context.Background(), p.ID); n != 1 {
		t.Errorf("stored %d events, want 1", n)
	}
	if logs := env.logs.String(); strings.Contains(logs, "level=ERROR") || strings.Contains(logs, "level=WARN") || strings.Contains(logs, "locked") {
		t.Errorf("a short busy period was logged as a problem:\n%s", logs)
	}
}

func TestWriter_DatabaseFailureIsLoggedNotFatal(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := startWriter(t, env, &recordingHooks{}, func(w *Writer) {
		w.flushInterval = 20 * time.Millisecond
		w.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	})

	env.db.Close()
	enqueue(t, env, errorItem(t, p.ID, eventIDN(1), "charge", t0, t0))
	eventually(t, "the drop to be logged", func() bool { return strings.Contains(env.logs.String(), "dropped") })
	w.Close()

	logs := env.logs.String()
	if strings.Count(logs, "retrying") != 2 || !strings.Contains(logs, "level=ERROR") {
		t.Errorf("want two retries and one error line:\n%s", logs)
	}
	// Admin alerts read these.
	if st := w.Stats(); st.FailedBatches != 1 || st.LostEvents != 1 || st.LastError == "" {
		t.Errorf("Stats = %+v, want one failed batch of one event, with its error", st)
	}
}

func TestWriter_KeepsSDKFingerprintThroughScrub(t *testing.T) {
	// The handler scrubs before the writer groups, so a fingerprint with a
	// sensitive-looking word must reach the writer unchanged.
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)

	withFingerprint := func(n int, fingerprint, function string) []byte {
		body := envelope(eventIDN(n), "")
		body = bytes.Replace(body, []byte(`"function":"charge"`), []byte(`"function":"`+function+`"`), 1)
		return bytes.Replace(body, []byte(`"platform":"php",`), []byte(`"platform":"php","fingerprint":`+fingerprint+`,`), 1)
	}
	bodies := [][]byte{
		withFingerprint(1, `["auth-failure","{{ default }}"]`, "charge"), // same SDK fingerprint,
		withFingerprint(2, `["auth-failure","{{ default }}"]`, "refund"), // different stacks: two issues
		withFingerprint(3, `["card-declined","checkout"]`, "charge"),     // different custom values:
		withFingerprint(4, `["card-declined","signup"]`, "charge"),       // two issues
	}
	for _, body := range bodies {
		if rec := env.post(envelopePath(p), body, authHeader(p.SentryKey)); rec.Code != http.StatusOK {
			t.Fatalf("status = %d; body %s", rec.Code, rec.Body)
		}
	}
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	w.Close() // writes the queue

	issues, err := env.db.ListIssues(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 4 {
		t.Errorf("got %d issues, want 4: scrubbing changed the SDK fingerprints", len(issues))
	}
	stored, _ := env.db.ErrorEventByEventID(context.Background(), p.ID, eventIDN(3))
	if !strings.Contains(stored.Data, `"fingerprint":["card-declined","checkout"]`) {
		t.Errorf("stored fingerprint changed: %s", stored.Data)
	}
}
