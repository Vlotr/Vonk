package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"monitor/internal/posthog"
	"monitor/internal/store"
	"monitor/internal/store/storetest"
)

// A value the database still refuses (put into the queued items by hand
// here, past every clean-up at parse time) loses only its own event: the
// writer splits the part until the refused event is alone, and drops,
// counts and logs only that one, without its values. The other events of
// the batch are stored once each (a resent event id stays a duplicate
// across the halves), their issue counts each event once, and their hours
// are marked for the aggregation job. SQLite stores these values, so there
// nothing is lost.
func TestWriter_ARefusedValueLosesOnlyItsEvent(t *testing.T) {
	env := newTestEnv(t, 100)
	webshop := env.project(t, "Webshop", 0)
	portal := env.project(t, "Customer portal", 0)
	received := t0.Add(time.Minute)

	var items []Item
	for i := 1; i <= 8; i++ {
		item := errorItem(t, webshop.ID, eventIDN(i), "charge", t0, received)
		item.Route = "/checkout"
		items = append(items, item)
		if i == 5 {
			bad := errorItem(t, portal.ID, eventIDN(9), "charge", t0, received)
			bad.Route = "/orders/secret\x00route"
			items = append(items, bad)
		}
	}
	items = append(items, items[0]) // event 1 resent, in the other half
	page := func(projectID int64, id, session, user string) Item {
		return Item{ProjectID: projectID, ReceivedAt: received, Path: "/checkout", Route: "/checkout",
			Page: &posthog.Event{Name: posthog.EventPageview, UUID: id, SessionID: session, WindowID: "w1", DistinctID: user}}
	}
	// Random, so Postgres cannot compress it below its index entry limit.
	longUser := make([]byte, 3000)
	rand.Read(longUser) // never fails; see crypto/rand
	custom := func(projectID int64, id, name string) Item {
		return Item{ProjectID: projectID, ReceivedAt: received, Path: "/checkout", Route: "/checkout",
			Custom: &posthog.Event{Name: name, UUID: id, DistinctID: "42"}}
	}
	items = append(items,
		page(webshop.ID, "v1", "s1", "42"), page(portal.ID, "v2", "secret\x00session", "42"), page(webshop.ID, "v3", "s3", "42"),
		page(portal.ID, "v4", "s4", "secret"+hex.EncodeToString(longUser)),
		custom(webshop.ID, "c1", "checkout_started"), custom(portal.ID, "c2", "secret\x00name"), custom(webshop.ID, "c3", "checkout_started"))
	enqueue(t, env, items...)

	hooks := &recordingHooks{}
	w := NewWriter(env.db, env.queue, hooks, env.logger)
	w.retryDelays = []time.Duration{time.Millisecond}
	w.Close()

	ctx := context.Background()
	db := env.rawDB(t)
	count := func(table string, projectID int64) int {
		t.Helper()
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id = $1", projectID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count("error_events", webshop.ID); n != 8 {
		t.Errorf("Webshop stored %d error events, want 8", n)
	}
	if n, m := count("pageviews", webshop.ID), count("custom_events", webshop.ID); n != 2 || m != 2 {
		t.Errorf("Webshop stored %d page views and %d custom events, want 2 and 2", n, m)
	}
	var timesSeen int
	if err := db.QueryRow("SELECT times_seen FROM issues WHERE project_id = $1", webshop.ID).Scan(&timesSeen); err != nil || timesSeen != 8 {
		t.Errorf("Webshop's issue: times_seen %d (%v), want 8: each stored event counted once", timesSeen, err)
	}
	hours, err := env.db.DirtyHours(ctx)
	if err != nil {
		t.Fatal(err)
	}
	marked := map[int64]bool{}
	for _, h := range hours {
		marked[h.ProjectID] = true
	}
	if !marked[webshop.ID] {
		t.Errorf("Webshop's hour is not marked for the aggregation job: %v", hours)
	}

	st := w.Stats()
	logs := env.logs.String()
	if !storetest.Postgres() {
		if st.LostEvents != 0 || count("error_events", portal.ID) != 1 || count("pageviews", portal.ID) != 2 || count("custom_events", portal.ID) != 1 {
			t.Errorf("SQLite stores these values: lost %d (%s)", st.LostEvents, st.LastError)
		}
		if hooks.createdCount() != 2 {
			t.Errorf("OnIssueCreated called %d times, want 2 (one issue per project)", hooks.createdCount())
		}
		return
	}
	for _, table := range []string{"error_events", "pageviews", "custom_events"} {
		if n := count(table, portal.ID); n != 0 {
			t.Errorf("Customer portal has %d rows in %s, want 0 (Postgres refuses a NUL and a 6 KB index entry)", n, table)
		}
	}
	if hooks.createdCount() != 1 {
		t.Errorf("OnIssueCreated called %d times, want 1", hooks.createdCount())
	}
	if st.LostEvents != 4 || st.FailedBatches != 4 || !strings.Contains(st.LastError, "SQLSTATE 22021") {
		t.Errorf("Stats = %+v, want 4 lost events, one per refused value, with the SQLSTATE", st)
	}
	if !strings.Contains(logs, "SQLSTATE 54000") {
		t.Errorf("the 6 KB user id is not logged as SQLSTATE 54000:\n%s", logs)
	}
	if n := strings.Count(logs, "level=ERROR"); n != 4 {
		t.Errorf("%d error lines, want one per dropped event:\n%s", n, logs)
	}
	if strings.Contains(logs, "retrying") {
		t.Errorf("a refused value was tried again as it was:\n%s", logs)
	}
	for _, secret := range []string{"secret", "orders"} {
		if strings.Contains(logs, secret) || strings.Contains(st.LastError, secret) {
			t.Errorf("the log or the admin alert shows a value of the dropped event (%q):\n%s\n%s", secret, logs, st.LastError)
		}
	}
}

