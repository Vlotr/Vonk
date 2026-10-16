package issues_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/issues"
	"monitor/internal/sentry"
	"monitor/internal/store"
)

// t0 is when the first event of every test arrives.
var t0 = time.Date(2026, 10, 16, 10, 0, 0, 0, time.UTC)

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

func (h *recordingHooks) regressedIssues() []store.Issue {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]store.Issue(nil), h.regressed...)
}

// env is a migrated database, a project, the status service on a settable
// clock, and hooks that record what the writer reports.
type env struct {
	db      *store.DB
	project store.Project
	service *issues.Service
	clock   time.Time
	hooks   *recordingHooks
	nextID  int
}

func newEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := db.CreateProject(context.Background(), store.NewProject{Name: "Webshop", TracksLaravel: true})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{db: db, project: p, clock: t0, hooks: &recordingHooks{}}
	e.service = issues.NewService(db, func() time.Time { return e.clock })
	return e
}

// send writes one batch through the ingest queue and writer, as SDK traffic
// would arrive: each event is received at its received time.
func (e *env) send(t *testing.T, received ...time.Time) {
	t.Helper()
	queue := ingest.NewQueue(len(received))
	for _, at := range received {
		e.nextID++
		data, _ := json.Marshal(map[string]any{
			"event_id":  eventID(e.nextID),
			"timestamp": float64(at.UnixMilli()) / 1000,
			"platform":  "php",
			"exception": map[string]any{"values": []any{map[string]any{
				"type": "RuntimeException", "value": "Checkout failed",
				"stacktrace": map[string]any{"frames": []any{
					map[string]any{"filename": "/app/Services/PaymentGateway.php", "function": "charge", "lineno": 12, "in_app": true},
				}},
			}}},
		})
		ev, err := sentry.DecodeEvent(data)
		if err != nil {
			t.Fatal(err)
		}
		if !queue.TryEnqueue(ingest.Item{ProjectID: e.project.ID, ReceivedAt: at, Event: ev}) {
			t.Fatal("queue full")
		}
	}
	// Close without Run writes the whole queue as one batch.
	ingest.NewWriter(e.db, queue, e.hooks, slog.New(slog.NewTextHandler(io.Discard, nil))).Close()
}

func eventID(n int) string {
	const digits = "0123456789abcdef"
	id := []byte("00000000000000000000000000000000")
	for i := len(id) - 1; n > 0; i-- {
		id[i] = digits[n%16]
		n /= 16
	}
	return string(id)
}

// issue returns the project's only issue.
func (e *env) issue(t *testing.T) store.Issue {
	t.Helper()
	list, err := e.db.ListIssues(context.Background(), e.project.ID)
	if err != nil || len(list) != 1 {
		t.Fatalf("issues = %+v, %v; want exactly one", list, err)
	}
	return list[0]
}

// setStatus changes the status at the given time and checks it worked.
func (e *env) setStatus(t *testing.T, at time.Time, status string) store.Issue {
	t.Helper()
	e.clock = at
	issue, err := e.service.SetStatus(context.Background(), e.issue(t).ID, status)
	if err != nil {
		t.Fatalf("SetStatus(%s): %v", status, err)
	}
	if issue.Status != status {
		t.Fatalf("SetStatus(%s) returned status %q", status, issue.Status)
	}
	return issue
}

func TestStatus_ResolveAndReopenOnNewEvent(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)

	resolved := e.setStatus(t, t0.Add(time.Minute), store.IssueResolved)
	if resolved.ResolvedAt == nil || !resolved.ResolvedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("resolved_at = %v, want %v", resolved.ResolvedAt, t0.Add(time.Minute))
	}
	if got := e.issue(t); got.Status != store.IssueResolved || got.ResolvedAt == nil {
		t.Fatalf("stored issue = %+v, want resolved with resolved_at", got)
	}

	// The same error again: the issue opens, forgets when it was resolved
	// and remembers when it came back.
	e.send(t, t0.Add(2*time.Minute))
	got := e.issue(t)
	if got.Status != store.IssueOpen || got.ResolvedAt != nil {
		t.Errorf("after a new event: status %q, resolved_at %v; want open and none", got.Status, got.ResolvedAt)
	}
	if got.RegressedAt == nil || !got.RegressedAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("regressed_at = %v, want the receive time %v", got.RegressedAt, t0.Add(2*time.Minute))
	}
	if got.TimesSeen != 2 {
		t.Errorf("times_seen = %d, want 2", got.TimesSeen)
	}
	regressed := e.hooks.regressedIssues()
	if len(regressed) != 1 || regressed[0].ID != got.ID || regressed[0].Status != store.IssueOpen {
		t.Errorf("OnIssueRegressed calls = %+v, want one with the reopened issue", regressed)
	}

	// Reopening by hand is not a regression: regressed_at stays as it was.
	e.setStatus(t, t0.Add(3*time.Minute), store.IssueResolved)
	reopened := e.setStatus(t, t0.Add(4*time.Minute), store.IssueOpen)
	if reopened.ResolvedAt != nil || reopened.RegressedAt == nil || !reopened.RegressedAt.Equal(t0.Add(2*time.Minute)) {
		t.Errorf("reopened by hand: resolved_at %v, regressed_at %v", reopened.ResolvedAt, reopened.RegressedAt)
	}
	if n := len(e.hooks.regressedIssues()); n != 1 {
		t.Errorf("OnIssueRegressed called %d times, want still 1", n)
	}
}

