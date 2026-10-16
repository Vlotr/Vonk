package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"monitor/internal/store"
)

type alertsResponse struct {
	Project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	MailEnabled  bool    `json:"mailEnabled"`
	Recipients   int     `json:"recipients"`
	SlackWebhook *string `json:"slackWebhook"`
	Alerts       struct {
		NewIssue   struct{ Email, Slack bool } `json:"newIssue"`
		Regression struct{ Email, Slack bool } `json:"regression"`
		Spike      struct {
			Email, Slack bool
			Threshold    int `json:"threshold"`
		} `json:"spike"`
		Digest struct {
			Email, Slack bool
			Frequency    string `json:"frequency"`
		} `json:"digest"`
	} `json:"alerts"`
}

const secretWebhook = "https://hooks.slack.com/services/T0001/B0001/verysecretwebhookabcd"

func TestAlerts_DefaultsAndChanges(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	env.member(t, "member@example.com", shop.ID)
	path := fmt.Sprintf("/ui/api/projects/%d/alerts", shop.ID)

	got := getJSON[alertsResponse](t, env, path)
	a := got.Alerts
	if got.Project.Name != "Webshop" || got.MailEnabled || got.Recipients != 2 || got.SlackWebhook != nil {
		t.Errorf("alerts = %+v", got)
	}
	if !a.NewIssue.Email || !a.NewIssue.Slack || !a.Regression.Email || !a.Spike.Slack || a.Spike.Threshold != 5 ||
		a.Digest.Email || a.Digest.Slack || a.Digest.Frequency != "weekly" {
		t.Errorf("defaults = %+v", a)
	}

	// A change sends only what changes; the answer is the new state.
	rec := env.as(t, env.admin, "PATCH", path, `{"alerts":{"newIssue":{"email":false},"spike":{"threshold":10},
		"digest":{"email":true,"frequency":"daily"}},"slackWebhookUrl":" `+secretWebhook+` "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH = %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "verysecret") {
		t.Errorf("the answer shows the webhook: %s", rec.Body)
	}
	got = getJSON[alertsResponse](t, env, path)
	a = got.Alerts
	if a.NewIssue.Email || !a.NewIssue.Slack || a.Spike.Threshold != 10 || !a.Digest.Email || a.Digest.Frequency != "daily" {
		t.Errorf("after the change = %+v", a)
	}
	if got.SlackWebhook == nil || *got.SlackWebhook != "https://hooks.slack.com/…/abcd" {
		t.Errorf("masked webhook = %v", got.SlackWebhook)
	}
	if s, _ := env.db.AlertSettings(context.Background(), shop.ID); s.SlackWebhookURL != secretWebhook {
		t.Errorf("stored webhook = %q", s.SlackWebhookURL)
	}

	for _, bad := range []string{
		`{"alerts":{"spike":{"threshold":1}}}`,
		`{"alerts":{"spike":{"threshold":101}}}`,
		`{"alerts":{"digest":{"frequency":"hourly"}}}`,
		`{"slackWebhookUrl":"ftp://hooks.slack.com/x"}`,
		`{"slackWebhookUrl":"not a url"}`,
		`{"slackWebhookUrl":"https://user:pass@hooks.slack.com/services/x"}`,
	} {
		if rec := env.as(t, env.admin, "PATCH", path, bad); rec.Code != http.StatusBadRequest || !strings.HasSuffix(strings.TrimSpace(rec.Body.String()), `."}`) {
			t.Errorf("PATCH %s = %d %s, want 400 with a sentence", bad, rec.Code, rec.Body)
		}
	}

	// A member who sees the project may change its alerts; "" removes the webhook.
	member := env.member(t, "second@example.com", shop.ID)
	if rec := env.as(t, member, "PATCH", path, `{"slackWebhookUrl":""}`); rec.Code != http.StatusOK {
		t.Fatalf("member PATCH = %d %s", rec.Code, rec.Body)
	}
	if got := getJSON[alertsResponse](t, env, path); got.SlackWebhook != nil || got.Alerts.Spike.Threshold != 10 {
		t.Errorf("after removing the webhook = %+v", got)
	}
}

func TestAlerts_SlackTestMessage(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	path := fmt.Sprintf("/ui/api/projects/%d/alerts/slack-test", shop.ID)

	rec := env.as(t, env.admin, "POST", path, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Add a Slack webhook first.") {
		t.Errorf("without a webhook = %d %s", rec.Code, rec.Body)
	}

	// A webhook in the body is tried before it is saved.
	if rec := env.as(t, env.admin, "POST", path, `{"webhookUrl":"`+secretWebhook+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("test with a webhook = %d %s", rec.Code, rec.Body)
	}
	posts := env.slack.sent()
	if len(posts) != 1 || posts[0].webhook != secretWebhook ||
		posts[0].text != "Test message from monitor.example.com: alerts of Webshop will arrive here." {
		t.Errorf("posts = %+v", posts)
	}

	// Without one in the body, the saved webhook.
	env.as(t, env.admin, "PATCH", fmt.Sprintf("/ui/api/projects/%d/alerts", shop.ID), `{"slackWebhookUrl":"https://hooks.slack.com/services/saved"}`)
	if rec := env.as(t, env.admin, "POST", path, `{}`); rec.Code != http.StatusOK {
		t.Fatalf("test of the saved webhook = %d %s", rec.Code, rec.Body)
	}
	if posts := env.slack.sent(); len(posts) != 2 || posts[1].webhook != "https://hooks.slack.com/services/saved" {
		t.Errorf("posts = %+v", posts)
	}

	// Slack's answer is passed on.
	env.slack.err = errors.New("Slack answered 404 (no_service).")
	rec = env.as(t, env.admin, "POST", path, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "The test message did not arrive. Slack answered 404 (no_service).") {
		t.Errorf("failed test = %d %s", rec.Code, rec.Body)
	}
}

// The stored settings of a project, to show a refused change changed nothing.
func alertSettingsAre(t *testing.T, env *testEnv, projectID int64, want store.AlertSettings) {
	t.Helper()
	got, err := env.db.AlertSettings(context.Background(), projectID)
	if err != nil || got != want {
		t.Errorf("alert settings after a refused request = %+v, %v; want %+v", got, err, want)
	}
}
