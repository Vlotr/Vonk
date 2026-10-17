package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"monitor/internal/auth"
	"monitor/internal/mail"
	"monitor/internal/store"
)

// recordingMailer records the mail the auth service sends.
type recordingMailer struct {
	mu   sync.Mutex
	sent []mail.Message
}

func (m *recordingMailer) Send(_ context.Context, msg mail.Message) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sent = append(m.sent, msg)
	return nil
}

func (m *recordingMailer) messages() []mail.Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]mail.Message(nil), m.sent...)
}

func withMailer(m auth.Mailer) envOption {
	return func(o *auth.Options) { o.Mailer = m }
}

type usersResponse struct {
	Users []struct {
		ID         int64   `json:"id"`
		Email      string  `json:"email"`
		Role       string  `json:"role"`
		ProjectIDs []int64 `json:"projectIds"`
	} `json:"users"`
	Invites []struct {
		ID         int64     `json:"id"`
		Email      string    `json:"email"`
		Role       string    `json:"role"`
		ProjectIDs []int64   `json:"projectIds"`
		ExpiresAt  time.Time `json:"expiresAt"`
	} `json:"invites"`
}

type linkResponse struct {
	Link      string    `json:"link"`
	ExpiresAt time.Time `json:"expiresAt"`
	Mailed    bool      `json:"mailed"`
}

func decode[T any](t *testing.T, body []byte) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return v
}

// tokenOf returns the token in a link such as https://host/invite#token=abc.
func tokenOf(t *testing.T, link string) string {
	t.Helper()
	_, token, ok := strings.Cut(link, "#token=")
	if !ok || token == "" {
		t.Fatalf("no token in %q", link)
	}
	return token
}

