package alerts

import (
	"fmt"
	"strings"

	"monitor/internal/store"
	"monitor/internal/text"
)

// message is one notification, written for mail and for Slack.
type message struct {
	subject string // the mail's subject
	text    string // the mail's body, plain text
	slack   string // the Slack text, mrkdwn
}

// issueMessage writes the notification about a new or returning issue.
func (n *Notifier) issueMessage(kind string, p store.Project, issue store.Issue) message {
	link := n.link("/projects/%d/issues/%d", p.ID, issue.ID)
	line := issueLine(issue)
	head, lead := "New issue", fmt.Sprintf("A new error came in for %s:", p.Name)
	if kind == store.AlertRegression {
		head = "Returning issue"
		lead = fmt.Sprintf("An issue of %s that was resolved came back, and is open again:", p.Name)
	}

	var details []string
	if issue.Culprit != "" {
		details = append(details, issue.Culprit)
	}
	if issue.Source != "" {
		details = append(details, sourceLabel(issue.Source))
	}

	// The release it came back in, as the issue page says it.
	cameBack := ""
	if kind == store.AlertRegression && issue.RegressedRelease != "" {
		cameBack = "Came back in " + issue.RegressedRelease
	}

	var body strings.Builder
	fmt.Fprintf(&body, "%s\n\n%s\n", lead, line)
	if len(details) > 0 {
		fmt.Fprintf(&body, "%s\n", strings.Join(details, " · "))
	}
	if cameBack != "" {
		fmt.Fprintf(&body, "%s\n", cameBack)
	}
	fmt.Fprintf(&body, "\nOpen the issue: %s\n", link)
	body.WriteString(n.footer(p))

	slack := fmt.Sprintf("*%s in %s*\n%s", head, slackEscape(p.Name), slackLink(link, line))
	if issue.Culprit != "" {
		slack += "\n`" + strings.ReplaceAll(slackEscape(issue.Culprit), "`", "'") + "`"
	}
	if cameBack != "" {
		slack += "\n" + slackEscape(cameBack)
	}
	return message{
		subject: fmt.Sprintf("[%s] %s: %s", p.Name, head, text.Shorten(line, 150)),
		text:    body.String(),
		slack:   slack,
	}
}

// issueLine names an issue: "RuntimeException: Checkout failed".
func issueLine(issue store.Issue) string {
	line := issue.Title
	if issue.Message != "" && issue.Message != issue.Title {
		line += ": " + issue.Message
	}
	return text.Shorten(strings.Join(strings.Fields(line), " "), 300)
}

// sourceLabel is how the dashboard names a source.
func sourceLabel(source string) string {
	switch source {
	case "laravel":
		return "Laravel"
	case "browser":
		return "Browser"
	}
	return source
}

// link is an address in the dashboard.
func (n *Notifier) link(format string, args ...any) string {
	return n.baseURL + fmt.Sprintf(format, args...)
}
