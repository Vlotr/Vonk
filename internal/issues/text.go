package issues

import (
	"monitor/internal/sentry"
	"monitor/internal/store"
)

// EventText is the text an issue shows when the stored event (its JSON,
// as the ingest writer stored it) is its newest: the same title, message
// and culprit as sentry.Summarize gives at ingest, and the event's level.
// store.DeleteUserData uses it for an issue whose newest event it deletes.
func EventText(data string) (store.IssueText, error) {
	ev, err := sentry.DecodeEvent([]byte(data))
	if err != nil {
		return store.IssueText{}, err
	}
	s := sentry.Summarize(ev)
	return store.IssueText{Title: s.Title, Message: s.Message, Culprit: s.Culprit, Level: ev.Level}, nil
}
