package alerts

import (
	"context"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

func amsterdam(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func (e *testEnv) summaries() []string {
	var out []string
	for _, m := range e.slack.Messages() {
		if strings.Contains(m, "summary") {
			out = append(out, m)
		}
	}
	return out
}

// digestJob returns the notifier's summary job.
func (e *testEnv) digestJob(t *testing.T) func(time.Time) {
	t.Helper()
	for _, j := range e.n.Jobs() {
		if j.Name == "summaries" {
			return func(at time.Time) {
				e.clock.Set(at)
				j.Run(context.Background(), at)
				e.waitIdle()
			}
		}
	}
	t.Fatal("no summaries job")
	return nil
}

func TestDigest_SchedulesInTZ(t *testing.T) {
	loc := amsterdam(t)
	env := newEnv(t, inLocation(loc))
	env.withSlack(t, env.shop.ID)
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) {
		s.Digest = store.Channels{Email: true, Slack: true}
		s.DigestFrequency = store.DigestDaily
	})

	// The job runs at 08:00 on Amsterdam's clock: 06:00 UTC in summer time.
	var job func(time.Time)
	for _, j := range env.n.Jobs() {
		if j.Name == "summaries" {
			next := j.Schedule.Next(time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC))
			if want := time.Date(2026, 10, 16, 8, 0, 0, 0, loc); !next.Equal(want) {
				t.Errorf("next summary run = %s, want %s", next, want)
			}
		}
	}
	job = env.digestJob(t)

	// Thursday 15 October in Amsterdam runs from 22:00 UTC on the 14th to
	// 22:00 UTC on the 15th.
	env.saveAt(t, env.shop.ID, "early", time.Date(2026, 10, 14, 22, 30, 0, 0, time.UTC), 3)  // 00:30 local on the 15th
	env.saveAt(t, env.shop.ID, "late", time.Date(2026, 10, 15, 21, 30, 0, 0, time.UTC), 2)   // 23:30 local on the 15th
	env.saveAt(t, env.shop.ID, "before", time.Date(2026, 10, 14, 21, 30, 0, 0, time.UTC), 7) // the 14th, local
	env.saveAt(t, env.shop.ID, "after", time.Date(2026, 10, 15, 22, 30, 0, 0, time.UTC), 11) // the 16th, local

	job(time.Date(2026, 10, 16, 8, 0, 0, 0, loc))
	got := env.summaries()
	if len(got) != 1 {
		t.Fatalf("summaries = %q, want one", got)
	}
	if !strings.Contains(got[0], "*Daily summary of Webshop* for Thursday, October 15") || !strings.Contains(got[0], "5 errors") {
		t.Errorf("summary = %q, want Thursday's 5 errors", got[0])
	}
	if !strings.Contains(got[0], "early failed") || !strings.Contains(got[0], "late failed") || strings.Contains(got[0], "after failed") {
		t.Errorf("summary top issues = %q", got[0])
	}
	lines, bodies := env.mails()
	if len(lines) != 2 || lines[0] != "admin@example.com: [Webshop] Daily summary for Thursday, October 15" {
		t.Errorf("summary mails = %q", lines)
	}
	if len(bodies) > 0 && (!strings.Contains(bodies[0], "Errors: 5") || !strings.Contains(bodies[0], "New issues: 2")) {
		t.Errorf("summary mail:\n%s", bodies[0])
	}

	// Weekly summaries go out on Mondays only, for the week before.
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) { s.DigestFrequency = store.DigestWeekly })
	job(time.Date(2026, 10, 17, 8, 0, 0, 0, loc)) // Saturday
	if n := len(env.summaries()); n != 1 {
		t.Fatalf("a weekly summary went out on a Saturday")
	}
	job(time.Date(2026, 10, 19, 8, 0, 0, 0, loc)) // Monday
	got = env.summaries()
	if len(got) != 2 || !strings.Contains(got[1], "*Weekly summary of Webshop* for October 12 to October 18") {
		t.Fatalf("weekly summary = %q", got[len(got)-1])
	}

	// Monthly ones on the 1st, for the month before.
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) { s.DigestFrequency = store.DigestMonthly })
	job(time.Date(2026, 10, 26, 8, 0, 0, 0, loc)) // a Monday, not the 1st
	job(time.Date(2026, 11, 1, 8, 0, 0, 0, loc))
	got = env.summaries()
	if len(got) != 3 || !strings.Contains(got[2], "*Monthly summary of Webshop* for October 2026") ||
		!strings.Contains(got[2], "23 errors") {
		t.Errorf("monthly summary = %q", got[len(got)-1])
	}
}

