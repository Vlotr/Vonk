package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

type settingsProjectResponse struct {
	ID              int64     `json:"id"`
	Name            string    `json:"name"`
	CreatedAt       time.Time `json:"createdAt"`
	RetentionDays   *int      `json:"retentionDays"`
	TracksLaravel   bool      `json:"tracksLaravel"`
	TracksBrowser   bool      `json:"tracksBrowser"`
	TracksPageviews bool      `json:"tracksPageviews"`
	DSN             string    `json:"dsn"`
	PosthogToken    string    `json:"posthogToken"`
	PosthogHost     string    `json:"posthogHost"`
}

func days(p *int) string {
	if p == nil {
		return "indefinitely"
	}
	return fmt.Sprint(*p)
}

func TestSettingsProjects_CreateAndList(t *testing.T) {
	env := newTestEnv(t)

	rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects",
		`{"name":"  Internal tools ","retentionDays":null,"tracksLaravel":true,"tracksBrowser":false,"tracksPageviews":false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	created := decode[settingsProjectResponse](t, rec.Body.Bytes())
	if created.Name != "Internal tools" || created.RetentionDays != nil || !created.TracksLaravel || created.TracksBrowser {
		t.Errorf("created = %+v", created)
	}
	// The answer has what the setup page needs.
	if !strings.HasPrefix(created.DSN, "https://") || !strings.HasSuffix(created.DSN, fmt.Sprintf("@monitor.example.com/%d", created.ID)) ||
		!strings.HasPrefix(created.PosthogToken, "phc_") || created.PosthogHost != "https://monitor.example.com" {
		t.Errorf("keys = %q %q %q", created.DSN, created.PosthogToken, created.PosthogHost)
	}

	// Retention left out: 90 days.
	rec = env.as(t, env.admin, "POST", "/ui/api/settings/projects", `{"name":"Webshop","tracksBrowser":true}`)
	if got := decode[settingsProjectResponse](t, rec.Body.Bytes()); rec.Code != http.StatusCreated || days(got.RetentionDays) != "90" {
		t.Errorf("default retention = %d %s", rec.Code, rec.Body)
	}
	for _, d := range []int{30, 180, 365} {
		rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects", fmt.Sprintf(`{"name":"P%d","retentionDays":%d}`, d, d))
		if got := decode[settingsProjectResponse](t, rec.Body.Bytes()); rec.Code != http.StatusCreated || days(got.RetentionDays) != fmt.Sprint(d) {
			t.Errorf("retention %d = %d %s", d, rec.Code, rec.Body)
		}
	}
	for _, body := range []string{
		`{"name":"","retentionDays":90}`,
		`{"name":"   "}`,
		`{"retentionDays":90}`,
		`{"name":"Odd","retentionDays":45}`,
		`{"name":"Odd","retentionDays":"90"}`,
		fmt.Sprintf(`{"name":%q}`, strings.Repeat("x", 101)),
	} {
		if rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects", body); rec.Code != http.StatusBadRequest {
			t.Errorf("create %s = %d %s, want 400", body, rec.Code, rec.Body)
		}
	}

	rec = env.as(t, env.admin, "GET", "/ui/api/settings/projects", "")
	list := decode[[]settingsProjectResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || len(list) != 5 || list[0].Name != "Internal tools" || list[0].DSN != "" {
		t.Errorf("list = %d %+v, want 5 projects by name, without keys", rec.Code, list)
	}
	if strings.Contains(rec.Body.String(), created.PosthogToken) {
		t.Error("the list carries keys; only the setup page needs them")
	}
}

func TestSettingsProjects_EditRotateDelete(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	p := env.project(t, "Webshop")
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1)}))
	path := fmt.Sprintf("/ui/api/settings/projects/%d", p.ID)

	got := decode[settingsProjectResponse](t, env.as(t, env.admin, "GET", path, "").Body.Bytes())
	if got.Name != "Webshop" || !strings.Contains(got.DSN, p.SentryKey) || got.PosthogToken != p.PosthogToken {
		t.Errorf("GET = %+v", got)
	}

	// Only what is sent changes; null keeps data indefinitely.
	rec := env.as(t, env.admin, "PATCH", path, `{"retentionDays":null,"tracksPageviews":true}`)
	got = decode[settingsProjectResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.Name != "Webshop" || got.RetentionDays != nil || !got.TracksPageviews || !got.TracksLaravel || !got.TracksBrowser {
		t.Errorf("PATCH retention and page views = %d %+v", rec.Code, got)
	}
	rec = env.as(t, env.admin, "PATCH", path, `{"name":"Web shop","retentionDays":30,"tracksBrowser":false}`)
	got = decode[settingsProjectResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || got.Name != "Web shop" || days(got.RetentionDays) != "30" || got.TracksBrowser || !got.TracksPageviews {
		t.Errorf("PATCH name, retention, browser = %d %+v", rec.Code, got)
	}
	for _, body := range []string{`{"name":""}`, `{"retentionDays":7}`, `{"tracksLaravel":"yes"}`} {
		if rec := env.as(t, env.admin, "PATCH", path, body); rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d, want 400", body, rec.Code)
		}
	}
	if rec := env.as(t, env.admin, "PATCH", "/ui/api/settings/projects/999", `{"name":"x"}`); rec.Code != http.StatusNotFound {
		t.Errorf("PATCH unknown = %d, want 404", rec.Code)
	}

	// Rotate: new keys, and the old DSN key no longer finds the project.
	rec = env.as(t, env.admin, "POST", path+"/rotate-keys", `{}`)
	rotated := decode[settingsProjectResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || strings.Contains(rotated.DSN, p.SentryKey) || rotated.PosthogToken == p.PosthogToken {
		t.Errorf("rotate = %d %+v", rec.Code, rotated)
	}
	if _, err := env.db.ProjectBySentryKey(ctx, p.SentryKey); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("old key after rotation: %v", err)
	}

	// Delete: the project and its issues are gone.
	if rec := env.as(t, env.admin, "DELETE", path, ""); rec.Code != http.StatusOK {
		t.Fatalf("DELETE = %d %s", rec.Code, rec.Body)
	}
	if rec := env.as(t, env.admin, "GET", path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", rec.Code)
	}
	if issues, _ := env.db.ListIssues(ctx, p.ID); len(issues) != 0 {
		t.Errorf("issues after delete = %d, want 0", len(issues))
	}
	if rec := env.as(t, env.admin, "DELETE", path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("DELETE twice = %d, want 404", rec.Code)
	}
}
