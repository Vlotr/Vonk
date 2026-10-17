package auth

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countChecks makes the service count the password checks against a real
// hash (not the dummy one that unknown and locked addresses get).
func countChecks(env *authEnv) *atomic.Int64 {
	var real atomic.Int64
	env.svc.verify = func(ctx context.Context, hash, password string) (bool, error) {
		if hash != dummyHash() {
			real.Add(1)
		}
		return VerifyPassword(ctx, hash, password)
	}
	return &real
}

// fireLogins sends parallel logins at the same moment; who gives the client
// address and email of request i. It returns the status codes.
func fireLogins(t *testing.T, env *authEnv, parallel int, who func(i int) (remote, email string)) []int {
	t.Helper()
	start := make(chan struct{})
	codes := make([]int, parallel)
	var wg sync.WaitGroup
	for i := range parallel {
		wg.Add(1)
		go func() {
			defer wg.Done()
			remote, email := who(i)
			b := newBrowser()
			b.remote = remote
			body := jsonBody(map[string]string{"email": email, "password": "wrong password"})
			<-start
			codes[i] = env.do(b, "POST", "/ui/api/login", body).Code
		}()
	}
	close(start)
	wg.Wait()
	return codes
}

func countCodes(codes []int) map[int]int {
	n := map[int]int{}
	for _, c := range codes {
		n[c]++
	}
	return n
}

// TestLogin_ThrottleHoldsUnderConcurrency fires many wrong logins at the
// same moment. Each try is reserved before the slow password check, or
// every request that arrives before the first failure is counted gets its
// password checked.
func TestLogin_ThrottleHoldsUnderConcurrency(t *testing.T) {
	const parallel = 60

	t.Run("one account from many clients", func(t *testing.T) {
		env := newAuthEnv(t)
		env.setup(t, "jan@example.com", adminPassword)
		checks := countChecks(env)
		codes := countCodes(fireLogins(t, env, parallel, func(i int) (string, string) {
			return fmt.Sprintf("203.0.113.%d:5000", i+1), "jan@example.com"
		}))
		// All get the wrong-password answer; only 5 had the password checked.
		if codes[http.StatusUnauthorized] != parallel {
			t.Errorf("answers %v, want %d × 401", codes, parallel)
		}
		if n := checks.Load(); n != failuresPerEmail {
			t.Errorf("%d password checks, want %d", n, failuresPerEmail)
		}
	})

	t.Run("one client for many addresses", func(t *testing.T) {
		env := newAuthEnv(t)
		env.setup(t, "jan@example.com", adminPassword)
		var checks atomic.Int64
		env.svc.verify = func(ctx context.Context, hash, password string) (bool, error) {
			checks.Add(1)
			return VerifyPassword(ctx, hash, password)
		}
		codes := countCodes(fireLogins(t, env, parallel, func(i int) (string, string) {
			return "198.51.100.7:5000", fmt.Sprintf("user%d@example.com", i)
		}))
		if codes[http.StatusUnauthorized] != failuresPerClient || codes[http.StatusTooManyRequests] != parallel-failuresPerClient {
			t.Errorf("answers %v, want %d × 401 and %d × 429", codes, failuresPerClient, parallel-failuresPerClient)
		}
		if n := checks.Load(); n != failuresPerClient {
			t.Errorf("%d password checks, want %d", n, failuresPerClient)
		}
	})
}

// TestLogin_InvalidAddressNotCounted: what cannot be an address is refused
// with 400 before the throttle sees it.
func TestLogin_InvalidAddressNotCounted(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	for _, email := range []string{"not-an-address", strings.Repeat("a", 250) + "@example.com", "Jan <jan@example.com>"} {
		rec := env.do(newBrowser(), "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": "wrong password"}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("login as %.20q… = %d, want 400", email, rec.Code)
		}
	}
	l := env.svc.logins
	l.mu.Lock()
	defer l.mu.Unlock()
	if n, m := len(l.byAccount.events), len(l.byClient.events); n != 0 || m != 0 {
		t.Errorf("the throttle tracks %d accounts and %d clients, want none", n, m)
	}
}

