package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

func TestNotify_NewIssueSlackAndMail(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)

	env.ingest(t, env.shop.ID, errorEvent(t, 1, "charge"))

	issues, _ := env.db.ListIssues(context.Background(), env.shop.ID)
	link := fmt.Sprintf("%s/projects/%d/issues/%d", testBaseURL, env.shop.ID, issues[0].ID)

	msgs := env.slack.Messages()
	if len(msgs) != 1 {
		t.Fatalf("Slack got %d messages, want 1: %q", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0], "*New issue in Webshop*") || !strings.Contains(msgs[0], "<"+link+"|RuntimeException: Checkout failed in charge>") {
		t.Errorf("Slack message = %q", msgs[0])
	}

	// Everyone who may see Webshop, nobody else (ruling 10).
	lines, bodies := env.mails()
	slices.Sort(lines)
	want := []string{
		"admin@example.com: [Webshop] New issue: RuntimeException: Checkout failed in charge",
		"member@example.com: [Webshop] New issue: RuntimeException: Checkout failed in charge",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("mails = %q, want %q", lines, want)
	}
	for _, body := range bodies {
		if !strings.Contains(body, "Open the issue: "+link) || !strings.Contains(body, "PaymentGateway.php:12 · Laravel") ||
			!strings.Contains(body, fmt.Sprintf("%s/projects/%d/alerts", testBaseURL, env.shop.ID)) {
			t.Errorf("mail body:\n%s", body)
		}
	}

	// A second event of the same issue is no new issue.
	env.ingest(t, env.shop.ID, errorEvent(t, 2, "charge"))
	if n := len(env.slack.Messages()); n != 1 {
		t.Errorf("Slack got %d messages after a repeat, want still 1", n)
	}
}

func TestNotify_RegressionOncePerReopen(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	ctx := context.Background()

	env.ingest(t, env.shop.ID, errorEvent(t, 1, "charge"))
	issues, _ := env.db.ListIssues(ctx, env.shop.ID)
	issue := issues[0]

	returning := func() int {
		n := 0
		for _, m := range env.slack.Messages() {
			if strings.Contains(m, "*Returning issue in Webshop*") {
				n++
			}
		}
		return n
	}

	// Resolved, then three events in two batches: one notification.
	env.clock.Add(time.Minute)
	if _, err := env.db.SetIssueStatus(ctx, issue.ID, store.IssueResolved, env.clock.Now()); err != nil {
		t.Fatal(err)
	}
	env.clock.Add(time.Minute)
	env.ingest(t, env.shop.ID, errorEvent(t, 2, "charge"), errorEvent(t, 3, "charge"))
	env.ingest(t, env.shop.ID, errorEvent(t, 4, "charge"))
	if got := returning(); got != 1 {
		t.Fatalf("returning notifications after one reopen = %d, want 1", got)
	}

	// Resolved again and back again: a second one.
	env.clock.Add(time.Minute)
	if _, err := env.db.SetIssueStatus(ctx, issue.ID, store.IssueResolved, env.clock.Now()); err != nil {
		t.Fatal(err)
	}
	env.clock.Add(time.Minute)
	env.ingest(t, env.shop.ID, errorEvent(t, 5, "charge"))
	if got := returning(); got != 2 {
		t.Errorf("returning notifications after two reopens = %d, want 2", got)
	}
	lines, _ := env.mails()
	if n := countPrefix(lines, "admin@example.com: [Webshop] Returning issue: "); n != 2 {
		t.Errorf("returning mails to the admin = %d, want 2: %q", n, lines)
	}
}

