package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Every check-then-write keeps its answer when another writer runs at the
// same moment. On SQLite that is free: a write transaction holds the whole
// database. On Postgres (make test-postgres) writers run side by side, and
// these tests fail unless the code locks what it reads (dialect.go).
//
// Each test holds a write transaction that does what a second writer would
// (and locks what that writer's code locks), starts the code under test
// while it is open, and commits a moment later: the code must wait for it
// and then see its result.

// whileHeld runs hold in a write transaction, runs next while that
// transaction stays open, commits it and returns next's error. The commit
// comes once next waits for the transaction or has finished without
// waiting: on Postgres when pg_stat_activity shows a backend waiting for a
// lock the transaction holds; on SQLite, where next waits in its busy
// timeout and nothing shows that, after 150 ms.
func whileHeld(t *testing.T, db *DB, hold func(ctx context.Context, tx *writeTx) error, next func() error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() // no-op after Commit
	if err := hold(ctx, tx); err != nil {
		t.Fatal(err)
	}
	var holder int64
	if db.Postgres() {
		if err := tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&holder); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- next() }()

	var result error
	finished := false
	if db.Postgres() {
		finished, result = waitForBlocked(t, db, holder, done)
	} else {
		time.Sleep(150 * time.Millisecond) // next runs into what tx holds, or finishes without waiting
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if !finished {
		result = <-done
	}
	return result
}

