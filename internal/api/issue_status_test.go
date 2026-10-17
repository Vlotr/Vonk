package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"monitor/internal/store"
)

func statusPath(issueID int64) string {
	return fmt.Sprintf("/ui/api/issues/%d/status", issueID)
}

func TestStatus_SetThroughTheAPI(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1)}))
	issue, _ := env.db.ListIssues(context.Background(), p.ID)
	id := issue[0].ID

	rec := env.post(t, statusPath(id), "application/json", `{"status":"resolved"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST resolved = %d %s", rec.Code, rec.Body)
	}
	var got struct {
		ID        int64  `json:"id"`
		ProjectID int64  `json:"projectId"`
		Status    string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.ID != id || got.ProjectID != p.ID || got.Status != "resolved" {
		t.Errorf("body = %s (%v)", rec.Body, err)
	}
	if open := getJSON[issuesResponse](t, env, issuesPath(p.ID, "")); len(open.Issues) != 0 || open.Counts.Resolved != 1 {
		t.Errorf("open list = %+v, counts %+v; want empty and 1 resolved", open.Issues, open.Counts)
	}
	if d := getJSON[detailResponse](t, env, fmt.Sprintf("/ui/api/issues/%d", id)); d.Issue.Status != "resolved" {
		t.Errorf("detail status = %q", d.Issue.Status)
	}

	// A charset parameter is fine; open it again.
	if rec := env.post(t, statusPath(id), "application/json; charset=utf-8", `{"status":"open"}`); rec.Code != http.StatusOK {
		t.Errorf("POST open = %d %s", rec.Code, rec.Body)
	}
}

func TestStatus_InvalidValue400(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1)}))
	issue, _ := env.db.ListIssues(context.Background(), p.ID)
	id := issue[0].ID

	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		code        int
	}{
		{"unknown status", statusPath(id), "application/json", `{"status":"deleted"}`, http.StatusBadRequest},
		{"wrong case", statusPath(id), "application/json", `{"status":"Resolved"}`, http.StatusBadRequest},
		{"empty status", statusPath(id), "application/json", `{"status":""}`, http.StatusBadRequest},
		{"no status", statusPath(id), "application/json", `{}`, http.StatusBadRequest},
		{"status not a string", statusPath(id), "application/json", `{"status":1}`, http.StatusBadRequest},
		{"not JSON", statusPath(id), "application/json", `status=resolved`, http.StatusBadRequest},
		{"two objects", statusPath(id), "application/json", `{"status":"resolved"}{"status":"open"}`, http.StatusBadRequest},
		{"too large", statusPath(id), "application/json", `{"status":"resolved","x":"` + strings.Repeat("a", 2000) + `"}`, http.StatusBadRequest},
		// A form or text/plain POST is what another site can send without
		// asking the browser first; the API only takes JSON.
		{"form", statusPath(id), "application/x-www-form-urlencoded", `{"status":"resolved"}`, http.StatusUnsupportedMediaType},
		{"text", statusPath(id), "text/plain", `{"status":"resolved"}`, http.StatusUnsupportedMediaType},
		{"no content type", statusPath(id), "", `{"status":"resolved"}`, http.StatusUnsupportedMediaType},
		// The status is checked first: a bad request answers the same for any id.
		{"bad status, unknown issue", statusPath(999), "application/json", `{"status":"deleted"}`, http.StatusBadRequest},
		{"unknown issue", statusPath(999), "application/json", `{"status":"resolved"}`, http.StatusNotFound},
		{"id not a number", "/ui/api/issues/abc/status", "application/json", `{"status":"resolved"}`, http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := env.post(t, tt.path, tt.contentType, tt.body)
			if rec.Code != tt.code || !strings.HasPrefix(rec.Body.String(), `{"error":`) {
				t.Errorf("POST %s %s = %d %s, want %d with a JSON error", tt.path, tt.body, rec.Code, rec.Body, tt.code)
			}
		})
	}
	if got, _ := env.db.IssueByID(context.Background(), id); got.Status != store.IssueOpen {
		t.Errorf("status = %q after refused requests, want open", got.Status)
	}
}
