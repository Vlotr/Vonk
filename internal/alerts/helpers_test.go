package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/quotedprintable"
	"net"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/ingest"
	"monitor/internal/mail"
	"monitor/internal/sentry"
	"monitor/internal/store"
	"monitor/internal/store/storetest"
	"monitor/internal/testutil"
)

const testBaseURL = "https://monitor.example.com"

// testStart is the clock's start: a Friday afternoon in October.
var testStart = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// fakeClock is a time the test moves; the notifier reads it from several goroutines.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// syncBuffer is a log buffer that goroutines can write to at once.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// testEnv is a migrated database with two projects and three people, a
// fake mail server, a fake Slack webhook and a started Notifier.
type testEnv struct {
	db      *store.DB
	smtp    *testutil.SMTPServer
	slack   *testutil.SlackServer
	clock   *fakeClock
	logs    *syncBuffer
	n       *Notifier
	adminID int64
	shop    store.Project // Webshop: the admin and member@ see it
	portal  store.Project // Portal: the admin and other@ see it
}

type envOptions struct {
	noMail   bool
	location *time.Location
}

func newEnv(t *testing.T, opts ...func(*envOptions)) *testEnv {
	t.Helper()
	var o envOptions
	for _, opt := range opts {
		opt(&o)
	}
	ctx := context.Background()
	dir := t.TempDir()
	db, err := store.Open(ctx, config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db"), DatabaseURL: storetest.DatabaseURL(t)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	env := &testEnv{db: db, smtp: testutil.NewSMTPServer(t, testutil.SMTPPlain), slack: testutil.NewSlackServer(t),
		clock: &fakeClock{t: testStart}, logs: &syncBuffer{}}
	env.shop = env.project(t, "Webshop")
	env.portal = env.project(t, "Portal")
	admin, err := db.CreateFirstAdmin(ctx, "admin@example.com", "not-a-real-hash", testStart)
	if err != nil {
		t.Fatal(err)
	}
	env.adminID = admin.ID
	env.member(t, "member@example.com", env.shop.ID)
	env.member(t, "other@example.com", env.portal.ID)

	var mailer Mailer
	if !o.noMail {
		host, port, _ := net.SplitHostPort(env.smtp.Addr)
		p, _ := strconv.Atoi(port)
		mailer = mail.NewSender(mail.Config{Host: host, Port: p}, testBaseURL)
	}
	// The fake Slack speaks plain http, as DEV_ALLOW_HTTP_WEBHOOKS allows.
	env.n = New(Options{DB: db, Mailer: mailer, BaseURL: testBaseURL, Logger: slog.New(slog.NewTextHandler(env.logs, nil)),
		Now: env.clock.Now, Location: o.location, AllowHTTPWebhooks: true})
	env.n.retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	env.n.Start()
	t.Cleanup(func() { env.n.Close(5 * time.Second) })
	return env
}

func withoutMail(o *envOptions) { o.noMail = true }

func inLocation(loc *time.Location) func(*envOptions) {
	return func(o *envOptions) { o.location = loc }
}

