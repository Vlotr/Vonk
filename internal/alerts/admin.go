package alerts

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"monitor/internal/health"
	"monitor/internal/jobs"
	"monitor/internal/store"
)

// Admins hear about problems of the tool itself: by mail to every admin,
// and on the admins' Slack webhook when one is set (Settings → Status).
// Each kind of problem alerts at most once in 12 hours.

// Kinds of admin alerts.
const (
	AdminDiskLow       = "disk_low"
	AdminDBWriteFailed = "db_write_failed"
	AdminQueueDrops    = "queue_drops"
)

// adminCooldown is how long one kind of admin alert stays quiet after it fired.
const adminCooldown = 12 * time.Hour

// IngestStats count what ingest could not take or store, since the start.
type IngestStats struct {
	// QueueRefused are events turned away because the queue was full.
	QueueRefused int64
	// FailedBatches and LostEvents are what the database refused to store.
	FailedBatches int64
	LostEvents    int64
	// LastError is the database's answer to the last failed batch.
	LastError string
}

// AdminAlerts checks the tool every minute and tells the admins what is wrong.
type AdminAlerts struct {
	n      *Notifier
	health *health.Checker
	ingest func() IngestStats

	mu   sync.Mutex
	last IngestStats
	// fired is the cooldown in memory, for when the database itself fails
	// and alert_state cannot be written.
	fired map[string]time.Time
}

// NewAdminAlerts returns the admin checks. stats reads the ingest counters
// (nil: none).
func NewAdminAlerts(n *Notifier, checker *health.Checker, stats func() IngestStats) *AdminAlerts {
	if stats == nil {
		stats = func() IngestStats { return IngestStats{} }
	}
	a := &AdminAlerts{n: n, health: checker, ingest: stats, fired: map[string]time.Time{}}
	a.last = stats()
	return a
}

// Job returns the check, every minute.
func (a *AdminAlerts) Job() jobs.Job {
	return jobs.Job{Name: "admin checks", Schedule: jobs.Every(time.Minute), Run: a.Check}
}

// Check looks at the disk and the ingest counters and alerts the admins
// about what is wrong.
func (a *AdminAlerts) Check(ctx context.Context, _ time.Time) {
	report := a.health.Check(ctx)
	if report.Disk == "low" || report.Disk == "critical" {
		a.raise(ctx, AdminDiskLow, diskMessage(report))
	}

	stats := a.ingest()
	a.mu.Lock()
	last := a.last
	a.last = stats
	a.mu.Unlock()
	if lost := stats.LostEvents - last.LostEvents; lost > 0 {
		a.raise(ctx, AdminDBWriteFailed, adminText{
			summary: fmt.Sprintf("The database could not store %d events; they are lost.", lost),
			detail: fmt.Sprintf("Writing %d events failed %d times in a row since the last check, so they were dropped. The database said: %s\n\nCheck the disk and the log of the container.",
				lost, stats.FailedBatches-last.FailedBatches, stats.LastError),
		})
	}
	if refused := stats.QueueRefused - last.QueueRefused; refused > 0 {
		a.raise(ctx, AdminQueueDrops, adminText{
			summary: fmt.Sprintf("%d events were turned away because the ingest queue was full.", refused),
			detail:  fmt.Sprintf("In the last minute the tool answered %d events with 429 (try again later): the database did not keep up. SDKs send most of them again; some may be lost.\n\nA slow or full disk is the usual cause.", refused),
		})
	}
}

// adminText is an admin alert: one line for the subject and the status
// page, and the rest of the message.
type adminText struct {
	summary string
	detail  string
}

func diskMessage(r health.Report) adminText {
	percent := 0.0
	if r.DiskTotalBytes > 0 {
		percent = float64(r.DiskFreeBytes) / float64(r.DiskTotalBytes) * 100
	}
	return adminText{
		summary: fmt.Sprintf("Disk space is low: %s free (%s %%).", Bytes(r.DiskFreeBytes), decimal(percent)),
		detail: "When the disk is full, errors can no longer be stored; under 1 % or 256 MiB free, /health answers 503. " +
			"Free up space or give the volume more room. Each project keeps its data as long as Settings → Projects says; a shorter time keeps less.",
	}
}

// raise sends an admin alert unless the same kind fired less than 12 hours ago.
func (a *AdminAlerts) raise(ctx context.Context, kind string, t adminText) {
	n := a.n
	now := n.now()
	fired, err := n.db.ClaimCooldown(ctx, "admin:"+kind, 0, t.summary, now, adminCooldown)
	if err != nil {
		// The database fails too: keep the cooldown in memory.
		a.mu.Lock()
		last, seen := a.fired[kind]
		fired = !seen || !now.Before(last.Add(adminCooldown))
		if fired {
			a.fired[kind] = now
		}
		a.mu.Unlock()
	}
	if !fired {
		return
	}
	n.log.Warn("admin alert", "kind", kind, "summary", t.summary)

	msg := message{
		subject: fmt.Sprintf("[%s] %s", n.host(), strings.TrimSuffix(t.summary, ".")),
		text:    t.summary + "\n\n" + t.detail + "\n" + n.adminFooter(),
		slack:   fmt.Sprintf("*%s*: %s\n%s %s", slackEscape(n.host()), slackEscape(t.summary), slackEscape(t.detail), slackLink(n.link("/settings/status"), "Status")),
	}
	if n.mailer != nil {
		if admins, err := n.db.AdminEmails(ctx); err != nil {
			n.log.Error("alerts: read admins", "err", err)
		} else {
			n.mailAll(0, "admin:"+kind, admins, msg)
		}
	}
	if webhook, err := n.db.Setting(ctx, store.SettingAdminSlackWebhook); err == nil && webhook != "" {
		n.post(0, "admin:"+kind, webhook, msg.slack)
	}
}

// Bytes writes a size the way people read it: "512 MiB", "3.2 GiB".
func Bytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	value, exp := float64(b)/unit, 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return decimal(value) + " " + []string{"KiB", "MiB", "GiB", "TiB", "PiB"}[exp]
}
