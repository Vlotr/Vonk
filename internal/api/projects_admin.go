package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"monitor/internal/httpjson"
	"monitor/internal/project"
	"monitor/internal/routes"
	"monitor/internal/store"
)

// Settings → Projects, for admins:
//
//	GET    /ui/api/settings/projects                  every project with its settings
//	POST   /ui/api/settings/projects                  create: {name, retentionDays, tracksLaravel, tracksBrowser, tracksPageviews}
//	GET    /ui/api/settings/projects/{id}             one project with its DSN and PostHog token
//	PATCH  /ui/api/settings/projects/{id}             change any of the create fields
//	POST   /ui/api/settings/projects/{id}/rotate-keys new DSN key and PostHog token
//	DELETE /ui/api/settings/projects/{id}             the project and all its data
//	POST   /ui/api/settings/projects/{id}/route-preview  recent paths and their routes under {routePatterns}
//
// retentionDays is 30, 90, 180 or 365, or null to keep data indefinitely.
// PATCH also takes routePatterns, such as ["/products/:slug"]; they apply
// to data that comes in from then on (ruling 14).

// maxBodySettings caps the JSON bodies of the settings routes.
const maxBodySettings = 16 << 10

// maxBodyProject caps the bodies that carry route patterns: up to 100 of
// 300 characters, which take up to 4 bytes each.
const maxBodyProject = 128 << 10

// previewPaths is how many recent paths the route preview shows at most,
// and previewPerSection how many of them start with the same segment, so
// one busy section (/products/...) leaves room for the others.
const (
	previewPaths      = 20
	previewPerSection = 3
)

// maxProjectName is the longest project name, in characters.
const maxProjectName = 100

// retentionChoices are the "Keep data for" options besides Indefinitely.
var retentionChoices = map[int]bool{30: true, 90: true, 180: true, 365: true}

// settingsProjectJSON is a row of "Your projects".
type settingsProjectJSON struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	// RetentionDays is null when data is kept indefinitely.
	RetentionDays   *int `json:"retentionDays"`
	TracksLaravel   bool `json:"tracksLaravel"`
	TracksBrowser   bool `json:"tracksBrowser"`
	TracksPageviews bool `json:"tracksPageviews"`
	// RoutePatterns turn paths into routes before the automatic rules.
	RoutePatterns []string `json:"routePatterns"`
}

// projectKeysJSON is a project with what its "Setup and keys" page shows.
type projectKeysJSON struct {
	settingsProjectJSON
	DSN          string `json:"dsn"`
	PosthogToken string `json:"posthogToken"`
	// PosthogHost is where the PostHog SDK sends its data: BASE_URL.
	PosthogHost string `json:"posthogHost"`
}

func toSettingsProject(p store.Project) settingsProjectJSON {
	return settingsProjectJSON{
		ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, RetentionDays: p.RetentionDays,
		TracksLaravel: p.TracksLaravel, TracksBrowser: p.TracksBrowser, TracksPageviews: p.TracksPageviews,
		RoutePatterns: p.RoutePatterns,
	}
}

func (h *Handler) toProjectKeys(p store.Project) projectKeysJSON {
	return projectKeysJSON{
		settingsProjectJSON: toSettingsProject(p),
		DSN:                 project.DSN(h.baseURL, p),
		PosthogToken:        p.PosthogToken,
		PosthogHost:         h.baseURL,
	}
}

// optionalDays is retentionDays in a request: Set tells whether the field
// was sent at all, and Days is nil for null (keep data indefinitely).
type optionalDays struct {
	Set  bool
	Days *int
}

func (o *optionalDays) UnmarshalJSON(data []byte) error {
	o.Set = true
	if string(data) == "null" {
		o.Days = nil
		return nil
	}
	var days int
	if err := json.Unmarshal(data, &days); err != nil {
		return err
	}
	o.Days = &days
	return nil
}

// projectBody is the body of create and edit; fields left out stay as they are.
type projectBody struct {
	Name            *string      `json:"name"`
	RetentionDays   optionalDays `json:"retentionDays"`
	TracksLaravel   *bool        `json:"tracksLaravel"`
	TracksBrowser   *bool        `json:"tracksBrowser"`
	TracksPageviews *bool        `json:"tracksPageviews"`
	// RoutePatterns is for PATCH only (create refuses it); nil leaves them
	// as they are.
	RoutePatterns *[]string `json:"routePatterns"`
}

// cleanPatterns returns the route patterns without blank lines and
// surrounding spaces, or why they cannot be saved.
func cleanPatterns(patterns []string) ([]string, error) {
	clean := []string{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) > store.MaxRoutePatternChars {
			return nil, errors.New("A route pattern can have at most 300 characters.")
		}
		clean = append(clean, p)
	}
	if len(clean) > store.MaxRoutePatterns {
		return nil, errors.New("A project can have at most 100 route patterns.")
	}
	return clean, nil
}

// apply puts the fields that were sent onto s and checks the result.
func (b projectBody) apply(s *store.ProjectSettings) error {
	if b.Name != nil {
		s.Name = strings.TrimSpace(*b.Name)
	}
	if b.RetentionDays.Set {
		s.RetentionDays = b.RetentionDays.Days
	}
	if b.TracksLaravel != nil {
		s.TracksLaravel = *b.TracksLaravel
	}
	if b.TracksBrowser != nil {
		s.TracksBrowser = *b.TracksBrowser
	}
	if b.TracksPageviews != nil {
		s.TracksPageviews = *b.TracksPageviews
	}
	switch {
	case s.Name == "":
		return errors.New("Give the project a name.")
	case utf8.RuneCountInString(s.Name) > maxProjectName:
		return errors.New("The name can have at most 100 characters.")
	case s.RetentionDays != nil && !retentionChoices[*s.RetentionDays]:
		return errors.New("Keep data for 30, 90, 180 or 365 days, or indefinitely (null).")
	}
	return nil
}

