// Package api is the JSON API the dashboard reads, under /ui/api/.
//
// It answers in camelCase JSON with times in RFC 3339, and errors as
// {"error":"..."}. Every route sits behind auth.RequireUser. A project, or
// anything in one, that the signed-in user may not see answers 404, the
// same as one that does not exist, so ids cannot be probed; lists and
// totals leave such projects out. Settings routes answer 404 to members.
package api

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"monitor/internal/alerts"
	"monitor/internal/auth"
	"monitor/internal/health"
	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// SlackPoster posts a message to a Slack webhook; *alerts.Slack is one.
type SlackPoster interface {
	Post(ctx context.Context, webhook, text string) error
}

// Options configure the API handler.
type Options struct {
	DB *store.DB
	// Auth makes invites and reset links, and says whether mail is on.
	Auth *auth.Service
	// BaseURL is BASE_URL, which DSNs and links are built from.
	BaseURL string
	Logger  *slog.Logger
	// Health runs the checks the status page shows.
	Health *health.Checker
	// Slack sends the test messages; nil means the real Slack client.
	Slack SlackPoster
	// Started is when the process started, for the status page's uptime.
	Started time.Time
}

// Handler serves the dashboard API.
type Handler struct {
	db      *store.DB
	auth    *auth.Service
	baseURL string // for the DSNs on the setup page
	issues  *issues.Service
	health  *health.Checker
	slack   SlackPoster
	started time.Time
	log     *slog.Logger
	now     func() time.Time // the clock; tests set it
}

// New returns the API handler.
func New(o Options) *Handler {
	h := &Handler{db: o.DB, auth: o.Auth, baseURL: o.BaseURL, health: o.Health, slack: o.Slack,
		started: o.Started, log: o.Logger, now: time.Now}
	if h.slack == nil {
		h.slack = alerts.NewSlack()
	}
	if h.started.IsZero() {
		h.started = time.Now()
	}
	// The service reads the handler's clock, so a test that sets h.now sets both.
	h.issues = issues.NewService(o.DB, func() time.Time { return h.now() })
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
	mux.HandleFunc("GET /ui/api/projects/{id}/alerts", h.projectAlerts)
	mux.HandleFunc("PATCH /ui/api/projects/{id}/alerts", h.updateProjectAlerts)
	mux.HandleFunc("POST /ui/api/projects/{id}/alerts/slack-test", h.testProjectSlack)

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
	mux.HandleFunc("GET /ui/api/settings/status", h.adminOnly(h.status))
	mux.HandleFunc("PUT /ui/api/settings/admin-slack", h.adminOnly(h.setAdminSlack))
	mux.HandleFunc("POST /ui/api/settings/admin-slack/test", h.adminOnly(h.testAdminSlack))
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