// savePart splits a part only as far as it must: down to the refused
// event, with every other event in exactly one write that succeeded.
func TestSavePart_SplitsUntilTheRefusedEventIsAlone(t *testing.T) {
	env := newTestEnv(t, 100)
	p := env.project(t, "Webshop", 0)
	w := NewWriter(env.db, env.queue, &recordingHooks{}, env.logger)
	ctx := context.Background()

	var events []store.NewErrorEvent
	for i := 1; i <= 10; i++ {
		ev, err := errorEvent(errorItem(t, p.ID, eventIDN(i), "charge", t0, t0))
		if err != nil {
			t.Fatal(err)
		}
		ev.Route = "/checkout"
		events = append(events, ev)
	}
	events = append(events, events[0]) // a resend at the end, in the other half
	refused := eventIDN(4)

	var (
		writes int
		total  store.SaveResult
	)
	savePart(w, "error event", events, time.Now().Add(time.Minute), func(part []store.NewErrorEvent) error {
		writes++
		for _, ev := range part {
			if ev.EventID == refused {
				return fmt.Errorf("event %s: %w", ev.EventID, &pgconn.PgError{Code: "22021", Message: `invalid byte sequence for encoding "UTF8": 0x00`})
			}
		}
		r, err := env.db.SaveErrorEvents(ctx, part)
		if err == nil {
			total.Stored += r.Stored
			total.Duplicates += r.Duplicates
			total.NewIssues = append(total.NewIssues, r.NewIssues...)
		}
		return err
	})

	if total.Stored != 9 || total.Duplicates != 1 || len(total.NewIssues) != 1 {
		t.Errorf("stored %d, duplicates %d, new issues %d; want 9, 1, 1", total.Stored, total.Duplicates, len(total.NewIssues))
	}
	// 11 events: the whole part, then its halves (5 and 6 events), then
	// the halves of the one with the refused event only, down to it alone
	// (5 → 2 and 3, 3 → 1 and 2, 2 → 1 and 1): 1 + 2 + 2 + 2 + 2 = 9 writes.
	if writes != 9 {
		t.Errorf("%d writes, want 9", writes)
	}
	var timesSeen int
	if err := env.rawDB(t).QueryRow("SELECT times_seen FROM issues WHERE project_id = $1", p.ID).Scan(&timesSeen); err != nil || timesSeen != 9 {
		t.Errorf("times_seen = %d (%v), want 9", timesSeen, err)
	}
	if st := w.Stats(); st.LostEvents != 1 || st.FailedBatches != 1 {
		t.Errorf("Stats = %+v, want the one refused event lost", st)
	}
	if hours, _ := env.db.DirtyHours(ctx); len(hours) != 1 {
		t.Errorf("dirty hours = %v, want the hour of the stored events", hours)
	}
}
