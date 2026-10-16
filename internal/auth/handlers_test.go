package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

const adminPassword = "admin password 1"

func TestSetup_OnlyWhenNoUsers(t *testing.T) {
	env := newAuthEnv(t)
	anon := newBrowser()

	var status struct {
		SetupNeeded bool `json:"setupNeeded"`
		MailEnabled bool `json:"mailEnabled"`
	}
	rec := env.do(anon, "GET", "/ui/api/auth/status", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil || !status.SetupNeeded || !status.MailEnabled {
		t.Fatalf("status before setup = %d %s", rec.Code, rec.Body)
	}

	// Bad input is refused and creates nobody.
	for _, body := range []map[string]string{
		{"email": "not-an-address", "password": adminPassword},
		{"email": "Jan <jan@example.com>", "password": adminPassword},
		{"email": "jan@example.com", "password": "too short"},
	} {
		if rec := env.do(anon, "POST", "/ui/api/setup", jsonBody(body)); rec.Code != http.StatusBadRequest || errorOf(rec) == "" {
			t.Errorf("setup %v = %d %s, want 400 with a message", body, rec.Code, rec.Body)
		}
	}

	admin := newBrowser()
	me := signedIn(t, admin, env.do(admin, "POST", "/ui/api/setup", jsonBody(map[string]string{"email": " Jan@Example.com ", "password": adminPassword})))
	if me.Email != "jan@example.com" || me.Role != "admin" || !me.MailEnabled {
		t.Errorf("setup answer = %+v, want admin jan@example.com", me)
	}

	// The session cookie: HttpOnly, SameSite=Lax, Secure (BASE_URL is https), 30 days.
	c := admin.cookie
	if !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || !c.Secure || c.Path != "/" || c.MaxAge != 30*24*3600 {
		t.Errorf("cookie = %+v", c)
	}
	if c.Name != "__Host-monitor_session" {
		t.Errorf("cookie name = %q, want the __Host- prefix over https", c.Name)
	}
	// Only a hash of the token is stored.
	if _, _, err := env.db.SessionByTokenHash(context.Background(), c.Value); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("session found by the raw cookie value: err = %v", err)
	}
	if _, _, err := env.db.SessionByTokenHash(context.Background(), env.svc.hashToken(c.Value)); err != nil {
		t.Errorf("session by the token's hash: %v", err)
	}
	// The password is stored as an argon2id hash.
	u, err := env.db.UserByEmail(context.Background(), "jan@example.com")
	if err != nil || !strings.HasPrefix(u.PasswordHash, "$argon2id$") || strings.Contains(u.PasswordHash, adminPassword) {
		t.Errorf("stored user = %+v, %v", u, err)
	}

	if rec := env.do(admin, "GET", "/ui/api/me", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"role":"admin"`) {
		t.Errorf("GET /ui/api/me = %d %s", rec.Code, rec.Body)
	}
	rec = env.do(anon, "GET", "/ui/api/auth/status", "")
	if json.Unmarshal(rec.Body.Bytes(), &status); status.SetupNeeded {
		t.Errorf("status after setup = %s, want setupNeeded false", rec.Body)
	}

	// Registration is closed now: a second setup is not found.
	rec = env.do(anon, "POST", "/ui/api/setup", jsonBody(map[string]string{"email": "evil@example.com", "password": "evil password"}))
	if rec.Code != http.StatusNotFound || anon.cookie != nil {
		t.Errorf("second setup = %d %s, want 404 and no session", rec.Code, rec.Body)
	}
	if n, _ := env.db.CountUsers(context.Background()); n != 1 {
		t.Errorf("users = %d, want 1", n)
	}
}

func TestLogin_WrongPassword401(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)

	b := newBrowser()
	wrong := env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": "jan@example.com", "password": "wrong password"}))
	unknown := env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": "nobody@example.com", "password": "wrong password"}))
	for name, rec := range map[string]int{"wrong password": wrong.Code, "unknown email": unknown.Code} {
		if rec != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, rec)
		}
	}
	// Both say the same, so an attacker cannot tell which addresses have an account.
	if wrong.Body.String() != unknown.Body.String() {
		t.Errorf("answers differ: %s and %s", wrong.Body, unknown.Body)
	}
	if b.cookie != nil {
		t.Error("a failed sign-in set a session cookie")
	}

	// Any case of the address works.
	me := signedIn(t, b, env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": "JAN@example.com", "password": adminPassword})))
	if me.Email != "jan@example.com" {
		t.Errorf("signed in as %q", me.Email)
	}

	// Not JSON (a form another site could post): refused before anything is checked.
	rec := env.do(newBrowser(), "POST", "/ui/api/login", "")
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("login without a JSON body = %d, want 415", rec.Code)
	}
}

func TestLogin_Throttle429(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	attempt := func(remote, email, password string) *httptest.ResponseRecorder {
		b := newBrowser()
		b.remote = remote
		return env.do(b, "POST", "/ui/api/login", jsonBody(map[string]string{"email": email, "password": password}))
	}

	// Five failures for one address, from several clients...
	for i := range 5 {
		if rec := attempt("203.0.113."+strconv.Itoa(i+1)+":5000", "jan@example.com", "wrong password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("failure %d = %d, want 401", i+1, rec.Code)
		}
	}
	// ...block it, even with the right password, and say for how long.
	rec := attempt("198.51.100.1:5000", "jan@example.com", adminPassword)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("sixth attempt = %d %s, want 429", rec.Code, rec.Body)
	}
	if retry, err := strconv.Atoi(rec.Header().Get("Retry-After")); err != nil || retry < 1 || retry > 15*60 {
		t.Errorf("Retry-After = %q, want 1 to 900 seconds", rec.Header().Get("Retry-After"))
	}
	// Fifteen minutes after the first failure the address may try again.
	env.advance(15 * time.Minute)
	if rec := attempt("198.51.100.1:5000", "jan@example.com", adminPassword); rec.Code != http.StatusOK {
		t.Errorf("after 15 minutes = %d %s, want 200", rec.Code, rec.Body)
	}

	// Twenty failures from one client, each for another address, block that client...
	for i := range 20 {
		if rec := attempt("203.0.113.99:5000", "user"+strconv.Itoa(i)+"@example.com", "wrong password"); rec.Code != http.StatusUnauthorized {
			t.Fatalf("client failure %d = %d, want 401", i+1, rec.Code)
		}
	}
	if rec := attempt("203.0.113.99:6000", "jan@example.com", adminPassword); rec.Code != http.StatusTooManyRequests {
		t.Errorf("21st attempt from the client = %d, want 429", rec.Code)
	}
	// ...but not others.
	if rec := attempt("203.0.113.100:5000", "jan@example.com", adminPassword); rec.Code != http.StatusOK {
		t.Errorf("another client = %d, want 200", rec.Code)
	}
}

func TestSession_ExpiredRejected(t *testing.T) {
	env := newAuthEnv(t)
	b := env.setup(t, "jan@example.com", adminPassword)

	// Used within 30 days, the session keeps sliding forward...
	env.advance(20 * 24 * time.Hour)
	rec := env.do(b, "GET", "/ui/api/ping", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("after 20 days = %d", rec.Code)
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != 30*24*3600 {
		t.Errorf("the cookie was not renewed for 30 days: %+v", cookies)
	}
	// Within the hour it is not written again.
	if rec := env.do(b, "GET", "/ui/api/ping", ""); len(rec.Result().Cookies()) != 0 {
		t.Errorf("renewed again right away: %+v", rec.Result().Cookies())
	}
	env.advance(20 * 24 * time.Hour)
	if rec := env.do(b, "GET", "/ui/api/ping", ""); rec.Code != http.StatusOK {
		t.Fatalf("40 days after sign-in, 20 after last use = %d, want 200", rec.Code)
	}

	// ...and expires 30 days after it was last used.
	env.advance(30*24*time.Hour + time.Second)
	expired := b.cookie
	rec = env.do(b, "GET", "/ui/api/ping", "")
	if rec.Code != http.StatusUnauthorized || errorOf(rec) == "" {
		t.Errorf("expired session, API = %d %s, want 401", rec.Code, rec.Body)
	}
	if b.cookie != nil {
		t.Error("the expired cookie was not cleared")
	}
	b.cookie = expired
	rec = env.do(b, "GET", "/projects/1/issues", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/login?next="+url.QueryEscape("/projects/1/issues") {
		t.Errorf("expired session, page = %d Location %q, want 303 to the login page", rec.Code, rec.Header().Get("Location"))
	}
}

func TestRequireUser_PagesAndAPI(t *testing.T) {
	env := newAuthEnv(t)
	anon := newBrowser()

	// Before setup every page leads to it.
	rec := env.do(anon, "GET", "/", "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/setup" {
		t.Errorf("GET / before setup = %d %q, want 303 /setup", rec.Code, rec.Header().Get("Location"))
	}
	env.setup(t, "jan@example.com", adminPassword)

	rec = env.do(anon, "GET", "/projects/1/issues?status=resolved", "")
	want := "/login?next=" + url.QueryEscape("/projects/1/issues?status=resolved")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want {
		t.Errorf("page without a session = %d %q, want 303 %s", rec.Code, rec.Header().Get("Location"), want)
	}
	for _, method := range []string{"GET", "POST"} {
		rec := env.do(anon, method, "/ui/api/ping", "")
		if rec.Code != http.StatusUnauthorized || !strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
			t.Errorf("%s API without a session = %d %q, want a JSON 401", method, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
	// A made-up cookie is no session either.
	anon.cookie = &http.Cookie{Name: env.svc.cookieName, Value: "made-up"}
	if rec := env.do(anon, "GET", "/ui/api/ping", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("made-up cookie = %d, want 401", rec.Code)
	}
}

func TestCSRF_Missing403(t *testing.T) {
	env := newAuthEnv(t)
	b := env.setup(t, "jan@example.com", adminPassword)
	token := b.csrf

	b.csrf = ""
	if rec := env.do(b, "POST", "/ui/api/ping", `{}`); rec.Code != http.StatusForbidden || errorOf(rec) == "" {
		t.Errorf("POST without X-CSRF-Token = %d %s, want 403", rec.Code, rec.Body)
	}
	if rec := env.do(b, "POST", "/ui/api/logout", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("logout without X-CSRF-Token = %d, want 403", rec.Code)
	}
	b.csrf = token[:len(token)-1] + "x"
	if rec := env.do(b, "POST", "/ui/api/ping", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("POST with a wrong X-CSRF-Token = %d, want 403", rec.Code)
	}
	// Another session's token does not fit this one.
	other := env.login(t, "jan@example.com", adminPassword)
	b.csrf = other.csrf
	if rec := env.do(b, "POST", "/ui/api/ping", `{}`); rec.Code != http.StatusForbidden {
		t.Errorf("POST with another session's token = %d, want 403", rec.Code)
	}

	b.csrf = ""
	if rec := env.do(b, "GET", "/ui/api/ping", ""); rec.Code != http.StatusOK {
		t.Errorf("GET without a token = %d, want 200", rec.Code)
	}
	b.csrf = token
	if rec := env.do(b, "POST", "/ui/api/ping", `{}`); rec.Code != http.StatusOK {
		t.Errorf("POST with the token = %d %s, want 200", rec.Code, rec.Body)
	}
}

func TestLogout_EndsTheSession(t *testing.T) {
	env := newAuthEnv(t)
	b := env.setup(t, "jan@example.com", adminPassword)
	cookie := b.cookie

	if rec := env.do(b, "POST", "/ui/api/logout", `{}`); rec.Code != http.StatusOK || b.cookie != nil {
		t.Fatalf("logout = %d %s, cookie %+v; want 200 and the cookie cleared", rec.Code, rec.Body, b.cookie)
	}
	b.cookie = cookie // a copy kept from before
	if rec := env.do(b, "GET", "/ui/api/ping", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("old cookie after logout = %d, want 401", rec.Code)
	}
}

func TestChangePassword_SignsOutOtherSessions(t *testing.T) {
	env := newAuthEnv(t)
	b := env.setup(t, "jan@example.com", adminPassword)
	other := env.login(t, "jan@example.com", adminPassword)

	rec := env.do(b, "POST", "/ui/api/me/password", jsonBody(map[string]string{"currentPassword": "wrong one!", "newPassword": "a new password"}))
	if rec.Code != http.StatusBadRequest || errorOf(rec) == "" {
		t.Errorf("wrong current password = %d %s, want 400", rec.Code, rec.Body)
	}
	rec = env.do(b, "POST", "/ui/api/me/password", jsonBody(map[string]string{"currentPassword": adminPassword, "newPassword": "short"}))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("short new password = %d, want 400", rec.Code)
	}
	rec = env.do(b, "POST", "/ui/api/me/password", jsonBody(map[string]string{"currentPassword": adminPassword, "newPassword": "a new password"}))
	if rec.Code != http.StatusOK {
		t.Fatalf("change password = %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(b, "GET", "/ui/api/ping", ""); rec.Code != http.StatusOK {
		t.Errorf("this session after the change = %d, want 200", rec.Code)
	}
	if rec := env.do(other, "GET", "/ui/api/ping", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("other session after the change = %d, want 401", rec.Code)
	}
	env.login(t, "jan@example.com", "a new password")
}

func TestInvite_AcceptCreatesUserWithRoleAndAccess(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()
	env.setup(t, "admin@example.com", adminPassword)
	admin, _ := env.db.UserByEmail(ctx, "admin@example.com")
	shop, _ := env.db.CreateProject(ctx, store.NewProject{Name: "Webshop"})
	portal, _ := env.db.CreateProject(ctx, store.NewProject{Name: "Portal"})

	link, err := env.svc.Invite(ctx, admin.ID, "Piet@Example.com", store.RoleMember, []int64{shop.ID})
	if err != nil {
		t.Fatal(err)
	}
	if !link.Mailed {
		t.Error("mail is on, but the invite was not mailed")
	}
	sent := env.mailer.messages()
	if len(sent) != 1 || sent[0].To != "piet@example.com" || !strings.Contains(sent[0].Text, link.URL) || !strings.Contains(sent[0].Text, "admin@example.com") {
		t.Errorf("mail = %+v, want the link to piet@example.com", sent)
	}
	token := tokenFrom(t, link.URL, "/invite")

	anon := newBrowser()
	rec := env.do(anon, "GET", "/ui/api/invite?token="+token, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"email":"piet@example.com"`) || !strings.Contains(rec.Body.String(), `"role":"member"`) {
		t.Errorf("invite details = %d %s", rec.Code, rec.Body)
	}
	if rec := env.do(anon, "POST", "/ui/api/invite/accept", jsonBody(map[string]string{"token": token, "password": "short"})); rec.Code != http.StatusBadRequest {
		t.Errorf("accept with a short password = %d, want 400", rec.Code)
	}

	me := signedIn(t, anon, env.do(anon, "POST", "/ui/api/invite/accept", jsonBody(map[string]string{"token": token, "password": "piet password"})))
	if me.Email != "piet@example.com" || me.Role != "member" {
		t.Errorf("accepted as %+v", me)
	}
	var ping struct {
		Projects []int64 `json:"projects"`
	}
	json.Unmarshal(env.do(anon, "GET", "/ui/api/ping", "").Body.Bytes(), &ping)
	if !reflect.DeepEqual(ping.Projects, []int64{shop.ID}) {
		t.Errorf("member sees projects %v, want only [%d] (not %d)", ping.Projects, shop.ID, portal.ID)
	}
	env.login(t, "piet@example.com", "piet password")

	// An admin invite gives every project.
	link, err = env.svc.Invite(ctx, admin.ID, "kees@example.com", store.RoleAdmin, nil)
	if err != nil {
		t.Fatal(err)
	}
	kees := newBrowser()
	me = signedIn(t, kees, env.do(kees, "POST", "/ui/api/invite/accept", jsonBody(map[string]string{"token": tokenFrom(t, link.URL, "/invite"), "password": "kees password"})))
	json.Unmarshal(env.do(kees, "GET", "/ui/api/ping", "").Body.Bytes(), &ping)
	if me.Role != "admin" || !reflect.DeepEqual(ping.Projects, []int64{shop.ID, portal.ID}) {
		t.Errorf("admin invite: role %q, projects %v; want admin with both", me.Role, ping.Projects)
	}

	// Invites check their input.
	if _, err := env.svc.Invite(ctx, admin.ID, "piet@example.com", store.RoleMember, nil); !errors.Is(err, store.ErrEmailTaken) {
		t.Errorf("invite for an existing account: err = %v, want ErrEmailTaken", err)
	}
	if _, err := env.svc.Invite(ctx, admin.ID, "no address", store.RoleMember, nil); err == nil {
		t.Error("invite for a bad address: want an error")
	}
	if _, err := env.svc.Invite(ctx, admin.ID, "new@example.com", "owner", nil); err == nil {
		t.Error("invite with an unknown role: want an error")
	}
}

