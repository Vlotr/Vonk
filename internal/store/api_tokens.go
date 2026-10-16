package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// APIToken is a token for the source map upload API (sentry-cli,
// @sentry/vite-plugin). Only its hash is stored.
type APIToken struct {
	ID   int64
	Name string
	// Hint is the token's last four characters, to tell tokens apart.
	Hint string
	// ProjectIDs are the projects it may upload to, sorted; the ones that still exist.
	ProjectIDs []int64
	CreatedBy  int64 // 0 when that admin was removed since
	CreatedAt  time.Time
	LastUsedAt *time.Time // nil when it was never used
}

// NewAPIToken holds what CreateAPIToken stores.
type NewAPIToken struct {
	Name       string
	TokenHash  string
	Hint       string
	ProjectIDs []int64
	CreatedBy  int64
	Now        time.Time
}

// apiTokenUseEvery is how often a token's last use is written: an upload
// makes several requests within seconds, and one write is enough.
const apiTokenUseEvery = time.Minute

// CreateAPIToken stores a token for the given projects. A project id that
// does not exist gives ErrUnknownProject. CreatedBy must still be an admin
// when the token is written (ErrNotAdmin otherwise), as for invites.
func (db *DB) CreateAPIToken(ctx context.Context, nt NewAPIToken) (APIToken, error) {
	tok := APIToken{
		Name:      strings.TrimSpace(nt.Name),
		Hint:      nt.Hint,
		CreatedBy: nt.CreatedBy,
		CreatedAt: msTime(nt.Now),
	}
	if tok.Name == "" || nt.TokenHash == "" {
		return APIToken{}, fmt.Errorf("API token needs a name and a token hash")
	}

	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return APIToken{}, err
	}
	defer tx.Rollback() // no-op after Commit
	// The maker stays an admin until the commit, as for invites.
	if err := tx.lock(ctx, lockAccounts); err != nil {
		return APIToken{}, err
	}

	var role string
	err = tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", tok.CreatedBy).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && role != RoleAdmin) {
		return APIToken{}, ErrNotAdmin
	}
	if err != nil {
		return APIToken{}, err
	}
	if tok.ProjectIDs, err = existingProjects(ctx, tx, nt.ProjectIDs); err != nil {
		return APIToken{}, err
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO api_tokens (name, token_hash, hint, created_by, created_at)
		VALUES (?, ?, ?, ?, ?) RETURNING id`,
		tok.Name, nt.TokenHash, tok.Hint, tok.CreatedBy, tok.CreatedAt.UnixMilli()).Scan(&tok.ID)
	if err != nil {
		return APIToken{}, fmt.Errorf("insert API token: %w", err)
	}
	for _, projectID := range tok.ProjectIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO api_token_projects (token_id, project_id) VALUES (?, ?)", tok.ID, projectID); err != nil {
			return APIToken{}, err
		}
	}
	return tok, tx.Commit()
}

const apiTokenColumns = "id, name, hint, created_by, created_at, last_used_at"

// scanAPIToken reads one api_tokens row; its project ids are loaded separately.
func scanAPIToken(s scanner) (APIToken, error) {
	var (
		tok                 APIToken
		createdBy, lastUsed sql.NullInt64
		created             int64
	)
	err := s.Scan(&tok.ID, &tok.Name, &tok.Hint, &createdBy, &created, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return APIToken{}, ErrNotFound
	}
	if err != nil {
		return APIToken{}, err
	}
	tok.CreatedBy = createdBy.Int64
	tok.CreatedAt = time.UnixMilli(created).UTC()
	if lastUsed.Valid {
		used := time.UnixMilli(lastUsed.Int64).UTC()
		tok.LastUsedAt = &used
	}
	return tok, nil
}

// APITokenByHash returns the token with this hash, with its projects, or ErrNotFound.
func (db *DB) APITokenByHash(ctx context.Context, tokenHash string) (APIToken, error) {
	tok, err := scanAPIToken(db.read.QueryRowContext(ctx,
		"SELECT "+apiTokenColumns+" FROM api_tokens WHERE token_hash = ?", tokenHash))
	if err != nil {
		return APIToken{}, err
	}
	tok.ProjectIDs, err = queryIDs(ctx, db.read,
		"SELECT project_id FROM api_token_projects WHERE token_id = ? ORDER BY project_id", tok.ID)
	return tok, err
}

// ListAPITokens returns every token, the newest first, with their projects.
func (db *DB) ListAPITokens(ctx context.Context) ([]APIToken, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT "+apiTokenColumns+" FROM api_tokens ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	tokens := []APIToken{}
	index := map[int64]int{}
	for rows.Next() {
		tok, err := scanAPIToken(rows)
		if err != nil {
			return nil, err
		}
		tok.ProjectIDs = []int64{}
		index[tok.ID] = len(tokens)
		tokens = append(tokens, tok)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()

	access, err := db.read.QueryContext(ctx, "SELECT token_id, project_id FROM api_token_projects ORDER BY token_id, project_id")
	if err != nil {
		return nil, err
	}
	defer access.Close()
	for access.Next() {
		var tokenID, projectID int64
		if err := access.Scan(&tokenID, &projectID); err != nil {
			return nil, err
		}
		if i, ok := index[tokenID]; ok {
			tokens[i].ProjectIDs = append(tokens[i].ProjectIDs, projectID)
		}
	}
	return tokens, access.Err()
}

// DeleteAPIToken removes a token, which stops working at once; ErrNotFound
// when there is none with this id.
func (db *DB) DeleteAPIToken(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, "DELETE FROM api_tokens WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIToken records that the token was used at now. It writes at most
// once a minute per token.
func (db *DB) TouchAPIToken(ctx context.Context, id int64, now time.Time) error {
	_, err := db.write.ExecContext(ctx,
		"UPDATE api_tokens SET last_used_at = ? WHERE id = ? AND (last_used_at IS NULL OR last_used_at < ?)",
		now.UnixMilli(), id, now.Add(-apiTokenUseEvery).UnixMilli())
	return err
}
