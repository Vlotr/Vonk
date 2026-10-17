package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"monitor/internal/alerts"
	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// A project's alerts. Everyone who may see the project reads them; only
// admins change them (ruling 33; members get 404, as on every admin route):
//
//	GET   /ui/api/projects/{id}/alerts             the choices, the masked Slack webhook, whether mail is on
//	PATCH /ui/api/projects/{id}/alerts             {alerts?: {newIssue?: {email?, slack?}, …, spike?: {threshold?}, digest?: {frequency?}}, slackWebhookUrl?}
//	POST  /ui/api/projects/{id}/alerts/slack-test  {webhookUrl?}: post a test message (to the saved webhook without one)
//
// The webhook URL is a secret: answers carry it only masked. It must be
// https, unless DEV_ALLOW_HTTP_WEBHOOKS is on.

// Spike thresholds are whole multiples of the usual rate, from 2 to 100.
const (
	minSpikeThreshold = 2
	maxSpikeThreshold = 100
)

// slackTestTimeout limits the test message.
const slackTestTimeout = 10 * time.Second

type alertChannelsJSON struct {
	Email bool `json:"email"`
	Slack bool `json:"slack"`
}

type spikeJSON struct {
	alertChannelsJSON
	Threshold int `json:"threshold"`
}

type digestJSON struct {
	alertChannelsJSON
	Frequency string `json:"frequency"`
}

type projectAlertsJSON struct {
	Project struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
	// MailEnabled is false without MAIL_URL: then nothing is mailed.
	MailEnabled bool `json:"mailEnabled"`
	// Recipients is how many people get the project's mail.
	Recipients int `json:"recipients"`
	// SlackWebhook is the masked webhook, null when there is none.
	SlackWebhook *string `json:"slackWebhook"`
	Alerts       struct {
		NewIssue   alertChannelsJSON `json:"newIssue"`
		Regression alertChannelsJSON `json:"regression"`
		Spike      spikeJSON         `json:"spike"`
		Digest     digestJSON        `json:"digest"`
	} `json:"alerts"`
}

func channelsJSON(c store.Channels) alertChannelsJSON {
	return alertChannelsJSON{Email: c.Email, Slack: c.Slack}
}

// maskedWebhook is the webhook for an answer: masked, or null.
func maskedWebhook(raw string) *string {
	if raw == "" {
		return nil
	}
	masked := alerts.MaskWebhook(raw)
	return &masked
}

// alertsProject reads the project of the URL that the user may see, answering 404 itself.
func (h *Handler) alertsProject(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	id, ok := pathID(r, "id")
	if !ok || !canSee(r, id) {
		notFound(w)
		return store.Project{}, false
	}
	p, err := h.db.ProjectByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return store.Project{}, false
	}
	if err != nil {
		h.internalError(w, r, err)
		return store.Project{}, false
	}
	return p, true
}