func TestInvite_ExpiredOrUsed410(t *testing.T) {
	env := newAuthEnv(t, withoutMail)
	ctx := context.Background()
	env.setup(t, "admin@example.com", adminPassword)
	admin, _ := env.db.UserByEmail(ctx, "admin@example.com")

	accept := func(token string) int {
		return env.do(newBrowser(), "POST", "/ui/api/invite/accept", jsonBody(map[string]string{"token": token, "password": "a good password"})).Code
	}
	details := func(token string) int {
		return env.do(newBrowser(), "GET", "/ui/api/invite?token="+url.QueryEscape(token), "").Code
	}

	link, err := env.svc.Invite(ctx, admin.ID, "piet@example.com", store.RoleMember, nil)
	if err != nil {
		t.Fatal(err)
	}
	if link.Mailed || len(env.mailer.messages()) != 0 {
		t.Error("mail is off, but the invite was mailed")
	}
	used := tokenFrom(t, link.URL, "/invite")
	if code := accept(used); code != http.StatusOK {
		t.Fatalf("first accept = %d", code)
	}
	if code := accept(used); code != http.StatusGone {
		t.Errorf("second accept = %d, want 410", code)
	}
	if code := details(used); code != http.StatusGone {
		t.Errorf("details of a used invite = %d, want 410", code)
	}

	link, _ = env.svc.Invite(ctx, admin.ID, "kees@example.com", store.RoleMember, nil)
	expired := tokenFrom(t, link.URL, "/invite")
	env.advance(7*24*time.Hour - time.Second)
	if code := details(expired); code != http.StatusOK {
		t.Errorf("details just before 7 days = %d, want 200", code)
	}
	env.advance(time.Second)
	if code := details(expired); code != http.StatusGone {
		t.Errorf("details after 7 days = %d, want 410", code)
	}
	if code := accept(expired); code != http.StatusGone {
		t.Errorf("accept after 7 days = %d, want 410", code)
	}
	if n, _ := env.db.CountUsers(ctx); n != 2 {
		t.Errorf("users = %d, want 2 (admin and piet)", n)
	}

	for _, token := range []string{"made-up", ""} {
		if code := accept(token); code != http.StatusNotFound {
			t.Errorf("accept %q = %d, want 404", token, code)
		}
		if code := details(token); code != http.StatusNotFound {
			t.Errorf("details %q = %d, want 404", token, code)
		}
	}
}

