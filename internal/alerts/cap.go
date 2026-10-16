package alerts

import (
	"context"
	"fmt"
	"time"

	"monitor/internal/store"
)

// The cap: new and returning issue notifications go out at most notifyCap
// times per project and channel in the hour that starts with the first of
// them; after that hour, one message says how many were held back.

const (
	// notifyCap is how many new and returning issue notifications one
	// project sends per channel within capWindow.
	notifyCap = 30
	capWindow = time.Hour
)

// capKey is one project's channel.
type capKey struct {
	projectID int64
	channel   string
}

// capWindowState counts one channel's notifications in the hour that
// started with the first of them.
type capWindowState struct {
	start      time.Time
	sent       int
	suppressed int
}

// admit counts a notification against the hourly cap of the project's
// channel and reports whether it may go out. When the window it falls in
// is new and the last one ended with suppressed notifications, the
// "N more were suppressed" message goes out first.
func (n *Notifier) admit(ctx context.Context, p store.Project, settings store.AlertSettings, channel string, now time.Time, capped bool) bool {
	if !capped {
		return true
	}
	key := capKey{p.ID, channel}
	n.mu.Lock()
	w := n.windows[key]
	suppressed := 0
	if w != nil && !now.Before(w.start.Add(capWindow)) {
		suppressed = w.suppressed
		w = nil
	}
	if w == nil {
		w = &capWindowState{start: now}
		n.windows[key] = w
	}
	ok := w.sent < notifyCap
	if ok {
		w.sent++
	} else {
		w.suppressed++
	}
	n.mu.Unlock()

	if suppressed > 0 {
		n.sendSuppressed(ctx, p, settings, channel, suppressed)
	}
	return ok
}

// FlushSuppressed sends the "N more notifications were suppressed"
// message of every capped hour that has ended. A job runs it every minute.
func (n *Notifier) FlushSuppressed(ctx context.Context) {
	now := n.now()
	ended := map[capKey]int{}
	n.mu.Lock()
	for key, w := range n.windows {
		if !now.Before(w.start.Add(capWindow)) {
			if w.suppressed > 0 {
				ended[key] = w.suppressed
			}
			delete(n.windows, key)
		}
	}
	n.mu.Unlock()

	for key, count := range ended {
		p, err := n.db.ProjectByID(ctx, key.projectID)
		if err != nil {
			continue // deleted meanwhile
		}
		settings, err := n.db.AlertSettings(ctx, key.projectID)
		if err != nil {
			continue
		}
		n.sendSuppressed(ctx, p, settings, key.channel, count)
	}
}

// sendSuppressed sends one channel the message about notifications its cap held back.
func (n *Notifier) sendSuppressed(ctx context.Context, p store.Project, settings store.AlertSettings, channel string, count int) {
	what := "notifications were"
	if count == 1 {
		what = "notification was"
	}
	issues := n.link("/projects/%d/issues", p.ID)
	msg := message{
		subject: fmt.Sprintf("[%s] %d more %s suppressed", p.Name, count, what),
		text: fmt.Sprintf("%d more %s suppressed in the last hour: at most %d go out per hour.\n\nSee the open issues of %s: %s\n",
			count, what, notifyCap, p.Name, issues) + n.footer(p),
		slack: fmt.Sprintf("*%s*: %d more %s suppressed in the last hour (at most %d go out per hour). %s",
			slackEscape(p.Name), count, what, notifyCap, slackLink(issues, "See the open issues")),
	}
	switch channel {
	case store.ChannelEmail:
		if n.mailer == nil {
			return
		}
		recipients, err := n.db.ProjectRecipients(ctx, p.ID)
		if err != nil {
			n.log.Error("alerts: read recipients", "project", p.ID, "err", err)
			return
		}
		n.mailAll(p.ID, "suppressed", recipients, msg)
	case store.ChannelSlack:
		if settings.SlackWebhookURL != "" {
			n.post(p.ID, "suppressed", settings.SlackWebhookURL, msg.slack)
		}
	}
}
