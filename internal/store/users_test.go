package store

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

var userTestNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

// addUser stores a user directly; the store does not check roles or hashes.
func addUser(t *testing.T, db *DB, email, role string, projectIDs ...int64) User {
	t.Helper()
	ctx := context.Background()
	hash := "hash-of-" + email
	var u User
	var err error
	if n, _ := db.CountUsers(ctx); n == 0 && role == RoleAdmin {
		u, err = db.CreateFirstAdmin(ctx, email, hash, userTestNow)
	} else {
		// Through an invite, the only other way users are made.
		_, err = db.CreateInvite(ctx, NewInvite{Email: email, Role: role, ProjectIDs: projectIDs,
			TokenHash: "invite-" + email, Now: userTestNow, ExpiresAt: userTestNow.Add(time.Hour)})
		if err == nil {
			u, err = db.AcceptInvite(ctx, "invite-"+email, hash, userTestNow)
		}
	}
	if err != nil {
		t.Fatalf("add user %s: %v", email, err)
	}
	return u
}

func addProject(t *testing.T, db *DB, name string) Project {
	t.Helper()
	p, err := db.CreateProject(context.Background(), NewProject{Name: name, TracksLaravel: true})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCreateFirstAdmin_OnlyOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	u, err := db.CreateFirstAdmin(ctx, "  Jan@Example.COM ", "hash", userTestNow)
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 || u.Email != "jan@example.com" || u.Role != RoleAdmin || !u.CreatedAt.Equal(userTestNow) {
		t.Errorf("first admin = %+v", u)
	}
	if _, err := db.CreateFirstAdmin(ctx, "other@example.com", "hash", userTestNow); !errors.Is(err, ErrUsersExist) {
		t.Errorf("second CreateFirstAdmin: err = %v, want ErrUsersExist", err)
	}
	if n, _ := db.CountUsers(ctx); n != 1 {
		t.Errorf("CountUsers = %d, want 1", n)
	}

	byEmail, err := db.UserByEmail(ctx, "JAN@example.com")
	if err != nil || !reflect.DeepEqual(byEmail, u) {
		t.Errorf("UserByEmail = %+v, %v; want %+v", byEmail, err, u)
	}
	if _, err := db.UserByEmail(ctx, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown email: err = %v, want ErrNotFound", err)
	}
}

