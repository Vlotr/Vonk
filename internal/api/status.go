package api

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"monitor/internal/alerts"
	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// Settings → Status, for admins:
//
//	GET  /ui/api/settings/status           how the tool itself is doing
//	PUT  /ui/api/settings/admin-slack      {webhookUrl}: the admins' Slack webhook ("" removes it)
//	POST /ui/api/settings/admin-slack/test {webhookUrl?}: post a test message
//
// The admins' webhook gets the alerts about the tool itself, next to the
// mail to every admin. Like project webhooks it is answered only masked.

type backupJSON struct {
	Name string `json:"name"`
	// Kind is "pre-migrate" (before a database update) or "pre-restore"
	// (the database a restore replaced).
	Kind      string    `json:"kind"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
}

type adminAlertJSON struct {
	Kind    string    `json:"kind"`
	Message string    `json:"message"`
	SentAt  time.Time `json:"sentAt"`
}

type statusJSON struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	// UptimeSeconds is how long the process has run.
	UptimeSeconds int64 `json:"uptimeSeconds"`
	// Status is what /health says: "ok", "degraded" (it works, but a
	// check failed; 200) or "error" (it cannot work; 503). Failing names
	// the checks that failed.
	Status  string   `json:"status"`
	Failing []string `json:"failing"`
	// DB is "ok" or "error"; Disk "ok", "low", "critical" or "error".
	DB   string `json:"db"`
	Disk string `json:"disk"`
	// DatabaseKind is "sqlite" or "postgres" (DATABASE_URL); the tool
	// backs up only SQLite, so for Postgres the page shows how to use pg_dump.
	DatabaseKind   string `json:"databaseKind"`
	DatabaseBytes  int64  `json:"databaseBytes"`
	DiskFreeBytes  uint64 `json:"diskFreeBytes"`
	DiskTotalBytes uint64 `json:"diskTotalBytes"`
	// EventsLastHour counts the events stored in the last hour (since the
	// start, when that is less than an hour ago), all projects.
	EventsLastHour int `json:"eventsLastHour"`
	QueueDepth     int `json:"queueDepth"`
	QueueCapacity  int `json:"queueCapacity"`
	// LastBackup is the newest automatic backup (before a database update
	// or a restore); null when none was needed yet, and always on Postgres.
	LastBackup *backupJSON `json:"lastBackup"`
	// AdminAlerts are the last alert of each kind about the tool, newest first.
	AdminAlerts []adminAlertJSON `json:"adminAlerts"`
	// AdminSlackWebhook is masked, null when none is set.
	AdminSlackWebhook *string `json:"adminSlackWebhook"`
	MailEnabled       bool    `json:"mailEnabled"`
	// Admins is how many people get the admin mails.
	Admins int `json:"admins"`
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	now := h.now()
	report := h.health.Check(ctx)

	out := statusJSON{
		Version:        report.Version,
		StartedAt:      h.started,
		UptimeSeconds:  int64(now.Sub(h.started).Seconds()),
		Status:         report.Status,
		Failing:        report.Failing,
		DB:             report.DB,
		Disk:           report.Disk,
		DatabaseKind:   "sqlite",
		DatabaseBytes:  h.db.SizeBytes(ctx),
		DiskFreeBytes:  report.DiskFreeBytes,
		DiskTotalBytes: report.DiskTotalBytes,
		QueueDepth:     report.QueueDepth,
		AdminAlerts:    []adminAlertJSON{},
		MailEnabled:    h.auth.MailEnabled(),
	}
	if h.db.Postgres() {
		out.DatabaseKind = "postgres"
	}
	if out.Failing == nil {
		out.Failing = []string{}
	}
	if h.health.Queue != nil {
		out.QueueCapacity = h.health.Queue.Cap()
	}

	out.EventsLastHour = h.stored()
	backups, err := h.db.Backups()
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if len(backups) > 0 {
		b := backups[0]
		out.LastBackup = &backupJSON{Name: b.Name, Kind: b.Kind, CreatedAt: b.CreatedAt, SizeBytes: b.SizeBytes}
	}
	states, err := h.db.AlertStates(ctx, "admin:")
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	for _, st := range states {
		out.AdminAlerts = append(out.AdminAlerts, adminAlertJSON{Kind: strings.TrimPrefix(st.Name, "admin:"), Message: st.Value, SentAt: st.SentAt})
	}
	webhook, err := h.db.Setting(ctx, store.SettingAdminSlackWebhook)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out.AdminSlackWebhook = maskedWebhook(webhook)
	admins, err := h.db.AdminEmails(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	out.Admins = len(admins)
	httpjson.Write(w, http.StatusOK, out)
}

func (h *Handler) setAdminSlack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookURL *string `json:"webhookUrl"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	if body.WebhookURL == nil {
		httpjson.Error(w, http.StatusBadRequest, "Send webhookUrl: the webhook, or \"\" to remove it.")
		return
	}
	webhook := *body.WebhookURL
	if webhook != "" {
		checked, err := alerts.CheckWebhookURL(webhook, h.allowHTTP)
		if err != nil {
			httpjson.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		webhook = checked
	}
	if err := h.db.SetSetting(r.Context(), store.SettingAdminSlackWebhook, webhook, h.now()); err != nil {
		h.internalError(w, r, err)
		return
	}
	h.log.Info("admin Slack webhook changed", "set", webhook != "")
	httpjson.Write(w, http.StatusOK, map[string]*string{"adminSlackWebhook": maskedWebhook(webhook)})
}

func (h *Handler) testAdminSlack(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WebhookURL string `json:"webhookUrl"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	webhook := body.WebhookURL
	if webhook == "" {
		saved, err := h.db.Setting(r.Context(), store.SettingAdminSlackWebhook)
		if err != nil {
			h.internalError(w, r, err)
			return
		}
		webhook = saved
	}
	h.sendSlackTest(w, r, webhook, fmt.Sprintf("Test message from %s: alerts about the tool itself will arrive here.", hostOf(h.baseURL)))
}
