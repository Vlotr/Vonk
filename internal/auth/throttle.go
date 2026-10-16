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

// Login throttling: at most 5 tries per address and 20 per client in 15
// minutes; after that the answer is 429 until the oldest try is 15 minutes
// old. A try is reserved before the password is checked, so requests that
// arrive at the same moment cannot all slip through; a sign-in that works
// gives its try back. Counts live in memory and start over when the server
// restarts. Neither addresses nor client addresses are kept: the keys are
// salted hashes of them.
const (
	failuresPerEmail  = 5
	failuresPerClient = 20
	throttleWindow    = 15 * time.Minute
)

// maxLimiterKeys caps how many keys a limiter tracks, so a flood of new
// addresses cannot grow it without end.
const maxLimiterKeys = 10_000

// evictionSample is how many keys a full limiter looks at to make room.
const evictionSample = 32

// limiter counts events per key over a sliding window. It is not safe for
// concurrent use: the throttle that owns it holds a lock around every call.
type limiter struct {
	limit   int
	window  time.Duration
	maxKeys int
	events  map[string][]time.Time // oldest first, at most limit of them
}

func newLimiter(limit int, window time.Duration) *limiter {
	return &limiter{limit: limit, window: window, maxKeys: maxLimiterKeys, events: map[string][]time.Time{}}
}

// wait returns how long key must wait until another event is allowed: 0
// when it is allowed now.
func (l *limiter) wait(key string, now time.Time) time.Duration {
	events := l.recent(key, now)
	if len(events) < l.limit {
		return 0
	}
	// The oldest event that keeps the count at the limit must leave the window.
	return events[len(events)-l.limit].Add(l.window).Sub(now)
}

// add counts an event for key.
func (l *limiter) add(key string, now time.Time) {
	events := l.recent(key, now)
	if events == nil && len(l.events) >= l.maxKeys {
		l.makeRoom(now)
	}
	events = append(events, now)
	if len(events) > l.limit {
		events = events[len(events)-l.limit:] // older ones no longer decide anything
	}
	l.events[key] = events
}

// remove takes back one event of key at the given time.
func (l *limiter) remove(key string, at time.Time) {
	events := l.events[key]
	for i, t := range events {
		if t.Equal(at) {
			events = append(events[:i:i], events[i+1:]...)
			break
		}
	}
	if len(events) == 0 {
		delete(l.events, key)
		return
	}
	l.events[key] = events
}

// reset forgets key's events.
func (l *limiter) reset(key string) {
	delete(l.events, key)
}

// makeRoom frees at least one key in a full limiter, looking at a few keys
// only (Go visits a map in random order): the ones whose events all left
// the window go, and if none did, the one used longest ago.
func (l *limiter) makeRoom(now time.Time) {
	var oldestKey string
	var oldest time.Time
	freed, seen := false, 0
	for key, events := range l.events {
		newest := events[len(events)-1]
		if !newest.Add(l.window).After(now) {
			delete(l.events, key)
			freed = true
		} else if oldestKey == "" || newest.Before(oldest) {
			oldestKey, oldest = key, newest
		}
		if seen++; seen >= evictionSample {
			break
		}
	}
	if !freed && oldestKey != "" {
		delete(l.events, oldestKey)
	}
}

// recent drops key's events that left the window and returns the rest.
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

// throttle limits tries per email address and per client.
type throttle struct {
	mu       sync.Mutex // around both limiters, so a try is checked and reserved in one step
	byEmail  *limiter
	byClient *limiter
	salt     []byte // random per process, so the keys cannot be turned back into addresses
}

func newThrottle(perEmail, perClient int, window time.Duration) *throttle {
	salt := make([]byte, 32)
	rand.Read(salt)
	return &throttle{byEmail: newLimiter(perEmail, window), byClient: newLimiter(perClient, window), salt: salt}
}

// attempt is one reserved try of an address from a client.
type attempt struct {
	emailKey, clientKey string
	at                  time.Time
}

// begin reserves a try for the address and the client. When either has
// used up its tries it reserves nothing, and wait says how long to wait.
// A reserved try counts as a failure unless succeeded gives it back.
func (t *throttle) begin(email, clientKey string, now time.Time) (attempt, time.Duration, bool) {
	a := attempt{emailKey: t.hash("email", email), clientKey: clientKey, at: now}
	t.mu.Lock()
	defer t.mu.Unlock()
	if wait := max(t.byEmail.wait(a.emailKey, now), t.byClient.wait(clientKey, now)); wait > 0 {
		return attempt{}, wait, false
	}
	t.byEmail.add(a.emailKey, now)
	t.byClient.add(clientKey, now)
	return a, 0, true
}

// succeeded gives a try back after a sign-in that worked: the address
// starts over and the client's try does not count.
func (t *throttle) succeeded(a attempt) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byEmail.reset(a.emailKey)
	t.byClient.remove(a.clientKey, a.at)
}

// forget clears an address, after its owner proved it is theirs.
func (t *throttle) forget(email string) {
	key := t.hash("email", email)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.byEmail.reset(key)
}

// clientKey identifies the client of r without keeping its address.
func (t *throttle) clientKey(r *http.Request) string {
	return t.hash("client", clientAddress(r))
}

// hash is the key for a value of a kind ("email", "client").
func (t *throttle) hash(kind, value string) string {
	h := sha256.New()
	h.Write(t.salt)
	h.Write([]byte(kind + "\x00" + value))
	return hex.EncodeToString(h.Sum(nil))
}

// trustedProxy reports whether addr may be a reverse proxy: this machine
// or a private network, as Caddy in Docker is.
func trustedProxy(addr netip.Addr) bool {
	return addr.IsLoopback() || addr.IsPrivate()
}

// clientAddress is the address the throttle counts a request under. Behind
// a reverse proxy (a peer that is trustedProxy) it is the right-most
// X-Forwarded-For entry that is not a proxy itself: proxies append the
// address they saw, so entries further left were written by the client and
// are ignored. From any other peer the header is ignored altogether. IPv6
// addresses count per /64, the block one subscriber usually gets.
func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	client := peer.Unmap()
	if trustedProxy(client) {
		hops := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
		for i := len(hops) - 1; i >= 0; i-- {
			hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break // not written by a proxy; keep what we have
			}
			client = hop.Unmap()
			if !trustedProxy(client) {
				break
			}
		}
	}
	if client.Is6() {
		return netip.PrefixFrom(client, 64).Masked().String()
	}
	return client.String()
}