func TestNotify_DisabledKindSendsNothing(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) {
		s.NewIssue = store.Channels{}
		s.Regression = store.Channels{Slack: true} // Slack only
	})
	ctx := context.Background()

	env.ingest(t, env.shop.ID, errorEvent(t, 1, "charge"))
	if msgs := env.slack.Messages(); len(msgs) != 0 {
		t.Errorf("Slack got %q with new-issue alerts off", msgs)
	}
	if lines, _ := env.mails(); len(lines) != 0 {
		t.Errorf("mails %q with new-issue alerts off", lines)
	}

	issues, _ := env.db.ListIssues(ctx, env.shop.ID)
	env.db.SetIssueStatus(ctx, issues[0].ID, store.IssueResolved, env.clock.Now())
	env.clock.Add(time.Minute)
	env.ingest(t, env.shop.ID, errorEvent(t, 2, "charge"))
	if msgs := env.slack.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "Returning issue") {
		t.Errorf("Slack = %q, want the returning issue", msgs)
	}
	if lines, _ := env.mails(); len(lines) != 0 {
		t.Errorf("mails %q with mail off for returning issues", lines)
	}
}

func TestNotify_NoMailURLSkipsMail(t *testing.T) {
	env := newEnv(t, withoutMail)
	env.withSlack(t, env.shop.ID)
	if env.n.MailEnabled() {
		t.Error("MailEnabled without a mailer")
	}
	env.ingest(t, env.shop.ID, errorEvent(t, 1, "charge"))
	if n := len(env.slack.Messages()); n != 1 {
		t.Errorf("Slack got %d messages, want 1", n)
	}
	if n := len(env.smtp.Messages()); n != 0 {
		t.Errorf("%d mails without MAIL_URL", n)
	}
}

func TestNotify_CapAndSuppressedSummary(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	ctx := context.Background()

	// 35 new issues in one batch: 30 go out per channel, 5 are held back.
	batch := make([]*sentry.Event, 0, 35)
	for i := range 35 {
		batch = append(batch, errorEvent(t, i+1, fmt.Sprintf("step%d", i)))
	}
	env.ingest(t, env.shop.ID, batch...)
	if n := len(env.slack.Messages()); n != notifyCap {
		t.Fatalf("Slack got %d messages, want %d", n, notifyCap)
	}
	lines, _ := env.mails()
	if n := countPrefix(lines, "admin@example.com: "); n != notifyCap {
		t.Fatalf("the admin got %d mails, want %d", n, notifyCap)
	}

	// Within the hour nothing more; once it has passed, one message per channel.
	env.clock.Add(59 * time.Minute)
	env.n.FlushSuppressed(ctx)
	env.waitIdle()
	if n := len(env.slack.Messages()); n != notifyCap {
		t.Fatalf("Slack got %d messages before the hour passed", n)
	}
	env.clock.Add(time.Minute)
	env.n.FlushSuppressed(ctx)
	env.waitIdle()
	msgs := env.slack.Messages()
	if len(msgs) != notifyCap+1 || !strings.Contains(msgs[notifyCap], "5 more notifications were suppressed") {
		t.Fatalf("after the hour Slack got %q", msgs[notifyCap:])
	}
	lines, _ = env.mails()
	for _, who := range []string{"admin@example.com", "member@example.com"} {
		if !slices.Contains(lines, who+": [Webshop] 5 more notifications were suppressed") {
			t.Errorf("%s got no summary mail: %q", who, lines[len(lines)-2:])
		}
	}
	env.n.FlushSuppressed(ctx)
	env.waitIdle()
	if n := len(env.slack.Messages()); n != notifyCap+1 {
		t.Errorf("the summary went out twice: %d messages", n)
	}

	// A new hour starts with a full allowance.
	env.ingest(t, env.shop.ID, errorEvent(t, 100, "later"))
	if n := len(env.slack.Messages()); n != notifyCap+2 {
		t.Errorf("Slack got %d messages, want the new issue of the new hour too", n)
	}

	// Another project has its own cap.
	env.withSlack(t, env.portal.ID)
	env.ingest(t, env.portal.ID, errorEvent(t, 200, "portal"))
	if n := len(env.slack.Messages()); n != notifyCap+3 {
		t.Errorf("Slack got %d messages, want Portal's too", n)
	}
}

