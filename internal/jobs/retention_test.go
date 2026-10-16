package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/store"
)

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

func openDB(t *testing.T) *store.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(context.Background(), config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func projectKeeping(t *testing.T, db *store.DB, name string, days int) store.Project {
	t.Helper()
	np := store.NewProject{Name: name, RetentionDays: days, KeepForever: days == 0}
	p, err := db.CreateProject(context.Background(), np)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// saveEvents stores n events of one issue (fingerprint) in the project,
// all at the same age.
func saveEvents(t *testing.T, db *store.DB, projectID int64, fingerprint string, age time.Duration, n int) {
	t.Helper()
	at := testNow.Add(-age)
	events := make([]store.NewErrorEvent, n)
	for i := range events {
		events[i] = store.NewErrorEvent{
			ProjectID: projectID, EventID: fmt.Sprintf("%s-%d-%d", fingerprint, age/time.Hour, i), Fingerprint: fingerprint,
			Title: fingerprint, Timestamp: at, ReceivedAt: at, Data: "{}",
		}
	}
	if _, err := db.SaveErrorEvents(context.Background(), events); err != nil {
		t.Fatal(err)
	}
}

func issueTitles(t *testing.T, db *store.DB, projectID int64) map[string]bool {
	t.Helper()
	list, err := db.ListIssues(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]bool{}
	for _, i := range list {
		titles[i.Title] = true
	}
	return titles
}

func countEvents(t *testing.T, db *store.DB, projectID int64) int {
	t.Helper()
	n, err := db.CountErrorEvents(context.Background(), projectID)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

const day = 24 * time.Hour

func TestRetention_DeletesOnlyOldRowsOfThatProject(t *testing.T) {
	db := openDB(t)
	short := projectKeeping(t, db, "Short", 30)
	long := projectKeeping(t, db, "Long", 90)

	// Short: an issue only seen 40 days ago (goes whole), and an issue with
	// events 40 and 2 days ago (keeps the issue and the recent events).
	saveEvents(t, db, short.ID, "old", 40*day, 7)
	saveEvents(t, db, short.ID, "busy", 40*day, 5)
	saveEvents(t, db, short.ID, "busy", 2*day, 3)
	// Long: the same ages are all within 90 days.
	saveEvents(t, db, long.ID, "old", 40*day, 7)
	saveEvents(t, db, long.ID, "busy", 2*day, 3)
	saveEvents(t, db, long.ID, "ancient", 100*day, 2)

	// A batch of 4 makes several rounds per project.
	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }, BatchSize: 4}
	r.Run(context.Background(), testNow)

	if got := countEvents(t, db, short.ID); got != 3 {
		t.Errorf("Short keeps %d events, want the 3 of the last 30 days", got)
	}
	if got := issueTitles(t, db, short.ID); !got["busy"] || got["old"] || len(got) != 1 {
		t.Errorf("Short issues = %v, want only busy", got)
	}
	if got := countEvents(t, db, long.ID); got != 10 {
		t.Errorf("Long keeps %d events, want 10 (7 + 3, not the 100-day-old ones)", got)
	}
	if got := issueTitles(t, db, long.ID); !got["old"] || !got["busy"] || got["ancient"] {
		t.Errorf("Long issues = %v, want old and busy", got)
	}
}

func TestRetention_UnlimitedKeepsAll(t *testing.T) {
	db := openDB(t)
	forever := projectKeeping(t, db, "Forever", 0)
	saveEvents(t, db, forever.ID, "old", 3*365*day, 5)
	saveEvents(t, db, forever.ID, "new", time.Hour, 1)

	r := &Retention{DB: db, Log: quietLogger(), Now: func() time.Time { return testNow }}
	r.Run(context.Background(), testNow)

	if got := countEvents(t, db, forever.ID); got != 6 {
		t.Errorf("%d events kept, want all 6", got)
	}
	if got := issueTitles(t, db, forever.ID); len(got) != 2 {
		t.Errorf("issues = %v, want both", got)
	}
}