func TestReset_ByMail(t *testing.T) {
	env := newAuthEnv(t)
	env.setup(t, "jan@example.com", adminPassword)
	signedInBefore := env.login(t, "jan@example.com", adminPassword)

	forgot := func(email string) *httptest.ResponseRecorder {
		return env.do(newBrowser(), "POST", "/ui/api/password/forgot", jsonBody(map[string]string{"email": email}))
	}
	known, unknown := forgot("JAN@example.com"), forgot("nobody@example.com")
	env.svc.waitForMail()
	// The same answer whether or not the address has an account.
	if known.Code != http.StatusOK || known.Code != unknown.Code || known.Body.String() != unknown.Body.String() {
		t.Errorf("forgot = %d %s and %d %s, want the same 200", known.Code, known.Body, unknown.Code, unknown.Body)
	}
	sent := env.mailer.messages()
	if len(sent) != 1 || sent[0].To != "jan@example.com" {
		t.Fatalf("mail = %+v, want one to jan@example.com", sent)
	}
	token := tokenFrom(t, sent[0].Text, "/reset-password")

	anon := newBrowser()
	if rec := env.do(anon, "GET", "/ui/api/password/reset?token="+token, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "jan@example.com") {
		t.Errorf("reset details = %d %s", rec.Code, rec.Body)
	}
	me := signedIn(t, anon, env.do(anon, "POST", "/ui/api/password/reset", jsonBody(map[string]string{"token": token, "password": "my new password"})))
	if me.Email != "jan@example.com" {
		t.Errorf("signed in as %q", me.Email)
	}
	// Every session from before is signed out; the new password works, the old one not.
	if rec := env.do(signedInBefore, "GET", "/ui/api/ping", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("session from before the reset = %d, want 401", rec.Code)
	}
	env.login(t, "jan@example.com", "my new password")
	if rec := env.do(newBrowser(), "POST", "/ui/api/login", jsonBody(map[string]string{"email": "jan@example.com", "password": adminPassword})); rec.Code != http.StatusUnauthorized {
		t.Errorf("old password = %d, want 401", rec.Code)
	}
	// The link works once.
	if rec := env.do(newBrowser(), "POST", "/ui/api/password/reset", jsonBody(map[string]string{"token": token, "password": "another password"})); rec.Code != http.StatusGone {
		t.Errorf("second use = %d, want 410", rec.Code)
	}

	// A link works for one hour.
	forgot("jan@example.com")
	env.svc.waitForMail()
	sent = env.mailer.messages()
	late := tokenFrom(t, sent[len(sent)-1].Text, "/reset-password")
	env.advance(time.Hour)
	if rec := env.do(newBrowser(), "POST", "/ui/api/password/reset", jsonBody(map[string]string{"token": late, "password": "another password"})); rec.Code != http.StatusGone {
		t.Errorf("link after an hour = %d, want 410", rec.Code)
	}
	if rec := env.do(newBrowser(), "GET", "/ui/api/password/reset?token=made-up", ""); rec.Code != http.StatusNotFound {
		t.Errorf("made-up token = %d, want 404", rec.Code)
	}

	// Asking again and again for one address is throttled like sign-ins.
	var last int
	for range 6 {
		last = forgot("jan@example.com").Code
	}
	if last != http.StatusTooManyRequests {
		t.Errorf("sixth request within 15 minutes = %d, want 429", last)
	}
}