func (h *Handler) settingsProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.db.ListProjects(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	list := make([]settingsProjectJSON, 0, len(projects))
	for _, p := range projects {
		list = append(list, toSettingsProject(p))
	}
	httpjson.Write(w, http.StatusOK, list)
}

// createProject answers 201 with the new project and its keys. Data is kept
// 90 days unless retentionDays says otherwise.
func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	if body.RoutePatterns != nil {
		httpjson.Error(w, http.StatusBadRequest, "Route patterns are set on Edit project, once the project exists.")
		return
	}
	days := store.DefaultRetentionDays
	settings := store.ProjectSettings{RetentionDays: &days}
	if err := body.apply(&settings); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	np := store.NewProject{
		Name:            settings.Name,
		KeepForever:     settings.RetentionDays == nil,
		TracksLaravel:   settings.TracksLaravel,
		TracksBrowser:   settings.TracksBrowser,
		TracksPageviews: settings.TracksPageviews,
	}
	if settings.RetentionDays != nil {
		np.RetentionDays = *settings.RetentionDays
	}
	p, err := h.db.CreateProject(r.Context(), np)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusCreated, h.toProjectKeys(p))
}

// settingsProjectByID reads the project of the URL, answering 404 itself.
func (h *Handler) settingsProjectByID(w http.ResponseWriter, r *http.Request) (store.Project, bool) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return store.Project{}, false
	}
	p, err := h.db.ProjectByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return store.Project{}, false
	}
	if err != nil {
		h.internalError(w, r, err)
		return store.Project{}, false
	}
	return p, true
}

func (h *Handler) settingsProject(w http.ResponseWriter, r *http.Request) {
	if p, ok := h.settingsProjectByID(w, r); ok {
		httpjson.Write(w, http.StatusOK, h.toProjectKeys(p))
	}
}

// invalidSettings is a change updateProject refuses: the merged settings
// do not hold (a name too long, a retention that is not offered).
type invalidSettings struct{ error }

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if !httpjson.Read(w, r, maxBodyProject, &body) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	var patterns *[]string
	if body.RoutePatterns != nil {
		clean, err := cleanPatterns(*body.RoutePatterns)
		if err != nil {
			httpjson.Error(w, http.StatusBadRequest, err.Error())
			return
		}
		patterns = &clean
	}
	// One write that reads the project locked, applies what was sent and
	// writes it back: two edits at once never undo each other, and the
	// settings and the patterns change together or not at all.
	updated, err := h.db.ChangeProject(r.Context(), id, func(s *store.ProjectSettings) error {
		if err := body.apply(s); err != nil {
			return invalidSettings{err}
		}
		s.RoutePatterns = patterns
		return nil
	})
	var invalid invalidSettings
	switch {
	case errors.As(err, &invalid):
		httpjson.Error(w, http.StatusBadRequest, invalid.Error())
	case errors.Is(err, store.ErrNotFound):
		notFound(w)
	case err != nil:
		h.internalError(w, r, err)
	default:
		httpjson.Write(w, http.StatusOK, h.toProjectKeys(updated))
	}
}

// routePreviewJSON is a recent path and the route it gets.
type routePreviewJSON struct {
	Path  string `json:"path"`
	Route string `json:"route"`
}

// routePreview answers POST /ui/api/settings/projects/{id}/route-preview
// with {routePatterns}: the project's recent paths (of page views and
// error URLs, with their secrets masked) and the route each would get
// under those patterns. Nothing is saved.
func (h *Handler) routePreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RoutePatterns []string `json:"routePatterns"`
	}
	if !httpjson.Read(w, r, maxBodyProject, &body) {
		return
	}
	p, ok := h.settingsProjectByID(w, r)
	if !ok {
		return
	}
	patterns, err := cleanPatterns(body.RoutePatterns)
	if err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	recent, err := h.db.RecentPaths(r.Context(), p.ID, 500)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	list := []routePreviewJSON{}
	done := map[string]bool{}
	perSection := map[string]int{}
	for _, raw := range recent {
		path := routes.Path(raw) // masked, also when stored before masking existed
		section, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
		if done[path] || perSection[section] == previewPerSection {
			continue
		}
		done[path] = true
		perSection[section]++
		list = append(list, routePreviewJSON{Path: path, Route: routes.Normalize(path, patterns)})
		if len(list) == previewPaths {
			break
		}
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"paths": list})
}

// rotateKeys gives the project a new DSN key and PostHog token; apps using
// the old ones are refused from now on.
func (h *Handler) rotateKeys(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	p, err := h.db.RotateProjectKeys(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.log.Info("project keys rotated", "project", p.ID)
	httpjson.Write(w, http.StatusOK, h.toProjectKeys(p))
}

// deleteProject removes the project with all its issues, events and access.
func (h *Handler) deleteProject(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	err := h.db.DeleteProject(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.log.Info("project deleted", "project", id)
	httpjson.Write(w, http.StatusOK, struct{}{})
}
