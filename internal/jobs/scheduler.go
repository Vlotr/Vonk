// Package jobs runs background work on a schedule: retention every day,
// alert checks every few minutes, the summary mail at 08:00. One Scheduler
// goroutine runs the jobs one after another, so they never overlap.
package jobs

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Schedule says when a job runs next.
type Schedule interface {
	// Next returns the first run time strictly after after.
	Next(after time.Time) time.Time
}

// Every runs a job at a fixed interval.
func Every(interval time.Duration) Schedule {
	return every{interval}
}

type every struct{ interval time.Duration }

func (e every) Next(after time.Time) time.Time {
	return after.Add(e.interval)
}

// DailyAt runs a job every day at hour:minute on the clock of loc (TZ), so
// "08:00" stays 08:00 when daylight saving time starts or ends.
func DailyAt(hour, minute int, loc *time.Location) Schedule {
	if loc == nil {
		loc = time.UTC
	}
	return daily{hour, minute, loc}
}

type daily struct {
	hour, minute int
	loc          *time.Location
}

func (d daily) Next(after time.Time) time.Time {
	local := after.In(d.loc)
	next := time.Date(local.Year(), local.Month(), local.Day(), d.hour, d.minute, 0, 0, d.loc)
	for !next.After(after) {
		local = local.AddDate(0, 0, 1)
		next = time.Date(local.Year(), local.Month(), local.Day(), d.hour, d.minute, 0, 0, d.loc)
	}
	return next
}

// Job is one piece of background work.
type Job struct {
	Name     string
	Schedule Schedule
	// FirstRun is how long after Add the job runs the first time; 0 means
	// at the schedule's first time.
	FirstRun time.Duration
	// Run does the work. at is the time the run was scheduled for, which
	// can be a little before now.
	Run func(ctx context.Context, at time.Time)
}

type entry struct {
	job  Job
	next time.Time
}

// Scheduler runs jobs when they are due. Its clock is injectable: tests
// call RunDue with a clock of their own instead of waiting.
type Scheduler struct {
	now func() time.Time
	log *slog.Logger

	mu   sync.Mutex
	jobs []*entry
}

// New returns a scheduler that reads the time from now (nil: time.Now).
func New(now func() time.Time, logger *slog.Logger) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{now: now, log: logger}
}

// Add registers a job; its first run is FirstRun from now, or the
// schedule's first time after now.
func (s *Scheduler) Add(job Job) {
	now := s.now()
	next := job.Schedule.Next(now)
	if job.FirstRun > 0 {
		next = now.Add(job.FirstRun)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs = append(s.jobs, &entry{job: job, next: next})
}

// RunDue runs every job whose time has come, one after another, and
// schedules its next run after now. A job that was due several times (the
// machine slept) runs once.
func (s *Scheduler) RunDue(ctx context.Context) {
	s.mu.Lock()
	jobs := append([]*entry(nil), s.jobs...)
	s.mu.Unlock()

	for _, e := range jobs {
		if ctx.Err() != nil {
			return
		}
		s.mu.Lock()
		at := e.next
		s.mu.Unlock()
		if at.After(s.now()) {
			continue
		}
		s.run(ctx, e.job, at)
		next := e.job.Schedule.Next(s.now())
		s.mu.Lock()
		e.next = next
		s.mu.Unlock()
	}
}

// run runs one job; a panic is logged and the scheduler carries on.
func (s *Scheduler) run(ctx context.Context, job Job, at time.Time) {
	defer func() {
		if err := recover(); err != nil {
			s.log.Error("job panicked", "job", job.Name, "panic", err)
		}
	}()
	start := time.Now()
	job.Run(ctx, at)
	s.log.Debug("job done", "job", job.Name, "took", time.Since(start).Round(time.Millisecond))
}

// nextDue returns the earliest next run of all jobs.
func (s *Scheduler) nextDue() (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var next time.Time
	for _, e := range s.jobs {
		if next.IsZero() || e.next.Before(next) {
			next = e.next
		}
	}
	return next, !next.IsZero()
}

// maxSleep is the longest the scheduler sleeps before looking at the clock
// again, so a clock that jumps (or a machine that slept) is noticed.
const maxSleep = time.Minute

// Run runs jobs as they come due until ctx ends. The job that is running
// when ctx ends gets the ended ctx and should stop soon.
func (s *Scheduler) Run(ctx context.Context) {
	for {
		s.RunDue(ctx)
		wait := maxSleep
		if next, ok := s.nextDue(); ok {
			wait = min(max(next.Sub(s.now()), 0), maxSleep)
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
