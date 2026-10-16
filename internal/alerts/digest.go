package alerts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"monitor/internal/store"
)

// Summaries go out at 08:00 in TZ: daily ones every day for the day
// before, weekly ones on Mondays for the week before (Monday to Sunday),
// monthly ones on the 1st for the month before. Each period goes out once:
// the period is claimed in alert_state right before the messages are queued.

// digestTop is how many of the busiest issues a summary lists, and how
// many of the new ones.
const digestTop = 5

// digestPeriod is the stretch of time one summary covers, on TZ's clock.
type digestPeriod struct {
	from, to time.Time
	title    string // "Daily summary"
	label    string // "Thursday, October 15"
	key      string // "2026-10-15", the period's first day
}

// periodFor returns the period a summary of frequency sent at at covers,
// or false when at is not its day (weekly: Monday; monthly: the 1st).
func periodFor(frequency string, at time.Time, loc *time.Location) (digestPeriod, bool) {
	local := at.In(loc)
	today := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var p digestPeriod
	switch frequency {
	case store.DigestDaily:
		p.from, p.to, p.title = today.AddDate(0, 0, -1), today, "Daily summary"
		p.label = p.from.Format("Monday, January 2")
	case store.DigestWeekly:
		if local.Weekday() != time.Monday {
			return p, false
		}
		p.from, p.to, p.title = today.AddDate(0, 0, -7), today, "Weekly summary"
		p.label = p.from.Format("January 2") + " to " + today.AddDate(0, 0, -1).Format("January 2")
	case store.DigestMonthly:
		if local.Day() != 1 {
			return p, false
		}
		p.from, p.to, p.title = today.AddDate(0, -1, 0), today, "Monthly summary"
		p.label = p.from.Format("January 2006")
	default:
		return p, false
	}
	p.key = p.from.Format("2006-01-02")
	return p, true
}

// SendDigests sends the summaries due at at to every project that has
// them on. The summaries job runs it at 08:00 in TZ.
func (n *Notifier) SendDigests(ctx context.Context, at time.Time) {
	projects, err := n.db.ListProjects(ctx)
	if err != nil {
		n.log.Error("alerts: summaries: list projects", "err", err)
		return
	}
	open, err := n.db.CountOpenIssuesPerProject(ctx)
	if err != nil {
		n.log.Error("alerts: summaries: count open issues", "err", err)
		return
	}
	for _, p := range projects {
		if ctx.Err() != nil {
			return
		}
		if err := n.sendDigest(ctx, p, at, open[p.ID]); err != nil {
			n.log.Error("alerts: summary", "project", p.ID, "err", err)
		}
	}
}

// sendDigest sends one project's summary; open is how many of its issues are open now.
func (n *Notifier) sendDigest(ctx context.Context, p store.Project, at time.Time, open int) error {
	settings, err := n.db.AlertSettings(ctx, p.ID)
	if err != nil || !settings.Digest.Any() {
		return err
	}
	period, due := periodFor(settings.DigestFrequency, at, n.loc)
	if !due {
		return nil
	}
	total, err := n.db.CountErrorEventsBetween(ctx, p.ID, period.from, period.to)
	if err != nil {
		return err
	}
	newCount, newIssues, err := n.db.NewIssuesBetween(ctx, p.ID, period.from, period.to, digestTop)
	if err != nil {
		return err
	}
	top, err := n.db.TopIssuesBetween(ctx, p.ID, period.from, period.to, digestTop)
	if err != nil {
		return err
	}
	// Page views when the project shows them: from the hourly summaries
	// of the hours that start in the period.
	var pages *store.RouteStats
	if p.TracksPageviews {
		stats, err := n.db.PageStats(ctx, p.ID, period.from, period.to)
		if err != nil {
			return err
		}
		pages = &stats.Totals
	}
	// Claimed last, so a failed query above leaves the period to the next run.
	claimed, err := n.db.ClaimPeriod(ctx, fmt.Sprintf("digest:%d:%s", p.ID, settings.DigestFrequency), p.ID, period.key, n.now())
	if err != nil || !claimed {
		return err
	}
	n.send(ctx, p, settings, settings.Digest, n.digestMessage(p, period, total, newCount, newIssues, top, open, pages), store.AlertDigest, false)
	return nil
}

// digestMessage writes a summary: the totals, the new issues and the
// busiest issues of the period, how many issues are open now, and the
// page views, visitors and average time on a page when pages is not nil.
func (n *Notifier) digestMessage(p store.Project, period digestPeriod, total, newCount int, newIssues []store.Issue, top []store.IssueCount, open int, pages *store.RouteStats) message {
	issues := n.link("/projects/%d/issues", p.ID)
	issueLink := func(i store.Issue) string { return n.link("/projects/%d/issues/%d", p.ID, i.ID) }

	var text strings.Builder
	fmt.Fprintf(&text, "%s of %s for %s.\n\n", period.title, p.Name, period.label)
	fmt.Fprintf(&text, "Errors: %d\nNew issues: %d\nOpen issues now: %d\n", total, newCount, open)
	var slack strings.Builder
	fmt.Fprintf(&slack, "*%s of %s* for %s: %s, %s, %s open now.", period.title, slackEscape(p.Name), period.label,
		plural(total, "error"), plural(newCount, "new issue"), plural(open, "issue"))
	if pages != nil {
		fmt.Fprintf(&text, "Page views: %d\nVisitors: %d\n", pages.Views, pages.Visitors)
		fmt.Fprintf(&slack, "\n%s by %s", plural(int(pages.Views), "page view"), plural(pages.Visitors, "visitor"))
		if avg, ok := pages.AvgDurationMs(); ok {
			fmt.Fprintf(&text, "Average time on page: %s\n", formatDuration(avg))
			fmt.Fprintf(&slack, ", %s on a page on average", formatDuration(avg))
		}
		slack.WriteString(".")
	}

	if len(newIssues) > 0 {
		text.WriteString("\nNew issues:\n")
		slack.WriteString("\nNew:")
		for _, i := range newIssues {
			fmt.Fprintf(&text, "  %s\n  %s\n", issueLine(i), issueLink(i))
			fmt.Fprintf(&slack, "\n• %s", slackLink(issueLink(i), issueLine(i)))
		}
		if newCount > len(newIssues) {
			fmt.Fprintf(&text, "  and %d more\n", newCount-len(newIssues))
		}
	}
	if len(top) > 0 {
		text.WriteString("\nMost errors:\n")
		slack.WriteString("\nMost errors:")
		for _, ic := range top {
			fmt.Fprintf(&text, "  %5d  %s\n", ic.Events, issueLine(ic.Issue))
			fmt.Fprintf(&slack, "\n• %s (%d)", slackLink(issueLink(ic.Issue), issueLine(ic.Issue)), ic.Events)
		}
	}
	fmt.Fprintf(&text, "\nSee the issues: %s\n", issues)
	text.WriteString(n.footer(p))
	return message{
		subject: fmt.Sprintf("[%s] %s for %s", p.Name, period.title, period.label),
		text:    text.String(),
		slack:   slack.String(),
	}
}

// plural writes "1 error" or "5 errors".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// formatDuration writes a time on a page as the dashboard does: "45s",
// "1m 12s", "2m 05s", or "1h 02m" from an hour on.
func formatDuration(ms int64) string {
	seconds := (ms + 500) / 1000
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm %02ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%dh %02dm", seconds/3600, seconds%3600/60)
}
