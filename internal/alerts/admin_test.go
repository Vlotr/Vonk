package alerts

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"monitor/internal/health"
	"monitor/internal/ingest"
	"monitor/internal/store"
)

const gib = 1 << 30

// adminAlerts returns the admin checks with a disk that has free of total
// bytes, and ingest counters the test sets.
func (e *testEnv) adminAlerts(t *testing.T, free, total *uint64, stats *IngestStats) *AdminAlerts {
	t.Helper()
	checker := &health.Checker{DB: e.db, Queue: ingest.NewQueue(10), Version: "test", Log: e.n.log,
		DiskUsage: func(string) (uint64, uint64, error) { return *free, *total, nil }}
	return NewAdminAlerts(e.n, checker, func() IngestStats { return *stats })
}

func (e *testEnv) adminCheck(a *AdminAlerts) {
	a.Check(context.Background(), e.clock.Now())
	e.waitIdle()
}

func TestAdminAlert_DiskLow(t *testing.T) {
	env := newEnv(t)
	if err := env.db.SetSetting(context.Background(), store.SettingAdminSlackWebhook, env.slack.URL, testStart); err != nil {
		t.Fatal(err)
	}
	free, total := uint64(50*gib), uint64(100*gib)
	stats := IngestStats{}
	a := env.adminAlerts(t, &free, &total, &stats)

	env.adminCheck(a)
	if lines, _ := env.mails(); len(lines) != 0 || len(env.slack.Messages()) != 0 {
		t.Fatalf("alerts with half the disk free: %q %q", lines, env.slack.Messages())
	}

	// 3 percent free: mail to the admins only, and the admins' Slack.
	free = 3 * gib
	env.adminCheck(a)
	lines, bodies := env.mails()
	if want := []string{"admin@example.com: [monitor.example.com] Disk space is low: 3 GiB free (3 %)"}; !slices.Equal(lines, want) {
		t.Errorf("mails = %q, want %q", lines, want)
	}
	if len(bodies) == 1 && !strings.Contains(bodies[0], testBaseURL+"/settings/status") {
		t.Errorf("mail body:\n%s", bodies[0])
	}
	if msgs := env.slack.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0], "Disk space is low: 3 GiB free") {
		t.Errorf("Slack = %q", msgs)
	}

	// Still low: quiet for 12 hours, then once more.
	env.clock.Add(11 * time.Hour)
	env.adminCheck(a)
	if lines, _ := env.mails(); len(lines) != 1 {
		t.Errorf("mails inside the cooldown = %d, want 1", len(lines))
	}
	env.clock.Add(time.Hour)
	env.adminCheck(a)
	if lines, _ := env.mails(); len(lines) != 2 {
		t.Errorf("mails after 12 hours = %d, want 2", len(lines))
	}

	// Under 1 GiB is low on any disk size; the status page lists the alert.
	free, total = gib/2, 4*gib
	env.clock.Add(12 * time.Hour)
	env.adminCheck(a)
	states, err := env.db.AlertStates(context.Background(), "admin:")
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Value != "Disk space is low: 512 MiB free (12.5 %)." {
		t.Errorf("recent admin alerts = %+v", states)
	}
}

func TestAdminAlert_LostEventsAndQueueDrops(t *testing.T) {
	env := newEnv(t)
	free, total := uint64(50*gib), uint64(100*gib)
	stats := IngestStats{QueueRefused: 7} // before the checks started: not news
	a := env.adminAlerts(t, &free, &total, &stats)

	env.adminCheck(a)
	if lines, _ := env.mails(); len(lines) != 0 {
		t.Fatalf("alerts without news: %q", lines)
	}

	stats = IngestStats{QueueRefused: 47, FailedBatches: 2, LostEvents: 12, LastError: "disk I/O error"}
	env.adminCheck(a)
	lines, bodies := env.mails()
	slices.Sort(lines)
	want := []string{
		"admin@example.com: [monitor.example.com] 40 events were turned away because the ingest queue was full",
		"admin@example.com: [monitor.example.com] The database could not store 12 events; they are lost",
	}
	if !slices.Equal(lines, want) {
		t.Errorf("mails = %q, want %q", lines, want)
	}
	if !strings.Contains(strings.Join(bodies, "\n"), "The database said: disk I/O error") {
		t.Errorf("bodies:\n%s", strings.Join(bodies, "\n---\n"))
	}
	// Admin alerts are mailed to admins only.
	for _, l := range lines {
		if !strings.HasPrefix(l, "admin@") {
			t.Errorf("an admin alert went to a member: %s", l)
		}
	}
}