func TestStatus_EventReceivedBeforeResolveKeepsItResolved(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)
	e.setStatus(t, t0.Add(time.Minute), store.IssueResolved)

	// An event that arrived just before the click, still in the queue then:
	// it counts, but it is not news after the resolve.
	e.send(t, t0.Add(time.Minute-time.Millisecond))
	got := e.issue(t)
	if got.Status != store.IssueResolved || got.TimesSeen != 2 {
		t.Errorf("status %q, times_seen %d; want resolved and 2", got.Status, got.TimesSeen)
	}
	if n := len(e.hooks.regressedIssues()); n != 0 {
		t.Errorf("OnIssueRegressed called %d times, want 0", n)
	}
}

func TestStatus_IgnoredStaysIgnored(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)
	ignored := e.setStatus(t, t0.Add(time.Minute), store.IssueIgnored)
	if ignored.ResolvedAt != nil {
		t.Errorf("an ignored issue has resolved_at %v", ignored.ResolvedAt)
	}

	e.send(t, t0.Add(2*time.Minute), t0.Add(3*time.Minute))
	got := e.issue(t)
	if got.Status != store.IssueIgnored || got.RegressedAt != nil {
		t.Errorf("status %q, regressed_at %v; want ignored and none", got.Status, got.RegressedAt)
	}
	// Ignored, but still counted.
	if got.TimesSeen != 3 || !got.LastSeen.Equal(t0.Add(3*time.Minute)) {
		t.Errorf("times_seen %d, last_seen %v; want 3 and %v", got.TimesSeen, got.LastSeen, t0.Add(3*time.Minute))
	}
	if n := len(e.hooks.regressedIssues()); n != 0 {
		t.Errorf("OnIssueRegressed called %d times, want 0", n)
	}
}

func TestStatus_RegressionHookOnce(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)
	e.setStatus(t, t0.Add(time.Minute), store.IssueResolved)

	// Three events in one batch, then one more batch: one regression.
	e.send(t, t0.Add(2*time.Minute), t0.Add(2*time.Minute+time.Second), t0.Add(2*time.Minute+2*time.Second))
	e.send(t, t0.Add(3*time.Minute))
	regressed := e.hooks.regressedIssues()
	if len(regressed) != 1 {
		t.Fatalf("OnIssueRegressed called %d times, want once: %+v", len(regressed), regressed)
	}
	// The hook sees the issue as it is after the batch.
	if regressed[0].TimesSeen != 4 || !regressed[0].LastSeen.Equal(t0.Add(2*time.Minute+2*time.Second)) {
		t.Errorf("hook got times_seen %d, last_seen %v", regressed[0].TimesSeen, regressed[0].LastSeen)
	}

	// Resolved again and back again: a second regression.
	e.setStatus(t, t0.Add(4*time.Minute), store.IssueResolved)
	e.send(t, t0.Add(5*time.Minute))
	if n := len(e.hooks.regressedIssues()); n != 2 {
		t.Errorf("OnIssueRegressed called %d times after the second regression, want 2", n)
	}
	if n := len(e.hooks.created); n != 1 {
		t.Errorf("OnIssueCreated called %d times, want 1", n)
	}
}

func TestStatus_UnknownValueAndIssue(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)
	id := e.issue(t).ID

	for _, status := range []string{"", "deleted", "Resolved", "open "} {
		if _, err := e.service.SetStatus(context.Background(), id, status); !errors.Is(err, issues.ErrInvalidStatus) {
			t.Errorf("SetStatus(%q) = %v, want ErrInvalidStatus", status, err)
		}
	}
	if got := e.issue(t); got.Status != store.IssueOpen {
		t.Errorf("status = %q after refused changes, want open", got.Status)
	}
	if _, err := e.service.SetStatus(context.Background(), id+100, store.IssueResolved); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("SetStatus on a missing issue = %v, want store.ErrNotFound", err)
	}
}

func TestStatus_SameStatusTwiceKeepsTheFirstTime(t *testing.T) {
	e := newEnv(t)
	e.send(t, t0)
	e.setStatus(t, t0.Add(time.Minute), store.IssueResolved)
	again := e.setStatus(t, t0.Add(5*time.Minute), store.IssueResolved) // a double click, or two tabs
	if again.ResolvedAt == nil || !again.ResolvedAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("resolved_at = %v, want the first resolve %v", again.ResolvedAt, t0.Add(time.Minute))
	}
	if ignored := e.setStatus(t, t0.Add(6*time.Minute), store.IssueIgnored); ignored.ResolvedAt != nil {
		t.Errorf("ignoring a resolved issue kept resolved_at %v", ignored.ResolvedAt)
	}
}