func TestNotify_ThreeAttemptsAndNoWebhookInLogs(t *testing.T) {
	env := newEnv(t, withoutMail)
	env.withSlack(t, env.shop.ID)

	env.slack.Fail(2)
	env.ingest(t, env.shop.ID, errorEvent(t, 1, "charge"))
	if n := len(env.slack.Messages()); n != 1 || env.slack.Requests() != 3 {
		t.Errorf("after two failures: %d delivered in %d requests, want 1 in 3", n, env.slack.Requests())
	}

	env.slack.Fail(3)
	env.ingest(t, env.shop.ID, errorEvent(t, 2, "refund"))
	if env.slack.Requests() != 6 || len(env.slack.Messages()) != 1 {
		t.Errorf("three failures: %d requests, %d delivered; want 6 and still 1", env.slack.Requests(), len(env.slack.Messages()))
	}
	logs := env.logs.String()
	if !strings.Contains(logs, "notification not delivered") || !strings.Contains(logs, "Slack answered 500 (internal_error)") {
		t.Errorf("the lost message is not logged:\n%s", logs)
	}
	if strings.Contains(logs, "s3cr3t") || strings.Contains(logs, "/services/") {
		t.Errorf("the log shows the webhook URL:\n%s", logs)
	}
}

func countPrefix(lines []string, prefix string) int {
	n := 0
	for _, l := range lines {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

// stuckSlack never answers: every post waits until its context ends.
type stuckSlack struct{}

func (stuckSlack) Post(ctx context.Context, _, _ string) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestNotifier_CloseTwiceAfterTimeout(t *testing.T) {
	logs := &syncBuffer{}
	n := New(Options{Slack: stuckSlack{}, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	n.Start()
	for range workers + 6 { // the workers are stuck; six wait in the queue
		n.post(1, "test", "https://hooks.slack.com/services/T1/B1/x", "hello")
	}

	start := time.Now()
	n.Close(50 * time.Millisecond)
	n.Close(10 * time.Second) // serve calls it twice: the second does nothing
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("closing twice took %s; the second Close waited again", took)
	}
	if got := strings.Count(logs.String(), "stopping with notifications undelivered"); got != 1 {
		t.Errorf("logged the undelivered warning %d times, want 1", got)
	}

	// What was left in the queue counts as done, so nothing waits on it forever.
	done := make(chan struct{})
	go func() {
		n.busy.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("deliveries left in the queue are never marked done")
	}
}

// startedSlack never answers either, and tells when each post started.
type startedSlack struct{ started chan struct{} }

func (s startedSlack) Post(ctx context.Context, _, _ string) error {
	select {
	case s.started <- struct{}{}:
	default: // the test counts only the first posts
	}
	<-ctx.Done()
	return ctx.Err()
}

func TestNotifier_FullDeliveryQueueDrops(t *testing.T) {
	logs := &syncBuffer{}
	slack := startedSlack{started: make(chan struct{}, workers)}
	n := New(Options{Slack: slack, Logger: slog.New(slog.NewTextHandler(logs, nil))})
	n.Start()
	t.Cleanup(func() { n.Close(10 * time.Millisecond) })
	const webhook = "https://hooks.slack.com/services/T1/B1/x"

	// First every worker takes a message and gets stuck on it, so the
	// queue's room is known exactly.
	for range workers {
		n.post(1, store.AlertDigest, webhook, "summary")
	}
	for i := range workers {
		select {
		case <-slack.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d workers started a post", i, workers)
		}
	}

	// The scheduler goroutine queues summaries and suppressed counts; a
	// full queue must not make it wait.
	queued := make(chan struct{})
	go func() {
		for range queueSize + 3 {
			n.post(1, store.AlertDigest, webhook, "summary")
		}
		close(queued)
	}()
	select {
	case <-queued:
	case <-time.After(5 * time.Second):
		t.Fatal("queueing deliveries blocked on a full queue")
	}
	if got := strings.Count(logs.String(), "too many messages waiting; one dropped"); got != 3 {
		t.Errorf("dropped-message warnings = %d, want 3:\n%s", got, logs.String())
	}
	if strings.Contains(logs.String(), "hooks.slack.com") {
		t.Error("the log names the webhook")
	}
}
