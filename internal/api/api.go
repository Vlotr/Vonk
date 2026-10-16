// Package api is the JSON API the dashboard reads, under /ui/api/.
//
// It answers in camelCase JSON with times in RFC 3339, and errors as
// {"error":"..."}. Every route sits behind auth.RequireUser. A project, or
// anything in one, that the signed-in user may not see answers 404, the
// same as one that does not exist, so ids cannot be probed; lists and
// totals leave such projects out. Settings routes answer 404 to members.
package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"monitor/internal/auth"
	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// Handler serves the dashboard API.
type Handler struct {
	db      *store.DB
	auth    *auth.Service
	baseURL string // for the DSNs on the setup page
	issues  *issues.Service
	log     *slog.Logger
	now     func() time.Time // the clock; tests set it
}

// New returns the API handler. authService makes invites and reset links;
// baseURL is BASE_URL, which DSNs are built from.
func New(db *store.DB, authService *auth.Service, baseURL string, logger *slog.Logger) *Handler {
	h := &Handler{db: db, auth: authService, baseURL: baseURL, log: logger, now: time.Now}
	// The service reads the handler's clock, so a test that sets h.now sets both.
	h.issues = issues.NewService(db, func() time.Time { return h.now() })
	return h
}

// Register adds the API routes to mux. Unknown paths under /ui/api/ are
// left to the caller's catch-all.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /ui/api/overview", h.overview)
	mux.HandleFunc("GET /ui/api/projects", h.listProjects)
	mux.HandleFunc("GET /ui/api/projects/{id}/issues", h.listIssues)
	mux.HandleFunc("GET /ui/api/issues/{id}", h.issueDetail)
	mux.HandleFunc("POST /ui/api/issues/{id}/status", h.setIssueStatus)

	// Settings, for admins only.
	mux.HandleFunc("GET /ui/api/settings/projects", h.adminOnly(h.settingsProjects))
	mux.HandleFunc("POST /ui/api/settings/projects", h.adminOnly(h.createProject))
	mux.HandleFunc("GET /ui/api/settings/projects/{id}", h.adminOnly(h.settingsProject))
	mux.HandleFunc("PATCH /ui/api/settings/projects/{id}", h.adminOnly(h.updateProject))
	mux.HandleFunc("POST /ui/api/settings/projects/{id}/rotate-keys", h.adminOnly(h.rotateKeys))
	mux.HandleFunc("DELETE /ui/api/settings/projects/{id}", h.adminOnly(h.deleteProject))
	mux.HandleFunc("GET /ui/api/settings/users", h.adminOnly(h.settingsUsers))
	mux.HandleFunc("PATCH /ui/api/settings/users/{id}", h.adminOnly(h.updateUser))
	mux.HandleFunc("DELETE /ui/api/settings/users/{id}", h.adminOnly(h.deleteUser))
	mux.HandleFunc("POST /ui/api/settings/users/{id}/reset-link", h.adminOnly(h.resetLink))
	mux.HandleFunc("POST /ui/api/settings/invites", h.adminOnly(h.createInvite))
	mux.HandleFunc("DELETE /ui/api/settings/invites/{id}", h.adminOnly(h.revokeInvite))
}

// adminOnly answers 404 to anyone but an admin, as if the route did not exist.
func (h *Handler) adminOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.UserFrom(r.Context()).IsAdmin() {
			notFound(w)
			return
		}
		next(w, r)
	}
}

// canSee reports whether the signed-in user may see the project.
func canSee(r *http.Request, projectID int64) bool {
	ctx := r.Context()
	return auth.CanAccessProject(ctx, auth.UserFrom(ctx), projectID)
}

// pathID reads a positive id from the URL path; ok is false for anything else.
func pathID(r *http.Request, name string) (id int64, ok bool) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	return id, err == nil && id > 0
}

func notFound(w http.ResponseWriter) {
	httpjson.Error(w, http.StatusNotFound, "not found")
}

// internalError logs a failed query and answers 500 without details.
func (h *Handler) internalError(w http.ResponseWriter, r *http.Request, err error) {
	h.log.Error("dashboard api", "path", r.URL.Path, "err", err)
	httpjson.Error(w, http.StatusInternalServerError, "internal error")
}
