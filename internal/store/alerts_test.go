package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestAlertSettings_DefaultsThenSaved(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")

	got, err := db.AlertSettings(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, DefaultAlertSettings()) {
		t.Errorf("never saved = %+v, want the defaults %+v", got, DefaultAlertSettings())
	}

	want := AlertSettings{
		NewIssue:        Channels{Email: false, Slack: true},
		Regression:      Channels{Email: true, Slack: false},
		Spike:           Channels{},
		SpikeThreshold:  10,
		Digest:          Channels{Email: true},
		DigestFrequency: DigestMonthly,
		SlackWebhookURL: "https://hooks.slack.com/services/T0/B0/abcd",
	}
	if err := db.SaveAlertSettings(ctx, p.ID, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.AlertSettings(ctx, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("saved = %+v, want %+v", got, want)
	}
	// Saving again replaces the rows.
	want.NewIssue.Email = true
	if err := db.SaveAlertSettings(ctx, p.ID, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.AlertSettings(ctx, p.ID); !reflect.DeepEqual(got, want) {
		t.Errorf("saved twice = %+v, want %+v", got, want)
	}

	if _, err := db.AlertSettings(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
	if err := db.SaveAlertSettings(ctx, 999, want); !errors.Is(err, ErrNotFound) {
		t.Errorf("save for an unknown project: err = %v, want ErrNotFound", err)
	}
}

func TestProjectRecipients_AdminsAndMembersWithAccess(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	shop := addProject(t, db, "Webshop")
	portal := addProject(t, db, "Portal")
	addUser(t, db, "admin@example.com", RoleAdmin)
	addUser(t, db, "second-admin@example.com", RoleAdmin)
	addUser(t, db, "shop@example.com", RoleMember, shop.ID)
	addUser(t, db, "portal@example.com", RoleMember, portal.ID)

	got, err := db.ProjectRecipients(ctx, shop.ID)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"admin@example.com", "second-admin@example.com", "shop@example.com"}; !reflect.DeepEqual(got, want) {
		t.Errorf("recipients = %v, want %v", got, want)
	}
	if got, _ := db.AdminEmails(ctx); !reflect.DeepEqual(got, []string{"admin@example.com", "second-admin@example.com"}) {
		t.Errorf("admins = %v", got)
	}
}

func TestClaim_CooldownAndPeriod(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	claim := func(at time.Time) bool {
		t.Helper()
		ok, err := db.ClaimCooldown(ctx, "spike:1", p.ID, "", at, 6*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !claim(now) {
		t.Error("first claim refused")
	}
	if claim(now.Add(5*time.Hour + 59*time.Minute)) {
		t.Error("claim inside the cooldown went through")
	}
	if !claim(now.Add(6 * time.Hour)) {
		t.Error("claim after the cooldown refused")
	}

	period := func(value string) bool {
		t.Helper()
		ok, err := db.ClaimPeriod(ctx, "digest:1:daily", p.ID, value, now)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !period("2026-10-15") || period("2026-10-15") || !period("2026-10-16") {
		t.Error("a period must be claimed once")
	}

	// Admin alerts have no project; deleting a project removes its states.
	if ok, err := db.ClaimCooldown(ctx, "admin:disk_low", 0, "Disk space is low.", now, 12*time.Hour); !ok || err != nil {
		t.Fatalf("admin claim = %v, %v", ok, err)
	}
	if err := db.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	all, err := db.AlertStates(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].Name != "admin:disk_low" || all[0].Value != "Disk space is low." || !all[0].SentAt.Equal(now) {
		t.Errorf("states after deleting the project = %+v, want only the admin alert", all)
	}
}

func TestSettings_SetAndRemove(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if v, err := db.Setting(ctx, SettingAdminSlackWebhook); v != "" || err != nil {
		t.Fatalf("unset = %q, %v", v, err)
	}
	if err := db.SetSetting(ctx, SettingAdminSlackWebhook, "https://hooks.slack.com/services/x", userTestNow); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Setting(ctx, SettingAdminSlackWebhook); v != "https://hooks.slack.com/services/x" {
		t.Errorf("set = %q", v)
	}
	if err := db.SetSetting(ctx, SettingAdminSlackWebhook, "", userTestNow); err != nil {
		t.Fatal(err)
	}
	if v, _ := db.Setting(ctx, SettingAdminSlackWebhook); v != "" {
		t.Errorf("removed = %q", v)
	}
}

// An update that is still deciding holds the write transaction: a second
// update waits for it, then starts from its result.
func TestUpdateAlertSettings_OneAfterTheOther(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")

	inside := make(chan struct{})
	release := make(chan struct{})
	first := make(chan error, 1)
	go func() {
		_, err := db.UpdateAlertSettings(ctx, p.ID, func(s *AlertSettings) {
			close(inside)
			<-release
			s.NewIssue.Email = false
		})
		first <- err
	}()
	<-inside
	second := make(chan error, 1)
	go func() {
		_, err := db.UpdateAlertSettings(ctx, p.ID, func(s *AlertSettings) { s.SpikeThreshold = 20 })
		second <- err
	}()
	time.Sleep(50 * time.Millisecond) // the second one waits for the write connection
	close(release)
	if err := <-first; err != nil {
		t.Fatal(err)
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	got, _ := db.AlertSettings(ctx, p.ID)
	if got.NewIssue.Email || got.SpikeThreshold != 20 {
		t.Errorf("settings = %+v, want both changes", got)
	}
	if _, err := db.UpdateAlertSettings(ctx, 999, func(*AlertSettings) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}
}
