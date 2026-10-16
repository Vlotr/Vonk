package api

import (
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"monitor/internal/auth"
	"monitor/internal/httpjson"
	"monitor/internal/sourcemaps"
	"monitor/internal/store"
)

// Settings → API tokens, for admins: tokens for the source map upload API
// (sentry-cli, @sentry/vite-plugin), each for some projects.
//
//	GET    /ui/api/settings/api-tokens       {tokens}: name, last four characters, projects, made, last used
//	POST   /ui/api/settings/api-tokens       {name, projectIds} → 201 {token, secret, url}: the token, this once,
//	                                         and the address the tools upload to (BASE_URL, which the token names)
//	DELETE /ui/api/settings/api-tokens/{id}  the token stops working at once
//
// Only an HMAC of a token is stored (as for sessions), so it cannot be shown again.

// maxTokenNameChars is the longest name a token can have.
const maxTokenNameChars = 100

type apiTokenJSON struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// Hint is the token's last four characters.
	Hint       string     `json:"hint"`
	ProjectIDs []int64    `json:"projectIds"`
	CreatedAt  time.Time  `json:"createdAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

func toAPITokenJSON(t store.APIToken) apiTokenJSON {
	ids := t.ProjectIDs
	if ids == nil {
		ids = []int64{}
	}
	return apiTokenJSON{ID: t.ID, Name: t.Name, Hint: t.Hint, ProjectIDs: ids, CreatedAt: t.CreatedAt, LastUsedAt: t.LastUsedAt}
}

func (h *Handler) settingsAPITokens(w http.ResponseWriter, r *http.Request) {
	tokens, err := h.db.ListAPITokens(r.Context())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	resp := struct {
		Tokens []apiTokenJSON `json:"tokens"`
	}{Tokens: make([]apiTokenJSON, 0, len(tokens))}
	for _, t := range tokens {
		resp.Tokens = append(resp.Tokens, toAPITokenJSON(t))
	}
	httpjson.Write(w, http.StatusOK, resp)
}

// createAPIToken makes a token and answers it once.
func (h *Handler) createAPIToken(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name       string  `json:"name"`
		ProjectIDs []int64 `json:"projectIds"`
	}
	if !httpjson.Read(w, r, maxBodySettings, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	switch {
	case name == "":
		httpjson.Error(w, http.StatusBadRequest, "Give the token a name.")
		return
	case utf8.RuneCountInString(name) > maxTokenNameChars:
		httpjson.Error(w, http.StatusBadRequest, "A token's name can have at most 100 characters.")
		return
	case len(req.ProjectIDs) == 0:
		httpjson.Error(w, http.StatusBadRequest, "Choose at least one project.")
		return
	}

	ctx := r.Context()
	now := h.now()
	secret, hint, err := sourcemaps.NewToken(h.baseURL, now)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	token, err := h.db.CreateAPIToken(ctx, store.NewAPIToken{Name: name, TokenHash: h.auth.HashToken(secret), Hint: hint,
		ProjectIDs: req.ProjectIDs, CreatedBy: auth.UserFrom(ctx).ID, Now: now})
	switch {
	case errors.Is(err, store.ErrUnknownProject):
		httpjson.Error(w, http.StatusBadRequest, "One of the projects does not exist any more. Reload the page.")
	case errors.Is(err, store.ErrNotAdmin):
		notFound(w) // made a member (or removed) while the request was on its way, as adminOnly answers members
	case err != nil:
		h.internalError(w, r, err)
	default:
		h.log.Info("API token made", "token", token.ID, "projects", token.ProjectIDs)
		httpjson.Write(w, http.StatusCreated, map[string]any{"token": toAPITokenJSON(token), "secret": secret, "url": h.baseURL})
	}
}

func (h *Handler) deleteAPIToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "id")
	if !ok {
		notFound(w)
		return
	}
	err := h.db.DeleteAPIToken(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		notFound(w)
		return
	}
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	h.log.Info("API token revoked", "token", id)
	httpjson.Write(w, http.StatusOK, struct{}{})
}
