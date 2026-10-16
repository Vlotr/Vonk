// Package issues holds what happens around issues outside the database. For
// now that is the Hooks the ingest writer calls; Task 4 adds status changes
// and regressions, Task 6 the notifier that implements Hooks.
package issues

import (
	"context"

	"monitor/internal/store"
)

// Hooks is told about issue changes the ingest writer makes. The writer calls
// them on its own goroutine, after the batch is committed, so they must
// return quickly: hand slow work such as sending mail to another goroutine.
type Hooks interface {
	// OnIssueCreated is called once for every new issue.
	OnIssueCreated(ctx context.Context, issue store.Issue)
	// OnIssueRegressed is called when a resolved issue gets a new event and opens again.
	OnIssueRegressed(ctx context.Context, issue store.Issue)
}

// NoHooks does nothing. It is the default until notifications exist.
type NoHooks struct{}

// OnIssueCreated does nothing.
func (NoHooks) OnIssueCreated(context.Context, store.Issue) {}

// OnIssueRegressed does nothing.
func (NoHooks) OnIssueRegressed(context.Context, store.Issue) {}
