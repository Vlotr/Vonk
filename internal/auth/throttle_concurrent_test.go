package auth

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
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
	if n, m := len(env.svc.logins.byEmail.events), len(env.svc.logins.byClient.events); n != 0 || m != 0 {
		t.Errorf("the throttle tracks %d addresses and %d clients, want none", n, m)
	}
}
