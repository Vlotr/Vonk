package api

import (
	"errors"
	"net/http"
	"strings"

	"monitor/internal/auth"
	"monitor/internal/httpjson"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// Settings → Privacy, for admins:
//
//	POST /ui/api/privacy/delete-user {userId, projectIds?}
//
// deletes everything stored with one user id (the id the SDKs send:
// Sentry's user.id, PostHog's distinct_id) in the chosen projects, or in
// every project when projectIds is left out (store.DeleteUserData). The
// log gets one line: which admin, and how many rows; never the user id.

type userDataDeletedJSON struct {
	Projects      int   `json:"projects"`
	ErrorEvents   int64 `json:"errorEvents"`
	Pageviews     int64 `json:"pageviews"`
	Pageleaves    int64 `json:"pageleaves"`
	CustomEvents  int64 `json:"customEvents"`
	IssuesDeleted int64 `json:"issuesDeleted"`
	IssuesUpdated int64 `json:"issuesUpdated"`
	// Rows is everything deleted together: events, page views and leaves, issues.
	Rows int64 `json:"rows"`
}

func (h *Handler) deleteUserData(w http.ResponseWriter, r *http.Request) {
	var body struct {
		UserID     *string  `json:"userId"`
		ProjectIDs *[]int64 `json:"projectIds"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &body) {
		return
	}
	if body.UserID == nil || strings.TrimSpace(*body.UserID) == "" {
		httpjson.Error(w, http.StatusBadRequest, "Give the user id whose data should be deleted.")
		return
	}
	var projectIDs []int64 // nil: every project
	if body.ProjectIDs != nil {
		if len(*body.ProjectIDs) == 0 {
			httpjson.Error(w, http.StatusBadRequest, "Choose at least one project, or leave projectIds out for every project.")
			return
		}
		projectIDs = *body.ProjectIDs
	}

	deleted, err := h.db.DeleteUserData(r.Context(), store.UserDataRequest{UserID: *body.UserID, ProjectIDs: projectIDs, Describe: issues.EventText})
	if errors.Is(err, store.ErrUnknownProject) {
		httpjson.Error(w, http.StatusBadRequest, "One of the chosen projects does not exist (any more). Reload the page and choose again.")
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.log.Info("privacy: deleted the data of a user id on request", "admin", auth.UserFrom(r.Context()).ID,
		"projects", deleted.Projects, "rows", deleted.Rows(), "error_events", deleted.ErrorEvents, "pageviews", deleted.Pageviews,
		"pageleaves", deleted.Pageleaves, "custom_events", deleted.CustomEvents, "issues_deleted", deleted.IssuesDeleted)
	httpjson.Write(w, http.StatusOK, userDataDeletedJSON{
		Projects: deleted.Projects, ErrorEvents: deleted.ErrorEvents, Pageviews: deleted.Pageviews, Pageleaves: deleted.Pageleaves,
		CustomEvents: deleted.CustomEvents, IssuesDeleted: deleted.IssuesDeleted, IssuesUpdated: deleted.IssuesUpdated, Rows: deleted.Rows(),
	})
}
