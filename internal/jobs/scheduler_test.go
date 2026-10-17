package jobs

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// clock is a time the test moves by hand.
type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func TestScheduler_FirstRunThenEvery(t *testing.T) {
	c := &clock{time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	s := New(c.now, quietLogger())
	var runs []time.Time
	s.Add(Job{Name: "retention", Schedule: Every(24 * time.Hour), FirstRun: time.Minute,
		Run: func(_ context.Context, at time.Time) { runs = append(runs, at) }})

	ctx := context.Background()
	s.RunDue(ctx)
	if len(runs) != 0 {
		t.Fatalf("ran at once: %v", runs)
	}
	c.t = c.t.Add(time.Minute)
	s.RunDue(ctx)
	s.RunDue(ctx) // not due again
	if len(runs) != 1 || !runs[0].Equal(c.t) {
		t.Fatalf("runs after a minute = %v, want one at %v", runs, c.t)
	}
	// Three days asleep: one run, then a day later the next.
	c.t = c.t.Add(72 * time.Hour)
	s.RunDue(ctx)
	if len(runs) != 2 {
		t.Fatalf("runs after sleeping = %d, want 2", len(runs))
	}
	c.t = c.t.Add(24*time.Hour - time.Second)
	s.RunDue(ctx)
	if len(runs) != 2 {
		t.Fatalf("ran a second early")
	}
	c.t = c.t.Add(time.Second)
	s.RunDue(ctx)
	if len(runs) != 3 {
		t.Fatalf("runs = %d, want 3", len(runs))
	}
}

func TestScheduler_PanicDoesNotStopOtherJobs(t *testing.T) {
	c := &clock{time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
	s := New(c.now, quietLogger())
	ran := false
	s.Add(Job{Name: "broken", Schedule: Every(time.Minute), Run: func(context.Context, time.Time) { panic("boom") }})
	s.Add(Job{Name: "fine", Schedule: Every(time.Minute), Run: func(context.Context, time.Time) { ran = true }})
	c.t = c.t.Add(time.Minute)
	s.RunDue(context.Background())
	if !ran {
		t.Error("a panicking job kept the next job from running")
	}
}

func TestDailyAt_KeepsTheLocalTimeAcrossDaylightSaving(t *testing.T) {
	amsterdam, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Fatal(err)
	}
	at8 := DailyAt(8, 0, amsterdam)
	cases := []struct{ after, want string }{
		// Summer time (UTC+2): 08:00 is 06:00 UTC.
		{"2026-10-16T05:59:00Z", "2026-10-16T06:00:00Z"},
		{"2026-10-16T06:00:00Z", "2026-10-17T06:00:00Z"},
		// Daylight saving ends on Sunday 25 October: 08:00 is 07:00 UTC from then on.
		{"2026-10-24T06:30:00Z", "2026-10-25T07:00:00Z"},
		{"2026-10-25T07:00:00Z", "2026-10-26T07:00:00Z"},
		// And starts again on 28 March 2027.
		{"2027-03-27T07:30:00Z", "2027-03-28T06:00:00Z"},
	}
	for _, tc := range cases {
		after, _ := time.Parse(time.RFC3339, tc.after)
		want, _ := time.Parse(time.RFC3339, tc.want)
		if got := at8.Next(after); !got.Equal(want) {
			t.Errorf("Next(%s) = %s, want %s", tc.after, got.UTC().Format(time.RFC3339), tc.want)
		}
	}
}