func (e *testEnv) project(t *testing.T, name string) store.Project {
	t.Helper()
	p, err := e.db.CreateProject(context.Background(), store.NewProject{Name: name, TracksLaravel: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// member makes a member who sees projectIDs, the way accepting an invite does.
func (e *testEnv) member(t *testing.T, email string, projectIDs ...int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := e.db.CreateInvite(ctx, store.NewInvite{Email: email, Role: store.RoleMember, ProjectIDs: projectIDs,
		TokenHash: "invite-" + email, CreatedBy: e.adminID, Now: testStart, ExpiresAt: testStart.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.AcceptInvite(ctx, "invite-"+email, "not-a-real-hash", testStart); err != nil {
		t.Fatal(err)
	}
}

// configure changes a project's alert settings, starting from what they are.
func (e *testEnv) configure(t *testing.T, projectID int64, change func(*store.AlertSettings)) {
	t.Helper()
	ctx := context.Background()
	s, err := e.db.AlertSettings(ctx, projectID)
	if err != nil {
		t.Fatal(err)
	}
	change(&s)
	if err := e.db.SaveAlertSettings(ctx, projectID, s); err != nil {
		t.Fatal(err)
	}
}

// withSlack gives the project the fake webhook.
func (e *testEnv) withSlack(t *testing.T, projectID int64) {
	t.Helper()
	e.configure(t, projectID, func(s *store.AlertSettings) { s.SlackWebhookURL = e.slack.URL })
}

// errorEvent is a sentry-laravel style event whose top frame is function,
// so each function is its own issue.
func errorEvent(t *testing.T, n int, function string) *sentry.Event {
	t.Helper()
	raw := map[string]any{
		"event_id": fmt.Sprintf("%032x", n),
		"platform": "php",
		"sdk":      map[string]any{"name": "sentry.php.laravel", "version": "4.29.0"},
		"exception": map[string]any{"values": []any{map[string]any{
			"type": "RuntimeException", "value": "Checkout failed in " + function,
			"stacktrace": map[string]any{"frames": []any{
				map[string]any{"filename": "/app/Services/PaymentGateway.php", "function": function, "lineno": 12, "in_app": true},
			}},
		}}},
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := sentry.DecodeEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// ingest stores events through the queue and the batch writer, received
// now, with the notifier as the writer's hooks, and waits until every
// notification is delivered.
func (e *testEnv) ingest(t *testing.T, projectID int64, events ...*sentry.Event) {
	t.Helper()
	queue := ingest.NewQueue(len(events))
	for _, ev := range events {
		if !queue.TryEnqueue(ingest.Item{ProjectID: projectID, ReceivedAt: e.clock.Now(), Event: ev}) {
			t.Fatal("queue full")
		}
	}
	ingest.NewWriter(e.db, queue, e.n, slog.New(slog.NewTextHandler(e.logs, nil))).Close()
	e.n.busy.Wait()
}

// saveAt stores n events of one issue of the project directly, with the
// given timestamp, for the spike and summary tests.
func (e *testEnv) saveAt(t *testing.T, projectID int64, fingerprint string, at time.Time, n int) {
	t.Helper()
	events := make([]store.NewErrorEvent, n)
	for i := range events {
		events[i] = store.NewErrorEvent{ProjectID: projectID, EventID: fmt.Sprintf("%s-%d-%d", fingerprint, at.UnixNano(), i),
			Fingerprint: fingerprint, Title: "RuntimeException", Message: fingerprint + " failed", Culprit: fingerprint + ".php:1",
			Source: "laravel", Timestamp: at, ReceivedAt: at, Data: "{}"}
	}
	if _, err := e.db.SaveErrorEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

// mails returns the received mails as "to: subject" lines, plus their
// bodies, sorted by line: the workers deliver side by side, in no order.
func (e *testEnv) mails() (lines []string, bodies []string) {
	type received struct{ line, body string }
	var all []received
	for _, m := range e.smtp.Messages() {
		subject := ""
		for _, line := range strings.Split(m.Data, "\r\n") {
			if strings.HasPrefix(line, "Subject: ") {
				subject = decodeSubject(strings.TrimPrefix(line, "Subject: "))
			}
		}
		_, body, _ := strings.Cut(m.Data, "\r\n\r\n")
		all = append(all, received{strings.Join(m.To, ",") + ": " + subject, decodeQP(body)})
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].line < all[j].line })
	for _, r := range all {
		lines = append(lines, r.line)
		bodies = append(bodies, r.body)
	}
	return lines, bodies
}

// waitIdle waits until the notifier delivered everything it was given.
func (e *testEnv) waitIdle() {
	e.n.busy.Wait()
}

func decodeSubject(s string) string {
	decoded, err := new(mime.WordDecoder).DecodeHeader(s)
	if err != nil {
		return s
	}
	return decoded
}

func decodeQP(s string) string {
	b, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(s)))
	if err != nil {
		return s
	}
	return string(b)
}
