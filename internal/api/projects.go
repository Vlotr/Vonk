package api

import (
	"net/http"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// projectJSON is a project in GET /ui/api/projects.
type projectJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
	// Sources are the kinds of errors the project tracks: "laravel", "browser".
	Sources []string `json:"sources"`
	// OpenIssues feeds the count next to Issues in the sidebar.
	OpenIssues int `json:"openIssues"`
}

// listProjects answers GET /ui/api/projects: the projects the signed-in
// user may see, sorted by name.
func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.visibleProjects(r)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	open, err := h.db.CountOpenIssuesPerProject(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	list := make([]projectJSON, 0, len(projects))
	for _, p := range projects {
		list = append(list, projectJSON{
			ID:         p.ID,
			Name:       p.Name,
			Slug:       p.Slug,
			Sources:    sources(p),
			OpenIssues: open[p.ID],
		})
	}
	httpjson.Write(w, http.StatusOK, list)
}

// visibleProjects returns the projects the signed-in user may see, sorted by name.
func (h *Handler) visibleProjects(r *http.Request) ([]store.Project, error) {
	all, err := h.db.ListProjects(r.Context())
	if err != nil {
		return nil, err
	}
	visible := make([]store.Project, 0, len(all))
	for _, p := range all {
		if canSee(r, p.ID) {
			visible = append(visible, p)
		}
	}
	return visible, nil
}

// sources lists what a project tracks, Laravel first, as the designs show the tags.
func sources(p store.Project) []string {
	list := []string{}
	if p.TracksLaravel {
		list = append(list, "laravel")
	}
	if p.TracksBrowser {
		list = append(list, "browser")
	}
	return list
}
