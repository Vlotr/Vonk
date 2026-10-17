package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Roles. Admins manage projects and users and see every project; members
// see only the projects in project_access.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// ValidRole reports whether role is admin or member.
func ValidRole(role string) bool {
	return role == RoleAdmin || role == RoleMember
}

var (
	// ErrUsersExist is returned by CreateFirstAdmin once any account exists.
	ErrUsersExist = errors.New("an account already exists")
	// ErrEmailTaken is returned for an invite to an address that has an account.
	ErrEmailTaken = errors.New("this email address already has an account")
	// ErrLastAdmin is returned when a change would leave no admin.
	ErrLastAdmin = errors.New("this is the last admin")
	// ErrGone is returned for an invite or reset link that expired or was used.
	ErrGone = errors.New("expired or already used")
	// ErrUnknownProject is returned when a list of project ids names a project that does not exist.
	ErrUnknownProject = errors.New("unknown project")
	// ErrNotAdmin is returned by CreateInvite when the user making the
	// invite is no longer an admin (or no longer exists).
	ErrNotAdmin = errors.New("only an admin can do this")
)

// User is an account that can sign in to the dashboard.
type User struct {
	ID    int64
	Email string // trimmed and lowercase
	// PasswordHash is the argon2id hash in PHC form (see internal/auth).
	PasswordHash string
	Role         string // RoleAdmin or RoleMember
	CreatedAt    time.Time
}

// NormalizeEmail trims an address and makes it lowercase, the form it is stored in.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

const userColumns = "id, email, password_hash, role, created_at"

func scanUser(s scanner) (User, error) {
	var u User
	var createdAt int64
	err := s.Scan(&u.ID, &u.Email, &u.PasswordHash, &u.Role, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	u.CreatedAt = time.UnixMilli(createdAt).UTC()
	return u, err
}

// CountUsers returns the number of accounts.
func (db *DB) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n)
	return n, err
}

// CreateFirstAdmin creates the first account, an admin. Once any account
// exists it returns ErrUsersExist: registration closes after the first user.
func (db *DB) CreateFirstAdmin(ctx context.Context, email, passwordHash string, now time.Time) (User, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback() // no-op after Commit

	// Checked inside the write transaction, after the accounts lock, so two
	// setups at once cannot both win.
	if err := tx.lock(ctx, lockAccounts); err != nil {
		return User{}, err
	}
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users").Scan(&n); err != nil {
		return User{}, err
	}
	if n > 0 {
		return User{}, ErrUsersExist
	}
	u, err := insertUser(ctx, tx, email, passwordHash, RoleAdmin, now)
	if err != nil {
		return User{}, err
	}
	return u, tx.Commit()
}

func insertUser(ctx context.Context, tx *writeTx, email, passwordHash, role string, now time.Time) (User, error) {
	u := User{Email: NormalizeEmail(email), PasswordHash: passwordHash, Role: role, CreatedAt: msTime(now)}
	if u.Email == "" || !ValidRole(role) {
		return User{}, fmt.Errorf("insert user: need an email and a valid role, got %q and %q", u.Email, role)
	}
	var taken int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE email = ?", u.Email).Scan(&taken); err != nil {
		return User{}, err
	}
	if taken > 0 {
		return User{}, ErrEmailTaken
	}
	err := tx.QueryRowContext(ctx,
		"INSERT INTO users (email, password_hash, role, created_at) VALUES (?, ?, ?, ?) RETURNING id",
		u.Email, u.PasswordHash, u.Role, u.CreatedAt.UnixMilli()).Scan(&u.ID)
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

// UserByID returns the account with this id, or ErrNotFound.
func (db *DB) UserByID(ctx context.Context, id int64) (User, error) {
	return scanUser(db.read.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
}

// UserByEmail returns the account with this address (any case), or ErrNotFound.
func (db *DB) UserByEmail(ctx context.Context, email string) (User, error) {
	return scanUser(db.read.QueryRowContext(ctx,
		"SELECT "+userColumns+" FROM users WHERE email = ?", NormalizeEmail(email)))
}

// ListUsers returns every account, sorted by email.
func (db *DB) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT "+userColumns+" FROM users ORDER BY email")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		users = append(users, u)
	}
	return users, rows.Err()
}

// ProjectAccess returns the ids of the projects a member was given, sorted.
// Admins have none: they see every project.
func (db *DB) ProjectAccess(ctx context.Context, userID int64) ([]int64, error) {
	return queryIDs(ctx, db.read, "SELECT project_id FROM project_access WHERE user_id = ? ORDER BY project_id", userID)
}

// ProjectAccessByUser returns every user's project ids, sorted; users without any are left out.
func (db *DB) ProjectAccessByUser(ctx context.Context) (map[int64][]int64, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT user_id, project_id FROM project_access ORDER BY user_id, project_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	access := map[int64][]int64{}
	for rows.Next() {
		var userID, projectID int64
		if err := rows.Scan(&userID, &projectID); err != nil {
			return nil, err
		}
		access[userID] = append(access[userID], projectID)
	}
	return access, rows.Err()
}

