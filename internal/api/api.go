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

	"monitor/internal/alerts"
	"monitor/internal/auth"
	"monitor/internal/health"
	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/sourcemaps"
	"monitor/internal/store"
)

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
	Slack alerts.SlackPoster
	// Started is when the process started, for the status page's uptime.
	Started time.Time
	// StoredLastHour counts the events ingest stored in the last hour; nil
	// counts none.
	StoredLastHour func() int
	// AllowHTTPWebhooks accepts plain http webhooks (DEV_ALLOW_HTTP_WEBHOOKS,
	// for local fakes); otherwise they must be https.
	AllowHTTPWebhooks bool
	// SourceMaps symbolicates events stored before their source map was
	// uploaded, and drops a revoked token's waiting chunks; nil does neither.
	SourceMaps *sourcemaps.Service
}

// Handler serves the dashboard API.
type Handler struct {
	db        *store.DB
	auth      *auth.Service
	baseURL   string // for the DSNs on the setup page
	issues    *issues.Service
	health    *health.Checker
	slack     alerts.SlackPoster
	started   time.Time
	stored    func() int
	allowHTTP bool
	maps      *sourcemaps.Service // Options.SourceMaps; may be nil
	log       *slog.Logger
	now       func() time.Time // the clock; tests set it

	// maxRestoreBytes caps a restore upload (defaultMaxRestoreBytes); tests lower it.
	maxRestoreBytes int64
	// snapshots and restores let one backup download be made, and one
	// restore be uploaded, at a time: each needs disk space for a copy.
	snapshots, restores chan struct{}
}

// New returns the API handler.
func New(o Options) *Handler {
	h := &Handler{db: o.DB, auth: o.Auth, baseURL: o.BaseURL, health: o.Health, slack: o.Slack,
		started: o.Started, stored: o.StoredLastHour, allowHTTP: o.AllowHTTPWebhooks, maps: o.SourceMaps,
		log: o.Logger, now: time.Now,
		maxRestoreBytes: defaultMaxRestoreBytes, snapshots: make(chan struct{}, 1), restores: make(chan struct{}, 1)}
	if h.stored == nil {
		h.stored = func() int { return 0 }
	}
	if h.slack == nil {
		h.slack = alerts.NewSlack(o.AllowHTTPWebhooks)
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
	mux.HandleFunc("GET /ui/api/projects/{id}/pages", h.projectPages)
	mux.HandleFunc("GET /ui/api/projects/{id}/events", h.projectEvents)
	mux.HandleFunc("GET /ui/api/projects/{id}/events/{name}", h.eventSeries)
	mux.HandleFunc("GET /ui/api/projects/{id}/users/{userId}/activity", h.userActivity)
	mux.HandleFunc("GET /ui/api/issues/{id}", h.issueDetail)
	mux.HandleFunc("POST /ui/api/issues/{id}/status", h.setIssueStatus)
	mux.HandleFunc("GET /ui/api/projects/{id}/alerts", h.projectAlerts)
	// Changing alerts is for admins (ruling 33); everyone who sees the project reads them.
	mux.HandleFunc("PATCH /ui/api/projects/{id}/alerts", h.adminOnly(h.updateProjectAlerts))
	mux.HandleFunc("POST /ui/api/projects/{id}/alerts/slack-test", h.adminOnly(h.testProjectSlack))

	// Settings, for admins only.
	mux.HandleFunc("GET /ui/api/settings/projects", h.adminOnly(h.settingsProjects))
	mux.HandleFunc("POST /ui/api/settings/projects", h.adminOnly(h.createProject))
	mux.HandleFunc("GET /ui/api/settings/projects/{id}", h.adminOnly(h.settingsProject))
	mux.HandleFunc("PATCH /ui/api/settings/projects/{id}", h.adminOnly(h.updateProject))
	mux.HandleFunc("POST /ui/api/settings/projects/{id}/rotate-keys", h.adminOnly(h.rotateKeys))
	mux.HandleFunc("POST /ui/api/settings/projects/{id}/route-preview", h.adminOnly(h.routePreview))
	mux.HandleFunc("DELETE /ui/api/settings/projects/{id}", h.adminOnly(h.deleteProject))
	mux.HandleFunc("GET /ui/api/settings/users", h.adminOnly(h.settingsUsers))
	mux.HandleFunc("PATCH /ui/api/settings/users/{id}", h.adminOnly(h.updateUser))
	mux.HandleFunc("DELETE /ui/api/settings/users/{id}", h.adminOnly(h.deleteUser))
	mux.HandleFunc("POST /ui/api/settings/users/{id}/reset-link", h.adminOnly(h.resetLink))
	mux.HandleFunc("POST /ui/api/settings/invites", h.adminOnly(h.createInvite))
	mux.HandleFunc("DELETE /ui/api/settings/invites/{id}", h.adminOnly(h.revokeInvite))
	mux.HandleFunc("GET /ui/api/settings/api-tokens", h.adminOnly(h.settingsAPITokens))
	mux.HandleFunc("POST /ui/api/settings/api-tokens", h.adminOnly(h.createAPIToken))
	mux.HandleFunc("DELETE /ui/api/settings/api-tokens/{id}", h.adminOnly(h.deleteAPIToken))
	mux.HandleFunc("GET /ui/api/settings/status", h.adminOnly(h.status))
	mux.HandleFunc("PUT /ui/api/settings/admin-slack", h.adminOnly(h.setAdminSlack))
	mux.HandleFunc("POST /ui/api/settings/admin-slack/test", h.adminOnly(h.testAdminSlack))
	mux.HandleFunc("GET /ui/api/backup", h.adminOnly(h.backupDownload))
	mux.HandleFunc("GET /ui/api/settings/backup", h.adminOnly(h.backupPage))
	mux.HandleFunc("POST /ui/api/settings/backup/restore", h.adminOnly(h.restoreUpload))
	mux.HandleFunc("DELETE /ui/api/settings/backup/restore", h.adminOnly(h.cancelRestore))
	mux.HandleFunc("POST /ui/api/privacy/delete-user", h.adminOnly(h.deleteUserData))
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
	if httpjson.ClientGone(r) {
		h.log.Debug("dashboard api: the client went away", "path", httpjson.LogPath(r.URL.Path), "err", err)
		return
	}
	h.log.Error("dashboard api", "path", httpjson.LogPath(r.URL.Path), "err", err)
	httpjson.Error(w, http.StatusInternalServerError, "internal error")
}
