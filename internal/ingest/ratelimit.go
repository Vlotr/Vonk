package ingest

import (
	"sync"
	"time"
)

// RateLimiter counts events per project per minute. The window is the clock
// minute (12:00:00 to 12:00:59), so the count starts again on every whole minute.
type RateLimiter struct {
	now func() time.Time

	mu      sync.Mutex
	windows map[int64]*window
}

// window is one project's count in the current minute.
type window struct {
	start time.Time
	count int
}

// NewRateLimiter returns a limiter with no events counted.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{now: time.Now, windows: map[int64]*window{}}
}

// Allow reports whether n more events of the project fit in limit for this
// minute, and counts them when they do. When they do not, retryAfter is the
// time until the next minute starts. A limit of 0 or less means no limit.
func (l *RateLimiter) Allow(projectID int64, limit, n int) (ok bool, retryAfter time.Duration) {
	_, refused, retryAfter := l.AllowAll([]Ask{{ProjectID: projectID, Limit: limit, Events: n}})
	return refused == nil, retryAfter
}

// Ask is one project's events in a request: Events more against Limit.
type Ask struct {
	ProjectID int64
	Limit     int
	Events    int
}

// AllowAll reports whether the events of every ask fit in their project's
// limit for this minute, and counts them only when all do, so a refused
// request spends no project's budget. refused is nil when they fit, else
// the first ask that does not (its Limit is what the answer names).
// minute is the minute they were counted in, for Return. A project should
// be asked once per call.
func (l *RateLimiter) AllowAll(asks []Ask) (minute time.Time, refused *Ask, retryAfter time.Duration) {
	now := l.now()
	minute = now.Truncate(time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()
	for i, a := range asks {
		if a.Limit > 0 && l.window(a.ProjectID, minute).count+a.Events > a.Limit {
			return minute, &asks[i], minute.Add(time.Minute).Sub(now)
		}
	}
	for _, a := range asks {
		if a.Limit > 0 {
			l.window(a.ProjectID, minute).count += a.Events
		}
	}
	return minute, nil, 0
}

// Return takes back the events AllowAll counted in minute, for a request
// that was refused after all (the queue filled up in between). Events of a
// minute that is over are not given to the next one.
func (l *RateLimiter) Return(minute time.Time, asks []Ask) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, a := range asks {
		if w := l.windows[a.ProjectID]; w != nil && w.start.Equal(minute) {
			w.count = max(0, w.count-a.Events)
		}
	}
}

// window returns the project's count in minute, starting a new one when
// the minute changed. l.mu must be held.
func (l *RateLimiter) window(projectID int64, minute time.Time) *window {
	w := l.windows[projectID]
	if w == nil || !w.start.Equal(minute) {
		w = &window{start: minute}
		l.windows[projectID] = w
	}
	return w
}
