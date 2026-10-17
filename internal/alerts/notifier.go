// Package alerts tells people about their projects and about the tool
// itself, by mail (when MAIL_URL is set) and Slack.
//
// Per project: a new issue, a returning issue (resolved, then seen again),
// a spike (many more errors than usual) and a summary (daily, weekly or
// monthly), each on or off per channel. Mail goes to everyone who may see
// the project; Slack to the project's webhook. Admins hear about problems
// of the tool itself (a full disk, events that could not be stored).
//
// Delivery happens in the background, in memory: each message is tried 3
// times with a pause in between, and lost when the process stops first.
// New and returning issue notifications are capped at 30 per project and
// channel per hour; one message then says how many more were suppressed.
package alerts

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"monitor/internal/jobs"
	"monitor/internal/mail"
	"monitor/internal/store"
)

const (
	// attempts is how often one message is tried before it is given up.
	attempts = 3
	// attemptTimeout limits one try of one message.
	attemptTimeout = 30 * time.Second
	// queueSize is how many issue events and how many deliveries wait at most.
	queueSize = 1000
	// workers deliver messages side by side.
	workers = 4
)

// defaultRetryDelays are the pauses between the 3 attempts.
var defaultRetryDelays = []time.Duration{5 * time.Second, 30 * time.Second}

// Mailer sends one mail; *mail.Sender is one.
type Mailer interface {
	Send(ctx context.Context, msg mail.Message) error
}

// SlackPoster posts to an incoming webhook; *Slack is one.
type SlackPoster interface {
	Post(ctx context.Context, webhook, text string) error
}

// Options configure a Notifier.
type Options struct {
	DB *store.DB
	// Mailer sends mail; nil when MAIL_URL is not set, and then nothing is mailed.
	Mailer Mailer
	// Slack posts to webhooks; nil means NewSlack(AllowHTTPWebhooks).
	Slack SlackPoster
	// AllowHTTPWebhooks lets the default Slack client post to plain http
	// webhooks (DEV_ALLOW_HTTP_WEBHOOKS, for local fakes).
	AllowHTTPWebhooks bool
	// BaseURL is the dashboard's address; links in messages start with it.
	BaseURL string
	Logger  *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
	// Location is TZ: summaries go out at 08:00 there and name its days.
	Location *time.Location
}

// Notifier sends the notifications. It implements issues.Hooks, so the
// ingest writer tells it about new and returning issues.
type Notifier struct {
	db      *store.DB
	mailer  Mailer
	slack   SlackPoster
	baseURL string
	log     *slog.Logger
	now     func() time.Time
	loc     *time.Location

	retryDelays []time.Duration

	events     chan issueEvent
	deliveries chan delivery
	busy       sync.WaitGroup // events and deliveries not done yet
	closed     atomic.Bool

	ctx       context.Context
	cancel    context.CancelFunc
	running   sync.WaitGroup // the goroutines Start started
	start     sync.Once
	closeOnce sync.Once

	mu      sync.Mutex
	windows map[capKey]*capWindowState
}

// New returns a Notifier; Start makes it deliver.
func New(o Options) *Notifier {
	now := o.Now
	if now == nil {
		now = time.Now
	}
	loc := o.Location
	if loc == nil {
		loc = time.UTC
	}
	var slack SlackPoster = NewSlack(o.AllowHTTPWebhooks)
	if o.Slack != nil {
		slack = o.Slack
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Notifier{
		db:          o.DB,
		mailer:      o.Mailer,
		slack:       slack,
		baseURL:     o.BaseURL,
		log:         o.Logger,
		now:         now,
		loc:         loc,
		retryDelays: defaultRetryDelays,
		events:      make(chan issueEvent, queueSize),
		deliveries:  make(chan delivery, queueSize),
		ctx:         ctx,
		cancel:      cancel,
		windows:     map[capKey]*capWindowState{},
	}
}

// MailEnabled reports whether notifications are mailed (MAIL_URL is set).
func (n *Notifier) MailEnabled() bool {
	return n.mailer != nil
}

// Start starts the goroutines that turn issue events into messages and
// deliver them. Calling it again does nothing.
func (n *Notifier) Start() {
	n.start.Do(func() {
		n.running.Add(1 + workers)
		go n.dispatch()
		for range workers {
			go n.work()
		}
	})
}

// Close stops taking notifications, gives those under way up to wait to
// be delivered, then stops. What is still undelivered is lost. Calling it
// again does nothing.
func (n *Notifier) Close(wait time.Duration) {
	n.closeOnce.Do(func() { n.close(wait) })
}

func (n *Notifier) close(wait time.Duration) {
	n.closed.Store(true)
	done := make(chan struct{})
	go func() {
		n.busy.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(wait):
		n.log.Warn("alerts: stopping with notifications undelivered")
	}
	n.cancel()
	n.running.Wait()
	// The goroutines are gone; what they left in the queues is lost. Count
	// it done, so busy reaches zero and the waiter above returns.
	for {
		select {
		case <-n.events:
			n.busy.Done()
		case <-n.deliveries:
			n.busy.Done()
		default:
			return
		}
	}
}

// Jobs returns the notifier's scheduled work: spike checks every 5
// minutes, summaries at 08:00 in TZ, and the "suppressed" messages of
// capped hours every minute.
func (n *Notifier) Jobs() []jobs.Job {
	return []jobs.Job{
		{Name: "spike check", Schedule: jobs.Every(5 * time.Minute), Run: n.CheckSpikes},
		{Name: "summaries", Schedule: jobs.DailyAt(8, 0, n.loc), Run: n.SendDigests},
		{Name: "suppressed notifications", Schedule: jobs.Every(time.Minute), Run: func(ctx context.Context, _ time.Time) { n.FlushSuppressed(ctx) }},
	}
}

// issueEvent is a new or returning issue the writer reported.
type issueEvent struct {
	kind  string // store.AlertNewIssue or store.AlertRegression
	issue store.Issue
}

// OnIssueCreated queues a "new issue" notification. It never waits: the
// ingest writer calls it.
func (n *Notifier) OnIssueCreated(_ context.Context, issue store.Issue) {
	n.queueIssue(issueEvent{kind: store.AlertNewIssue, issue: issue})
}

// OnIssueRegressed queues a "returning issue" notification. The writer
// calls it once each time a resolved issue opens again.
func (n *Notifier) OnIssueRegressed(_ context.Context, issue store.Issue) {
	n.queueIssue(issueEvent{kind: store.AlertRegression, issue: issue})
}

func (n *Notifier) queueIssue(ev issueEvent) {
	if n.closed.Load() {
		return
	}
	n.busy.Add(1)
	select {
	case n.events <- ev:
	default:
		n.busy.Done()
		n.log.Warn("alerts: too many notifications waiting; one dropped", "project", ev.issue.ProjectID, "kind", ev.kind)
	}
}

// dispatch turns issue events into messages, one at a time.
func (n *Notifier) dispatch() {
	defer n.running.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case ev := <-n.events:
			n.handleIssue(n.ctx, ev)
			n.busy.Done()
		}
	}
}

