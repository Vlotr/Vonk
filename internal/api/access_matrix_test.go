package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

// TestAccessMatrix calls every route that shows or changes project data as
// an admin, as a member with access to the project and as a member without
// it (who sees another project). Without access a route answers 404, and
// lists and totals leave the project out completely: no name, no issue, no
// count.
//
// Every task that adds a project-scoped or admin-only route adds it here.
func TestAccessMatrix(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()

	// The project under test, with text found nowhere else.
	shop := env.project(t, "Secret Webshop")
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1), message: "secret-shop-message", url: "https://shop.example.com/secret-shop-path"}))
	// Another project the member without access does see.
	portal := env.project(t, "Portal")
	env.save(t, portal.ID, phpEvent(t, eventSpec{id: hexID(2), message: "portal message", function: "portal"}))
	// A project only admin-only routes touch.
	spare := env.project(t, "Spare")
	// The project tracks page views: the Pages screen and the overview's page columns have its numbers.
	if _, err := env.db.UpdateProject(ctx, shop.ID, store.ProjectSettings{Name: shop.Name, RetentionDays: shop.RetentionDays,
		TracksLaravel: true, TracksBrowser: true, TracksPageviews: true}); err != nil {
		t.Fatal(err)
	}
	env.visits(t, shop.ID, visit{user: "u1", route: "/secret-shop-page", at: testNow.Add(-time.Hour), stay: time.Minute})
	env.visits(t, portal.ID, visit{user: "u2", route: "/portal-page", at: testNow.Add(-time.Hour), stay: time.Minute})

	shopIssue := firstIssue(t, env, shop.ID)
	shopAlerts := store.DefaultAlertSettings()
	shopAlerts.SlackWebhookURL = "https://hooks.slack.com/services/T1/B1/shop-webhook-secret"
	if err := env.db.SaveAlertSettings(ctx, shop.ID, shopAlerts); err != nil {
		t.Fatal(err)
	}
	withAccess := env.member(t, "with@example.com", shop.ID)
	without := env.member(t, "without@example.com", portal.ID)
	invite, err := env.db.CreateInvite(ctx, store.NewInvite{Email: "invited@example.com", Role: store.RoleMember,
		TokenHash: "matrix-invite", CreatedBy: env.admin.userID, Now: testNow, ExpiresAt: testNow.AddDate(0, 0, 7)})
	if err != nil {
		t.Fatal(err)
	}

	// What the project's data looks like in an answer.
	leaks := []string{"Secret Webshop", "secret-shop", shop.SentryKey, "shop-webhook-secret"}

	type route struct {
		name, method, path, body string
		// list routes answer everyone with 200; seesShop tells whether the
		// answer includes the project. Other routes answer 404 without access.
		list     bool
		seesShop func(body []byte) bool
		// adminOnly routes answer 404 to every member, even with access.
		adminOnly bool
		// adminCode is what an admin gets; 0 skips the admin (the route
		// changes too much for this table; its own tests cover it).
		adminCode int
		// unchanged checks after a refused request that nothing changed.
		unchanged func(t *testing.T)
	}
	issueStatusIs := func(want string) func(t *testing.T) {
		return func(t *testing.T) {
			if got, _ := env.db.IssueByID(ctx, shopIssue); got.Status != want {
				t.Errorf("issue status = %q after a refused request, want %q", got.Status, want)
			}
		}
	}
	projectUnchanged := func(t *testing.T) {
		got, err := env.db.ProjectByID(ctx, shop.ID)
		if err != nil || got.Name != shop.Name || got.SentryKey != shop.SentryKey {
			t.Errorf("project after a refused request = %+v, %v", got, err)
		}
	}
	spareExists := func(t *testing.T) {
		if _, err := env.db.ProjectByID(ctx, spare.ID); err != nil {
			t.Errorf("spare project after a refused delete: %v", err)
		}
	}
	userIs := func(id int64, role string) func(t *testing.T) {
		return func(t *testing.T) {
			if u, err := env.db.UserByID(ctx, id); err != nil || u.Role != role {
				t.Errorf("user %d after a refused request = %+v, %v; want role %s", id, u, err, role)
			}
		}
	}
	projectCount := func(want int) func(t *testing.T) {
		return func(t *testing.T) {
			if ids, _ := env.db.ProjectIDs(ctx); len(ids) != want {
				t.Errorf("%d projects after a refused create, want %d", len(ids), want)
			}
		}
	}
	inviteCount := func(want int) func(t *testing.T) {
		return func(t *testing.T) {
			if pending, _ := env.db.PendingInvites(ctx, testNow); len(pending) != want {
				t.Errorf("%d pending invites after a refused request, want %d", len(pending), want)
			}
		}
	}

	alertsUnchanged := func(t *testing.T) { alertSettingsAre(t, env, shop.ID, shopAlerts) }
	noPostTo := func(webhook string) func(t *testing.T) {
		return func(t *testing.T) {
			for _, p := range env.slack.sent() {
				if p.webhook == webhook {
					t.Errorf("a refused request posted to %s", webhook)
				}
			}
		}
	}
	adminWebhookUnset := func(t *testing.T) {
		if v, _ := env.db.Setting(ctx, store.SettingAdminSlackWebhook); v != "" {
			t.Errorf("admin webhook after a refused request = %q", v)
		}
	}

	patternsUnchanged := func(t *testing.T) {
		if got, err := env.db.ProjectByID(ctx, shop.ID); err != nil || len(got.RoutePatterns) != 0 {
			t.Errorf("route patterns after a refused change = %v, %v", got.RoutePatterns, err)
		}
	}

	shopIssues := fmt.Sprintf("/ui/api/projects/%d/issues", shop.ID)
	shopPages := fmt.Sprintf("/ui/api/projects/%d/pages", shop.ID)
	shopAlertsPath := fmt.Sprintf("/ui/api/projects/%d/alerts", shop.ID)
	routes := []route{
		// Project data: everyone with access.
		{name: "projects", method: "GET", path: "/ui/api/projects", list: true, seesShop: func(b []byte) bool {
			var list []struct{ ID int64 }
			json.Unmarshal(b, &list)
			return containsID(len(list), func(i int) int64 { return list[i].ID }, shop.ID)
		}},
		{name: "overview aggregates", method: "GET", path: "/ui/api/overview", list: true, seesShop: func(b []byte) bool {
			var o struct{ Projects []struct{ ID int64 } }
			json.Unmarshal(b, &o)
			return containsID(len(o.Projects), func(i int) int64 { return o.Projects[i].ID }, shop.ID)
		}},
		{name: "newest issues", method: "GET", path: "/ui/api/overview", list: true, seesShop: func(b []byte) bool {
			var o struct {
				NewestIssues []struct{ ProjectID int64 }
			}
			json.Unmarshal(b, &o)
			return containsID(len(o.NewestIssues), func(i int) int64 { return o.NewestIssues[i].ProjectID }, shop.ID)
		}},
		{name: "issues list", method: "GET", path: shopIssues},
		{name: "issues list, filtered", method: "GET", path: shopIssues + "?status=open&source=laravel&sort=events&limit=5"},
		{name: "search", method: "GET", path: shopIssues + "?q=secret-shop-path"},
		{name: "issues list, by page", method: "GET", path: shopIssues + "?route=/secret-shop-path"},
		{name: "pages", method: "GET", path: shopPages},
		{name: "pages, 30 days", method: "GET", path: shopPages + "?range=30d&tz=Europe/Amsterdam"},
		{name: "issue detail", method: "GET", path: fmt.Sprintf("/ui/api/issues/%d", shopIssue)},
		{name: "issue status", method: "POST", path: fmt.Sprintf("/ui/api/issues/%d/status", shopIssue),
			body: `{"status":"ignored"}`, unchanged: issueStatusIs(store.IssueOpen)},
		// Everyone who sees the project reads its alerts; only admins change them (ruling 33).
		{name: "alerts", method: "GET", path: shopAlertsPath},
		// The same values again, so the rows after it see the same settings.
		{name: "alerts: change", method: "PATCH", path: shopAlertsPath,
			body: `{"alerts":{"newIssue":{"email":true}}}`, adminOnly: true, adminCode: 200, unchanged: alertsUnchanged},
		{name: "alerts: take over the webhook", method: "PATCH", path: shopAlertsPath,
			body: `{"slackWebhookUrl":"https://evil.example.com/hook"}`, adminOnly: true, unchanged: alertsUnchanged},
		{name: "alerts: Slack test", method: "POST", path: shopAlertsPath + "/slack-test",
			body: `{"webhookUrl":"https://hooks.slack.com/services/matrix-project"}`, adminOnly: true, adminCode: 200,
			unchanged: noPostTo("https://hooks.slack.com/services/matrix-project")},
		{name: "alerts: Slack test of the saved webhook", method: "POST", path: shopAlertsPath + "/slack-test",
			body: `{}`, adminOnly: true, adminCode: 200, unchanged: noPostTo(shopAlerts.SlackWebhookURL)},

		// Settings: admins only.
		{name: "settings: projects", method: "GET", path: "/ui/api/settings/projects", adminOnly: true, adminCode: 200},
		{name: "settings: one project", method: "GET", path: fmt.Sprintf("/ui/api/settings/projects/%d", shop.ID), adminOnly: true, adminCode: 200},
		{name: "settings: edit project", method: "PATCH", path: fmt.Sprintf("/ui/api/settings/projects/%d", shop.ID),
			body: `{"name":"Taken over"}`, adminOnly: true, unchanged: projectUnchanged},
		{name: "settings: route patterns", method: "PATCH", path: fmt.Sprintf("/ui/api/settings/projects/%d", shop.ID),
			body: `{"routePatterns":["/secret-shop-page/:x"]}`, adminOnly: true, unchanged: patternsUnchanged},
		{name: "settings: route preview", method: "POST", path: fmt.Sprintf("/ui/api/settings/projects/%d/route-preview", shop.ID),
			body: `{"routePatterns":["/secret-shop-page"]}`, adminOnly: true, adminCode: 200, unchanged: patternsUnchanged},
		{name: "settings: rotate keys", method: "POST", path: fmt.Sprintf("/ui/api/settings/projects/%d/rotate-keys", shop.ID),
			body: `{}`, adminOnly: true, unchanged: projectUnchanged},
		{name: "settings: delete project", method: "DELETE", path: fmt.Sprintf("/ui/api/settings/projects/%d", spare.ID),
			adminOnly: true, unchanged: spareExists},
		{name: "settings: create project", method: "POST", path: "/ui/api/settings/projects",
			body: `{"name":"Mine","retentionDays":90,"tracksLaravel":true}`, adminOnly: true, unchanged: projectCount(3)},
		{name: "settings: users", method: "GET", path: "/ui/api/settings/users", adminOnly: true, adminCode: 200},
		{name: "settings: invite", method: "POST", path: "/ui/api/settings/invites",
			body: `{"email":"friend@example.com","role":"admin","projectIds":[]}`, adminOnly: true, unchanged: inviteCount(1)},
		{name: "settings: revoke invite", method: "DELETE", path: fmt.Sprintf("/ui/api/settings/invites/%d", invite.ID),
			adminOnly: true, unchanged: inviteCount(1)},
		// A member making themselves admin is the escalation to rule out.
		{name: "settings: make yourself admin", method: "PATCH", path: fmt.Sprintf("/ui/api/settings/users/%d", without.userID),
			body: `{"role":"admin"}`, adminOnly: true, unchanged: userIs(without.userID, store.RoleMember)},
		{name: "settings: give yourself the project", method: "PATCH", path: fmt.Sprintf("/ui/api/settings/users/%d", without.userID),
			body: fmt.Sprintf(`{"projectIds":[%d,%d]}`, portal.ID, shop.ID), adminOnly: true, unchanged: func(t *testing.T) {
				if ids, _ := env.db.ProjectAccess(ctx, without.userID); len(ids) != 1 || ids[0] != portal.ID {
					t.Errorf("access after a refused change = %v, want [%d]", ids, portal.ID)
				}
			}},
		{name: "settings: remove the admin", method: "DELETE", path: fmt.Sprintf("/ui/api/settings/users/%d", env.admin.userID),
			adminOnly: true, unchanged: userIs(env.admin.userID, store.RoleAdmin)},
		{name: "settings: reset link for a member", method: "POST", path: fmt.Sprintf("/ui/api/settings/users/%d/reset-link", withAccess.userID),
			body: `{}`, adminOnly: true, adminCode: 200},
		{name: "settings: status", method: "GET", path: "/ui/api/settings/status", adminOnly: true, adminCode: 200},
		{name: "settings: admin Slack webhook", method: "PUT", path: "/ui/api/settings/admin-slack",
			body: `{"webhookUrl":"https://hooks.slack.com/services/matrix-admin"}`, adminOnly: true, adminCode: 200, unchanged: adminWebhookUnset},
		{name: "settings: admin Slack test", method: "POST", path: "/ui/api/settings/admin-slack/test",
			body: `{"webhookUrl":"https://hooks.slack.com/services/matrix-admin-test"}`, adminOnly: true, adminCode: 200,
			unchanged: noPostTo("https://hooks.slack.com/services/matrix-admin-test")},
	}

	users := []struct {
		name string
		who  *session
		// access: whether this user may see the shop project.
		access, admin bool
	}{
		// The member without access goes first, so a refused change is seen before anyone makes it.
		{"member without access", without, false, false},
		{"member with access", withAccess, true, false},
		{"admin", env.admin, true, true},
	}

	for _, rt := range routes {
		for _, u := range users {
			t.Run(rt.name+"/"+u.name, func(t *testing.T) {
				if u.admin && rt.adminOnly && rt.adminCode == 0 {
					t.Skip("changes too much for this table; covered by the route's own tests")
				}
				if _, err := env.db.SetIssueStatus(ctx, shopIssue, store.IssueOpen, testNow); err != nil {
					t.Fatal(err)
				}
				rec := env.as(t, u.who, rt.method, rt.path, rt.body)
				body := rec.Body.Bytes()

				allowed := u.access && (!rt.adminOnly || u.admin)
				switch {
				case rt.list:
					if rec.Code != http.StatusOK {
						t.Fatalf("%s %s = %d %s, want 200", rt.method, rt.path, rec.Code, body)
					}
					if got := rt.seesShop(body); got != allowed {
						t.Errorf("answer includes the project: %v, want %v\n%s", got, allowed, body)
					}
				case allowed:
					want := http.StatusOK
					if rt.adminCode != 0 {
						want = rt.adminCode
					}
					if rec.Code != want {
						t.Errorf("%s %s = %d %s, want %d", rt.method, rt.path, rec.Code, body, want)
					}
				default:
					if rec.Code != http.StatusNotFound {
						t.Errorf("%s %s = %d %s, want 404", rt.method, rt.path, rec.Code, body)
					}
					if rt.unchanged != nil {
						rt.unchanged(t)
					}
				}
				if !allowed {
					for _, leak := range leaks {
						if strings.Contains(string(body), leak) {
							t.Errorf("the answer leaks %q: %s", leak, body)
						}
					}
				}
			})
		}
	}
}