// waitForBlocked returns once a Postgres backend waits for a lock the
// backend holder holds (finished false), or next is done (finished true,
// with its error).
func waitForBlocked(t *testing.T, db *DB, holder int64, done <-chan error) (finished bool, err error) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			return true, err
		default:
		}
		var waiting bool
		if err := db.read.QueryRowContext(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND ? = ANY (pg_blocking_pids(pid)))`, holder).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return false, nil
		}
		if time.Now().After(deadline) {
			t.Fatal("the second writer neither waited for the held transaction nor finished within 10 s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// An issue ignored while an event for it is being stored stays ignored:
// the writer reads the issue locked, so it never writes back the "resolved"
// it read before the change (which it would reopen).
func TestConcurrent_IssueIgnoredWhileAnEventIsStored(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(-time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	issue := res.NewIssues[0]
	if _, err := db.SetIssueStatus(ctx, issue.ID, IssueResolved, at(0)); err != nil {
		t.Fatal(err)
	}

	var saved SaveResult
	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET status = ?, resolved_at = NULL WHERE id = ?", IssueIgnored, issue.ID)
		return err
	}, func() (err error) {
		// Received after the resolve: on a resolved issue it would reopen it.
		saved, err = db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(2), "fp", at(time.Minute))})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.IssueByID(ctx, issue.ID)
	if err != nil || got.Status != IssueIgnored || got.TimesSeen != 2 || len(saved.Regressed) != 0 {
		t.Errorf("issue = %s with %d events, regressed %d (%v); want ignored with 2 and none", got.Status, got.TimesSeen, len(saved.Regressed), err)
	}
}

// Events of a project that is deleted while they are being stored are
// left out, as when the deletion came first: the batch waits for it.
func TestConcurrent_ProjectDeletedWhileItsEventsAreStored(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(-time.Hour))}); err != nil {
		t.Fatal(err)
	}

	var saved SaveResult
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", p.ID)
		return err
	}, func() (err error) {
		saved, err = db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(2), "fp", at(0))})
		return err
	})
	if err != nil || saved.Orphaned != 1 || saved.Stored != 0 {
		t.Errorf("save = %+v, %v; want the event left out", saved, err)
	}
}

// A project deleted while its hour is being rebuilt: neither deadlocks
// with the other (on Postgres one would be undone, usually the deletion).
// The writer of the mark holds it for a moment, so the rebuild waits after
// its start; the deletion comes in then. The rebuild keeps the project
// before it writes any of its rows, so the deletion waits for the rebuild
// instead of taking the project and then waiting for the hour's rows the
// rebuild holds while the rebuild waits for the project.
func TestConcurrent_ProjectDeletedDuringARebuild(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	hour := at(0).Truncate(time.Hour)
	savePages(t, db, PageEvent{ProjectID: p.ID, PageviewID: "a", Route: "/", Path: "/", At: hour.Add(time.Minute)})
	if err := db.RecomputeHour(ctx, p.ID, hour); err != nil {
		t.Fatal(err)
	}
	savePages(t, db, PageEvent{ProjectID: p.ID, PageviewID: "b", Route: "/", Path: "/", At: hour.Add(2 * time.Minute)})

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		var h int64
		return tx.QueryRowContext(ctx, "SELECT hour FROM stats_dirty_hours WHERE project_id = ? AND hour = ?"+tx.forUpdate(),
			p.ID, hour.UnixMilli()).Scan(&h)
	}, func() error {
		rebuilt := make(chan error, 1)
		go func() { rebuilt <- db.RecomputeHour(ctx, p.ID, hour) }()
		time.Sleep(50 * time.Millisecond) // the rebuild waits for the mark
		deleted := db.DeleteProject(ctx, p.ID)
		return errors.Join(<-rebuilt, deleted)
	})
	if err != nil {
		t.Errorf("rebuild and deletion: %v; want both to succeed", err)
	}
	if _, err := db.ProjectByID(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the project after its deletion: %v", err)
	}
}

// A rebuild of an hour of a project that is being deleted waits for the
// deletion and then has nothing to rebuild; so do a member's projects
// being set and an invite being made for that project: they find it gone
// (ErrUnknownProject) instead of writing rows of it.
func TestConcurrent_WritesWaitForAProjectDeletion(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := addUser(t, db, "admin@example.com", RoleAdmin)
	p := addProject(t, db, "Webshop")
	member := addUser(t, db, "member@example.com", RoleMember, p.ID)
	hour := at(0).Truncate(time.Hour)
	savePages(t, db, PageEvent{ProjectID: p.ID, PageviewID: "a", Route: "/", Path: "/", At: hour.Add(time.Minute)})

	deleteProject := func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", p.ID)
		return err
	}
	if err := whileHeld(t, db, deleteProject, func() error { return db.RecomputeHour(ctx, p.ID, hour) }); err != nil {
		t.Errorf("rebuild of a deleted project's hour: %v, want nothing to do", err)
	}

	q := addProject(t, db, "Portal")
	ids := []int64{q.ID}
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", q.ID)
		return err
	}, func() error {
		_, err := db.UpdateUser(ctx, member.ID, UserChange{ProjectIDs: &ids})
		return err
	})
	if !errors.Is(err, ErrUnknownProject) {
		t.Errorf("member's projects set to a deleted one: %v, want ErrUnknownProject", err)
	}

	r := addProject(t, db, "Admin")
	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", r.ID)
		return err
	}, func() error {
		_, err := db.CreateInvite(ctx, NewInvite{Email: "new@example.com", Role: RoleMember, ProjectIDs: []int64{r.ID}, TokenHash: "t",
			CreatedBy: admin.ID, Now: userTestNow, ExpiresAt: userTestNow.Add(time.Hour)})
		return err
	})
	if !errors.Is(err, ErrUnknownProject) {
		t.Errorf("invite to a deleted project: %v, want ErrUnknownProject", err)
	}
}

// Resolving an issue that someone else resolves at the same moment keeps
// the first resolve time, as a second click does.
func TestConcurrent_ResolvedTwiceKeepsTheFirstTime(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(-time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	issue := res.NewIssues[0]

	var got Issue
	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		_, err := tx.ExecContext(ctx, "UPDATE issues SET status = ?, resolved_at = ? WHERE id = ?", IssueResolved, at(0).UnixMilli(), issue.ID)
		return err
	}, func() (err error) {
		got, err = db.SetIssueStatus(ctx, issue.ID, IssueResolved, at(time.Minute))
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := db.IssueByID(ctx, issue.ID)
	if got.ResolvedAt == nil || !got.ResolvedAt.Equal(at(0)) || stored.ResolvedAt == nil || !stored.ResolvedAt.Equal(at(0)) {
		t.Errorf("resolved at %v (stored %v), want the first resolve %v", got.ResolvedAt, stored.ResolvedAt, at(0))
	}
}

// Two admins making each other members at the same moment: the second
// change sees the first and is refused, so an admin remains.
func TestConcurrent_LastAdminStays(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	one := addUser(t, db, "one@example.com", RoleAdmin)
	two := addUser(t, db, "two@example.com", RoleAdmin)

	member := RoleMember
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		if err := checkOtherAdmin(ctx, tx, one.ID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", RoleMember, one.ID)
		return err
	}, func() error {
		_, err := db.UpdateUser(ctx, two.ID, UserChange{Role: &member})
		return err
	})
	if !errors.Is(err, ErrLastAdmin) {
		t.Errorf("second demotion: %v, want ErrLastAdmin", err)
	}
	if admins, _ := db.AdminEmails(ctx); len(admins) != 1 {
		t.Errorf("admins = %v, want one", admins)
	}
}

// An admin deleted while the other admin is being made a member: the
// deletion sees the demotion and is refused.
func TestConcurrent_LastAdminNotDeleted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	one := addUser(t, db, "one@example.com", RoleAdmin)
	two := addUser(t, db, "two@example.com", RoleAdmin)

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", RoleMember, one.ID)
		return err
	}, func() error {
		return db.DeleteUser(ctx, two.ID)
	})
	if !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deletion: %v, want ErrLastAdmin", err)
	}
	if admins, _ := db.AdminEmails(ctx); len(admins) != 1 {
		t.Errorf("admins = %v, want one", admins)
	}
}

// Two setup pages sent at once make one account.
func TestConcurrent_FirstAdminOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		_, err := insertUser(ctx, tx, "first@example.com", "hash", RoleAdmin, userTestNow)
		return err
	}, func() error {
		_, err := db.CreateFirstAdmin(ctx, "second@example.com", "hash", userTestNow)
		return err
	})
	if !errors.Is(err, ErrUsersExist) {
		t.Errorf("second setup: %v, want ErrUsersExist", err)
	}
	if n, _ := db.CountUsers(ctx); n != 1 {
		t.Errorf("%d accounts, want 1", n)
	}
}

// An invite made by an admin who is being made a member is refused: its
// link would outlive the demotion, which removes that admin's invites.
func TestConcurrent_InviteOfAnAdminBeingDemoted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	addUser(t, db, "one@example.com", RoleAdmin)
	two := addUser(t, db, "two@example.com", RoleAdmin)

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE users SET role = ? WHERE id = ?", RoleMember, two.ID); err != nil {
			return err
		}
		return dropPendingInvites(ctx, tx, two.ID)
	}, func() error {
		_, err := db.CreateInvite(ctx, NewInvite{Email: "new@example.com", Role: RoleMember, TokenHash: "late", CreatedBy: two.ID,
			Now: userTestNow, ExpiresAt: userTestNow.Add(time.Hour)})
		return err
	})
	if !errors.Is(err, ErrNotAdmin) {
		t.Errorf("invite: %v, want ErrNotAdmin", err)
	}
	if pending, _ := db.PendingInvites(ctx, userTestNow); len(pending) != 0 {
		t.Errorf("pending invites = %+v, want none", pending)
	}
}

// An invite link opened twice at once makes one account; the second gets ErrGone.
func TestConcurrent_InviteAcceptedOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	admin := addUser(t, db, "admin@example.com", RoleAdmin)
	inv, err := db.CreateInvite(ctx, NewInvite{Email: "new@example.com", Role: RoleMember, TokenHash: "invite", CreatedBy: admin.ID,
		Now: userTestNow, ExpiresAt: userTestNow.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}

	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		if _, err := insertUser(ctx, tx, inv.Email, "first", RoleMember, userTestNow); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE invites SET used_at = ? WHERE id = ?", userTestNow.UnixMilli(), inv.ID)
		return err
	}, func() error {
		_, err := db.AcceptInvite(ctx, "invite", "second", userTestNow)
		return err
	})
	if !errors.Is(err, ErrGone) {
		t.Errorf("second accept: %v, want ErrGone", err)
	}
}

// A reset link used twice at once sets one password; the second gets ErrGone.
func TestConcurrent_ResetLinkUsedOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	u := addUser(t, db, "admin@example.com", RoleAdmin)
	if err := db.CreatePasswordReset(ctx, u.ID, "reset", userTestNow, userTestNow.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE password_resets SET used_at = ? WHERE token_hash = ?", userTestNow.UnixMilli(), "reset")
		return err
	}, func() error {
		_, err := db.UsePasswordReset(ctx, "reset", "second", userTestNow)
		return err
	})
	if !errors.Is(err, ErrGone) {
		t.Errorf("second use: %v, want ErrGone", err)
	}
	if got, _ := db.UserByID(ctx, u.ID); got.PasswordHash == "second" {
		t.Error("the second use set the password")
	}
}

// Two reset links made at once: only the newest works, as one after the other.
func TestConcurrent_OneResetLinkWorks(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	u := addUser(t, db, "admin@example.com", RoleAdmin)

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAccounts); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO password_resets (user_id, token_hash, created_at, expires_at) VALUES (?, ?, ?, ?)",
			u.ID, "first", userTestNow.UnixMilli(), userTestNow.Add(time.Hour).UnixMilli())
		return err
	}, func() error {
		return db.CreatePasswordReset(ctx, u.ID, "second", userTestNow, userTestNow.Add(time.Hour))
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.PasswordResetByTokenHash(ctx, "first"); !errors.Is(err, ErrNotFound) {
		t.Errorf("the older link: %v, want it removed", err)
	}
}

// A digest claimed by another run at the same moment is not claimed again,
// so it goes out once.
func TestConcurrent_ClaimOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	const name = "digest:1:daily"

	var claimed bool
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockAlertStatePrefix+name); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO alert_state (name, project_id, value, sent_at) VALUES (?, ?, ?, ?)",
			name, p.ID, "2026-10-15", userTestNow.UnixMilli())
		return err
	}, func() (err error) {
		claimed, err = db.ClaimPeriod(ctx, name, p.ID, "2026-10-15", userTestNow)
		return err
	})
	if err != nil || claimed {
		t.Errorf("second claim = %v, %v; want false", claimed, err)
	}
}

// Two projects with one name made at once get two slugs.
func TestConcurrent_ProjectSlugs(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	var p Project
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockProjectSlugs); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO projects (name, slug, sentry_key, posthog_token, created_at)
			VALUES ('Webshop', 'webshop', 'key-1', 'token-1', 0)`)
		return err
	}, func() (err error) {
		p, err = db.CreateProject(ctx, NewProject{Name: "Webshop"})
		return err
	})
	if err != nil || p.Slug != "webshop-2" {
		t.Errorf("second project: slug %q, %v; want webshop-2", p.Slug, err)
	}
}

