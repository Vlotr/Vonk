package ingest

import (
	"sync"
	"time"
)

// hourCounter counts events per minute over the last hour, in memory: the
// status page's "events in the last hour". After a restart it counts from
// the start.
type hourCounter struct {
	mu      sync.Mutex
	minutes [60]struct {
		minute int64 // unix minute the bucket holds
		count  int
	}
}

func (c *hourCounter) add(now time.Time, n int) {
	if n <= 0 {
		return
	}
	minute := now.Unix() / 60
	c.mu.Lock()
	defer c.mu.Unlock()
	b := &c.minutes[minute%60]
	if b.minute != minute {
		b.minute, b.count = minute, 0
	}
	b.count += n
}

// lastHour sums the minutes of the hour before now.
func (c *hourCounter) lastHour(now time.Time) int {
	minute := now.Unix() / 60
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for _, b := range c.minutes {
		if b.minute > minute-60 && b.minute <= minute {
			total += b.count
		}
	}
	return total
}
