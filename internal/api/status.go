package api

import (
	"encoding/json"
	"errors"
	"mime"
	"net/http"

	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// maxStatusBody caps the body of a status change; {"status":"resolved"} is 21 bytes.
const maxStatusBody = 1 << 10

// statusRequest is the body of POST /ui/api/issues/{id}/status.
type statusRequest struct {
	Status string `json:"status"`
}

// statusResponse is the issue after its status changed.
type statusResponse struct {
	ID        int64  `json:"id"`
	ProjectID int64  `json:"projectId"`
	Status    string `json:"status"`
}

// setIssueStatus answers POST /ui/api/issues/{id}/status with a body
// {"status":"open|resolved|ignored"}.
//
// Like every change, it needs the session's X-CSRF-Token (auth.RequireUser).
// Only application/json is accepted as well: a page elsewhere can make a
// browser send a form or text/plain POST here unasked, but a JSON one needs
// a CORS preflight, which this API never answers.
func (h *Handler) setIssueStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/json" {
		httpjson.Error(w, http.StatusUnsupportedMediaType, "send the status as JSON with Content-Type: application/json")
		return
	}
	var req statusRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxStatusBody))
	if err := dec.Decode(&req); err != nil || dec.More() {
		httpjson.Error(w, http.StatusBadRequest, `the body must be one JSON object such as {"status":"resolved"}`)
		return
	}

	// The status is checked before the issue is looked up, so a bad request
	// answers the same whether the issue exists (or may be seen) or not.
	if !store.ValidIssueStatus(req.Status) {
		httpjson.Error(w, http.StatusBadRequest, issues.ErrInvalidStatus.Error())
		return
	}
	current, err := h.db.IssueByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !canSee(r, current.ProjectID)) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	issue, err := h.issues.SetStatus(r.Context(), id, req.Status)
	switch {
	case errors.Is(err, issues.ErrInvalidStatus):
		httpjson.Error(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound):
		notFound(w)
	case err != nil:
		h.internalError(w, r, err)
	default:
		httpjson.Write(w, http.StatusOK, statusResponse{ID: issue.ID, ProjectID: issue.ProjectID, Status: issue.Status})
	}
}
