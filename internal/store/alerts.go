package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Alert kinds: what a notification is about.
const (
	AlertNewIssue   = "new_issue"  // an error comes in for the first time
	AlertRegression = "regression" // a resolved issue comes back
	AlertSpike      = "spike"      // many more errors than usual
	AlertDigest     = "digest"     // a summary of the past day, week or month
)

// Alert channels.
const (
	ChannelEmail = "email"
	ChannelSlack = "slack"
)

// Digest frequencies.
const (
	DigestDaily   = "daily"
	DigestWeekly  = "weekly"
	DigestMonthly = "monthly"
)

// Defaults for a project whose alerts were never changed.
const (
	DefaultSpikeThreshold  = 5
	DefaultDigestFrequency = DigestWeekly
)

// AlertKinds lists every kind, in the order the Alerts page shows them.
var AlertKinds = []string{AlertNewIssue, AlertRegression, AlertSpike, AlertDigest}

// ValidDigestFrequency reports whether f is daily, weekly or monthly.
func ValidDigestFrequency(f string) bool {
	return f == DigestDaily || f == DigestWeekly || f == DigestMonthly
}

// Channels says where one kind of alert goes.
type Channels struct {
	Email bool
	Slack bool
}

// Any reports whether the kind goes anywhere.
func (c Channels) Any() bool {
	return c.Email || c.Slack
}

// AlertSettings are a project's notification choices.
type AlertSettings struct {
	NewIssue   Channels
	Regression Channels
	Spike      Channels
	// SpikeThreshold fires a spike alert when the last hour has this many
	// times the usual hourly number of errors.
	SpikeThreshold int
	Digest         Channels
	// DigestFrequency is DigestDaily, DigestWeekly or DigestMonthly.
	DigestFrequency string
	// SlackWebhookURL is the project's Slack incoming webhook, "" for none.
	// A secret: show it masked.
	SlackWebhookURL string
}

// DefaultAlertSettings are the choices of a project nobody changed: new,
// returning and spike alerts on both channels, the summary off.
func DefaultAlertSettings() AlertSettings {
	on := Channels{Email: true, Slack: true}
	return AlertSettings{
		NewIssue:        on,
		Regression:      on,
		Spike:           on,
		SpikeThreshold:  DefaultSpikeThreshold,
		DigestFrequency: DefaultDigestFrequency,
	}
}

// Channels returns where alerts of kind go.
func (s AlertSettings) Channels(kind string) Channels {
	switch kind {
	case AlertNewIssue:
		return s.NewIssue
	case AlertRegression:
		return s.Regression
	case AlertSpike:
		return s.Spike
	case AlertDigest:
		return s.Digest
	}
	return Channels{}
}

func (s *AlertSettings) channels(kind string) *Channels {
	switch kind {
	case AlertNewIssue:
		return &s.NewIssue
	case AlertRegression:
		return &s.Regression
	case AlertSpike:
		return &s.Spike
	case AlertDigest:
		return &s.Digest
	}
	return nil
}

// AlertSettings returns the project's notification choices, with the
// defaults for kinds never saved, or ErrNotFound.
func (db *DB) AlertSettings(ctx context.Context, projectID int64) (AlertSettings, error) {
	s := DefaultAlertSettings()
	err := db.read.QueryRowContext(ctx, "SELECT slack_webhook_url FROM projects WHERE id = ?", projectID).Scan(&s.SlackWebhookURL)
	if errors.Is(err, sql.ErrNoRows) {
		return AlertSettings{}, ErrNotFound
	}
	if err != nil {
		return AlertSettings{}, err
	}

	rows, err := db.read.QueryContext(ctx,
		"SELECT kind, channel, enabled, threshold, frequency FROM alerts WHERE project_id = ?", projectID)
	if err != nil {
		return AlertSettings{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			kind, channel, frequency string
			enabled                  bool
			threshold                sql.NullInt64
		)
		if err := rows.Scan(&kind, &channel, &enabled, &threshold, &frequency); err != nil {
			return AlertSettings{}, err
		}
		c := s.channels(kind)
		if c == nil {
			continue // a kind this version does not know
		}
		switch channel {
		case ChannelEmail:
			c.Email = enabled
		case ChannelSlack:
			c.Slack = enabled
		}
		if kind == AlertSpike && threshold.Valid && threshold.Int64 > 0 {
			s.SpikeThreshold = int(threshold.Int64)
		}
		if kind == AlertDigest && ValidDigestFrequency(frequency) {
			s.DigestFrequency = frequency
		}
	}
	return s, rows.Err()
}

