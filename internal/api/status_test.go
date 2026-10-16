package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

type toolStatusResponse struct {
	Version        string   `json:"version"`
	UptimeSeconds  int64    `json:"uptimeSeconds"`
	Healthy        bool     `json:"healthy"`
	Failing        []string `json:"failing"`
	DB             string   `json:"db"`
	Disk           string   `json:"disk"`
	DatabaseBytes  int64    `json:"databaseBytes"`
	DiskFreeBytes  uint64   `json:"diskFreeBytes"`
	DiskTotalBytes uint64   `json:"diskTotalBytes"`
	EventsLastHour int      `json:"eventsLastHour"`
	QueueDepth     int      `json:"queueDepth"`
	QueueCapacity  int      `json:"queueCapacity"`
	LastBackup     *struct {
		Name      string    `json:"name"`
		CreatedAt time.Time `json:"createdAt"`
		SizeBytes int64     `json:"sizeBytes"`
	} `json:"lastBackup"`
	AdminAlerts []struct {
		Kind    string    `json:"kind"`
		Message string    `json:"message"`
		SentAt  time.Time `json:"sentAt"`
	} `json:"adminAlerts"`
	AdminSlackWebhook *string `json:"adminSlackWebhook"`
	MailEnabled       bool    `json:"mailEnabled"`
	Admins            int     `json:"admins"`
}

func TestStatus_ShowsHowTheToolIsDoing(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	shop := env.project(t, "Webshop")
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1)}), phpEvent(t, eventSpec{id: hexID(2), at: testNow.Add(-2 * time.Hour)}))

	got := getJSON[toolStatusResponse](t, env, "/ui/api/settings/status")
	if got.Version != "test" || got.UptimeSeconds != 90*60 || !got.Healthy || len(got.Failing) != 0 || got.DB != "ok" || got.Disk != "ok" {
		t.Errorf("status = %+v", got)
	}
	if got.DatabaseBytes <= 0 || got.DiskFreeBytes != 40<<30 || got.DiskTotalBytes != 100<<30 || got.QueueCapacity != 100 || got.Admins != 1 {
		t.Errorf("numbers = %+v", got)
	}
	if got.EventsLastHour != 1 {
		t.Errorf("events in the last hour = %d, want 1 (the other is 2 hours old)", got.EventsLastHour)
	}
	if got.LastBackup != nil || len(got.AdminAlerts) != 0 || got.AdminSlackWebhook != nil {
		t.Errorf("before any backup, alert or webhook = %+v", got)
	}

	// A backup, an admin alert, a webhook and a low disk.
	if err := os.MkdirAll(filepath.Join(env.dir, "backups"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.dir, "backups", "pre-migrate-0003-20261015T080000Z.db"), []byte("backup"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := env.db.ClaimCooldown(ctx, "admin:disk_low", 0, "Disk space is low: 3 GiB free (3 %).", testNow, 12*time.Hour); err != nil {
		t.Fatal(err)
	}
	if rec := env.as(t, env.admin, "PUT", "/ui/api/settings/admin-slack", `{"webhookUrl":"`+secretWebhook+`"}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"adminSlackWebhook":"https://hooks.slack.com/…/abcd"`) {
		t.Fatalf("PUT admin-slack = %d %s", rec.Code, rec.Body)
	}
	env.health.DiskUsage = func(string) (uint64, uint64, error) { return 3 << 30, 100 << 30, nil }

	rec := env.get(t, "/ui/api/settings/status")
	if strings.Contains(rec.Body.String(), "verysecret") {
		t.Errorf("the status shows the webhook: %s", rec.Body)
	}
	got = getJSON[toolStatusResponse](t, env, "/ui/api/settings/status")
	if got.Healthy || len(got.Failing) != 1 || got.Failing[0] != "disk" || got.Disk != "low" {
		t.Errorf("with a low disk = %+v", got)
	}
	if got.LastBackup == nil || got.LastBackup.Name != "pre-migrate-0003-20261015T080000Z.db" || got.LastBackup.SizeBytes != 6 ||
		!got.LastBackup.CreatedAt.Equal(time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("last backup = %+v", got.LastBackup)
	}
	if len(got.AdminAlerts) != 1 || got.AdminAlerts[0].Kind != "disk_low" || !strings.HasPrefix(got.AdminAlerts[0].Message, "Disk space is low") {
		t.Errorf("admin alerts = %+v", got.AdminAlerts)
	}
	if got.AdminSlackWebhook == nil || *got.AdminSlackWebhook != "https://hooks.slack.com/…/abcd" {
		t.Errorf("admin webhook = %v", got.AdminSlackWebhook)
	}
}

func TestStatus_AdminSlackWebhook(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	for _, bad := range []string{`{}`, `{"webhookUrl":"hooks.slack.com/x"}`, `{"webhookUrl":"javascript:alert(1)"}`} {
		if rec := env.as(t, env.admin, "PUT", "/ui/api/settings/admin-slack", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d %s, want 400", bad, rec.Code, rec.Body)
		}
	}

	if rec := env.as(t, env.admin, "POST", "/ui/api/settings/admin-slack/test", `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("test without a webhook = %d, want 400", rec.Code)
	}
	env.as(t, env.admin, "PUT", "/ui/api/settings/admin-slack", `{"webhookUrl":"`+secretWebhook+`"}`)
	if rec := env.as(t, env.admin, "POST", "/ui/api/settings/admin-slack/test", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("test = %d %s", rec.Code, rec.Body)
	}
	if posts := env.slack.sent(); len(posts) != 1 || posts[0].webhook != secretWebhook ||
		!strings.Contains(posts[0].text, "alerts about the tool itself will arrive here") {
		t.Errorf("posts = %+v", posts)
	}

	if rec := env.as(t, env.admin, "PUT", "/ui/api/settings/admin-slack", `{"webhookUrl":""}`); rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"adminSlackWebhook":null`) {
		t.Errorf("remove = %d %s", rec.Code, rec.Body)
	}
	if v, _ := env.db.Setting(ctx, store.SettingAdminSlackWebhook); v != "" {
		t.Errorf("webhook after removing = %q", v)
	}
}