func (h *Handler) projectAlerts(w http.ResponseWriter, r *http.Request) {
	p, ok := h.alertsProject(w, r)
	if !ok {
		return
	}
	settings, err := h.db.AlertSettings(r.Context(), p.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.writeAlerts(w, r, p, settings)
}

func (h *Handler) writeAlerts(w http.ResponseWriter, r *http.Request, p store.Project, s store.AlertSettings) {
	recipients, err := h.db.ProjectRecipients(r.Context(), p.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	var out projectAlertsJSON
	out.Project.ID, out.Project.Name = p.ID, p.Name
	out.MailEnabled = h.auth.MailEnabled()
	out.Recipients = len(recipients)
	out.SlackWebhook = maskedWebhook(s.SlackWebhookURL)
	out.Alerts.NewIssue = channelsJSON(s.NewIssue)
	out.Alerts.Regression = channelsJSON(s.Regression)
	out.Alerts.Spike = spikeJSON{channelsJSON(s.Spike), s.SpikeThreshold}
	out.Alerts.Digest = digestJSON{channelsJSON(s.Digest), s.DigestFrequency}
	httpjson.Write(w, http.StatusOK, out)
}

// kindBody is one row of the Alerts page in a PATCH; fields left out stay.
type kindBody struct {
	Email     *bool   `json:"email"`
	Slack     *bool   `json:"slack"`
	Threshold *int    `json:"threshold"` // spike only
	Frequency *string `json:"frequency"` // digest only
}

func (b *kindBody) apply(c *store.Channels) {
	if b == nil {
		return
	}
	if b.Email != nil {
		c.Email = *b.Email
	}
	if b.Slack != nil {
		c.Slack = *b.Slack
	}
}

type alertsBody struct {
	Alerts *struct {
		NewIssue   *kindBody `json:"newIssue"`
		Regression *kindBody `json:"regression"`
		Spike      *kindBody `json:"spike"`
		Digest     *kindBody `json:"digest"`
	} `json:"alerts"`
	// SlackWebhookURL: "" removes the webhook; left out, it stays.
	SlackWebhookURL *string `json:"slackWebhookUrl"`
}

// check refuses values that cannot be stored, and normalizes the webhook.
// It needs no stored settings, so the change itself cannot fail.
func (b *alertsBody) check(allowHTTP bool) error {
	if a := b.Alerts; a != nil {
		if a.Spike != nil && a.Spike.Threshold != nil {
			if t := *a.Spike.Threshold; t < minSpikeThreshold || t > maxSpikeThreshold {
				return fmt.Errorf("The spike threshold is a whole number from %d to %d.", minSpikeThreshold, maxSpikeThreshold)
			}
		}
		if a.Digest != nil && a.Digest.Frequency != nil && !store.ValidDigestFrequency(*a.Digest.Frequency) {
			return errors.New("The summary goes out daily, weekly or monthly.")
		}
	}
	if b.SlackWebhookURL != nil && *b.SlackWebhookURL != "" {
		webhook, err := alerts.CheckWebhookURL(*b.SlackWebhookURL, allowHTTP)
		if err != nil {
			return err
		}
		b.SlackWebhookURL = &webhook
	}
	return nil
}

// apply puts the fields that were sent onto s; check them first.
func (b alertsBody) apply(s *store.AlertSettings) {
	if a := b.Alerts; a != nil {
		a.NewIssue.apply(&s.NewIssue)
		a.Regression.apply(&s.Regression)
		a.Spike.apply(&s.Spike)
		a.Digest.apply(&s.Digest)
		if a.Spike != nil && a.Spike.Threshold != nil {
			s.SpikeThreshold = *a.Spike.Threshold
		}
		if a.Digest != nil && a.Digest.Frequency != nil {
			s.DigestFrequency = *a.Digest.Frequency
		}
	}
	if b.SlackWebhookURL != nil {
		s.SlackWebhookURL = *b.SlackWebhookURL
	}
}

// updateProjectAlerts changes what was sent, for admins (ruling 33). The
// change is read, applied and written in one transaction, so two changes
// sent at once (a checkbox, the threshold) both stay.
func (h *Handler) updateProjectAlerts(w http.ResponseWriter, r *http.Request) {
	var body alertsBody
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	if err := body.check(h.allowHTTP); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	p, ok := h.alertsProject(w, r)
	if !ok {
		return
	}
	settings, err := h.db.UpdateAlertSettings(r.Context(), p.ID, body.apply)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.writeAlerts(w, r, p, settings)
}

// testProjectSlack posts a test message to the webhook in the body, or to
// the project's saved one. 400 says why it did not arrive.
func (h *Handler) testProjectSlack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookURL string `json:"webhookUrl"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	p, ok := h.alertsProject(w, r)
	if !ok {
		return
	}
	webhook := body.WebhookURL
	if webhook == "" {
		settings, err := h.db.AlertSettings(r.Context(), p.ID)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		webhook = settings.SlackWebhookURL
	}
	h.sendSlackTest(w, r, webhook, fmt.Sprintf("Test message from %s: alerts of %s will arrive here.", hostOf(h.baseURL), p.Name))
}

// sendSlackTest checks webhook and posts text to it.
func (h *Handler) sendSlackTest(w http.ResponseWriter, r *http.Request, webhook, text string) {
	if webhook == "" {
		httpjson.Error(w, http.StatusBadRequest, "Add a Slack webhook first.")
		return
	}
	webhook, err := alerts.CheckWebhookURL(webhook, h.allowHTTP)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), slackTestTimeout)
	defer cancel()
	if err := h.slack.Post(ctx, webhook, text); err != nil {
		httpjson.Error(w, http.StatusBadRequest, "The test message did not arrive. "+err.Error())
		return
	}
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// hostOf is the host of BASE_URL, which names this installation in messages.
func hostOf(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && u.Host != "" {
		return u.Host
	}
	return baseURL
}