func TestDigest_NoDoubleSend(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) {
		s.Digest = store.Channels{Slack: true}
		s.DigestFrequency = store.DigestDaily
	})
	env.saveAt(t, env.shop.ID, "yesterday", time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC), 4)

	at := time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC)
	job := env.digestJob(t)
	job(at)
	job(at)
	job(at.Add(3 * time.Hour)) // the same day once more, as after a restart

	// A restarted process (a new notifier on the same database) neither.
	restarted := New(Options{DB: env.db, BaseURL: testBaseURL, Logger: env.n.log, Now: env.clock.Now, AllowHTTPWebhooks: true})
	restarted.Start()
	restarted.SendDigests(context.Background(), at)
	restarted.Close(5 * time.Second)

	if got := env.summaries(); len(got) != 1 {
		t.Errorf("summaries for one day = %d, want 1: %q", len(got), got)
	}

	// The next day has its own.
	job(at.Add(24 * time.Hour))
	if got := env.summaries(); len(got) != 2 {
		t.Errorf("summaries after two days = %d, want 2", len(got))
	}

	// Summaries that are off are not sent.
	env.configure(t, env.portal.ID, func(s *store.AlertSettings) { s.SlackWebhookURL = env.slack.URL })
	job(at.Add(48 * time.Hour))
	if got := env.summaries(); len(got) != 3 || strings.Contains(strings.Join(got, ""), "Portal") {
		t.Errorf("summaries = %q: Portal has them off", got)
	}
}

func TestDigest_PageViewsWhenTracked(t *testing.T) {
	env := newEnv(t)
	ctx := context.Background()
	env.withSlack(t, env.shop.ID)
	env.withSlack(t, env.portal.ID)
	for _, p := range []store.Project{env.shop, env.portal} {
		env.configure(t, p.ID, func(s *store.AlertSettings) {
			s.Digest = store.Channels{Email: true, Slack: true}
			s.DigestFrequency = store.DigestDaily
		})
	}
	// Webshop tracks page views; Portal does not show them, whatever comes in.
	if _, err := env.db.UpdateProject(ctx, env.shop.ID, store.ProjectSettings{Name: "Webshop", TracksLaravel: true, TracksPageviews: true}); err != nil {
		t.Fatal(err)
	}

	// Thursday the 15th: three page views by two visitors, two of them
	// left after 60 and 90 seconds (75 s on average), one by a bot; and
	// one page view the day before, which is not in the summary.
	day := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	view := func(p store.Project, id, user string, at time.Time, bot bool) store.PageEvent {
		return store.PageEvent{ProjectID: p.ID, PageviewID: id, SessionID: id, WindowID: "w", UserID: user, Route: "/", Path: "/", At: at, IsBot: bot}
	}
	leave := func(p store.Project, id string, at time.Time) store.PageEvent {
		return store.PageEvent{ProjectID: p.ID, Leave: true, EventID: "l" + id, PageviewID: id, SessionID: id, WindowID: "w", At: at}
	}
	events := []store.PageEvent{
		view(env.shop, "a", "u1", day, false), leave(env.shop, "a", day.Add(60*time.Second)),
		view(env.shop, "b", "u2", day.Add(time.Hour), false), leave(env.shop, "b", day.Add(time.Hour+90*time.Second)),
		view(env.shop, "c", "u1", day.Add(2*time.Hour), false),
		view(env.shop, "bot", "crawler", day.Add(2*time.Hour), true),
		view(env.shop, "old", "u3", day.Add(-24*time.Hour), false),
		view(env.portal, "p", "u9", day, false),
	}
	if _, err := env.db.SavePageEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	dirty, err := env.db.DirtyHours(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ph := range dirty {
		if err := env.db.RecomputeHour(ctx, ph.ProjectID, ph.Hour); err != nil {
			t.Fatal(err)
		}
	}

	env.digestJob(t)(time.Date(2026, 10, 16, 8, 0, 0, 0, time.UTC))
	var shop, portal string
	for _, m := range env.summaries() {
		if strings.Contains(m, "Webshop") {
			shop = m
		} else {
			portal = m
		}
	}
	if !strings.Contains(shop, "3 page views by 2 visitors, 1m 15s on a page on average") {
		t.Errorf("Webshop summary = %q, want its page views", shop)
	}
	if strings.Contains(portal, "page view") {
		t.Errorf("Portal summary = %q, but Portal does not track page views", portal)
	}
	_, bodies := env.mails()
	found := false
	for _, b := range bodies {
		b = strings.ReplaceAll(b, "\r\n", "\n")
		if strings.Contains(b, "Webshop") && strings.Contains(b, "Page views: 3\nVisitors: 2\nAverage time on page: 1m 15s\n") {
			found = true
		}
	}
	if !found {
		t.Errorf("no Webshop mail with the page views:\n%s", strings.Join(bodies, "\n---\n"))
	}
}

func TestFormatDuration(t *testing.T) {
	for ms, want := range map[int64]string{0: "0s", 45_000: "45s", 72_000: "1m 12s", 125_400: "2m 05s", 3_725_000: "1h 02m"} {
		if got := formatDuration(ms); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", ms, got, want)
		}
	}
}
