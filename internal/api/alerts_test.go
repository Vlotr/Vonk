package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

	// A member who sees the project reads its alerts (the webhook masked)
	// but cannot change them (ruling 33): 404, as on every admin route.
	member := env.member(t, "second@example.com", shop.ID)
	rec = env.as(t, member, "GET", path, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "https://hooks.slack.com/…/abcd") || strings.Contains(rec.Body.String(), "verysecret") {
		t.Errorf("member GET = %d %s, want the alerts with the webhook masked", rec.Code, rec.Body)
	}
	before, _ := env.db.AlertSettings(context.Background(), shop.ID)
	for _, change := range []string{`{"slackWebhookUrl":""}`, `{"alerts":{"newIssue":{"email":true}}}`} {
		if rec := env.as(t, member, "PATCH", path, change); rec.Code != http.StatusNotFound {
			t.Errorf("member PATCH %s = %d %s, want 404", change, rec.Code, rec.Body)
		}
	}
	alertSettingsAre(t, env, shop.ID, before)

	// An admin removes the webhook with "".
	if rec := env.as(t, env.admin, "PATCH", path, `{"slackWebhookUrl":""}`); rec.Code != http.StatusOK {
		t.Fatalf("admin PATCH = %d %s", rec.Code, rec.Body)
	}
	if got := getJSON[alertsResponse](t, env, path); got.SlackWebhook != nil || got.Alerts.Spike.Threshold != 10 {
		t.Errorf("after removing the webhook = %+v", got)
	}
}

// Webhooks must be https (ruling 33); plain http only with
// DEV_ALLOW_HTTP_WEBHOOKS, which this test server does not set.
func TestAlerts_WebhookMustBeHTTPS(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	plain := `"http://hooks.slack.com/services/T0001/B0001/plain"`
	for _, rt := range []struct{ method, path, body string }{
		{"PATCH", fmt.Sprintf("/ui/api/projects/%d/alerts", shop.ID), `{"slackWebhookUrl":` + plain + `}`},
		{"POST", fmt.Sprintf("/ui/api/projects/%d/alerts/slack-test", shop.ID), `{"webhookUrl":` + plain + `}`},
		{"PUT", "/ui/api/settings/admin-slack", `{"webhookUrl":` + plain + `}`},
		{"POST", "/ui/api/settings/admin-slack/test", `{"webhookUrl":` + plain + `}`},
	} {
		rec := env.as(t, env.admin, rt.method, rt.path, rt.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "must start with https://") {
			t.Errorf("%s %s = %d %s, want 400 asking for https", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
	if posts := env.slack.sent(); len(posts) != 0 {
		t.Errorf("posted to an http webhook: %+v", posts)
	}
	if s, _ := env.db.AlertSettings(context.Background(), shop.ID); s.SlackWebhookURL != "" {
		t.Errorf("stored webhook = %q", s.SlackWebhookURL)
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

// Two changes sent at once (a checkbox and the threshold field, say) each
// change their own field; neither undoes the other.
func TestAlerts_ConcurrentChangesBothStay(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	path := fmt.Sprintf("/ui/api/projects/%d/alerts", shop.ID)
	ctx := context.Background()

	for round := range 30 {
		on := round%2 == 0
		bodies := []string{
			fmt.Sprintf(`{"alerts":{"newIssue":{"email":%v}}}`, on),
			fmt.Sprintf(`{"alerts":{"regression":{"slack":%v}}}`, on),
			fmt.Sprintf(`{"alerts":{"spike":{"threshold":%d}}}`, 10+round),
		}
		var wg sync.WaitGroup
		start := make(chan struct{})
		for _, body := range bodies {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if rec := env.as(t, env.admin, "PATCH", path, body); rec.Code != http.StatusOK {
					t.Errorf("PATCH %s = %d %s", body, rec.Code, rec.Body)
				}
			}()
		}
		close(start)
		wg.Wait()

		got, err := env.db.AlertSettings(ctx, shop.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.NewIssue.Email != on || got.Regression.Slack != on || got.SpikeThreshold != 10+round {
			t.Fatalf("round %d: settings = %+v; want newIssue.email %v, regression.slack %v, threshold %d (a change was lost)",
				round, got, on, on, 10+round)
		}
	}
}
