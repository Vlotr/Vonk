package ingest

import (
	"testing"
	"time"
)

func TestHourCounter_CountsTheLastHour(t *testing.T) {
	var c hourCounter
	start := time.Date(2026, 10, 16, 12, 0, 30, 0, time.UTC)
	c.add(start, 5)
	c.add(start.Add(30*time.Minute), 3)
	c.add(start.Add(30*time.Minute+10*time.Second), 2)
	if got := c.lastHour(start.Add(59 * time.Minute)); got != 10 {
		t.Errorf("after 59 minutes = %d, want 10", got)
	}
	if got := c.lastHour(start.Add(60 * time.Minute)); got != 5 {
		t.Errorf("after an hour = %d, want 5 (the first minute has gone)", got)
	}
	// A bucket that comes round again starts over.
	c.add(start.Add(2*time.Hour), 1)
	if got := c.lastHour(start.Add(2 * time.Hour)); got != 1 {
		t.Errorf("two hours later = %d, want 1", got)
	}
}
