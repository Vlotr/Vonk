package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"monitor/internal/sourcemaps"
	"monitor/internal/store"
)

type apiTokenResponse struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Hint       string  `json:"hint"`
	ProjectIDs []int64 `json:"projectIds"`
	CreatedAt  string  `json:"createdAt"`
	LastUsedAt *string `json:"lastUsedAt"`
}

// An admin makes a token for some projects. The answer has the token once;
// the database and every later answer have only its hash and last four
// characters. The token names this tool, as Sentry's organization tokens
// name their server, so sentry-cli and the vite plugin need nothing else.
func TestAPITokens_CreateShowOnceDelete(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	portal := env.project(t, "Portal")

	rec := env.as(t, env.admin, "POST", "/ui/api/settings/api-tokens", fmt.Sprintf(`{"name":" CI ","projectIds":[%d,%d]}`, portal.ID, shop.ID))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	var created struct {
		Token  apiTokenResponse `json:"token"`
		Secret string           `json:"secret"`
		URL    string           `json:"url"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Token.Name != "CI" || len(created.Token.ProjectIDs) != 2 || created.Token.LastUsedAt != nil || created.URL != "https://monitor.example.com" {
		t.Errorf("token = %+v", created.Token)
	}
	payload, secret, ok := strings.Cut(strings.TrimPrefix(created.Secret, "sntrys_"), "_")
	if !strings.HasPrefix(created.Secret, "sntrys_") || !ok || len(secret) < 40 {
		t.Fatalf("secret = %q, want sntrys_<payload>_<secret>", created.Secret)
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	var fields map[string]any
	if err != nil || json.Unmarshal(decoded, &fields) != nil || fields["url"] != "https://monitor.example.com" || fields["iat"] == nil {
		t.Errorf("payload = %s, %v; want the BASE_URL and iat", decoded, err)
	}
	if created.Token.Hint != created.Secret[len(created.Secret)-4:] {
		t.Errorf("hint = %q", created.Token.Hint)
	}
	stored, err := env.db.APITokenByHash(context.Background(), env.auth.HashToken(created.Secret))
	if err != nil || stored.ID != created.Token.ID {
		t.Errorf("stored by its HMAC: %+v, %v", stored, err)
	}

	list := env.get(t, "/ui/api/settings/api-tokens")
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), secret) {
		t.Fatalf("list = %d %s, want it without the token", list.Code, list.Body)
	}
	var listed struct {
		Tokens []apiTokenResponse `json:"tokens"`
	}
	json.Unmarshal(list.Body.Bytes(), &listed)
	if len(listed.Tokens) != 1 || listed.Tokens[0].ID != created.Token.ID || listed.Tokens[0].Hint != created.Token.Hint {
		t.Errorf("tokens = %+v", listed.Tokens)
	}

	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/api-tokens/%d", created.Token.ID), ""); rec.Code != http.StatusOK {
		t.Fatalf("delete = %d %s", rec.Code, rec.Body)
	}
	if _, err := env.db.APITokenByHash(context.Background(), env.auth.HashToken(created.Secret)); err != store.ErrNotFound {
		t.Errorf("deleted token still there: %v", err)
	}
	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/api-tokens/%d", created.Token.ID), ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete = %d, want 404", rec.Code)
	}
}

func TestAPITokens_BadRequests(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	for body, want := range map[string]string{
		fmt.Sprintf(`{"name":"","projectIds":[%d]}`, shop.ID):                             "Give the token a name.",
		fmt.Sprintf(`{"name":"%s","projectIds":[%d]}`, strings.Repeat("x", 101), shop.ID): "A token's name can have at most 100 characters.",
		`{"name":"CI","projectIds":[]}`:                                                   "Choose at least one project.",
		`{"name":"CI","projectIds":[999]}`:                                                "One of the projects does not exist any more. Reload the page.",
	} {
		rec := env.as(t, env.admin, "POST", "/ui/api/settings/api-tokens", body)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s = %d %s, want 400 %q", body, rec.Code, rec.Body, want)
		}
	}
	if list, _ := env.db.ListAPITokens(context.Background()); len(list) != 0 {
		t.Errorf("refused requests stored tokens: %+v", list)
	}
}

// Revoking a token removes the chunks it had waiting for an upload: no
// token can put them together any more.
func TestAPITokens_RevokeRemovesWaitingChunks(t *testing.T) {
	env := newTestEnv(t)
	shop := env.project(t, "Webshop")
	dir := filepath.Join(env.dir, "sourcemaps")
	env.api.maps = sourcemaps.New(sourcemaps.Options{DB: env.db, Dir: dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	rec := env.as(t, env.admin, "POST", "/ui/api/settings/api-tokens", fmt.Sprintf(`{"name":"CI","projectIds":[%d]}`, shop.ID))
	var created struct {
		Token apiTokenResponse `json:"token"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("create = %d %s", rec.Code, rec.Body)
	}
	chunks := filepath.Join(dir, "chunks", fmt.Sprint(created.Token.ID))
	if err := os.MkdirAll(chunks, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(chunks, "0123456789abcdef0123456789abcdef01234567"), []byte("chunk"), 0o640); err != nil {
		t.Fatal(err)
	}

	if rec := env.as(t, env.admin, "DELETE", fmt.Sprintf("/ui/api/settings/api-tokens/%d", created.Token.ID), ""); rec.Code != http.StatusOK {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(chunks); !os.IsNotExist(err) {
		t.Errorf("the revoked token's chunks are still on disk: %v", err)
	}
}
