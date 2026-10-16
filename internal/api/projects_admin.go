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
//
// retentionDays is 30, 90, 180 or 365, or null to keep data indefinitely.

// maxBodySettings caps the JSON bodies of the settings routes.
const maxBodySettings = 16 << 10

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

func settingsOf(p store.Project) store.ProjectSettings {
	return store.ProjectSettings{
		Name: p.Name, RetentionDays: p.RetentionDays,
		TracksLaravel: p.TracksLaravel, TracksBrowser: p.TracksBrowser, TracksPageviews: p.TracksPageviews,
	}
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

func (h *Handler) updateProject(w http.ResponseWriter, r *http.Request) {
	var body projectBody
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	p, ok := h.settingsProjectByID(w, r)
	if !ok {
		return
	}
	settings := settingsOf(p)
	if err := body.apply(&settings); err != nil {
		httpjson.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := h.db.UpdateProject(r.Context(), p.ID, settings)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, h.toProjectKeys(updated))
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
