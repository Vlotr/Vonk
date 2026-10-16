package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a signed-in browser. The cookie holds a random token; the
// database holds only its hash (see internal/auth).
type Session struct {
	ID        int64
	UserID    int64
	CSRFToken string
	CreatedAt time.Time
	ExpiresAt time.Time
}

// CreateSession stores a new session for a user.
func (db *DB) CreateSession(ctx context.Context, userID int64, tokenHash, csrfToken string, now, expiresAt time.Time) (Session, error) {
	s := Session{UserID: userID, CSRFToken: csrfToken, CreatedAt: msTime(now), ExpiresAt: msTime(expiresAt)}
	err := db.write.QueryRowContext(ctx, `
		INSERT INTO sessions (user_id, token_hash, csrf_token, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?) RETURNING id`,
		userID, tokenHash, csrfToken, s.CreatedAt.UnixMilli(), s.ExpiresAt.UnixMilli()).Scan(&s.ID)
	return s, err
}

// SessionByTokenHash returns the session with this token hash and its user,
// or ErrNotFound. The caller checks ExpiresAt.
func (db *DB) SessionByTokenHash(ctx context.Context, tokenHash string) (Session, User, error) {
	var (
		s                Session
		u                User
		created, expires int64
		userCreated      int64
	)
	err := db.read.QueryRowContext(ctx, `
		SELECT s.id, s.user_id, s.csrf_token, s.created_at, s.expires_at,
			u.id, u.email, u.password_hash, u.role, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.token_hash = ?`, tokenHash).Scan(
		&s.ID, &s.UserID, &s.CSRFToken, &created, &expires,
		&u.ID, &u.Email, &u.PasswordHash, &u.Role, &userCreated)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, err
	}
	s.CreatedAt = time.UnixMilli(created).UTC()
	s.ExpiresAt = time.UnixMilli(expires).UTC()
	u.CreatedAt = time.UnixMilli(userCreated).UTC()
	return s, u, nil
}

// ExtendSession moves a session's expiry.
func (db *DB) ExtendSession(ctx context.Context, id int64, expiresAt time.Time) error {
	_, err := db.write.ExecContext(ctx, "UPDATE sessions SET expires_at = ? WHERE id = ?", expiresAt.UnixMilli(), id)
	return err
}

// DeleteSession signs one session out.
func (db *DB) DeleteSession(ctx context.Context, id int64) error {
	_, err := db.write.ExecContext(ctx, "DELETE FROM sessions WHERE id = ?", id)
	return err
}

// DeleteExpiredSessions removes the sessions that expired before now and
// returns how many there were.
func (db *DB) DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := db.write.ExecContext(ctx, "DELETE FROM sessions WHERE expires_at <= ?", now.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
