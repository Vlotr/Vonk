package auth

import (
	"net/http/httptest"
	"testing"
	"time"
)

func TestLimiter_SlidingWindow(t *testing.T) {
	l := newLimiter(3, 15*time.Minute)
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	for i := range 3 {
		if wait := l.wait("k", start); wait != 0 {
			t.Fatalf("before event %d: wait = %v, want 0", i+1, wait)
		}
		l.add("k", start.Add(time.Duration(i)*time.Minute))
	}
	// Three events at 12:00, 12:01 and 12:02: blocked until the first leaves the window at 12:15.
	if wait := l.wait("k", start.Add(5*time.Minute)); wait != 10*time.Minute {
		t.Errorf("wait = %v, want 10m", wait)
	}
	if wait := l.wait("other", start); wait != 0 {
		t.Errorf("another key: wait = %v, want 0", wait)
	}
	if wait := l.wait("k", start.Add(15*time.Minute)); wait != 0 {
		t.Errorf("after the window: wait = %v, want 0", wait)
	}
	l.add("k", start.Add(15*time.Minute))
	if wait := l.wait("k", start.Add(15*time.Minute)); wait != time.Minute {
		t.Errorf("12:01, 12:02 and 12:15 counted: wait = %v, want 1m", wait)
	}
	l.reset("k")
	if wait := l.wait("k", start.Add(15*time.Minute)); wait != 0 {
		t.Errorf("after reset: wait = %v, want 0", wait)
	}
}

func TestLimiter_ForgetsOldKeys(t *testing.T) {
	l := newLimiter(1, time.Minute)
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for i := range maxLimiterKeys {
		l.add(string(rune(i)), start)
	}
	l.add("late", start.Add(2*time.Minute)) // sweeps every key whose events left the window
	if n := len(l.events); n != 1 {
		t.Errorf("keys after the sweep = %d, want 1", n)
	}
}

func TestClientAddress(t *testing.T) {
	tests := []struct {
		name, remote, forwarded, want string
	}{
		{"direct", "203.0.113.5:4000", "", "203.0.113.5"},
		// Anyone can send X-Forwarded-For; from a public peer it is ignored.
		{"public peer with a forwarded header", "203.0.113.5:4000", "198.51.100.7", "203.0.113.5"},
		// A proxy on this machine or the private network: its last hop is the client.
		{"loopback proxy", "127.0.0.1:4000", "198.51.100.7", "198.51.100.7"},
		{"docker proxy", "172.18.0.3:4000", "10.0.0.1, 198.51.100.7", "198.51.100.7"},
		{"proxy with garbage", "10.0.0.2:4000", "not-an-ip", "10.0.0.2"},
		{"ipv6", "[2001:db8::1]:4000", "", "2001:db8::1"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/ui/api/login", nil)
		r.RemoteAddr = tt.remote
		if tt.forwarded != "" {
			r.Header.Set("X-Forwarded-For", tt.forwarded)
		}
		if got := clientAddress(r); got != tt.want {
			t.Errorf("%s: clientAddress = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestThrottle_KeysAreNotAddresses(t *testing.T) {
	th := newThrottle(5, 20, 15*time.Minute)
	r := httptest.NewRequest("POST", "/ui/api/login", nil)
	r.RemoteAddr = "203.0.113.5:4000"
	key := th.clientKey(r)
	if key == "" || key == "203.0.113.5" || len(key) < 32 {
		t.Errorf("client key = %q, want a hash, not the address", key)
	}
	if again := th.clientKey(r); again != key {
		t.Errorf("client key changes between calls: %q, %q", key, again)
	}
}
