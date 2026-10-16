package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// at returns a fixed time plus d, truncated to milliseconds like the database.
func at(d time.Duration) time.Time {
	return time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC).Add(d)
}

func newEvent(projectID int64, eventID, fingerprint string, ts time.Time) NewErrorEvent {
	return NewErrorEvent{
		ProjectID:   projectID,
		EventID:     eventID,
		Fingerprint: fingerprint,
		Title:       "RuntimeException",
		Message:     "Checkout failed",
		Culprit:     "PaymentGateway.php:12",
		Source:      "laravel",
		Level:       "error",
		Timestamp:   ts,
		ReceivedAt:  ts.Add(time.Second),
		UserID:      "42",
		URL:         "https://shop.example.com/checkout",
		Release:     "webshop@1.4.2",
		Environment: "production",
		Platform:    "php",
		Data:        `{"event_id":"` + eventID + `"}`,
	}
}

func eventID(n int) string {
	return fmt.Sprintf("%032x", n)
}

func TestSaveErrorEvents_GroupsByFingerprint(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p, err := db.CreateProject(ctx, NewProject{Name: "Webshop"})
	if err != nil {
		t.Fatal(err)
	}

	later := newEvent(p.ID, eventID(2), "fp-a", at(time.Hour))
	later.Message = "Checkout failed again"
	later.Culprit = "PaymentGateway.php:14"
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{
		newEvent(p.ID, eventID(1), "fp-a", at(0)),
		later,
		newEvent(p.ID, eventID(3), "fp-b", at(time.Minute)),
	})
	if err != nil {
		t.Fatalf("SaveErrorEvents: %v", err)
	}
	if res.Stored != 3 || res.Duplicates != 0 || len(res.NewIssues) != 2 {
		t.Fatalf("result = %+v, want 3 stored and 2 new issues", res)
	}

	issues, err := db.ListIssues(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 2 {
		t.Fatalf("got %d issues, want 2", len(issues))
	}
	a := issues[0] // newest last_seen first
	if a.Fingerprint != "fp-a" || a.TimesSeen != 2 || !a.FirstSeen.Equal(at(0)) || !a.LastSeen.Equal(at(time.Hour)) {
		t.Errorf("issue a = %+v", a)
	}
	if a.Status != IssueOpen || a.Title != "RuntimeException" || a.Source != "laravel" || a.Level != "error" {
		t.Errorf("issue a fields = %+v", a)
	}
	if a.Message != "Checkout failed again" || a.Culprit != "PaymentGateway.php:14" {
		t.Errorf("issue a message/culprit = %q %q, want those of the newest event", a.Message, a.Culprit)
	}
	if res.NewIssues[0].Fingerprint != "fp-a" || res.NewIssues[0].TimesSeen != 2 {
		t.Errorf("NewIssues[0] = %+v, want fp-a as it is after the batch", res.NewIssues[0])
	}

	stored, err := db.ErrorEventByEventID(ctx, p.ID, eventID(2))
	if err != nil {
		t.Fatal(err)
	}
	if stored.IssueID != a.ID || !stored.Timestamp.Equal(at(time.Hour)) || !stored.ReceivedAt.Equal(at(time.Hour+time.Second)) {
		t.Errorf("stored event = %+v", stored)
	}
	if stored.UserID != "42" || stored.URL != "https://shop.example.com/checkout" || stored.Release != "webshop@1.4.2" ||
		stored.Environment != "production" || stored.Platform != "php" || stored.Data != `{"event_id":"`+eventID(2)+`"}` {
		t.Errorf("stored event fields = %+v", stored)
	}
}

func TestSaveErrorEvents_OlderEventKeepsLatestFields(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p, _ := db.CreateProject(ctx, NewProject{Name: "Webshop"})

	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(time.Hour))}); err != nil {
		t.Fatal(err)
	}
	late := newEvent(p.ID, eventID(2), "fp", at(0)) // arrives later, happened earlier
	late.Message = "older message"
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{late})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.NewIssues) != 0 {
		t.Errorf("NewIssues = %v, want none", res.NewIssues)
	}

	issues, _ := db.ListIssues(ctx, p.ID)
	if len(issues) != 1 {
		t.Fatalf("got %d issues", len(issues))
	}
	got := issues[0]
	if !got.FirstSeen.Equal(at(0)) || !got.LastSeen.Equal(at(time.Hour)) || got.TimesSeen != 2 || got.Message != "Checkout failed" {
		t.Errorf("issue = %+v, want first_seen moved back and the newest message kept", got)
	}
}

func TestSaveErrorEvents_DedupesEventID(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p, _ := db.CreateProject(ctx, NewProject{Name: "Webshop"})
	other, _ := db.CreateProject(ctx, NewProject{Name: "Admin"})

	ev := newEvent(p.ID, eventID(1), "fp", at(0))
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{ev, ev}) // twice in one batch
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 1 || res.Duplicates != 1 {
		t.Errorf("first batch = %+v, want 1 stored, 1 duplicate", res)
	}
	res, err = db.SaveErrorEvents(ctx, []NewErrorEvent{ev}) // an SDK retry later
	if err != nil {
		t.Fatal(err)
	}
	if res.Stored != 0 || res.Duplicates != 1 {
		t.Errorf("second batch = %+v, want 1 duplicate", res)
	}

	// The same id in another project is another event.
	res, err = db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(other.ID, eventID(1), "fp", at(0))})
	if err != nil || res.Stored != 1 {
		t.Errorf("other project: %+v, %v; want stored", res, err)
	}

	issues, _ := db.ListIssues(ctx, p.ID)
	if len(issues) != 1 || issues[0].TimesSeen != 1 {
		t.Errorf("issues = %+v, want one with times_seen 1", issues)
	}
	if n, _ := db.CountErrorEvents(ctx, p.ID); n != 1 {
		t.Errorf("CountErrorEvents = %d, want 1", n)
	}
}

func TestSaveErrorEvents_SkipsDeletedProject(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p, _ := db.CreateProject(ctx, NewProject{Name: "Webshop"})

	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{
		newEvent(999, eventID(1), "fp", at(0)), // project gone while the event was queued
		newEvent(p.ID, eventID(2), "fp", at(0)),
	})
	if err != nil {
		t.Fatalf("SaveErrorEvents: %v", err)
	}
	if res.Stored != 1 || res.Orphaned != 1 {
		t.Errorf("result = %+v, want 1 stored and 1 orphaned", res)
	}
}
