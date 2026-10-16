package auth

import (
	"fmt"
	"net/http/httptest"
	"strings"
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
	// Removing one event (a sign-in that worked gives its try back) frees a try.
	l.remove("k", start.Add(15*time.Minute))
	if wait := l.wait("k", start.Add(15*time.Minute)); wait != 0 {
		t.Errorf("after removing one: wait = %v, want 0", wait)
	}
	l.reset("k")
	if n := len(l.events["k"]); n != 0 {
		t.Errorf("after reset: %d events, want 0", n)
	}
}

func TestLimiter_CapsKeys(t *testing.T) {
	start := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	t.Run("never holds more than its cap", func(t *testing.T) {
		l := newLimiter(1, time.Minute)
		l.maxKeys = 100
		for i := range 1000 {
			l.add(fmt.Sprint("key", i), start)
			if len(l.events) > l.maxKeys {
				t.Fatalf("after %d keys the limiter holds %d, cap %d", i+1, len(l.events), l.maxKeys)
			}
		}
		if _, ok := l.events["key999"]; !ok {
			t.Error("the newest key was not kept")
		}
	})

	t.Run("keys whose events left the window go first", func(t *testing.T) {
		l := newLimiter(1, time.Minute)
		l.maxKeys = 100
		for i := range 99 {
			l.add(fmt.Sprint("old", i), start)
		}
		l.add("recent", start.Add(2*time.Minute))
		for i := range 50 {
			l.add(fmt.Sprint("new", i), start.Add(2*time.Minute))
		}
		if _, ok := l.events["recent"]; !ok {
			t.Error("a key still inside its window was dropped while expired ones were left")
		}
	})
}