func TestReset_AdminLinkForMember(t *testing.T) {
	mailer := &recordingMailer{}
	env := newTestEnv(t, withMailer(mailer))
	member := env.member(t, "piet@example.com")
	path := fmt.Sprintf("/ui/api/settings/users/%d/reset-link", member.userID)

	rec := env.as(t, env.admin, "POST", path, `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST reset-link = %d %s", rec.Code, rec.Body)
	}
	got := decode[linkResponse](t, rec.Body.Bytes())
	if !strings.HasPrefix(got.Link, "https://monitor.example.com/reset-password#token=") || !got.ExpiresAt.Equal(testNow.Add(time.Hour)) {
		t.Errorf("link = %+v, want a reset link valid for an hour", got)
	}
	// Mail is on, so the member also gets it.
	if sent := mailer.messages(); !got.Mailed || len(sent) != 1 || sent[0].To != "piet@example.com" || !strings.Contains(sent[0].Text, got.Link) {
		t.Errorf("mailed %v, mail %+v", got.Mailed, sent)
	}

	// The member uses the link: a new password, signed in, and the old session ends.
	reset := fmt.Sprintf(`{"token":%q,"password":"piet's new password"}`, tokenOf(t, got.Link))
	if rec := env.as(t, nil, "POST", "/ui/api/password/reset", reset); rec.Code != http.StatusOK {
		t.Fatalf("use the link = %d %s", rec.Code, rec.Body)
	}
	if rec := env.as(t, member, "GET", "/ui/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("member's old session after the reset = %d, want 401", rec.Code)
	}
	if rec := env.as(t, nil, "POST", "/ui/api/password/reset", reset); rec.Code != http.StatusGone {
		t.Errorf("second use = %d, want 410", rec.Code)
	}
	if rec := env.as(t, nil, "POST", "/ui/api/login", `{"email":"piet@example.com","password":"piet's new password"}`); rec.Code != http.StatusOK {
		t.Errorf("login with the new password = %d %s", rec.Code, rec.Body)
	}

	// Admins reset their own password on the server instead.
	rec = env.as(t, env.admin, "POST", fmt.Sprintf("/ui/api/settings/users/%d/reset-link", env.admin.userID), `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "monitor admin reset-password") {
		t.Errorf("reset-link for an admin = %d %s, want 400 naming the command", rec.Code, rec.Body)
	}
	if rec := env.as(t, env.admin, "POST", "/ui/api/settings/users/999/reset-link", `{}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user = %d, want 404", rec.Code)
	}
}

func TestReset_AdminLinkWithoutMail(t *testing.T) {
	env := newTestEnv(t)
	member := env.member(t, "piet@example.com")
	rec := env.as(t, env.admin, "POST", fmt.Sprintf("/ui/api/settings/users/%d/reset-link", member.userID), `{}`)
	if got := decode[linkResponse](t, rec.Body.Bytes()); rec.Code != http.StatusOK || got.Mailed || got.Link == "" {
		t.Errorf("reset-link without mail = %d %s, want a link that was not mailed", rec.Code, rec.Body)
	}
}

func TestLastAdmin_CannotBeRemovedOrDemoted(t *testing.T) {
	env := newTestEnv(t)
	self := fmt.Sprintf("/ui/api/settings/users/%d", env.admin.userID)

	rec := env.as(t, env.admin, "PATCH", self, `{"role":"member"}`)
	if rec.Code != http.StatusConflict || !strings.Contains(errorText(rec.Body.Bytes()), "last admin") {
		t.Errorf("demote the only admin = %d %s, want 409", rec.Code, rec.Body)
	}
	if rec := env.as(t, env.admin, "DELETE", self, ""); rec.Code != http.StatusConflict {
		t.Errorf("remove the only admin = %d %s, want 409", rec.Code, rec.Body)
	}
	if u, _ := env.db.UserByID(context.Background(), env.admin.userID); u.Role != store.RoleAdmin {
		t.Errorf("role after refused changes = %q", u.Role)
	}

	// With a second admin, the first may step down; then the second is the last one.
	second := env.user(t, "second@example.com", store.RoleAdmin)
	if rec := env.as(t, second, "PATCH", self, `{"role":"member"}`); rec.Code != http.StatusOK {
		t.Fatalf("demote one of two admins = %d %s", rec.Code, rec.Body)
	}
	secondPath := fmt.Sprintf("/ui/api/settings/users/%d", second.userID)
	if rec := env.as(t, second, "PATCH", secondPath, `{"role":"member"}`); rec.Code != http.StatusConflict {
		t.Errorf("demote the new last admin = %d, want 409", rec.Code)
	}
	if rec := env.as(t, second, "DELETE", secondPath, ""); rec.Code != http.StatusConflict {
		t.Errorf("remove the new last admin = %d, want 409", rec.Code)
	}
	// The first admin is a member now and may no longer manage anyone.
	if rec := env.as(t, env.admin, "GET", "/ui/api/settings/users", ""); rec.Code != http.StatusNotFound {
		t.Errorf("settings as the demoted admin = %d, want 404", rec.Code)
	}
	// Removing them ends their session.
	if rec := env.as(t, second, "DELETE", self, ""); rec.Code != http.StatusOK {
		t.Fatalf("remove a member = %d %s", rec.Code, rec.Body)
	}
	if rec := env.as(t, env.admin, "GET", "/ui/api/me", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("removed user's session = %d, want 401", rec.Code)
	}
	if rec := env.as(t, second, "DELETE", self, ""); rec.Code != http.StatusNotFound {
		t.Errorf("remove twice = %d, want 404", rec.Code)
	}
}

func TestInvites_FromSettings(t *testing.T) {
	env := newTestEnv(t)
	shop, portal := env.project(t, "Webshop"), env.project(t, "Portal")

	body := fmt.Sprintf(`{"email":"Piet@Example.com","role":"member","projectIds":[%d]}`, shop.ID)
	rec := env.as(t, env.admin, "POST", "/ui/api/settings/invites", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST invite = %d %s", rec.Code, rec.Body)
	}
	created := decode[struct {
		Invite struct {
			ID         int64     `json:"id"`
			Email      string    `json:"email"`
			ExpiresAt  time.Time `json:"expiresAt"`
			ProjectIDs []int64   `json:"projectIds"`
		} `json:"invite"`
		Link   string `json:"link"`
		Mailed bool   `json:"mailed"`
	}](t, rec.Body.Bytes())
	if created.Invite.Email != "piet@example.com" || created.Mailed || !created.Invite.ExpiresAt.Equal(testNow.AddDate(0, 0, 7)) ||
		!strings.HasPrefix(created.Link, "https://monitor.example.com/invite#token=") {
		t.Errorf("invite = %+v", created)
	}

	list := decode[usersResponse](t, env.as(t, env.admin, "GET", "/ui/api/settings/users", "").Body.Bytes())
	if len(list.Invites) != 1 || list.Invites[0].Email != "piet@example.com" || fmt.Sprint(list.Invites[0].ProjectIDs) != fmt.Sprint([]int64{shop.ID}) {
		t.Errorf("pending invites = %+v", list.Invites)
	}
	// The list never shows the link again.
	if strings.Contains(env.as(t, env.admin, "GET", "/ui/api/settings/users", "").Body.String(), tokenOf(t, created.Link)) {
		t.Error("the users list contains the invite token")
	}

	// Accepted: a member of Webshop only.
	accept := fmt.Sprintf(`{"token":%q,"password":"piet password"}`, tokenOf(t, created.Link))
	if rec := env.as(t, nil, "POST", "/ui/api/invite/accept", accept); rec.Code != http.StatusOK {
		t.Fatalf("accept = %d %s", rec.Code, rec.Body)
	}
	list = decode[usersResponse](t, env.as(t, env.admin, "GET", "/ui/api/settings/users", "").Body.Bytes())
	if len(list.Invites) != 0 || len(list.Users) != 2 || list.Users[1].Email != "piet@example.com" ||
		list.Users[1].Role != "member" || fmt.Sprint(list.Users[1].ProjectIDs) != fmt.Sprint([]int64{shop.ID}) {
		t.Errorf("after accept: %+v", list)
	}

	// Give Piet Portal instead of Webshop.
	piet := list.Users[1].ID
	rec = env.as(t, env.admin, "PATCH", fmt.Sprintf("/ui/api/settings/users/%d", piet), fmt.Sprintf(`{"projectIds":[%d]}`, portal.ID))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), fmt.Sprintf(`"projectIds":[%d]`, portal.ID)) {
		t.Errorf("change projects = %d %s", rec.Code, rec.Body)
	}
	if rec := env.as(t, env.admin, "PATCH", fmt.Sprintf("/ui/api/settings/users/%d", piet), `{"projectIds":[999]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown project = %d, want 400", rec.Code)
	}
	if rec := env.as(t, env.admin, "PATCH", fmt.Sprintf("/ui/api/settings/users/%d", piet), `{"role":"owner"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown role = %d, want 400", rec.Code)
	}

	// Refused invites.
	for _, tt := range []struct {
		body string
		code int
	}{
		{`{"email":"piet@example.com","role":"member","projectIds":[]}`, http.StatusConflict},
		{`{"email":"not an address","role":"member","projectIds":[]}`, http.StatusBadRequest},
		{`{"email":"kees@example.com","role":"owner","projectIds":[]}`, http.StatusBadRequest},
		{`{"email":"kees@example.com","role":"member","projectIds":[999]}`, http.StatusBadRequest},
	} {
		if rec := env.as(t, env.admin, "POST", "/ui/api/settings/invites", tt.body); rec.Code != tt.code {
			t.Errorf("POST invite %s = %d %s, want %d", tt.body, rec.Code, rec.Body, tt.code)
		}
	}

	// Revoke: the link stops working.
	rec = env.as(t, env.admin, "POST", "/ui/api/settings/invites", `{"email":"kees@example.com","role":"admin","projectIds":[]}`)
	kees := decode[struct {
		Invite struct {
			ID int64 `json:"id"`
		} `json:"invite"`
		Link string `json:"link"`
	}](t, rec.Body.Bytes())
	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/invites/%d", kees.Invite.ID), ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if rec := env.as(t, nil, "POST", "/ui/api/invite/check", fmt.Sprintf(`{"token":%q}`, tokenOf(t, kees.Link))); rec.Code != http.StatusNotFound {
		t.Errorf("revoked invite = %d, want 404", rec.Code)
	}
	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/invites/%d", kees.Invite.ID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("revoke twice = %d, want 404", rec.Code)
	}
}

func TestInvites_MailedWhenMailIsOn(t *testing.T) {
	mailer := &recordingMailer{}
	env := newTestEnv(t, withMailer(mailer))
	rec := env.as(t, env.admin, "POST", "/ui/api/settings/invites", `{"email":"piet@example.com","role":"member","projectIds":[]}`)
	got := decode[struct {
		Link   string `json:"link"`
		Mailed bool   `json:"mailed"`
	}](t, rec.Body.Bytes())
	sent := mailer.messages()
	if !got.Mailed || len(sent) != 1 || sent[0].To != "piet@example.com" || !strings.Contains(sent[0].Text, got.Link) {
		t.Errorf("mailed %v, mail %+v", got.Mailed, sent)
	}
}

func errorText(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	json.Unmarshal(body, &e)
	return e.Error
}

// TestInvites_GoneWithTheirAdmin: an admin who is removed (or made a
// member) has seen the links of their pending invites and could come back
// through one; those invites stop working.
func TestInvites_GoneWithTheirAdmin(t *testing.T) {
	env := newTestEnv(t)
	invite := func(by *session, email string) string {
		t.Helper()
		rec := env.as(t, by, "POST", "/ui/api/settings/invites", fmt.Sprintf(`{"email":%q,"role":"admin","projectIds":[]}`, email))
		if rec.Code != http.StatusCreated {
			t.Fatalf("invite = %d %s", rec.Code, rec.Body)
		}
		return tokenOf(t, decode[struct {
			Link string `json:"link"`
		}](t, rec.Body.Bytes()).Link)
	}
	accept := func(token string) int {
		return env.as(t, nil, "POST", "/ui/api/invite/accept", fmt.Sprintf(`{"token":%q,"password":"a good password"}`, token)).Code
	}

	removed := env.user(t, "removed@example.com", store.RoleAdmin)
	backDoor := invite(removed, "back-door@example.com")
	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/users/%d", removed.userID), ""); rec.Code != http.StatusOK {
		t.Fatalf("remove = %d %s", rec.Code, rec.Body)
	}
	if code := accept(backDoor); code != http.StatusNotFound && code != http.StatusGone {
		t.Errorf("accept the invite of a removed admin = %d, want 404 or 410", code)
	}

	demoted := env.user(t, "demoted@example.com", store.RoleAdmin)
	sideDoor := invite(demoted, "side-door@example.com")
	if rec := env.as(t, env.admin, "PATCH", fmt.Sprintf("/ui/api/settings/users/%d", demoted.userID), `{"role":"member"}`); rec.Code != http.StatusOK {
		t.Fatalf("demote = %d %s", rec.Code, rec.Body)
	}
	if code := accept(sideDoor); code != http.StatusNotFound && code != http.StatusGone {
		t.Errorf("accept the invite of a demoted admin = %d, want 404 or 410", code)
	}

	// Invites of admins who stay keep working.
	if code := accept(invite(env.admin, "welcome@example.com")); code != http.StatusOK {
		t.Errorf("accept the invite of an admin who stays = %d, want 200", code)
	}
	if list := decode[usersResponse](t, env.as(t, env.admin, "GET", "/ui/api/settings/users", "").Body.Bytes()); len(list.Invites) != 0 {
		t.Errorf("pending invites = %+v, want none", list.Invites)
	}
}

// An admin who is made a member while their invite request is on its way
// (after the sign-in check, before the invite is written) gets 404, as
// every admin route answers members, and no invite is left behind.
func TestInvites_AdminDemotedMeanwhile(t *testing.T) {
	env := newTestEnv(t)
	demoted := env.user(t, "demoted@example.com", store.RoleAdmin)
	member := store.RoleMember
	demoteFirst := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := env.db.UpdateUser(r.Context(), demoted.userID, store.UserChange{Role: &member}); err != nil {
			t.Error(err)
		}
		env.guarded.ServeHTTP(w, r)
	})
	env.handler = env.auth.RequireUser(demoteFirst)

	rec := env.as(t, demoted, "POST", "/ui/api/settings/invites", `{"email":"late@example.com","role":"admin","projectIds":[]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invite by an admin made a member meanwhile = %d %s, want 404", rec.Code, rec.Body)
	}
	invites, err := env.db.PendingInvites(context.Background(), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if len(invites) != 0 {
		t.Errorf("invites = %+v, want none", invites)
	}
}
