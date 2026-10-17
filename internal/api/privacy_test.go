package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestDeleteUserDataAPI(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	shop := env.project(t, "Webshop")
	portal := env.project(t, "Portal")
	// Jan's newest error names him; Piet's older one stays.
	env.save(t, shop.ID,
		phpEvent(t, eventSpec{id: hexID(1), userID: "piet", message: "Checkout failed", at: testNow.Add(-2 * time.Hour)}),
		phpEvent(t, eventSpec{id: hexID(2), userID: "jan@example.com", message: "Checkout failed for jan@example.com", at: testNow.Add(-time.Hour)}))
	env.save(t, portal.ID, phpEvent(t, eventSpec{id: hexID(3), userID: "jan@example.com"}))
	env.visits(t, shop.ID, visit{user: "jan@example.com", route: "/cart", at: testNow.Add(-time.Hour), stay: time.Minute},
		visit{user: "piet", route: "/cart", at: testNow.Add(-time.Hour), stay: time.Minute})
	env.saveCustomEvents(t, customEvent(shop.ID, 1, "checkout_started", "jan@example.com", "/cart", testNow.Add(-time.Hour)))

	var logs bytes.Buffer
	env.api.log = slog.New(slog.NewTextHandler(&logs, nil))

	rec := env.as(t, env.admin, "POST", "/ui/api/privacy/delete-user",
		fmt.Sprintf(`{"userId":"jan@example.com","projectIds":[%d]}`, shop.ID))
	if rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	var got map[string]int64
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"projects": 1, "errorEvents": 1, "pageviews": 1, "pageleaves": 1, "customEvents": 1,
		"issuesDeleted": 0, "issuesUpdated": 1, "rows": 4}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %d, want %d (%s)", k, got[k], v, rec.Body)
		}
	}

	// The issue shows Piet's event now, not the text that named Jan.
	issue := firstIssue(t, env, shop.ID)
	detail := env.get(t, fmt.Sprintf("/ui/api/issues/%d", issue)).Body.String()
	if strings.Contains(detail, "jan@example.com") {
		t.Errorf("the issue still names the deleted user: %s", detail)
	}
	if list, _ := env.db.ListIssues(ctx, portal.ID); len(list) != 1 {
		t.Errorf("the portal was not chosen, but has %d issues", len(list))
	}

	// One audit line: who and how many rows, never the user id.
	line := logs.String()
	if strings.Count(line, "\n") != 1 || !strings.Contains(line, "level=INFO") ||
		!strings.Contains(line, fmt.Sprintf("admin=%d", env.admin.userID)) || !strings.Contains(line, "rows=4") {
		t.Errorf("audit log = %q, want one INFO line with the admin and the rows", line)
	}
	if strings.Contains(line, "jan") {
		t.Errorf("the log names the user id: %q", line)
	}

	// Every project when projectIds is left out.
	rec = env.as(t, env.admin, "POST", "/ui/api/privacy/delete-user", `{"userId":"jan@example.com"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"issuesDeleted":1`) || !strings.Contains(rec.Body.String(), `"projects":2`) {
		t.Errorf("every project = %d %s", rec.Code, rec.Body)
	}

	for _, tc := range []struct{ name, body, says string }{
		{"no user id", `{}`, "user id"},
		{"blank user id", `{"userId":"  "}`, "user id"},
		{"no projects", `{"userId":"jan","projectIds":[]}`, "project"},
		{"unknown project", `{"userId":"jan","projectIds":[999]}`, "project"},
	} {
		rec := env.as(t, env.admin, "POST", "/ui/api/privacy/delete-user", tc.body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), tc.says) {
			t.Errorf("%s: %d %s, want 400 saying %q", tc.name, rec.Code, rec.Body, tc.says)
		}
	}
}
