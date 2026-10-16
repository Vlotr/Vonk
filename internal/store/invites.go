package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Invite is a link that lets one person create an account with a given role
// (and, for members, access to given projects). It works once, until ExpiresAt.
type Invite struct {
	ID         int64
	Email      string
	Role       string
	ProjectIDs []int64 // sorted; the projects that still exist
	CreatedBy  int64   // 0 when that admin was removed since
	CreatedAt  time.Time
	ExpiresAt  time.Time
	UsedAt     *time.Time
}

// NewInvite holds what CreateInvite stores.
type NewInvite struct {
	Email      string
	Role       string
	ProjectIDs []int64 // ignored for admins, who see every project
	TokenHash  string
	CreatedBy  int64
	Now        time.Time
	ExpiresAt  time.Time
}

// CreateInvite stores an invite. An address that has an account gets
// ErrEmailTaken; a project id that does not exist gives ErrUnknownProject.
// Earlier invites for the same address that were not used yet are removed,
// so only the newest link works. CreatedBy must still be an admin when the
// invite is written (ErrNotAdmin otherwise), so a request that was on its
// way while that admin was removed or made a member leaves no link behind.
func (db *DB) CreateInvite(ctx context.Context, ni NewInvite) (Invite, error) {
	inv := Invite{
		Email:     NormalizeEmail(ni.Email),
		Role:      ni.Role,
		CreatedBy: ni.CreatedBy,
		CreatedAt: msTime(ni.Now),
		ExpiresAt: msTime(ni.ExpiresAt),
	}
	if inv.Email == "" || !ValidRole(inv.Role) || ni.TokenHash == "" {
		return Invite{}, fmt.Errorf("invite needs an email, a valid role and a token, got %q and %q", inv.Email, inv.Role)
	}

	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return Invite{}, err
	}
	defer tx.Rollback() // no-op after Commit
	// The checks below (the maker is an admin, the address has no account,
	// earlier invites) hold until the commit: whoever changes accounts waits.
	if err := tx.lock(ctx, lockAccounts); err != nil {
		return Invite{}, err
	}

	var role string
	err = tx.QueryRowContext(ctx, "SELECT role FROM users WHERE id = ?", inv.CreatedBy).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && role != RoleAdmin) {
		return Invite{}, ErrNotAdmin
	}
	if err != nil {
		return Invite{}, err
	}

	var taken int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE email = ?", inv.Email).Scan(&taken); err != nil {
		return Invite{}, err
	}
	if taken > 0 {
		return Invite{}, ErrEmailTaken
	}
	inv.ProjectIDs = []int64{}
	if inv.Role == RoleMember {
		if inv.ProjectIDs, err = existingProjects(ctx, tx, ni.ProjectIDs); err != nil {
			return Invite{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM invites WHERE email = ? AND used_at IS NULL", inv.Email); err != nil {
		return Invite{}, err
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO invites (email, role, token_hash, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		inv.Email, inv.Role, ni.TokenHash, inv.CreatedBy, inv.CreatedAt.UnixMilli(), inv.ExpiresAt.UnixMilli()).Scan(&inv.ID)
	if err != nil {
		return Invite{}, fmt.Errorf("insert invite: %w", err)
	}
	for _, projectID := range inv.ProjectIDs {
		if _, err := tx.ExecContext(ctx, "INSERT INTO invite_projects (invite_id, project_id) VALUES (?, ?)", inv.ID, projectID); err != nil {
			return Invite{}, err
		}
	}
	return inv, tx.Commit()
}

const inviteColumns = "id, email, role, created_by, created_at, expires_at, used_at"

// scanInvite reads one invite row; its project ids are loaded separately.
func scanInvite(s scanner) (Invite, error) {
	var (
		inv               Invite
		createdBy, usedAt sql.NullInt64
		created, expires  int64
	)
	err := s.Scan(&inv.ID, &inv.Email, &inv.Role, &createdBy, &created, &expires, &usedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Invite{}, ErrNotFound
	}
	if err != nil {
		return Invite{}, err
	}
	inv.CreatedBy = createdBy.Int64
	inv.CreatedAt = time.UnixMilli(created).UTC()
	inv.ExpiresAt = time.UnixMilli(expires).UTC()
	if usedAt.Valid {
		used := time.UnixMilli(usedAt.Int64).UTC()
		inv.UsedAt = &used
	}
	return inv, nil
}

// Usable reports whether the invite can still be accepted at now.
func (inv Invite) Usable(now time.Time) bool {
	return inv.UsedAt == nil && now.Before(inv.ExpiresAt)
}

// InviteByTokenHash returns the invite with this token hash, used or not, or ErrNotFound.
func (db *DB) InviteByTokenHash(ctx context.Context, tokenHash string) (Invite, error) {
	inv, err := scanInvite(db.read.QueryRowContext(ctx, "SELECT "+inviteColumns+" FROM invites WHERE token_hash = ?", tokenHash))
	if err != nil {
		return Invite{}, err
	}
	inv.ProjectIDs, err = queryIDs(ctx, db.read, "SELECT project_id FROM invite_projects WHERE invite_id = ? ORDER BY project_id", inv.ID)
	return inv, err
}

// PendingInvites returns the invites that were neither used nor expired at now, newest first.
func (db *DB) PendingInvites(ctx context.Context, now time.Time) ([]Invite, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT "+inviteColumns+
		" FROM invites WHERE used_at IS NULL AND expires_at > ? ORDER BY created_at DESC, id DESC", now.UnixMilli())
	if err != nil {
		return nil, err
	}
	invites := []Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		invites = append(invites, inv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range invites {
		invites[i].ProjectIDs, err = queryIDs(ctx, db.read,
			"SELECT project_id FROM invite_projects WHERE invite_id = ? ORDER BY project_id", invites[i].ID)
		if err != nil {
			return nil, err
		}
	}
	return invites, nil
}

// DeleteInvite revokes an invite, or returns ErrNotFound.
func (db *DB) DeleteInvite(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, "DELETE FROM invites WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AcceptInvite turns an invite into an account with the invite's role and
// projects, and marks the invite used. It returns ErrNotFound for an unknown
// token, ErrGone for one that expired or was used, and ErrEmailTaken when
// the address got an account in the meantime.
func (db *DB) AcceptInvite(ctx context.Context, tokenHash, passwordHash string, now time.Time) (User, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback() // no-op after Commit
	// A link opened twice at once: the second waits here and finds it used.
	if err := tx.lock(ctx, lockAccounts); err != nil {
		return User{}, err
	}

	inv, err := scanInvite(tx.QueryRowContext(ctx, "SELECT "+inviteColumns+" FROM invites WHERE token_hash = ?", tokenHash))
	if err != nil {
		return User{}, err
	}
	if !inv.Usable(now) {
		return User{}, ErrGone
	}
	u, err := insertUser(ctx, tx, inv.Email, passwordHash, inv.Role, now)
	if err != nil {
		return User{}, err
	}
	if inv.Role == RoleMember {
		// Projects deleted since the invite was made are gone from invite_projects.
		_, err = tx.ExecContext(ctx, `INSERT INTO project_access (user_id, project_id)
			SELECT ?, project_id FROM invite_projects WHERE invite_id = ?`, u.ID, inv.ID)
		if err != nil {
			return User{}, err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE invites SET used_at = ? WHERE id = ?", now.UnixMilli(), inv.ID); err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
