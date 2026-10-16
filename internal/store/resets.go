package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PasswordReset is a link that lets a user choose a new password, once,
// until ExpiresAt.
type PasswordReset struct {
	ID        int64
	UserID    int64
	CreatedAt time.Time
	ExpiresAt time.Time
	UsedAt    *time.Time
}

// Usable reports whether the link can still be used at now.
func (pr PasswordReset) Usable(now time.Time) bool {
	return pr.UsedAt == nil && now.Before(pr.ExpiresAt)
}

// CreatePasswordReset stores a reset link for a user. Earlier links of that
// user that were not used are removed, so only the newest works.
func (db *DB) CreatePasswordReset(ctx context.Context, userID int64, tokenHash string, now, expiresAt time.Time) error {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	if _, err := tx.ExecContext(ctx, "DELETE FROM password_resets WHERE user_id = ? AND used_at IS NULL", userID); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO password_resets (user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)",
		userID, tokenHash, now.UnixMilli(), expiresAt.UnixMilli())
	if err != nil {
		return fmt.Errorf("insert password reset: %w", err)
	}
	return tx.Commit()
}

const resetColumns = "r.id, r.user_id, r.created_at, r.expires_at, r.used_at"

func scanReset(s scanner, extra ...any) (PasswordReset, error) {
	var (
		pr               PasswordReset
		created, expires int64
		usedAt           sql.NullInt64
	)
	err := s.Scan(append([]any{&pr.ID, &pr.UserID, &created, &expires, &usedAt}, extra...)...)
	if errors.Is(err, sql.ErrNoRows) {
		return PasswordReset{}, ErrNotFound
	}
	if err != nil {
		return PasswordReset{}, err
	}
	pr.CreatedAt = time.UnixMilli(created).UTC()
	pr.ExpiresAt = time.UnixMilli(expires).UTC()
	if usedAt.Valid {
		used := time.UnixMilli(usedAt.Int64).UTC()
		pr.UsedAt = &used
	}
	return pr, nil
}

// PasswordResetByTokenHash returns the reset link with this token hash and
// its user, used or not, or ErrNotFound.
func (db *DB) PasswordResetByTokenHash(ctx context.Context, tokenHash string) (PasswordReset, User, error) {
	var u User
	var userCreated int64
	pr, err := scanReset(db.read.QueryRowContext(ctx, `
		SELECT `+resetColumns+`, u.id, u.email, u.password_hash, u.role, u.created_at
		FROM password_resets r JOIN users u ON u.id = r.user_id
		WHERE r.token_hash = ?`, tokenHash),
		&u.ID, &u.Email, &u.PasswordHash, &u.Role, &userCreated)
	if err != nil {
		return PasswordReset{}, User{}, err
	}
	u.CreatedAt = time.UnixMilli(userCreated).UTC()
	return pr, u, nil
}

// UsePasswordReset sets a new password with a reset link, marks the link
// used and signs the user out everywhere. It returns the user as it is
// afterwards, ErrNotFound for an unknown token, or ErrGone for a link that
// expired or was used.
func (db *DB) UsePasswordReset(ctx context.Context, tokenHash, passwordHash string, now time.Time) (User, error) {
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback() // no-op after Commit

	pr, err := scanReset(tx.QueryRowContext(ctx, "SELECT "+resetColumns+" FROM password_resets r WHERE r.token_hash = ?", tokenHash))
	if err != nil {
		return User{}, err
	}
	if !pr.Usable(now) {
		return User{}, ErrGone
	}
	steps := []struct {
		query string
		args  []any
	}{
		{"UPDATE password_resets SET used_at = ? WHERE id = ?", []any{now.UnixMilli(), pr.ID}},
		{"UPDATE users SET password_hash = ? WHERE id = ?", []any{passwordHash, pr.UserID}},
		{"DELETE FROM sessions WHERE user_id = ?", []any{pr.UserID}},
	}
	for _, step := range steps {
		if _, err := tx.ExecContext(ctx, step.query, step.args...); err != nil {
			return User{}, err
		}
	}
	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", pr.UserID))
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}
