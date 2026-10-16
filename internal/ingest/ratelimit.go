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
	if limit <= 0 {
		return true, 0
	}
	now := l.now()
	start := now.Truncate(time.Minute)

	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.windows[projectID]
	if w == nil || !w.start.Equal(start) {
		w = &window{start: start}
		l.windows[projectID] = w
	}
	if w.count+n > limit {
		return false, start.Add(time.Minute).Sub(now)
	}
	w.count += n
	return true, 0
}
