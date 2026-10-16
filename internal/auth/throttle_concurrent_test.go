package auth

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestLogin_ThrottleHoldsUnderConcurrency fires many wrong logins at the
// same moment. Each one must reserve its try before the slow password check,
// or every request that arrives before the first failure is recorded gets
// checked: the limit would only count once the hashes are done.
func TestLogin_ThrottleHoldsUnderConcurrency(t *testing.T) {
	const parallel = 60

	// fire sends parallel logins at once; who returns the remote address
	// and email of request i. It returns how many were checked (401) and
	// how many refused (429).
	fire := func(t *testing.T, env *authEnv, who func(i int) (remote, email string)) (checked, refused int) {
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
		for _, code := range codes {
			switch code {
			case http.StatusUnauthorized:
				checked++
			case http.StatusTooManyRequests:
				refused++
			default:
				t.Errorf("unexpected status %d", code)
			}
		}
		return checked, refused
	}

	t.Run("one address from many clients", func(t *testing.T) {
		env := newAuthEnv(t)
		env.setup(t, "jan@example.com", adminPassword)
		checked, refused := fire(t, env, func(i int) (string, string) {
			return fmt.Sprintf("203.0.113.%d:5000", i+1), "jan@example.com"
		})
		if checked != failuresPerEmail || refused != parallel-failuresPerEmail {
			t.Errorf("%d checked, %d refused; want %d checked and the other %d refused", checked, refused, failuresPerEmail, parallel-failuresPerEmail)
		}
	})

	t.Run("one client for many addresses", func(t *testing.T) {
		env := newAuthEnv(t)
		env.setup(t, "jan@example.com", adminPassword)
		checked, refused := fire(t, env, func(i int) (string, string) {
			return "198.51.100.7:5000", fmt.Sprintf("user%d@example.com", i)
		})
		if checked != failuresPerClient || refused != parallel-failuresPerClient {
			t.Errorf("%d checked, %d refused; want %d checked and the other %d refused", checked, refused, failuresPerClient, parallel-failuresPerClient)
		}
	})
}

// TestLogin_InvalidAddressNotCounted: what cannot be an address is refused
// with 400 before the throttle sees it, so floods of junk (or of 1 MB
// "addresses") never grow its memory.
func TestLogin_InvalidAddressNotCounted(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	for _, email := range []string{"not-an-address", strings.Repeat("a", 250) + "@example.com", "Jan <jan@example.com>"} {
		rec := env.do(newBrowser(), "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": "wrong password"}))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("login as %.20q… = %d, want 400", email, rec.Code)
		}
	}
	env.svc.logins.mu.Lock()
	defer env.svc.logins.mu.Unlock()
	l := env.svc.logins
	if n, m := len(l.byAccount.events)+len(l.byAddress.events), len(l.byClient.events); n != 0 || m != 0 {
		t.Errorf("the throttle tracks %d addresses and %d clients, want none", n, m)
	}
}

// TestThrottle_FloodDoesNotFreeALockedAccount: a locked-out address of an
// existing account adds no events (refused tries reserve nothing), so if it
// shared the capped store with throwaway addresses it would be the first to
// be evicted. Accounts have a store of their own that never evicts.
func TestThrottle_FloodDoesNotFreeALockedAccount(t *testing.T) {
	th := newThrottle(failuresPerEmail, failuresPerClient, throttleWindow)
	now := time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)
	for i := range failuresPerEmail {
		if _, _, ok := th.begin("jan@example.com", true, fmt.Sprint("client", i), now); !ok {
			t.Fatalf("try %d refused", i+1)
		}
	}
	// 20 000 throwaway addresses from as many clients, a minute later.
	for i := range 20_000 {
		th.begin(fmt.Sprintf("throwaway%d@example.com", i), false, fmt.Sprint("flood", i), now.Add(time.Minute))
	}
	if n := len(th.byAddress.events); n > maxLimiterKeys {
		t.Errorf("the store of unknown addresses holds %d keys, cap %d", n, maxLimiterKeys)
	}
	if _, wait, ok := th.begin("jan@example.com", true, "fresh-client", now.Add(2*time.Minute)); ok || wait != 13*time.Minute {
		t.Errorf("after the flood: allowed %v, wait %v; want still locked for 13m", ok, wait)
	}
}

// TestLogin_FloodDoesNotFreeALockedAccount is the same through the login
// endpoint, with a small cap so the flood stays short.
func TestLogin_FloodDoesNotFreeALockedAccount(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	env.svc.logins.byAddress.maxKeys = 50
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
	if code := try("198.51.100.1:5000", "jan@example.com", adminPassword); code != http.StatusTooManyRequests {
		t.Errorf("the locked account after a flood of 200 addresses = %d, want 429", code)
	}
}

// TestLogin_SameThrottleForKnownAndUnknownAddresses: whether an address
// has an account, its tries run out the same way, so the answers do not
// tell.
func TestLogin_SameThrottleForKnownAndUnknownAddresses(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	pattern := func(email string, firstClient int) []string {
		var got []string
		for i := range failuresPerEmail + 2 {
			b := newBrowser()
			b.remote = fmt.Sprintf("203.0.113.%d:5000", firstClient+i)
			rec := env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": "wrong password"}))
			got = append(got, fmt.Sprint(rec.Code, " ", rec.Header().Get("Retry-After"), " ", strings.TrimSpace(rec.Body.String())))
		}
		return got
	}
	known, unknown := pattern("jan@example.com", 1), pattern("nobody@example.com", 101)
	if strings.Join(known, "|") != strings.Join(unknown, "|") {
		t.Errorf("answers differ:\nknown   %q\nunknown %q", known, unknown)
	}
	for i, answer := range known {
		want := "401"
		if i >= failuresPerEmail {
			want = "429"
		}
		if !strings.HasPrefix(answer, want) {
			t.Errorf("try %d = %q, want %s", i+1, answer, want)
		}
	}
}
