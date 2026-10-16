package alerts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"monitor/internal/store"
)

// Ruling 9: a spike is an hour with at least the project's threshold times
// the usual number of errors (the hourly average of the 7 days before),
// and at least 10 errors. A project alerts about a spike at most once in 6 hours.
const (
	spikeMinimum  = 10
	spikeCooldown = 6 * time.Hour
	spikeBaseline = 7 * 24 * time.Hour
)

// CheckSpikes looks at the last hour of every project with spike alerts
// on, and alerts those that spike. A job runs it every 5 minutes.
func (n *Notifier) CheckSpikes(ctx context.Context, _ time.Time) {
	projects, err := n.db.ListProjects(ctx)
	if err != nil {
		n.log.Error("alerts: spike check: list projects", "err", err)
		return
	}
	for _, p := range projects {
		if ctx.Err() != nil {
			return
		}
		if err := n.checkSpike(ctx, p); err != nil {
			n.log.Error("alerts: spike check", "project", p.ID, "err", err)
		}
	}
}

func (n *Notifier) checkSpike(ctx context.Context, p store.Project) error {
	settings, err := n.db.AlertSettings(ctx, p.ID)
	if err != nil || !settings.Spike.Any() {
		return err
	}
	now := n.now()
	hourAgo := now.Add(-time.Hour)
	// Events up to an hour ahead of the clock (a client clock that is off)
	// count in the last hour, as on the dashboard.
	last, err := n.db.CountErrorEventsBetween(ctx, p.ID, hourAgo, now.Add(time.Hour))
	if err != nil || last < spikeMinimum {
		return err
	}
	before, err := n.db.CountErrorEventsBetween(ctx, p.ID, hourAgo.Add(-spikeBaseline), hourAgo)
	if err != nil {
		return err
	}
	usual := float64(before) / spikeBaseline.Hours()
	if float64(last) < float64(settings.SpikeThreshold)*usual {
		return nil
	}

	fired, err := n.db.ClaimCooldown(ctx, fmt.Sprintf("spike:%d", p.ID), p.ID, strconv.Itoa(last), now, spikeCooldown)
	if err != nil || !fired {
		return err
	}
	top, err := n.db.TopIssuesBetween(ctx, p.ID, hourAgo, now.Add(time.Hour), 3)
	if err != nil {
		return err
	}
	n.send(ctx, p, settings, settings.Spike, n.spikeMessage(p, settings.SpikeThreshold, last, usual, top), store.AlertSpike, false)
	return nil
}

// spikeMessage says how many errors the last hour had against the usual
// rate, and which issues most of them belong to.
func (n *Notifier) spikeMessage(p store.Project, threshold, last int, usual float64, top []store.IssueCount) message {
	issues := n.link("/projects/%d/issues", p.ID)
	summary := fmt.Sprintf("%d errors in the last hour", last)
	compared := "; the 7 days before had none"
	if usual > 0 {
		compared = fmt.Sprintf(", %s times the usual %s an hour", decimal(float64(last)/usual), decimal(usual))
	}

	var text strings.Builder
	fmt.Fprintf(&text, "%s had %s%s.\nThe alert fires at %d times the usual rate (the hourly average of the 7 days before the last hour), and at %d errors or more; it waits 6 hours before it fires again.\n",
		p.Name, summary, compared, threshold, spikeMinimum)
	var slack strings.Builder
	fmt.Fprintf(&slack, "*Error spike in %s*: %s%s.", slackEscape(p.Name), summary, compared)
	if len(top) > 0 {
		text.WriteString("\nMost of them:\n")
		slack.WriteString("\nMost of them:")
		for _, ic := range top {
			fmt.Fprintf(&text, "  %5d  %s\n", ic.Events, issueLine(ic.Issue))
			fmt.Fprintf(&slack, "\n• %s (%d)", slackLink(n.link("/projects/%d/issues/%d", p.ID, ic.ID), issueLine(ic.Issue)), ic.Events)
		}
	}
	fmt.Fprintf(&text, "\nSee the issues: %s\n", issues)
	text.WriteString(n.footer(p))
	return message{
		subject: fmt.Sprintf("[%s] Error spike: %s", p.Name, summary),
		text:    text.String(),
		slack:   slack.String(),
	}
}

// decimal writes a number with at most one decimal: "5", "2.5", "0.3".
func decimal(f float64) string {
	return strconv.FormatFloat(float64(int64(f*10+0.5))/10, 'f', -1, 64)
}