// SaveAlertSettings stores all of a project's notification choices and its
// Slack webhook in one transaction, or returns ErrNotFound. The spike
// threshold and the digest frequency are stored on both channels' rows.
func (db *DB) SaveAlertSettings(ctx context.Context, projectID int64, s AlertSettings) error {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	res, err := tx.ExecContext(ctx, "UPDATE projects SET slack_webhook_url = ? WHERE id = ?", s.SlackWebhookURL, projectID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	for _, kind := range AlertKinds {
		c := s.Channels(kind)
		var threshold any
		frequency := ""
		switch kind {
		case AlertSpike:
			threshold = s.SpikeThreshold
		case AlertDigest:
			frequency = s.DigestFrequency
		}
		for _, ch := range []struct {
			name    string
			enabled bool
		}{{ChannelEmail, c.Email}, {ChannelSlack, c.Slack}} {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO alerts (project_id, kind, channel, enabled, threshold, frequency)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT (project_id, kind, channel)
				DO UPDATE SET enabled = excluded.enabled, threshold = excluded.threshold, frequency = excluded.frequency`,
				projectID, kind, ch.name, ch.enabled, threshold, frequency)
			if err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ProjectRecipients returns the addresses of everyone who may see the
// project: every admin, and the members given access to it.
func (db *DB) ProjectRecipients(ctx context.Context, projectID int64) ([]string, error) {
	return queryStrings(ctx, db.read, `
		SELECT email FROM users
		WHERE role = ? OR id IN (SELECT user_id FROM project_access WHERE project_id = ?)
		ORDER BY email`, RoleAdmin, projectID)
}

// AdminEmails returns the addresses of every admin.
func (db *DB) AdminEmails(ctx context.Context) ([]string, error) {
	return queryStrings(ctx, db.read, "SELECT email FROM users WHERE role = ? ORDER BY email", RoleAdmin)
}

func queryStrings(ctx context.Context, q querier, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	list := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		list = append(list, s)
	}
	return list, rows.Err()
}

// AlertState is what an alert last did.
type AlertState struct {
	Name   string
	Value  string
	SentAt time.Time
}

// ClaimCooldown records that the alert called name fires at now, with
// value, and returns true, unless it fired less than cooldown ago; then it
// changes nothing and returns false. projectID 0 means an alert of the
// tool itself. The check and the write are one transaction, so two
// callers never both fire.
func (db *DB) ClaimCooldown(ctx context.Context, name string, projectID int64, value string, now time.Time, cooldown time.Duration) (bool, error) {
	return db.claim(ctx, name, projectID, value, now, func(old AlertState) bool {
		return !now.Before(old.SentAt.Add(cooldown))
	})
}

// ClaimPeriod records that the alert called name covers period (a digest's
// "2026-10-15") and returns true, unless it already covered that period;
// then it returns false. It is how a digest is never sent twice.
func (db *DB) ClaimPeriod(ctx context.Context, name string, projectID int64, period string, now time.Time) (bool, error) {
	return db.claim(ctx, name, projectID, period, now, func(old AlertState) bool {
		return old.Value != period
	})
}

// claim writes the state of name when there is none yet or when due says
// the old one may be replaced.
func (db *DB) claim(ctx context.Context, name string, projectID int64, value string, now time.Time, due func(AlertState) bool) (bool, error) {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback() // no-op after Commit

	old := AlertState{Name: name}
	var sentAt int64
	err = tx.QueryRowContext(ctx, "SELECT value, sent_at FROM alert_state WHERE name = ?", name).Scan(&old.Value, &sentAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return false, err
	default:
		old.SentAt = time.UnixMilli(sentAt).UTC()
		if !due(old) {
			return false, nil
		}
	}

	var project any
	if projectID != 0 {
		project = projectID
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO alert_state (name, project_id, value, sent_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET project_id = excluded.project_id, value = excluded.value, sent_at = excluded.sent_at`,
		name, project, value, now.UnixMilli())
	if err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// AlertStates returns the states whose name starts with prefix, the most
// recently sent first.
func (db *DB) AlertStates(ctx context.Context, prefix string) ([]AlertState, error) {
	rows, err := db.read.QueryContext(ctx,
		`SELECT name, value, sent_at FROM alert_state WHERE substr(name, 1, ?) = ? ORDER BY sent_at DESC, name`,
		len(prefix), prefix)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []AlertState{}
	for rows.Next() {
		var st AlertState
		var sentAt int64
		if err := rows.Scan(&st.Name, &st.Value, &sentAt); err != nil {
			return nil, err
		}
		st.SentAt = time.UnixMilli(sentAt).UTC()
		states = append(states, st)
	}
	return states, rows.Err()
}

// Names of tool-wide settings.
const (
	// SettingAdminSlackWebhook is the Slack webhook for alerts about the
	// tool itself (a full disk), sent next to the mail to admins. A secret.
	SettingAdminSlackWebhook = "admin_slack_webhook_url"
)

// Setting returns a tool-wide setting, "" when it was never set.
func (db *DB) Setting(ctx context.Context, name string) (string, error) {
	var value string
	err := db.read.QueryRowContext(ctx, "SELECT value FROM settings WHERE name = ?", name).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return value, err
}

// SetSetting stores a tool-wide setting; "" removes it.
func (db *DB) SetSetting(ctx context.Context, name, value string, now time.Time) error {
	if strings.TrimSpace(value) == "" {
		_, err := db.write.ExecContext(ctx, "DELETE FROM settings WHERE name = ?", name)
		return err
	}
	_, err := db.write.ExecContext(ctx, `
		INSERT INTO settings (name, value, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		name, value, now.UnixMilli())
	return err
}
