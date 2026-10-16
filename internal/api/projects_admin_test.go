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
	RoutePatterns   []string  `json:"routePatterns"`
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

// Route patterns are set on Edit project, one per line; blank lines go.
func TestSettingsProjects_RoutePatterns(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	path := fmt.Sprintf("/ui/api/settings/projects/%d", p.ID)

	if got := decode[settingsProjectResponse](t, env.get(t, path).Body.Bytes()); got.RoutePatterns == nil || len(got.RoutePatterns) != 0 {
		t.Errorf("a new project's patterns = %#v, want []", got.RoutePatterns)
	}
	rec := env.as(t, env.admin, "PATCH", path, `{"routePatterns":[" /products/:slug ","","  ","/blog/:year/:post"]}`)
	got := decode[settingsProjectResponse](t, rec.Body.Bytes())
	if rec.Code != http.StatusOK || fmt.Sprint(got.RoutePatterns) != "[/products/:slug /blog/:year/:post]" || got.Name != "Webshop" {
		t.Errorf("PATCH patterns = %d %+v", rec.Code, got)
	}
	list := decode[[]settingsProjectResponse](t, env.get(t, "/ui/api/settings/projects").Body.Bytes())
	if len(list) != 1 || fmt.Sprint(list[0].RoutePatterns) != "[/products/:slug /blog/:year/:post]" {
		t.Errorf("list = %+v, want the patterns for the Edit form", list)
	}
	// Other changes leave the patterns alone.
	rec = env.as(t, env.admin, "PATCH", path, `{"name":"Web shop"}`)
	if got := decode[settingsProjectResponse](t, rec.Body.Bytes()); len(got.RoutePatterns) != 2 {
		t.Errorf("patterns after a rename = %v", got.RoutePatterns)
	}

	tooMany := make([]string, 101)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%q", fmt.Sprintf("/p%d/:id", i))
	}
	for _, body := range []string{
		`{"routePatterns":"/products/:slug"}`,
		`{"routePatterns":[` + strings.Join(tooMany, ",") + `]}`,
		`{"routePatterns":["/` + strings.Repeat("x", 300) + `"]}`,
		`{"name":"","routePatterns":["/a/:b"]}`,
	} {
		rec := env.as(t, env.admin, "PATCH", path, body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("PATCH %.60s = %d, want 400", body, rec.Code)
		}
	}
	if got := decode[settingsProjectResponse](t, env.get(t, path).Body.Bytes()); got.Name != "Web shop" || len(got.RoutePatterns) != 2 {
		t.Errorf("after refused changes = %+v", got)
	}

	// 300 characters, as the message says, also when they take more bytes.
	rec = env.as(t, env.admin, "PATCH", path, `{"routePatterns":["/`+strings.Repeat("é", 299)+`"]}`)
	if rec.Code != http.StatusOK {
		t.Errorf("a pattern of 300 characters = %d %s, want 200", rec.Code, rec.Body)
	}
	rec = env.as(t, env.admin, "PATCH", path, `{"routePatterns":["/`+strings.Repeat("é", 300)+`"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most 300 characters") {
		t.Errorf("a pattern of 301 characters = %d %s, want 400", rec.Code, rec.Body)
	}
}

// Route patterns are set on Edit project; a new project refuses them
// rather than dropping them without a word.
func TestSettingsProjects_CreateRefusesRoutePatterns(t *testing.T) {
	env := newTestEnv(t)
	rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects", `{"name":"Webshop","routePatterns":["/products/:slug"]}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Edit project") {
		t.Errorf("create with patterns = %d %s, want 400 naming Edit project", rec.Code, rec.Body)
	}
	if ids, _ := env.db.ProjectIDs(context.Background()); len(ids) != 0 {
		t.Errorf("%d projects after a refused create", len(ids))
	}
	// The body limit of create is the settings' 16 KiB again.
	big := `{"name":"Webshop","x":"` + strings.Repeat("a", 20<<10) + `"}`
	if rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects", big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("create with a 20 KiB body = %d, want 413", rec.Code)
	}
}

type routePreviewResponse struct {
	Paths []struct {
		Path  string `json:"path"`
		Route string `json:"route"`
	} `json:"paths"`
}

// The preview shows recent paths (page views and error URLs) with the
// route each gets under the patterns typed, without saving them.
func TestSettingsProjects_RoutePreview(t *testing.T) {
	env := newTestEnv(t)
	p := env.pageProject(t, "Webshop")
	ctx := context.Background()
	const token = "4f8c2a9e7b1d3f5a6c8e0b2d4f6a8c0e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a"
	var events []store.PageEvent
	paths := []string{"/products/blue-shoes", "/products/red-shoes", "/products/green-shoes", "/products/black-boots",
		"/orders/1234", "/", "/reset-password/" + token /* stored before ruling 38 */, "/products/blue-shoes"}
	for i, path := range paths {
		events = append(events, store.PageEvent{ProjectID: p.ID, PageviewID: fmt.Sprint(i), SessionID: fmt.Sprint(i),
			Route: "/x", Path: path, At: testNow.Add(-time.Duration(len(paths)-i) * time.Minute)})
	}
	if _, err := env.db.SavePageEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	env.save(t, p.ID, phpEvent(t, eventSpec{id: hexID(1), url: "https://shop.example.com/checkout/payment?step=2", at: testNow.Add(-time.Hour)}))

	preview := func(patterns string) map[string]string {
		t.Helper()
		rec := env.as(t, env.admin, "POST", fmt.Sprintf("/ui/api/settings/projects/%d/route-preview", p.ID), `{"routePatterns":`+patterns+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("preview = %d %s", rec.Code, rec.Body)
		}
		got := map[string]string{}
		for _, pr := range decode[routePreviewResponse](t, rec.Body.Bytes()).Paths {
			got[pr.Path] = pr.Route
		}
		return got
	}
	got := preview(`["/products/:slug", "/checkout/:step"]`)
	want := map[string]string{
		// At most three paths per first segment, the most recent first.
		"/products/blue-shoes": "/products/:slug", "/products/black-boots": "/products/:slug", "/products/green-shoes": "/products/:slug",
		"/orders/1234": "/orders/:id", "/": "/",
		"/reset-password/:hash": "/reset-password/:hash",
		"/checkout/payment":     "/checkout/:step",
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("preview =\n%v\nwant\n%v", got, want)
	}
	// Without patterns, the automatic rules alone. Nothing was saved.
	if got := preview(`[]`); got["/products/blue-shoes"] != "/products/blue-shoes" {
		t.Errorf("without patterns = %v", got)
	}
	if saved, _ := env.db.ProjectByID(ctx, p.ID); len(saved.RoutePatterns) != 0 {
		t.Errorf("the preview saved %v", saved.RoutePatterns)
	}
	if rec := env.as(t, env.admin, "POST", "/ui/api/settings/projects/999/route-preview", `{"routePatterns":[]}`); rec.Code != http.StatusNotFound {
		t.Errorf("unknown project = %d, want 404", rec.Code)
	}
}