func TestReset_WithoutMailSendsNothing(t *testing.T) {
	env := newAuthEnv(t, withoutMail)
	env.setup(t, "jan@example.com", adminPassword)
	rec := env.do(newBrowser(), "POST", "/ui/api/password/forgot", jsonBody(map[string]string{"email": "jan@example.com"}))
	env.svc.waitForMail()
	if rec.Code != http.StatusOK || len(env.mailer.messages()) != 0 {
		t.Errorf("forgot without mail = %d, %d mails; want 200 and none", rec.Code, len(env.mailer.messages()))
	}
}

func TestResetLink_ForTheCLIAndAdmins(t *testing.T) {
	env := newAuthEnv(t)
	ctx := context.Background()
	env.setup(t, "jan@example.com", adminPassword)
	u, _ := env.db.UserByEmail(ctx, "jan@example.com")

	link, err := env.svc.ResetLink(ctx, u.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if link.Mailed || len(env.mailer.messages()) != 0 {
		t.Error("ResetLink(mail=false) sent mail")
	}
	if !link.ExpiresAt.Equal(authTestStart.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want an hour from now", link.ExpiresAt)
	}
	b := newBrowser()
	signedIn(t, b, env.do(b, "POST", "/ui/api/password/reset", jsonBody(map[string]string{"token": tokenFrom(t, link.URL, "/reset-password"), "password": "from the cli 1"})))

	if _, err := env.svc.ResetLink(ctx, 999, false); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}
}

func TestAccess_FailsClosedWithoutAUser(t *testing.T) {
	ctx := context.Background()
	var nobody User
	if CanAccessProject(ctx, nobody, 1) || len(AccessibleProjectIDs(ctx, nobody)) != 0 || nobody.IsAdmin() {
		t.Error("a request without a user may see projects")
	}
	if u := UserFrom(ctx); u.ID != 0 || CanAccessProject(ctx, u, 1) {
		t.Errorf("UserFrom on a bare context = %+v", u)
	}
}
