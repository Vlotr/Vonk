package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

// Login throttling: at most 5 failed sign-ins per address and 20 per
// client in 15 minutes; after that the answer is 429 until the oldest
// failure is 15 minutes old. Counts live in memory and start over when the
// server restarts. Client addresses are not kept: the limiter's keys are
// salted hashes of them.
const (
	failuresPerEmail  = 5
	failuresPerClient = 20
	throttleWindow    = 15 * time.Minute
)

// maxLimiterKeys is how many keys a limiter holds before it sweeps out the
// ones whose events all left the window.
const maxLimiterKeys = 10_000

// limiter counts events per key over a sliding window.
type limiter struct {
	limit  int
	window time.Duration

	mu     sync.Mutex
	events map[string][]time.Time // oldest first
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, events: map[string][]time.Time{}}
}

// wait returns how long key must wait until another event is allowed: 0
// when it is allowed now.
func (l *limiter) wait(key string, now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	events := l.recent(key, now)
	if len(events) < l.limit {
		return 0
	}
	// The oldest event that keeps the count at the limit must leave the window.
	return events[len(events)-l.limit].Add(l.window).Sub(now)
}

// add counts an event for key.
func (l *limiter) add(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.events) >= maxLimiterKeys {
		for k := range l.events {
			l.recent(k, now)
		}
	}
	events := append(l.recent(key, now), now)
	if len(events) > l.limit {
		events = events[len(events)-l.limit:] // older ones no longer decide anything
	}
	l.events[key] = events
}

// reset forgets key's events.
func (l *limiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.events, key)
}

// recent drops key's events that left the window and returns the rest.
// The caller holds the lock.
func (l *limiter) recent(key string, now time.Time) []time.Time {
	events := l.events[key]
	i := 0
	for i < len(events) && !events[i].Add(l.window).After(now) {
		i++
	}
	events = events[i:]
	if len(events) == 0 {
		delete(l.events, key)
		return nil
	}
	l.events[key] = events
	return events
}

// throttle limits events per email address and per client.
type throttle struct {
	byEmail  *limiter
	byClient *limiter
	salt     []byte // random per process, so the keys cannot be turned back into addresses
}

func newThrottle(perEmail, perClient int, window time.Duration) *throttle {
	salt := make([]byte, 32)
	rand.Read(salt)
	return &throttle{byEmail: newLimiter(perEmail, window), byClient: newLimiter(perClient, window), salt: salt}
}

// retryAfter returns how long the email address or the client must wait; 0 means go ahead.
func (t *throttle) retryAfter(email, client string, now time.Time) time.Duration {
	return max(t.byEmail.wait(email, now), t.byClient.wait(client, now))
}

// record counts one event (a failed sign-in, a reset request) for both.
func (t *throttle) record(email, client string, now time.Time) {
	t.byEmail.add(email, now)
	t.byClient.add(client, now)
}

// forget clears an address after a sign-in that worked.
func (t *throttle) forget(email string) {
	t.byEmail.reset(email)
}

// clientKey identifies the client of r without keeping its address.
func (t *throttle) clientKey(r *http.Request) string {
	h := sha256.New()
	h.Write(t.salt)
	h.Write([]byte(clientAddress(r)))
	return hex.EncodeToString(h.Sum(nil))
}

// clientAddress is the address of the client that sent r. Behind a reverse
// proxy (a peer on this machine or a private network, as Caddy in Docker
// is) that is the last address in X-Forwarded-For, which the proxy added.
// From any other peer the header is ignored: the client could have written
// it to escape the limit.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = peer.Unmap()
	if peer.IsLoopback() || peer.IsPrivate() {
		forwarded := r.Header.Values("X-Forwarded-For")
		if len(forwarded) > 0 {
			hops := strings.Split(forwarded[len(forwarded)-1], ",")
			if last, err := netip.ParseAddr(strings.TrimSpace(hops[len(hops)-1])); err == nil {
				return last.Unmap().String()
			}
		}
	}
	return peer.String()
}
