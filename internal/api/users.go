package api

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"monitor/internal/auth"
	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// Settings → Users, for admins:
//
//	GET    /ui/api/settings/users                {users, invites}: every account and the pending invites
//	PATCH  /ui/api/settings/users/{id}           {role?, projectIds?}
//	DELETE /ui/api/settings/users/{id}
//	POST   /ui/api/settings/users/{id}/reset-link a one-time link for a member to choose a new password
//	POST   /ui/api/settings/invites              {email, role, projectIds}: a one-time invite link
//	DELETE /ui/api/settings/invites/{id}         revoke an invite
//
// The last admin can be neither removed nor made a member (409). Links are
// in the answer once only; when mail is on they are also mailed.

type settingsUserJSON struct {
	ID        int64     `json:"id"`
	Email     string    `json:"email"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"createdAt"`
	// ProjectIDs are a member's projects; empty for admins, who see them all.
	ProjectIDs []int64 `json:"projectIds"`
}

type inviteJSON struct {
	ID         int64     `json:"id"`
	Email      string    `json:"email"`
	Role       string    `json:"role"`
	ProjectIDs []int64   `json:"projectIds"`
	CreatedAt  time.Time `json:"createdAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
}

func toInviteJSON(inv store.Invite) inviteJSON {
	ids := inv.ProjectIDs
	if ids == nil {
		ids = []int64{}
	}
	return inviteJSON{ID: inv.ID, Email: inv.Email, Role: inv.Role, ProjectIDs: ids, CreatedAt: inv.CreatedAt, ExpiresAt: inv.ExpiresAt}
}

func (h *Handler) settingsUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := h.db.ListUsers(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	access, err := h.db.ProjectAccessByUser(ctx)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	invites, err := h.db.PendingInvites(ctx, h.now())
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	resp := struct {
		Users   []settingsUserJSON `json:"users"`
		Invites []inviteJSON       `json:"invites"`
	}{Users: make([]settingsUserJSON, 0, len(users)), Invites: make([]inviteJSON, 0, len(invites))}
	for _, u := range users {
		resp.Users = append(resp.Users, toSettingsUser(u, access[u.ID]))
	}
	for _, inv := range invites {
		resp.Invites = append(resp.Invites, toInviteJSON(inv))
	}
	httpjson.Write(w, http.StatusOK, resp)
}

func toSettingsUser(u store.User, projectIDs []int64) settingsUserJSON {
	if projectIDs == nil || u.Role == store.RoleAdmin {
		projectIDs = []int64{}
	}
	return settingsUserJSON{ID: u.ID, Email: u.Email, Role: u.Role, CreatedAt: u.CreatedAt, ProjectIDs: projectIDs}
}

// createInvite answers 201 with the invite and its link. An address that
// already has an account gets 409.
func (h *Handler) createInvite(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email      string  `json:"email"`
		Role       string  `json:"role"`
		ProjectIDs []int64 `json:"projectIds"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &req) {
		return
	}
	ctx := r.Context()
	link, err := h.auth.Invite(ctx, auth.UserFrom(ctx).ID, req.Email, req.Role, req.ProjectIDs)
	var input auth.InputError
	switch {
	case errors.As(err, &input):
		httpjson.Error(w, http.StatusBadRequest, input.Error())
	case errors.Is(err, store.ErrEmailTaken):
		httpjson.Error(w, http.StatusConflict, "This address already has an account.")
	case errors.Is(err, store.ErrUnknownProject):
		httpjson.Error(w, http.StatusBadRequest, "One of the projects does not exist any more. Reload the page.")
	case errors.Is(err, store.ErrNotAdmin):
		notFound(w) // made a member (or removed) while the request was on its way, as adminOnly answers members
	case err != nil:
		h.internalError(w, r, err)
	default:
		httpjson.Write(w, http.StatusCreated, map[string]any{
			"invite": toInviteJSON(link.Invite),
			"link":   link.URL,
			"mailed": link.Mailed,
		})
	}
}

func (h *Handler) revokeInvite(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	err := h.db.DeleteInvite(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// updateUser changes a user's role, a member's projects, or both.
func (h *Handler) updateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role       *string  `json:"role"`
		ProjectIDs *[]int64 `json:"projectIds"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &req) {
		return
	}
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	if req.Role != nil && !store.ValidRole(*req.Role) {
		httpjson.Error(w, http.StatusBadRequest, `The role must be "admin" or "member".`)
		return
	}
	ctx := r.Context()
	u, err := h.db.UpdateUser(ctx, id, store.UserChange{Role: req.Role, ProjectIDs: req.ProjectIDs})
	if !h.userError(w, r, err) {
		return
	}
	access, err := h.db.ProjectAccess(ctx, u.ID)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, toSettingsUser(u, access))
}

// deleteUser removes an account; its sessions end at once.
func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	if !h.userError(w, r, h.db.DeleteUser(r.Context(), id)) {
		return
	}
	h.log.Info("user removed", "user", id)
	httpjson.Write(w, http.StatusOK, struct{}{})
}

// userError answers for a failed user change and reports whether there was none.
func (h *Handler) userError(w http.ResponseWriter, r *http.Request, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrNotFound):
		notFound(w)
	case errors.Is(err, store.ErrLastAdmin):
		httpjson.Error(w, http.StatusConflict, "This is the last admin. Make someone else admin first.")
	case errors.Is(err, store.ErrUnknownProject):
		httpjson.Error(w, http.StatusBadRequest, "One of the projects does not exist any more. Reload the page.")
	default:
		h.internalError(w, r, err)
	}
	return false
}

// resetLink makes a one-time link with which a member chooses a new
// password, for the admin to hand over (and mailed, when mail is on).
// Admins reset their own password with the server command instead.
func (h *Handler) resetLink(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	ctx := r.Context()
	u, err := h.db.UserByID(ctx, id)
	if !h.userError(w, r, err) {
		return
	}
	if u.Role == store.RoleAdmin {
		httpjson.Error(w, http.StatusBadRequest, fmt.Sprintf(
			"Admins reset their password on the server: monitor admin reset-password --email %s", u.Email))
		return
	}
	link, err := h.auth.ResetLink(ctx, u.ID, true)
	if !h.userError(w, r, err) {
		return
	}
	httpjson.Write(w, http.StatusOK, map[string]any{"link": link.URL, "expiresAt": link.ExpiresAt, "mailed": link.Mailed})
}