func (n *Notifier) handleIssue(ctx context.Context, ev issueEvent) {
	settings, err := n.db.AlertSettings(ctx, ev.issue.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		return // the project was deleted meanwhile
	}
	if err != nil {
		n.log.Error("alerts: read alert settings", "project", ev.issue.ProjectID, "err", err)
		return
	}
	channels := settings.Channels(ev.kind)
	if !channels.Any() {
		return
	}
	p, err := n.db.ProjectByID(ctx, ev.issue.ProjectID)
	if err != nil {
		n.log.Error("alerts: read project", "project", ev.issue.ProjectID, "err", err)
		return
	}
	n.send(ctx, p, settings, channels, n.issueMessage(ev.kind, p, ev.issue), ev.kind, true)
}

// send hands msg to the delivery workers for the channels given: a mail to
// each person who may see the project, a post to its webhook. capped
// counts it against the hourly cap (new and returning issues).
func (n *Notifier) send(ctx context.Context, p store.Project, settings store.AlertSettings, channels store.Channels, msg message, kind string, capped bool) {
	now := n.now()
	if channels.Email && n.mailer != nil {
		if recipients, err := n.db.ProjectRecipients(ctx, p.ID); err != nil {
			n.log.Error("alerts: read recipients", "project", p.ID, "err", err)
		} else if len(recipients) > 0 && n.admit(ctx, p, settings, store.ChannelEmail, now, capped) {
			n.mailAll(p.ID, kind, recipients, msg)
		}
	}
	if channels.Slack && settings.SlackWebhookURL != "" && n.admit(ctx, p, settings, store.ChannelSlack, now, capped) {
		n.post(p.ID, kind, settings.SlackWebhookURL, msg.slack)
	}
}

// delivery is one message to one place: an address or a webhook.
type delivery struct {
	projectID int64 // 0 for the tool's own alerts
	kind      string
	channel   string
	to        string // the address, or the webhook URL (a secret: never logged)
	msg       message
}

func (n *Notifier) mailAll(projectID int64, kind string, recipients []string, msg message) {
	for _, to := range recipients {
		n.queueDelivery(delivery{projectID: projectID, kind: kind, channel: store.ChannelEmail, to: to, msg: msg})
	}
}

func (n *Notifier) post(projectID int64, kind, webhook, text string) {
	n.queueDelivery(delivery{projectID: projectID, kind: kind, channel: store.ChannelSlack, to: webhook, msg: message{slack: text}})
}

// queueDelivery hands d to the workers. It never waits: when the queue is
// full (the workers are stuck on a slow server), d is dropped with a
// warning, so the scheduler and the dispatcher keep going.
func (n *Notifier) queueDelivery(d delivery) {
	n.busy.Add(1)
	select {
	case n.deliveries <- d:
	default:
		n.busy.Done()
		n.log.Warn("alerts: too many messages waiting; one dropped", "channel", d.channel, "project", d.projectID, "kind", d.kind)
	}
}

// work delivers messages until the notifier stops.
func (n *Notifier) work() {
	defer n.running.Done()
	for {
		select {
		case <-n.ctx.Done():
			return
		case d := <-n.deliveries:
			n.deliver(d)
			n.busy.Done()
		}
	}
}

// deliver tries a message 3 times, pausing in between, and logs it when
// all fail. The log names the channel, never the webhook URL.
func (n *Notifier) deliver(d delivery) {
	var err error
	tries := 0
	for tries < attempts {
		tries++
		ctx, cancel := context.WithTimeout(n.ctx, attemptTimeout)
		if d.channel == store.ChannelEmail {
			err = n.mailer.Send(ctx, mail.Message{To: d.to, Subject: d.msg.subject, Text: d.msg.text})
		} else {
			err = n.slack.Post(ctx, d.to, d.msg.slack)
		}
		cancel()
		if err == nil {
			return
		}
		if tries == attempts || !n.pause(tries) {
			break
		}
	}
	n.log.Warn("alerts: notification not delivered", "channel", d.channel, "project", d.projectID, "kind", d.kind,
		"attempts", tries, "err", err)
}

// pause waits before attempt tries+1; false when the notifier stops first.
func (n *Notifier) pause(tries int) bool {
	wait := n.retryDelays[min(tries-1, len(n.retryDelays)-1)]
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-n.ctx.Done():
		return false
	}
}
