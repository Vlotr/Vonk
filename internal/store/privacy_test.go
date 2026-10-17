package store

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// userEvent is an error event of user in issue fp, at at, with a route and
// a message the test can find again (in the stored JSON too).
func userEvent(projectID int64, n int, fp, user, message string, ts time.Time) NewErrorEvent {
	ev := newEvent(projectID, eventID(n), fp, ts)
	ev.UserID = user
	ev.Route = "/checkout"
	ev.Title = "RuntimeException"
	ev.Message = message
	data, _ := json.Marshal(map[string]string{"event_id": ev.EventID, "message": message})
	ev.Data = string(data)
	return ev
}

// describeTest reads the issue text of the test's events: the message in their JSON.
func describeTest(data string) (IssueText, error) {
	var ev struct{ Message string }
	if err := json.Unmarshal([]byte(data), &ev); err != nil {
		return IssueText{}, err
	}
	return IssueText{Title: "RuntimeException", Message: ev.Message, Culprit: "PaymentGateway.php:12", Level: "error"}, nil
}

// rebuildDirtyHours does what the aggregation job does every minute.
func rebuildDirtyHours(t *testing.T, db *DB) []ProjectHour {
	t.Helper()
	ctx := context.Background()
	hours, err := db.DirtyHours(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, ph := range hours {
		if err := db.RecomputeHour(ctx, ph.ProjectID, ph.Hour); err != nil {
			t.Fatal(err)
		}
	}
	return hours
}

func countRows(t *testing.T, db *DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.read.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestDeleteUserData_RemovesEventsAndRecounts(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	shop := addProject(t, db, "Webshop")
	portal := addProject(t, db, "Portal")
	hour := at(-3 * time.Hour).Truncate(time.Hour)

	// Issue "both": jan's first and newest events, piet's in between.
	// Issue "jan": only jan's. Issue "piet": only piet's. Jan in the portal too.
	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{
		userEvent(shop.ID, 1, "both", "jan", "first, by jan@example.com", hour.Add(time.Minute)),
		userEvent(shop.ID, 2, "both", "piet", "in between, by piet", hour.Add(2*time.Minute)),
		userEvent(shop.ID, 3, "both", "jan", "newest, by jan@example.com", hour.Add(3*time.Minute)),
		userEvent(shop.ID, 4, "jan", "jan", "only jan", hour.Add(4*time.Minute)),
		userEvent(shop.ID, 5, "jan", "jan", "only jan", hour.Add(5*time.Minute)),
		userEvent(shop.ID, 6, "piet", "piet", "only piet", hour.Add(6*time.Minute)),
		userEvent(portal.ID, 7, "portal", "jan", "jan in the portal", hour.Add(7*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	// Page views in two tabs: jan's tab (a leave by page view id and one
	// without, which closes jan's page view) and piet's tab (a leave without id).
	savePages(t, db,
		PageEvent{ProjectID: shop.ID, PageviewID: "jan-1", SessionID: "s-jan", WindowID: "w", UserID: "jan", Route: "/", Path: "/", At: hour.Add(time.Minute)},
		PageEvent{ProjectID: shop.ID, PageviewID: "jan-2", SessionID: "s-jan", WindowID: "w", UserID: "jan", Route: "/cart", Path: "/cart", At: hour.Add(2 * time.Minute)},
		PageEvent{ProjectID: shop.ID, Leave: true, EventID: "leave-jan-1", PageviewID: "jan-1", SessionID: "s-jan", WindowID: "w", At: hour.Add(90 * time.Second)},
		PageEvent{ProjectID: shop.ID, Leave: true, EventID: "leave-jan-tab", SessionID: "s-jan", WindowID: "w", At: hour.Add(3 * time.Minute)},
		PageEvent{ProjectID: shop.ID, PageviewID: "piet-1", SessionID: "s-piet", WindowID: "w", UserID: "piet", Route: "/", Path: "/", At: hour.Add(time.Minute)},
		PageEvent{ProjectID: shop.ID, Leave: true, EventID: "leave-piet-tab", SessionID: "s-piet", WindowID: "w", At: hour.Add(2 * time.Minute)},
		PageEvent{ProjectID: portal.ID, PageviewID: "jan-portal", SessionID: "s-p", WindowID: "w", UserID: "jan", Route: "/", Path: "/", At: hour.Add(time.Minute)},
	)
	if _, err := db.SaveCustomEvents(ctx, []CustomEvent{
		{ProjectID: shop.ID, EventID: "c1", Name: "checkout_started", UserID: "jan", At: hour.Add(time.Minute)},
		{ProjectID: shop.ID, EventID: "c2", Name: "checkout_started", UserID: "jan", At: hour.Add(2 * time.Minute)},
		{ProjectID: shop.ID, EventID: "c3", Name: "checkout_started", UserID: "piet", At: hour.Add(3 * time.Minute)},
		{ProjectID: portal.ID, EventID: "c4", Name: "login", UserID: "jan", At: hour.Add(time.Minute)},
	}); err != nil {
		t.Fatal(err)
	}
	rebuildDirtyHours(t, db)
	issueOf := func(fp string) (Issue, error) {
		return scanIssue(db.read.QueryRowContext(ctx, "SELECT "+issueColumns+" FROM issues WHERE fingerprint = ?", fp))
	}
	pietIssue, _ := issueOf("piet")

	// Only the shop.
	got, err := db.DeleteUserData(ctx, UserDataRequest{UserID: "jan", ProjectIDs: []int64{shop.ID}, Describe: describeTest})
	if err != nil {
		t.Fatal(err)
	}
	want := UserDataDeleted{Projects: 1, ErrorEvents: 4, Pageviews: 2, Pageleaves: 2, CustomEvents: 2, IssuesDeleted: 1, IssuesUpdated: 1, Hours: 1}
	if got != want {
		t.Errorf("deleted %+v\nwant    %+v", got, want)
	}
	if got.Rows() != 4+2+2+2+1 {
		t.Errorf("Rows() = %d, want 11", got.Rows())
	}

	// Nothing of jan is left in the shop; piet's rows and the portal stay.
	for _, q := range []string{
		"SELECT COUNT(*) FROM error_events WHERE project_id = ? AND user_id = 'jan'",
		"SELECT COUNT(*) FROM pageviews WHERE project_id = ? AND user_id = 'jan'",
		"SELECT COUNT(*) FROM custom_events WHERE project_id = ? AND user_id = 'jan'",
		"SELECT COUNT(*) FROM pageleaves WHERE project_id = ? AND session_id = 's-jan'",
	} {
		if n := countRows(t, db, q, shop.ID); n != 0 {
			t.Errorf("%s = %d after the deletion, want 0", q, n)
		}
		if n := countRows(t, db, q, portal.ID); q != "SELECT COUNT(*) FROM pageleaves WHERE project_id = ? AND session_id = 's-jan'" && n == 0 {
			t.Errorf("%s in the portal = 0: the portal was not chosen", q)
		}
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM error_events WHERE project_id = ? AND user_id = 'piet'", shop.ID); n != 2 {
		t.Errorf("piet's error events = %d, want 2", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM pageviews WHERE project_id = ? AND user_id = 'piet'", shop.ID); n != 1 {
		t.Errorf("piet's page views = %d, want 1", n)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM pageleaves WHERE project_id = ? AND session_id = 's-piet'", shop.ID); n != 1 {
		t.Errorf("piet's page leave = %d, want 1", n)
	}

	// The issue with piet's event counts only that one now, and shows its text.
	both, err := issueOf("both")
	if err != nil {
		t.Fatal(err)
	}
	pietAt := msTime(hour.Add(2 * time.Minute))
	if both.TimesSeen != 1 || !both.FirstSeen.Equal(pietAt) || !both.LastSeen.Equal(pietAt) || both.Message != "in between, by piet" {
		t.Errorf("issue both = %d events, %v to %v, %q; want 1, both at %v, piet's message", both.TimesSeen, both.FirstSeen, both.LastSeen, both.Message, pietAt)
	}
	if _, err := issueOf("jan"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the issue with only jan's events: %v, want it removed", err)
	}
	if after, _ := issueOf("piet"); after.TimesSeen != pietIssue.TimesSeen || after.Message != pietIssue.Message {
		t.Errorf("piet's issue changed: %+v", after)
	}
	if row, err := db.IssueRowByID(ctx, both.ID); err != nil || row.Users != 1 {
		t.Errorf("users of issue both = %d (%v), want 1", row.Users, err)
	}

	// The hour is marked; rebuilt, its summaries no longer count jan.
	if hours := rebuildDirtyHours(t, db); len(hours) != 1 || hours[0].ProjectID != shop.ID || !hours[0].Hour.Equal(hour) {
		t.Errorf("hours marked for a rebuild = %v, want the shop's %v", hours, hour)
	}
	pages, err := db.PageStats(ctx, shop.ID, hour, hour.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if pages.Totals.Views != 1 || pages.Totals.Visitors != 1 || pages.Totals.Errors != 2 {
		t.Errorf("page totals = %+v, want piet's view, visitor and 2 errors", pages.Totals)
	}
	events, err := db.EventStats(ctx, shop.ID, hour, hour.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events.Events) != 1 || events.Events[0].Events != 1 || events.Events[0].Users != 1 {
		t.Errorf("custom events = %+v, want piet's one", events.Events)
	}

	// Every project: the portal's rows go too.
	got, err = db.DeleteUserData(ctx, UserDataRequest{UserID: "jan", Describe: describeTest})
	if err != nil {
		t.Fatal(err)
	}
	want = UserDataDeleted{Projects: 2, ErrorEvents: 1, Pageviews: 1, CustomEvents: 1, IssuesDeleted: 1, Hours: 1}
	if got != want {
		t.Errorf("second deletion %+v\nwant               %+v", got, want)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM error_events WHERE user_id = 'jan'"); n != 0 {
		t.Errorf("jan's error events left = %d", n)
	}
}

func TestDeleteUserData_Refuses(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	shop := addProject(t, db, "Webshop")
	// Events without a user id have user_id '': "" must never mean "them".
	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{userEvent(shop.ID, 1, "fp", "", "anonymous", at(-time.Hour))}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "   "} {
		if _, err := db.DeleteUserData(ctx, UserDataRequest{UserID: id}); !errors.Is(err, ErrNoUserID) {
			t.Errorf("user id %q: %v, want ErrNoUserID", id, err)
		}
	}
	_, err := db.DeleteUserData(ctx, UserDataRequest{UserID: "jan", ProjectIDs: []int64{shop.ID, shop.ID + 99}})
	var unknown *UnknownProjectError
	if !errors.Is(err, ErrUnknownProject) || !errors.As(err, &unknown) || unknown.ID != shop.ID+99 {
		t.Errorf("an unknown project: %v, want ErrUnknownProject naming project %d", err, shop.ID+99)
	}
	if n := countRows(t, db, "SELECT COUNT(*) FROM error_events"); n != 1 {
		t.Errorf("%d events after refused deletions, want 1", n)
	}
	// Without Describe the issue keeps its text; a user without data deletes nothing.
	got, err := db.DeleteUserData(ctx, UserDataRequest{UserID: "nobody"})
	if err != nil || got.Rows() != 0 || got.Projects != 1 {
		t.Errorf("a user without data: %+v, %v", got, err)
	}
}