func TestSessions_CreateFindExtendDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	u := addUser(t, db, "jan@example.com", RoleAdmin)

	s, err := db.CreateSession(ctx, u.ID, "token-hash", "csrf", userTestNow, userTestNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	got, user, err := db.SessionByTokenHash(ctx, "token-hash")
	if err != nil {
		t.Fatal(err)
	}
	if got != s || user.ID != u.ID || user.Email != u.Email {
		t.Errorf("SessionByTokenHash = %+v %+v, want %+v for %+v", got, user, s, u)
	}
	if _, _, err := db.SessionByTokenHash(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: err = %v, want ErrNotFound", err)
	}

	later := userTestNow.Add(48 * time.Hour)
	if err := db.ExtendSession(ctx, s.ID, later); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := db.SessionByTokenHash(ctx, "token-hash"); !got.ExpiresAt.Equal(later) {
		t.Errorf("ExpiresAt after extend = %v, want %v", got.ExpiresAt, later)
	}

	// Expired sessions are cleaned up; live ones stay.
	if _, err := db.CreateSession(ctx, u.ID, "old", "csrf", userTestNow, userTestNow.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, err := db.DeleteExpiredSessions(ctx, userTestNow.Add(time.Hour)); err != nil || n != 1 {
		t.Errorf("DeleteExpiredSessions = %d, %v; want 1", n, err)
	}
	if err := db.DeleteSession(ctx, s.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SessionByTokenHash(ctx, "token-hash"); !errors.Is(err, ErrNotFound) {
		t.Errorf("after delete: err = %v, want ErrNotFound", err)
	}
}

func TestInvites_AcceptOnceWithRoleAndAccess(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := addUser(t, db, "admin@example.com", RoleAdmin)
	shop, portal, tools := addProject(t, db, "Webshop"), addProject(t, db, "Portal"), addProject(t, db, "Tools")

	inv, err := db.CreateInvite(ctx, NewInvite{
		Email: " Piet@Example.com", Role: RoleMember, ProjectIDs: []int64{portal.ID, shop.ID, tools.ID},
		TokenHash: "invite-hash", CreatedBy: admin.ID, Now: userTestNow, ExpiresAt: userTestNow.Add(7 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Email != "piet@example.com" || inv.Role != RoleMember || inv.CreatedBy != admin.ID || inv.UsedAt != nil {
		t.Errorf("invite = %+v", inv)
	}
	if want := []int64{shop.ID, portal.ID, tools.ID}; !reflect.DeepEqual(inv.ProjectIDs, want) {
		t.Errorf("ProjectIDs = %v, want %v (sorted)", inv.ProjectIDs, want)
	}
	pending, err := db.PendingInvites(ctx, userTestNow)
	if err != nil || len(pending) != 1 || pending[0].ID != inv.ID {
		t.Errorf("PendingInvites = %+v, %v", pending, err)
	}

	// A project deleted before the invite is accepted is not given.
	if err := db.DeleteProject(ctx, tools.ID); err != nil {
		t.Fatal(err)
	}
	u, err := db.AcceptInvite(ctx, "invite-hash", "member-hash", userTestNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "piet@example.com" || u.Role != RoleMember || u.PasswordHash != "member-hash" {
		t.Errorf("accepted user = %+v", u)
	}
	access, err := db.ProjectAccess(ctx, u.ID)
	if err != nil || !reflect.DeepEqual(access, []int64{shop.ID, portal.ID}) {
		t.Errorf("ProjectAccess = %v, %v; want [%d %d]", access, err, shop.ID, portal.ID)
	}

	if _, err := db.AcceptInvite(ctx, "invite-hash", "again", userTestNow.Add(time.Hour)); !errors.Is(err, ErrGone) {
		t.Errorf("second accept: err = %v, want ErrGone", err)
	}
	if _, err := db.AcceptInvite(ctx, "unknown", "x", userTestNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown token: err = %v, want ErrNotFound", err)
	}
	if pending, _ := db.PendingInvites(ctx, userTestNow); len(pending) != 0 {
		t.Errorf("PendingInvites after accept = %+v, want none", pending)
	}
}

func TestInvites_ExpiredTakenAndReplaced(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	addUser(t, db, "admin@example.com", RoleAdmin)
	shop := addProject(t, db, "Webshop")
	week := 7 * 24 * time.Hour

	newInvite := func(email, hash string, projects ...int64) (Invite, error) {
		return db.CreateInvite(ctx, NewInvite{Email: email, Role: RoleMember, ProjectIDs: projects,
			TokenHash: hash, Now: userTestNow, ExpiresAt: userTestNow.Add(week)})
	}

	if _, err := newInvite("ADMIN@example.com", "h0"); !errors.Is(err, ErrEmailTaken) {
		t.Errorf("invite for an existing account: err = %v, want ErrEmailTaken", err)
	}
	if _, err := newInvite("new@example.com", "h0", 999); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("invite with an unknown project: err = %v, want ErrUnknownProject", err)
	}

	first, err := newInvite("piet@example.com", "h1", shop.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A second invite for the same address replaces the first.
	second, err := newInvite("piet@example.com", "h2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.InviteByTokenHash(ctx, "h1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("first invite after a second one: err = %v, want ErrNotFound", err)
	}
	// The first invite's projects went with it (row ids can be reused, so compare them, not ids).
	if got, err := db.InviteByTokenHash(ctx, "h2"); err != nil || got.ID != second.ID || len(got.ProjectIDs) != 0 || len(first.ProjectIDs) != 1 {
		t.Errorf("InviteByTokenHash = %+v, %v", got, err)
	}

	// Seven days later it no longer works.
	if _, err := db.AcceptInvite(ctx, "h2", "x", userTestNow.Add(week)); !errors.Is(err, ErrGone) {
		t.Errorf("expired accept: err = %v, want ErrGone", err)
	}
	if pending, _ := db.PendingInvites(ctx, userTestNow.Add(week)); len(pending) != 0 {
		t.Errorf("PendingInvites after expiry = %+v, want none", pending)
	}

	// A revoked invite no longer works.
	third, err := newInvite("kees@example.com", "h3")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteInvite(ctx, third.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptInvite(ctx, "h3", "x", userTestNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked invite: err = %v, want ErrNotFound", err)
	}
	if err := db.DeleteInvite(ctx, third.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("DeleteInvite twice: err = %v, want ErrNotFound", err)
	}
}

func TestPasswordReset_OnceAndRevokesSessions(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	u := addUser(t, db, "jan@example.com", RoleAdmin)
	for _, token := range []string{"s1", "s2"} {
		if _, err := db.CreateSession(ctx, u.ID, token, "csrf", userTestNow, userTestNow.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
	}

	if err := db.CreatePasswordReset(ctx, u.ID, "r1", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A newer link replaces the older one.
	if err := db.CreatePasswordReset(ctx, u.ID, "r2", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PasswordResetByTokenHash(ctx, "r1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("replaced link: err = %v, want ErrNotFound", err)
	}
	reset, user, err := db.PasswordResetByTokenHash(ctx, "r2")
	if err != nil || user.ID != u.ID || reset.UsedAt != nil || !reset.ExpiresAt.Equal(userTestNow.Add(time.Hour)) {
		t.Errorf("PasswordResetByTokenHash = %+v %+v %v", reset, user, err)
	}

	got, err := db.UsePasswordReset(ctx, "r2", "new-hash", userTestNow.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if got.PasswordHash != "new-hash" {
		t.Errorf("password hash = %q, want new-hash", got.PasswordHash)
	}
	for _, token := range []string{"s1", "s2"} {
		if _, _, err := db.SessionByTokenHash(ctx, token); !errors.Is(err, ErrNotFound) {
			t.Errorf("session %s after reset: err = %v, want ErrNotFound", token, err)
		}
	}
	if _, err := db.UsePasswordReset(ctx, "r2", "again", userTestNow.Add(time.Minute)); !errors.Is(err, ErrGone) {
		t.Errorf("second use: err = %v, want ErrGone", err)
	}

	if err := db.CreatePasswordReset(ctx, u.ID, "r3", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UsePasswordReset(ctx, "r3", "late", userTestNow.Add(time.Hour)); !errors.Is(err, ErrGone) {
		t.Errorf("expired link: err = %v, want ErrGone", err)
	}
	if u, _ := db.UserByID(ctx, u.ID); u.PasswordHash != "new-hash" {
		t.Errorf("password hash after refused uses = %q, want new-hash", u.PasswordHash)
	}
}

func TestChangePassword_KeepsOnlyTheCurrentSession(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	u := addUser(t, db, "jan@example.com", RoleAdmin)
	current, err := db.CreateSession(ctx, u.ID, "current", "csrf", userTestNow, userTestNow.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, u.ID, "other", "csrf", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	if err := db.ChangePassword(ctx, u.ID, "new-hash", current.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.UserByID(ctx, u.ID); got.PasswordHash != "new-hash" {
		t.Errorf("hash = %q", got.PasswordHash)
	}
	if _, _, err := db.SessionByTokenHash(ctx, "current"); err != nil {
		t.Errorf("current session: %v, want kept", err)
	}
	if _, _, err := db.SessionByTokenHash(ctx, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("other session: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateUser_RoleAndAccess(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := addUser(t, db, "admin@example.com", RoleAdmin)
	shop, portal := addProject(t, db, "Webshop"), addProject(t, db, "Portal")
	member := addUser(t, db, "piet@example.com", RoleMember, shop.ID)

	ids := []int64{portal.ID}
	if _, err := db.UpdateUser(ctx, member.ID, UserChange{ProjectIDs: &ids}); err != nil {
		t.Fatal(err)
	}
	if access, _ := db.ProjectAccess(ctx, member.ID); !reflect.DeepEqual(access, []int64{portal.ID}) {
		t.Errorf("access = %v, want [%d]", access, portal.ID)
	}
	unknown := []int64{999}
	if _, err := db.UpdateUser(ctx, member.ID, UserChange{ProjectIDs: &unknown}); !errors.Is(err, ErrUnknownProject) {
		t.Errorf("unknown project: err = %v, want ErrUnknownProject", err)
	}

	// Promoting drops the access rows: admins see everything.
	role := RoleAdmin
	promoted, err := db.UpdateUser(ctx, member.ID, UserChange{Role: &role})
	if err != nil || promoted.Role != RoleAdmin {
		t.Fatalf("promote = %+v, %v", promoted, err)
	}
	if access, _ := db.ProjectAccess(ctx, member.ID); len(access) != 0 {
		t.Errorf("access after promotion = %v, want none", access)
	}
	all, err := db.ProjectAccessByUser(ctx)
	if err != nil || len(all) != 0 {
		t.Errorf("ProjectAccessByUser = %v, %v; want empty", all, err)
	}

	// Two admins: one may step down, the last one may not.
	member2 := RoleMember
	if _, err := db.UpdateUser(ctx, admin.ID, UserChange{Role: &member2}); err != nil {
		t.Fatalf("demote one of two admins: %v", err)
	}
	if _, err := db.UpdateUser(ctx, member.ID, UserChange{Role: &member2}); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("demote the last admin: err = %v, want ErrLastAdmin", err)
	}
	if err := db.DeleteUser(ctx, member.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("delete the last admin: err = %v, want ErrLastAdmin", err)
	}
	if _, err := db.UpdateUser(ctx, 999, UserChange{Role: &member2}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user: err = %v, want ErrNotFound", err)
	}

	// Removing a member removes their sessions too.
	if _, err := db.CreateSession(ctx, admin.ID, "s", "csrf", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteUser(ctx, admin.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.SessionByTokenHash(ctx, "s"); !errors.Is(err, ErrNotFound) {
		t.Errorf("session of a removed user: err = %v, want ErrNotFound", err)
	}
	users, err := db.ListUsers(ctx)
	if err != nil || len(users) != 1 || users[0].ID != member.ID {
		t.Errorf("ListUsers = %+v, %v", users, err)
	}
}

func TestProjects_UpdateRotateDelete(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	member := addUser(t, db, "piet@example.com", RoleMember, p.ID)

	days := 30
	updated, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: " Web shop ", RetentionDays: &days, TracksBrowser: true, TracksPageviews: true})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "Web shop" || updated.Slug != p.Slug || *updated.RetentionDays != 30 ||
		updated.TracksLaravel || !updated.TracksBrowser || !updated.TracksPageviews || updated.SentryKey != p.SentryKey {
		t.Errorf("updated = %+v", updated)
	}
	forever, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "Web shop"})
	if err != nil || forever.RetentionDays != nil {
		t.Errorf("keep forever = %+v, %v", forever, err)
	}
	if _, err := db.UpdateProject(ctx, p.ID, ProjectSettings{Name: "  "}); err == nil {
		t.Error("blank name: want error")
	}
	if _, err := db.UpdateProject(ctx, 999, ProjectSettings{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown project: err = %v, want ErrNotFound", err)
	}

	rotated, err := db.RotateProjectKeys(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.SentryKey == p.SentryKey || rotated.PosthogToken == p.PosthogToken {
		t.Error("keys did not change")
	}
	if _, err := db.ProjectBySentryKey(ctx, p.SentryKey); !errors.Is(err, ErrNotFound) {
		t.Errorf("old key after rotation: err = %v, want ErrNotFound", err)
	}

	if err := db.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ProjectByID(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted project: err = %v", err)
	}
	if access, _ := db.ProjectAccess(ctx, member.ID); len(access) != 0 {
		t.Errorf("access to a deleted project = %v, want none", access)
	}
	if err := db.DeleteProject(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete twice: err = %v, want ErrNotFound", err)
	}
	if ids, err := db.ProjectIDs(ctx); err != nil || len(ids) != 0 {
		t.Errorf("ProjectIDs = %v, %v", ids, err)
	}
}

// TestInvites_GoWithTheAdminWhoMadeThem: an admin who is removed or made a
// member has seen the links of their invites; those that were not used
// stop working, or the admin could come back through one.
func TestInvites_GoWithTheAdminWhoMadeThem(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	first := addUser(t, db, "first@example.com", RoleAdmin)
	a := addUser(t, db, "a@example.com", RoleAdmin)
	b := addUser(t, db, "b@example.com", RoleAdmin)
	invite := func(by User, email, hash string) {
		t.Helper()
		_, err := db.CreateInvite(ctx, NewInvite{Email: email, Role: RoleAdmin, TokenHash: hash, CreatedBy: by.ID,
			Now: userTestNow, ExpiresAt: userTestNow.Add(7 * 24 * time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
	}
	invite(a, "back-door@example.com", "by-a")
	invite(b, "other-door@example.com", "by-b")
	invite(first, "kept@example.com", "by-first")

	// A is removed: A's invite goes, the others stay.
	if err := db.DeleteUser(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptInvite(ctx, "by-a", "hash", userTestNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite of a removed admin: err = %v, want ErrNotFound", err)
	}

	// B becomes a member: B's invite goes too.
	member := RoleMember
	if _, err := db.UpdateUser(ctx, b.ID, UserChange{Role: &member}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AcceptInvite(ctx, "by-b", "hash", userTestNow); !errors.Is(err, ErrNotFound) {
		t.Errorf("invite of a demoted admin: err = %v, want ErrNotFound", err)
	}

	if _, err := db.AcceptInvite(ctx, "by-first", "hash", userTestNow); err != nil {
		t.Errorf("invite of an admin who stays: %v", err)
	}
}