// One bundle assembled twice at once is stored once.
func TestConcurrent_SourceMapBundleOnce(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	file := SourceMapFile{ProjectID: p.ID, Release: "v1", File: "~/a.js.map", Kind: SourceMapKind, Bundle: "b", Path: "1/x", Size: 1, UploadedAt: userTestNow}

	var stored []SourceMapFile
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		if err := tx.lock(ctx, lockSourceMapBundles); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO source_maps (project_id, release, dist, file, kind, bundle, path, size, uploaded_at)
			VALUES (?, ?, '', ?, ?, ?, '1/first', 1, 0)`, p.ID, file.Release, file.File, file.Kind, file.Bundle)
		return err
	}, func() (err error) {
		stored, err = db.SaveSourceMapFiles(ctx, []SourceMapFile{file})
		return err
	})
	if err != nil || len(stored) != 0 {
		t.Errorf("second save stored %d files, %v; want none", len(stored), err)
	}
	if paths, _ := db.SourceMapPaths(ctx); len(paths) != 1 {
		t.Errorf("files = %v, want one", paths)
	}
}

// A page view stored while its hour's summary is rebuilt is never lost:
// the summary counts it, or the hour stays marked for the next run.
func TestConcurrent_DirtyMarkOutlivesARebuild(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	hour := at(0).Truncate(time.Hour)
	savePages(t, db, PageEvent{ProjectID: p.ID, PageviewID: "a", Route: "/", Path: "/", At: hour.Add(time.Minute)})

	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		// The writer: a second page view in the hour, and its mark.
		_, err := tx.ExecContext(ctx, "INSERT INTO pageviews (project_id, pageview_id, route, start) VALUES (?, 'b', '/', ?)",
			p.ID, hour.Add(2*time.Minute).UnixMilli())
		if err != nil {
			return err
		}
		return markDirty(ctx, tx, map[projectHour]bool{{p.ID, hour.UnixMilli()}: true})
	}, func() error {
		return db.RecomputeHour(ctx, p.ID, hour)
	})
	if err != nil {
		t.Fatal(err)
	}
	stats, err := db.PageStats(ctx, p.ID, hour, hour.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	dirty, _ := db.DirtyHours(ctx)
	if stats.Totals.Views != 2 && len(dirty) == 0 {
		t.Errorf("summary counts %d page views and no hour is marked: the second page view is lost", stats.Totals.Views)
	}
}

// Retention leaves an issue alone that got an event while it was deciding.
func TestConcurrent_RetentionSparesAnIssueThatCameBack(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	res, err := db.SaveErrorEvents(ctx, []NewErrorEvent{newEvent(p.ID, eventID(1), "fp", at(-100*24*time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	issue := res.NewIssues[0]

	var deleted int64
	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		// The writer: a new event, so the issue was seen just now.
		_, err := tx.ExecContext(ctx, "UPDATE issues SET last_seen = ? WHERE id = ?", at(0).UnixMilli(), issue.ID)
		return err
	}, func() (err error) {
		deleted, err = db.DeleteIssuesLastSeenBefore(ctx, p.ID, at(-90*24*time.Hour), 100)
		return err
	})
	if err != nil || deleted != 0 {
		t.Errorf("retention deleted %d issues, %v; want none", deleted, err)
	}
	if _, err := db.IssueByID(ctx, issue.ID); err != nil {
		t.Errorf("issue: %v", err)
	}
}

// Processes that start at once (serve and a CLI command) each migrate a
// new database without getting in each other's way.
func TestConcurrent_MigrateAtOnce(t *testing.T) {
	db, _ := openUnmigrated(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = db.Migrate(ctx)
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("Migrate %d: %v", i, err)
		}
	}
	files, _ := db.migrationFiles()
	var n int
	if err := db.read.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n != len(files) {
		t.Errorf("schema_migrations has %d rows (%v), want %d", n, err, len(files))
	}
}

// An event of the user that the writer stores while the user's data is
// deleted: the issue's counter matches the events it keeps afterwards
// (on Postgres the deletion takes the event's issue after the writer; on
// SQLite the writer goes first and the deletion takes its event too).
func TestConcurrent_UserEventStoredWhileTheirDataIsDeleted(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")
	if _, err := db.SaveErrorEvents(ctx, []NewErrorEvent{userEvent(p.ID, 1, "fp", "jan", "first", at(-time.Hour)),
		userEvent(p.ID, 2, "fp", "piet", "piet's", at(-30*time.Minute))}); err != nil {
		t.Fatal(err)
	}
	issue, err := scanIssue(db.read.QueryRowContext(ctx, "SELECT "+issueColumns+" FROM issues WHERE fingerprint = 'fp'"))
	if err != nil {
		t.Fatal(err)
	}

	err = whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		// The writer: the issue locked, one more event of jan and the counter.
		if exists, err := projectExists(ctx, tx, p.ID); err != nil || !exists {
			return err
		}
		var id int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM issues WHERE id = ?"+tx.forUpdate(), issue.ID).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO error_events (issue_id, project_id, event_id, timestamp, received_at, user_id, data)
			VALUES (?, ?, ?, ?, ?, 'jan', '{}')`, issue.ID, p.ID, eventID(3), at(-time.Minute).UnixMilli(), at(0).UnixMilli()); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE issues SET times_seen = times_seen + 1 WHERE id = ?", issue.ID)
		return err
	}, func() error {
		_, err := db.DeleteUserData(ctx, UserDataRequest{UserID: "jan", Describe: describeTest})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := db.IssueByID(ctx, issue.ID)
	events := countRows(t, db, "SELECT COUNT(*) FROM error_events WHERE issue_id = ?", issue.ID)
	if err != nil || after.TimesSeen != int64(events) {
		t.Errorf("issue counts %d events (%v), %d are stored", after.TimesSeen, err, events)
	}
}