// TestLogin_UnknownAddressesKeepNoState: addresses without an account are
// counted per client only; they leave no key of their own.
func TestLogin_UnknownAddressesKeepNoState(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	for i := range 100 {
		b := newBrowser()
		b.remote = fmt.Sprintf("11.0.0.%d:5000", i)
		env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": fmt.Sprintf("throwaway%d@example.com", i), "password": "wrong password"}))
		env.do(b, "POST", "/ui/api/password/forgot", jsonBody(map[string]string{"email": fmt.Sprintf("throwaway%d@example.com", i)}))
	}
	for name, th := range map[string]*throttle{"logins": env.svc.logins, "resets": env.svc.resets} {
		th.mu.Lock()
		if n := len(th.byAccount.events); n != 0 {
			t.Errorf("%s: %d address keys after 100 unknown addresses, want 0", name, n)
		}
		th.mu.Unlock()
	}
}

// TestThrottle_FloodDoesNotFreeALockedAccount: a flood of throwaway
// addresses has nothing to push out; the lock stays.
func TestThrottle_FloodDoesNotFreeALockedAccount(t *testing.T) {
	th := newThrottle(failuresPerEmail, failuresPerClient, throttleWindow)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for i := range failuresPerEmail {
		if a, _, ok := th.begin("jan@example.com", true, fmt.Sprint("client", i), now); !ok || a.locked {
			t.Fatalf("try %d: allowed %v, locked %v", i+1, ok, a.locked)
		}
	}
	for i := range 20_000 {
		th.begin(fmt.Sprintf("throwaway%d@example.com", i), false, fmt.Sprint("flood", i), now.Add(time.Minute))
	}
	if n := len(th.byClient.events); n > maxLimiterKeys {
		t.Errorf("the client store holds %d keys, cap %d", n, maxLimiterKeys)
	}
	if n := len(th.byAccount.events); n != 1 {
		t.Errorf("the account store holds %d keys, want only jan's", n)
	}
	a, _, ok := th.begin("jan@example.com", true, "fresh-client", now.Add(2*time.Minute))
	if !ok || !a.locked || a.lockedFor != 13*time.Minute {
		t.Errorf("after the flood: allowed %v, locked %v for %v; want locked for 13m", ok, a.locked, a.lockedFor)
	}
}

// TestLogin_SameAnswersForAccountsAndOtherAddresses: ten tries for an
// address with an account and ten for one without get the same answers,
// also after 20 000 throwaway addresses flooded the throttle.
func TestLogin_SameAnswersForAccountsAndOtherAddresses(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	answers := func(email string, firstClient int) []string {
		var got []string
		for i := range 10 {
			b := newBrowser()
			b.remote = fmt.Sprintf("203.0.%d.%d:5000", firstClient/256, firstClient%256+i)
			rec := env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": "wrong password"}))
			got = append(got, fmt.Sprint(rec.Code, " ", rec.Header(), " ", strings.TrimSpace(rec.Body.String())))
		}
		return got
	}
	compare := func(when string, known, unknown []string) {
		t.Helper()
		if strings.Join(known, "|") != strings.Join(unknown, "|") {
			t.Errorf("%s: answers differ:\nknown   %q\nunknown %q", when, known, unknown)
		}
		for i, a := range known {
			if !strings.HasPrefix(a, "401 ") {
				t.Errorf("%s: try %d = %q, want 401", when, i+1, a)
			}
		}
	}
	compare("before a flood", answers("jan@example.com", 1), answers("nobody@example.com", 20))

	env.advance(throttleWindow)
	for i := range 20_000 {
		env.svc.logins.begin(fmt.Sprintf("throwaway%d@example.com", i), false, fmt.Sprint("flood", i), env.clock())
	}
	compare("after a flood", answers("jan@example.com", 300), answers("someone@example.com", 320))
}

// TestLogin_FloodDoesNotFreeALockedAccount is the flood through the login
// endpoint, with a small client store so it stays short.
func TestLogin_FloodDoesNotFreeALockedAccount(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	env.svc.logins.byClient.maxKeys = 50
	try := func(remote, email, password string) int {
		b := newBrowser()
		b.remote = remote
		return env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": password})).Code
	}
	for i := range failuresPerEmail {
		try(fmt.Sprintf("203.0.113.%d:5000", i+1), "jan@example.com", "wrong password")
	}
	for i := range 200 {
		try(fmt.Sprintf("11.0.%d.%d:5000", i/256, i%256), fmt.Sprintf("throwaway%d@example.com", i), "wrong password")
	}
	if code := try("198.51.100.1:5000", "jan@example.com", adminPassword); code != http.StatusUnauthorized {
		t.Errorf("the locked account after a flood of 200 addresses = %d, want 401", code)
	}
}