// TestAccessMatrix_NoProjects checks a member without any project: empty
// lists, zero totals and 404 everywhere else.
func TestAccessMatrix_NoProjects(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	env.save(t, shop.ID, phpEvent(t, eventSpec{id: hexID(1)}))
	nobody := env.member(t, "nobody@example.com")

	if rec := env.as(t, nobody, "GET", "/ui/api/projects", ""); rec.Body.String() != "[]\n" {
		t.Errorf("projects = %d %s, want []", rec.Code, rec.Body)
	}
	rec := env.as(t, nobody, "GET", "/ui/api/overview", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"projects":[]`) || !strings.Contains(rec.Body.String(), `"newestIssues":[]`) {
		t.Errorf("overview = %d %s, want no projects and no issues", rec.Code, rec.Body)
	}
	if rec := env.as(t, nobody, "GET", fmt.Sprintf("/ui/api/issues/%d", firstIssue(t, env, shop.ID)), ""); rec.Code != http.StatusNotFound {
		t.Errorf("issue detail = %d, want 404", rec.Code)
	}
}

// TestAccessMatrix_SignedOut: without a session every API route answers 401.
func TestAccessMatrix_SignedOut(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	for _, path := range []string{"/ui/api/projects", "/ui/api/overview", fmt.Sprintf("/ui/api/projects/%d/issues", shop.ID),
		fmt.Sprintf("/ui/api/projects/%d/pages", shop.ID),
		"/ui/api/settings/projects", "/ui/api/settings/users", "/ui/api/me"} {
		if rec := env.as(t, nil, "GET", path, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s signed out = %d, want 401", path, rec.Code)
		}
	}
}

func firstIssue(t *testing.T, env *testEnv, projectID int64) int64 {
	t.Helper()
	list, err := env.db.ListIssues(context.Background(), projectID)
	if err != nil || len(list) == 0 {
		t.Fatalf("issues of project %d: %v %v", projectID, list, err)
	}
	return list[0].ID
}

func containsID(n int, id func(int) int64, want int64) bool {
	for i := range n {
		if id(i) == want {
			return true
		}
	}
	return false
}
