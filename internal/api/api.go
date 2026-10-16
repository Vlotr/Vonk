// Package api is the JSON API the dashboard reads, under /ui/api/.
//
// It answers in camelCase JSON with times in RFC 3339, and errors as
// {"error":"..."}. A missing object and an object the caller may not see
// both answer 404, so ids cannot be probed (logins arrive in Task 5).
package api

import (
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// Handler serves the dashboard API.
type Handler struct {
	db     *store.DB
	issues *issues.Service
	log    *slog.Logger
	now    func() time.Time // the clock; tests set it
}

// New returns the API handler.
func New(db *store.DB, logger *slog.Logger) *Handler {
	h := &Handler{db: db, log: logger, now: time.Now}
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
