package alerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode"
)

// A Slack incoming webhook URL (https://hooks.slack.com/services/T…/B…/…)
// is a secret: anyone who has it can post to the channel. It is never
// logged, never part of an error, and the API shows it only masked.

// slackTimeout limits one post to a webhook.
const slackTimeout = 10 * time.Second

// maxWebhookLength is the longest webhook URL accepted.
const maxWebhookLength = 500

// Slack posts messages to incoming webhooks.
type Slack struct {
	client *http.Client
}

// NewSlack returns a Slack client. Redirects are not followed: a webhook
// answers itself, and following one would send the message elsewhere.
func NewSlack() *Slack {
	return &Slack{client: &http.Client{
		Timeout: slackTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Post sends text (Slack's mrkdwn) to webhook. Its errors never contain the URL.
func (s *Slack) Post(ctx context.Context, webhook, text string) error {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, webhook, bytes.NewReader(body))
	if err != nil {
		return errors.New("The Slack webhook URL is not valid.")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		// A *url.Error repeats the whole URL; keep only what went wrong.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("Slack could not be reached: %v.", err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		if reason := slackReason(answer); reason != "" {
			return fmt.Errorf("Slack answered %d (%s).", resp.StatusCode, reason)
		}
		return fmt.Errorf("Slack answered %d.", resp.StatusCode)
	}
	return nil
}

// slackReason returns Slack's short error word ("no_service",
// "invalid_payload"), or "" when the answer is something else.
func slackReason(body []byte) string {
	s := strings.TrimSpace(string(body))
	if s == "" || len(s) > 60 {
		return ""
	}
	for _, r := range s {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' && r != ' ' {
			return ""
		}
	}
	return s
}

// CheckWebhookURL trims raw and checks that it can be a webhook: an
// http(s) URL with a host and no user name or password. Slack's start with
// https://hooks.slack.com/; Mattermost and others that take Slack's format
// work too, so other hosts (and plain http on a local network) are allowed.
func CheckWebhookURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	switch {
	case err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http"):
		return "", errors.New("Paste the whole webhook URL from Slack; it starts with https://hooks.slack.com/.")
	case u.User != nil:
		return "", errors.New("The webhook URL must not contain a user name or password.")
	case len(raw) > maxWebhookLength:
		return "", errors.New("The webhook URL is too long.")
	}
	return raw, nil
}

// MaskWebhook shows a webhook URL without its secret part: the scheme and
// host, and the last four characters, as https://hooks.slack.com/…/abcd.
// "" stays "".
func MaskWebhook(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "…"
	}
	secret := strings.TrimPrefix(raw, u.Scheme+"://"+u.Host)
	runes := []rune(secret)
	if len(runes) <= 8 {
		return u.Scheme + "://" + u.Host + "/…"
	}
	return u.Scheme + "://" + u.Host + "/…/" + string(runes[len(runes)-4:])
}

// slackEscape escapes the three characters Slack treats as markup in text.
func slackEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
}

// slackLink is a link in Slack's mrkdwn: <url|label>.
func slackLink(link, label string) string {
	return "<" + link + "|" + strings.ReplaceAll(slackEscape(label), "|", "¦") + ">"
}
