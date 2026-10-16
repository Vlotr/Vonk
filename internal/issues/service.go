package issues

import (
	"context"
	"errors"
	"time"

	"monitor/internal/store"
)

// ErrInvalidStatus is returned for a status other than open, resolved or ignored.
var ErrInvalidStatus = errors.New(`status must be "open", "resolved" or "ignored"`)

// Service changes issues on behalf of people using the dashboard. The
// ingest writer changes them too (new events, regressions); both write
// through the database's single write connection, so they never interleave.
type Service struct {
	db  *store.DB
	now func() time.Time
}

// NewService returns the service; now is the clock (time.Now outside tests).
func NewService(db *store.DB, now func() time.Time) *Service {
	return &Service{db: db, now: now}
}

// SetStatus sets an issue's status to open, resolved or ignored and returns
// the issue as it is afterwards. A resolved issue opens again by itself when
// a new event arrives; an ignored one stays ignored. It returns
// ErrInvalidStatus for any other status and store.ErrNotFound for an
// unknown issue.
func (s *Service) SetStatus(ctx context.Context, issueID int64, status string) (store.Issue, error) {
	if !store.ValidIssueStatus(status) {
		return store.Issue{}, ErrInvalidStatus
	}
	return s.db.SetIssueStatus(ctx, issueID, status, s.now())
}