// UserChange is an edit of a user; nil fields stay as they are.
type UserChange struct {
	Role *string
	// ProjectIDs replaces a member's projects. It is ignored for admins,
	// who see every project.
	ProjectIDs *[]int64
}

// UpdateUser changes a user's role and projects in one transaction and
// returns the user as it is afterwards. Making a user admin removes their
// project rows; making an admin a member removes the invites they made
// that were not used yet. It returns ErrNotFound, ErrLastAdmin when the
// last admin would become a member, or ErrUnknownProject.
func (db *DB) UpdateUser(ctx context.Context, id int64, change UserChange) (User, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback() // no-op after Commit
	// A change of role or projects writes access and invite rows of projects.
	if err := lockAccountChange(ctx, tx); err != nil {
		return User{}, err
	}

	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
	if err != nil {
		return User{}, err
	}

	if change.Role != nil && *change.Role != u.Role {
		role := *change.Role
		if !ValidRole(role) {
			return User{}, fmt.Errorf("unknown role %q", role)
		}
		if u.Role == RoleAdmin {
			if err := checkOtherAdmin(ctx, tx, u.ID); err != nil {
				return User{}, err
			}
			if err := dropPendingInvites(ctx, tx, u.ID); err != nil {
				return User{}, err
			}
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", role, u.ID); err != nil {
			return User{}, err
		}
		if role == RoleAdmin {
			if _, err := tx.ExecContext(ctx, "DELETE FROM project_access WHERE user_id = ?", u.ID); err != nil {
				return User{}, err
			}
		}
		u.Role = role
	}

	if change.ProjectIDs != nil && u.Role == RoleMember {
		if err := setProjectAccess(ctx, tx, u.ID, *change.ProjectIDs); err != nil {
			return User{}, err
		}
	}
	return u, tx.Commit()
}

// DeleteUser removes an account with its sessions, project rows and the
// invites it made that were not used yet. It returns ErrNotFound, or
// ErrLastAdmin for the last admin.
func (db *DB) DeleteUser(ctx context.Context, id int64) error {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit
	// The user's access rows and invites (with their projects) go too.
	if err := lockAccountChange(ctx, tx); err != nil {
		return err
	}

	u, err := scanUser(tx.QueryRowContext(ctx, "SELECT "+userColumns+" FROM users WHERE id = ?", id))
	if err != nil {
		return err
	}
	if u.Role == RoleAdmin {
		if err := checkOtherAdmin(ctx, tx, u.ID); err != nil {
			return err
		}
	}
	if err := dropPendingInvites(ctx, tx, u.ID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM users WHERE id = ?", u.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// checkOtherAdmin returns ErrLastAdmin unless an admin other than userID
// exists. It runs inside the write transaction, after the accounts lock, so
// two admins demoting each other at the same moment cannot both succeed.
func checkOtherAdmin(ctx context.Context, tx *writeTx, userID int64) error {
	var others int
	err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM users WHERE role = ? AND id <> ?", RoleAdmin, userID).Scan(&others)
	if err != nil {
		return err
	}
	if others == 0 {
		return ErrLastAdmin
	}
	return nil
}

// dropPendingInvites deletes the unused invites userID made. Whoever made an
// invite has seen its link; once they lose their admin role (or their
// account) the link must not let them, or anyone they gave it to, back in.
func dropPendingInvites(ctx context.Context, tx *writeTx, userID int64) error {
	_, err := tx.ExecContext(ctx, "DELETE FROM invites WHERE created_by = ? AND used_at IS NULL", userID)
	return err
}

// setProjectAccess replaces a user's projects with projectIDs, which must all exist.
func setProjectAccess(ctx context.Context, tx *writeTx, userID int64, projectIDs []int64) error {
	ids, err := existingProjects(ctx, tx, projectIDs)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM project_access WHERE user_id = ?", userID); err != nil {
		return err
	}
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, "INSERT INTO project_access (user_id, project_id) VALUES (?, ?)", userID, id); err != nil {
			return err
		}
	}
	return nil
}

func uniqueSorted(ids []int64) []int64 {
	out := make([]int64, 0, len(ids))
	seen := map[int64]bool{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ChangePassword stores a new password hash and signs the user out
// everywhere except the session keepSessionID (the one making the change).
func (db *DB) ChangePassword(ctx context.Context, userID int64, passwordHash string, keepSessionID int64) error {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after Commit

	res, err := tx.ExecContext(ctx, "UPDATE users SET password_hash = ? WHERE id = ?", passwordHash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM sessions WHERE user_id = ? AND id <> ?", userID, keepSessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// querier is what pools and transactions share for queries.
type querier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// queryIDs runs a query that selects one id column and returns the ids.
func queryIDs(ctx context.Context, q querier, query string, args ...any) ([]int64, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
