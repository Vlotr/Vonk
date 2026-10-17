package alerts

import (
	"context"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

// usualWeek stores an even spread of errors over the 7 days before the
// last hour: perHour each hour.
func (e *testEnv) usualWeek(t *testing.T, projectID int64, perHour int) {
	t.Helper()
	lastHour := e.clock.Now().Add(-time.Hour)
	for h := 1; h <= 7*24; h++ {
		e.saveAt(t, projectID, "usual", lastHour.Add(-time.Duration(h)*time.Hour+time.Minute), perHour)
	}
}

func (e *testEnv) spikes() []string {
	var out []string
	for _, m := range e.slack.Messages() {
		if strings.Contains(m, "Error spike") {
			out = append(out, m)
		}
	}
	return out
}

func (e *testEnv) checkSpikes(t *testing.T) {
	t.Helper()
	e.n.CheckSpikes(context.Background(), e.clock.Now())
	e.waitIdle()
}

func TestSpike_FiresAboveThreshold(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	env.usualWeek(t, env.shop.ID, 2) // 2 an hour

	// 9 in the last hour: under 5 × 2.
	env.saveAt(t, env.shop.ID, "deadlock", env.clock.Now().Add(-30*time.Minute), 9)
	env.checkSpikes(t)
	if got := env.spikes(); len(got) != 0 {
		t.Fatalf("spike at 9 errors with 2 an hour usual and 5×: %q", got)
	}

	// 10 in the last hour: 5 × 2, and the minimum of 10.
	env.saveAt(t, env.shop.ID, "deadlock", env.clock.Now().Add(-10*time.Minute), 1)
	env.checkSpikes(t)
	got := env.spikes()
	if len(got) != 1 {
		t.Fatalf("spikes = %q, want one", got)
	}
	if !strings.Contains(got[0], "*Error spike in Webshop*: 10 errors in the last hour, 5 times the usual 2 an hour") ||
		!strings.Contains(got[0], "RuntimeException: deadlock failed") {
		t.Errorf("spike message = %q", got[0])
	}
	lines, bodies := env.mails()
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "admin@example.com: [Webshop] Error spike: 10 errors in the last hour") {
		t.Errorf("spike mails = %q", lines)
	}
	if len(bodies) > 0 && !strings.Contains(bodies[0], "The alert fires at 5 times the usual rate") {
		t.Errorf("spike mail body:\n%s", bodies[0])
	}

	// The threshold is the project's: at 10× the same hour is no spike.
	other := newEnv(t)
	other.withSlack(t, other.shop.ID)
	other.configure(t, other.shop.ID, func(s *store.AlertSettings) { s.SpikeThreshold = 10 })
	other.usualWeek(t, other.shop.ID, 2)
	other.saveAt(t, other.shop.ID, "deadlock", other.clock.Now().Add(-10*time.Minute), 19)
	other.checkSpikes(t)
	if got := other.spikes(); len(got) != 0 {
		t.Errorf("spike at 19 errors with threshold 10× of 2 an hour: %q", got)
	}
}

func TestSpike_MinimumCount(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	// A quiet project: nothing the week before, 9 errors in the last hour.
	env.saveAt(t, env.shop.ID, "deadlock", env.clock.Now().Add(-5*time.Minute), 9)
	env.checkSpikes(t)
	if got := env.spikes(); len(got) != 0 {
		t.Fatalf("spike below the minimum of 10: %q", got)
	}
	env.saveAt(t, env.shop.ID, "deadlock", env.clock.Now().Add(-4*time.Minute), 1)
	env.checkSpikes(t)
	if got := env.spikes(); len(got) != 1 {
		t.Errorf("spikes at the minimum = %d, want 1", len(got))
	}
}

func TestSpike_Cooldown(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	burst := func() {
		env.saveAt(t, env.shop.ID, "deadlock", env.clock.Now().Add(-time.Minute), 50)
	}
	burst()
	env.checkSpikes(t)
	if n := len(env.spikes()); n != 1 {
		t.Fatalf("first spike: %d messages", n)
	}

	// Still spiking 5 minutes and 5 hours later: no new alert inside 6 hours.
	for _, later := range []time.Duration{5 * time.Minute, 5 * time.Hour} {
		env.clock.Set(testStart.Add(later))
		burst()
		env.checkSpikes(t)
	}
	if n := len(env.spikes()); n != 1 {
		t.Fatalf("spikes inside the cooldown = %d, want 1", n)
	}

	// 6 hours after the first: it fires again.
	env.clock.Set(testStart.Add(6 * time.Hour))
	burst()
	env.checkSpikes(t)
	if n := len(env.spikes()); n != 2 {
		t.Errorf("spikes after the cooldown = %d, want 2", n)
	}

	// Off means off.
	env.configure(t, env.shop.ID, func(s *store.AlertSettings) { s.Spike = store.Channels{} })
	env.clock.Set(testStart.Add(13 * time.Hour))
	burst()
	env.checkSpikes(t)
	if n := len(env.spikes()); n != 2 {
		t.Errorf("spikes with spike alerts off = %d, want still 2", n)
	}
}

// ignore sets the issue with this fingerprint to ignored.
func (e *testEnv) ignore(t *testing.T, projectID int64, fingerprint string) {
	t.Helper()
	list, err := e.db.ListIssues(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range list {
		if issue.Fingerprint == fingerprint {
			if _, err := e.db.SetIssueStatus(context.Background(), issue.ID, store.IssueIgnored, e.clock.Now()); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
	t.Fatalf("no issue %q", fingerprint)
}

// Events of ignored issues count neither in the last hour nor in the 7
// days before: ignoring a noisy issue silences its spikes, and its noise
// does not hide the spike of another issue.
func TestSpike_IgnoredIssuesDoNotCount(t *testing.T) {
	env := newEnv(t)
	env.withSlack(t, env.shop.ID)
	env.usualWeek(t, env.shop.ID, 2)
	env.saveAt(t, env.shop.ID, "noisy", env.clock.Now().Add(-10*time.Minute), 50)
	env.ignore(t, env.shop.ID, "noisy")
	env.checkSpikes(t)
	if got := env.spikes(); len(got) != 0 {
		t.Fatalf("an ignored issue's 50 errors spiked: %q", got)
	}

	other := newEnv(t)
	other.withSlack(t, other.shop.ID)
	other.usualWeek(t, other.shop.ID, 2)
	lastHour := other.clock.Now().Add(-time.Hour)
	for h := 1; h <= 7*24; h++ { // 20 an hour all week, ignored
		other.saveAt(t, other.shop.ID, "noisy", lastHour.Add(-time.Duration(h)*time.Hour+2*time.Minute), 20)
	}
	other.ignore(t, other.shop.ID, "noisy")
	other.saveAt(t, other.shop.ID, "noisy", other.clock.Now().Add(-20*time.Minute), 20)
	other.saveAt(t, other.shop.ID, "deadlock", other.clock.Now().Add(-10*time.Minute), 10)
	other.checkSpikes(t)
	got := other.spikes()
	if len(got) != 1 {
		t.Fatalf("spikes = %q, want one: 10 errors against the usual 2 an hour of the issues not ignored", got)
	}
	if !strings.Contains(got[0], "10 errors in the last hour, 5 times the usual 2 an hour") || strings.Contains(got[0], "noisy") {
		t.Errorf("spike message = %q, want the 10 errors and the 2 an hour without the ignored issue", got[0])
	}
}