func TestClientAddress(t *testing.T) {
	tests := []struct {
		name, remote, forwarded, want string
	}{
		{"direct", "203.0.113.5:4000", "", "203.0.113.5"},
		// Anyone can send X-Forwarded-For; from a public peer it is ignored.
		{"public peer with a forwarded header", "203.0.113.5:4000", "198.51.100.7", "203.0.113.5"},
		// A proxy on this machine or the private network: the right-most
		// hop that is not a proxy itself is the client.
		{"loopback proxy", "127.0.0.1:4000", "198.51.100.7", "198.51.100.7"},
		{"docker proxy", "172.18.0.3:4000", "10.0.0.1, 198.51.100.7", "198.51.100.7"},
		{"spoofed left-most entry", "127.0.0.1:4000", "6.6.6.6, 198.51.100.7", "198.51.100.7"},
		{"spoofed private left-most entry", "127.0.0.1:4000", "10.9.9.9, 198.51.100.7", "198.51.100.7"},
		{"two proxies", "127.0.0.1:4000", "198.51.100.7, 10.0.0.5", "198.51.100.7"},
		{"two header lines", "127.0.0.1:4000", "6.6.6.6\n198.51.100.7", "198.51.100.7"},
		{"a client on the private network", "172.18.0.3:4000", "192.168.1.20", "192.168.1.20"},
		{"proxy with garbage", "10.0.0.2:4000", "not-an-ip", "10.0.0.2"},
		{"garbage after the client", "10.0.0.2:4000", "198.51.100.7, not-an-ip", "10.0.0.2"},
		// IPv6 clients usually own a whole /64: one bucket for all of it.
		{"ipv6", "[2001:db8:1:2:aaaa::1]:4000", "", "2001:db8:1:2::/64"},
		{"ipv6 through a proxy", "127.0.0.1:4000", "2001:db8:1:2:bbbb::7", "2001:db8:1:2::/64"},
		{"ipv4 mapped", "[::ffff:203.0.113.5]:4000", "", "203.0.113.5"},
	}
	for _, tt := range tests {
		r := httptest.NewRequest("POST", "/ui/api/login", nil)
		r.RemoteAddr = tt.remote
		for _, line := range strings.Split(tt.forwarded, "\n") {
			if line != "" {
				r.Header.Add("X-Forwarded-For", line)
			}
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
	if key == "" || strings.Contains(key, "203.0.113") || len(key) < 32 {
		t.Errorf("client key = %q, want a hash, not the address", key)
	}
	if again := th.clientKey(r); again != key {
		t.Errorf("client key changes between calls: %q, %q", key, again)
	}

	// Rotating a spoofed left-most entry or the low half of an IPv6
	// address does not give a new key.
	viaProxy := func(forwarded string) string {
		r := httptest.NewRequest("POST", "/ui/api/login", nil)
		r.RemoteAddr = "127.0.0.1:4000"
		r.Header.Set("X-Forwarded-For", forwarded)
		return th.clientKey(r)
	}
	if viaProxy("1.1.1.1, 198.51.100.7") != viaProxy("2.2.2.2, 198.51.100.7") {
		t.Error("a spoofed left-most X-Forwarded-For entry changes the client key")
	}
	if viaProxy("2001:db8:1:2::1") != viaProxy("2001:db8:1:2:ffff:ffff:ffff:ffff") {
		t.Error("addresses in one IPv6 /64 get different client keys")
	}
	if viaProxy("2001:db8:1:2::1") == viaProxy("2001:db8:1:3::1") {
		t.Error("two IPv6 /64 networks share a client key")
	}

	// Addresses are kept as hashes too, whatever their length.
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	if a, _, ok := th.begin("jan@example.com", true, key, now); !ok || a.locked {
		t.Fatal("first try refused")
	}
	for k := range th.byAccount.events {
		if strings.Contains(k, "jan") || len(k) != 64 {
			t.Errorf("address key = %q, want a hash", k)
		}
	}
}

func TestThrottle_BeginReservesAndSucceededGivesBack(t *testing.T) {
	th := newThrottle(2, 3, 15*time.Minute)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

	// Two tries for the account are reserved; the third is let through
	// locked (it is answered like a wrong password) and adds no try.
	a, _, ok1 := th.begin("jan@example.com", true, "c1", now)
	b, _, ok2 := th.begin("jan@example.com", true, "c2", now)
	c, _, ok3 := th.begin("jan@example.com", true, "c3", now.Add(time.Minute))
	if !ok1 || !ok2 || !ok3 || a.locked || b.locked || !c.locked || c.lockedFor != 14*time.Minute {
		t.Fatalf("begin = %v/%v %v/%v %v/%v (locked for %v), want two tries and then a lock of 14m",
			ok1, a.locked, ok2, b.locked, ok3, c.locked, c.lockedFor)
	}
	if n := len(th.byAccount.events[a.emailKey]); n != 2 {
		t.Errorf("the account has %d tries counted, want 2 (a locked try adds none)", n)
	}
	// A locked try that is followed by succeeded (it never is) would still not unlock.
	th.succeeded(c)
	if again, _, _ := th.begin("jan@example.com", true, "c4", now.Add(time.Minute)); !again.locked {
		t.Error("succeeded on a locked try ended the lock")
	}
	// A try that worked clears the account and gives the client its try back.
	th.succeeded(a)
	if d, _, ok := th.begin("jan@example.com", true, "c1", now); !ok || d.locked {
		t.Error("after a sign-in that worked the account is still locked")
	}
	// Addresses without an account are counted per client only.
	for range 2 {
		th.begin("other@example.com", false, "c1", now)
	}
	// c1 has used its three tries now: jan once more and other twice.
	if _, wait, ok := th.begin("third@example.com", false, "c1", now); ok || wait != 15*time.Minute {
		t.Errorf("the client's fourth try: allowed %v, wait %v; want refused for 15m", ok, wait)
	}
	if n := len(th.byAccount.events); n != 1 {
		t.Errorf("%d account keys, want 1: other addresses keep none", n)
	}
}

func TestThrottle_AccountKeysAreSwept(t *testing.T) {
	th := newThrottle(5, 1000, 15*time.Minute)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for i := range 100 {
		th.begin(fmt.Sprintf("gone%d@example.com", i), true, "c", now) // accounts deleted since
	}
	th.begin("jan@example.com", true, "c", now.Add(16*time.Minute))
	if n := len(th.byAccount.events); n != 1 {
		t.Errorf("%d account keys after their window, want only jan's", n)
	}
}
