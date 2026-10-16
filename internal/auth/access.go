package auth

import (
	"context"
	"sort"

	"monitor/internal/store"
)

// User is the person signed in for a request, with the projects they may see.
type User struct {
	ID    int64
	Email string
	Role  string // store.RoleAdmin or store.RoleMember
	// projects holds the ids of every project this user may see: all of
	// them for an admin, the ones in project_access for a member. It is
	// read once per request.
	projects map[int64]bool
}

// IsAdmin reports whether the user manages projects and users.
func (u User) IsAdmin() bool {
	return u.Role == store.RoleAdmin
}

// CanAccessProject reports whether user may see the project. A zero User
// (no one signed in) may see nothing. ctx is unused today; it is part of
// the signature so a later check can look something up.
func CanAccessProject(_ context.Context, user User, projectID int64) bool {
	return user.projects[projectID]
}

// AccessibleProjectIDs returns the ids of the projects user may see, sorted;
// empty for a zero User. Pass them to queries that span projects.
func AccessibleProjectIDs(_ context.Context, user User) []int64 {
	ids := make([]int64, 0, len(user.projects))
	for id := range user.projects {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// requestUser is what RequireUser puts in the request context.
type requestUser struct {
	user    User
	session store.Session
}

type contextKey struct{}

func withSignedIn(ctx context.Context, user User, session store.Session) context.Context {
	return context.WithValue(ctx, contextKey{}, requestUser{user: user, session: session})
}

// UserFrom returns the signed-in user of a request that passed
// RequireUser, or a zero User (who may see nothing) for any other context.
func UserFrom(ctx context.Context) User {
	s, _ := ctx.Value(contextKey{}).(requestUser)
	return s.user
}

// sessionFrom returns the session of a request that passed RequireUser.
func sessionFrom(ctx context.Context) (store.Session, bool) {
	s, ok := ctx.Value(contextKey{}).(requestUser)
	return s.session, ok
}

// loadUser reads which projects u may see.
func (s *Service) loadUser(ctx context.Context, u store.User) (User, error) {
	var ids []int64
	var err error
	if u.Role == store.RoleAdmin {
		ids, err = s.db.ProjectIDs(ctx)
	} else {
		ids, err = s.db.ProjectAccess(ctx, u.ID)
	}
	if err != nil {
		return User{}, err
	}
	user := User{ID: u.ID, Email: u.Email, Role: u.Role, projects: make(map[int64]bool, len(ids))}
	for _, id := range ids {
		user.projects[id] = true
	}
	return user, nil
}