// Two edits of one project at once keep each other's changes: ChangeProject
// reads the settings it changes inside its write, locked, so it never
// writes back a name another edit just changed.
func TestConcurrent_ProjectEditsKeepEachOther(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	p := addProject(t, db, "Webshop")

	var changed Project
	err := whileHeld(t, db, func(ctx context.Context, tx *writeTx) error {
		// Another edit: it read the project locked and renames it.
		var id int64
		if err := tx.QueryRowContext(ctx, "SELECT id FROM projects WHERE id = ?"+tx.forUpdate(), p.ID).Scan(&id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, "UPDATE projects SET name = 'Renamed' WHERE id = ?", p.ID)
		return err
	}, func() (err error) {
		changed, err = db.ChangeProject(ctx, p.ID, func(s *ProjectSettings) error {
			s.TracksPageviews = true
			return nil
		})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.ProjectByID(ctx, p.ID)
	if err != nil || got.Name != "Renamed" || !got.TracksPageviews || changed.Name != "Renamed" {
		t.Errorf("project = %q tracking page views %v (answer %q), %v; want both changes", got.Name, got.TracksPageviews, changed.Name, err)
	}
}

// A project's settings and alerts change while an ingest batch of that
// project is being written, without waiting for it: the batch keeps the
// project (FOR KEY SHARE, projectlocks.go), and the edits lock its row
// FOR NO KEY UPDATE, which only conflicts with other edits and the
// deletion. Postgres only: on SQLite one write transaction at a time is
// the design.
func TestConcurrent_ProjectEditsDoNotWaitForAnIngestBatch(t *testing.T) {
	db := openTestDB(t)
	if !db.Postgres() {
		t.Skip("SQLite runs one write transaction at a time")
	}
	ctx := context.Background()
	p := addProject(t, db, "Webshop")

	// What an ingest batch holds until it commits: the project kept, and
	// a row of its tables written (a dirty hour, whose foreign key also
	// keeps the project).
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback() // no-op after Commit
	if exists, err := projectExists(ctx, tx, p.ID); err != nil || !exists {
		t.Fatalf("projectExists = %v, %v", exists, err)
	}
	if err := markDirty(ctx, tx, map[projectHour]bool{{p.ID, hourOf(at(0))}: true}); err != nil {
		t.Fatal(err)
	}

	edit, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := db.ChangeProject(edit, p.ID, func(s *ProjectSettings) error {
		s.Name = "Renamed"
		return nil
	}); err != nil {
		t.Errorf("a project edit during an ingest batch: %v; want it done without waiting for the batch", err)
	}
	if _, err := db.UpdateAlertSettings(edit, p.ID, func(s *AlertSettings) { s.SpikeThreshold = 7 }); err != nil {
		t.Errorf("an alerts edit during an ingest batch: %v; want it done without waiting for the batch", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	got, err := db.ProjectByID(ctx, p.ID)
	if err != nil || got.Name != "Renamed" {
		t.Errorf("project = %q, %v; want the edit kept", got.Name, err)
	}
}
