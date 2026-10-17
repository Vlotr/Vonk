package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/store"
)

func TestDeleteUserDataCommand(t *testing.T) {
	dir := t.TempDir()
	createProject(t, dir, "Webshop")
	createProject(t, dir, "Portal")
	cfg, err := config.Load(testEnv(dir))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	db, err := store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().Truncate(time.Millisecond)
	event := func(project int64, id, user string) store.NewErrorEvent {
		return store.NewErrorEvent{ProjectID: project, EventID: id, Fingerprint: "fp", Title: "RuntimeException", Message: "Checkout failed",
			Timestamp: now, ReceivedAt: now, UserID: user, Data: `{"exception":{"values":[{"type":"RuntimeException","value":"Checkout failed"}]}}`}
	}
	if _, err := db.SaveErrorEvents(ctx, []store.NewErrorEvent{event(1, "a1", "jan@example.com"), event(1, "a2", "piet"),
		event(2, "b1", "jan@example.com")}); err != nil {
		t.Fatal(err)
	}
	db.Close()

	var stdout, stderr bytes.Buffer
	code := run([]string{"delete-user-data", "--user-id", "jan@example.com", "--project", "1"}, testEnv(dir), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("delete-user-data = %d: %s", code, stderr.String())
	}
	if out := stdout.String(); !strings.Contains(out, "1 error event") || !strings.Contains(out, "1 project") {
		t.Errorf("stdout:\n%s", out)
	}
	if log := stderr.String(); strings.Count(log, "\n") != 1 || !strings.Contains(log, "level=INFO") || !strings.Contains(log, "rows=1") ||
		!strings.Contains(log, "by=command-line") || strings.Contains(log, "jan") {
		t.Errorf("want one INFO audit line with the rows and without the user id, got:\n%s", log)
	}

	// Without --project: every project.
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"delete-user-data", "--user-id", "jan@example.com"}, testEnv(dir), &stdout, &stderr); code != 0 ||
		!strings.Contains(stdout.String(), "2 projects") || !strings.Contains(stdout.String(), "1 issue") {
		t.Errorf("every project = %d:\n%s%s", code, stdout.String(), stderr.String())
	}
	db, err = store.Open(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, p := range []int64{1, 2} {
		items, err := db.UserActivity(ctx, p, "jan@example.com", now.Add(time.Hour), 10)
		if err != nil || len(items) != 0 {
			t.Errorf("project %d still has %d items of the user (%v)", p, len(items), err)
		}
	}
	if n, _ := db.CountErrorEvents(ctx, 1); n != 1 {
		t.Errorf("project 1 has %d events, want piet's", n)
	}

	for _, tc := range []struct {
		args []string
		code int
		says string
	}{
		{[]string{"delete-user-data"}, 2, "--user-id"},
		{[]string{"delete-user-data", "--user-id", " "}, 2, "--user-id"},
		{[]string{"delete-user-data", "--user-id", "jan", "extra"}, 2, "extra"},
		{[]string{"delete-user-data", "--user-id", "jan", "--project", "x"}, 2, "project"},
		{[]string{"delete-user-data", "--user-id", "jan", "--project", "9"}, 1, "no project 9"},
	} {
		stderr.Reset()
		if code := run(tc.args, testEnv(dir), &stdout, &stderr); code != tc.code || !strings.Contains(stderr.String(), tc.says) {
			t.Errorf("%v = %d, want %d saying %q; stderr:\n%s", tc.args, code, tc.code, tc.says, stderr.String())
		}
	}
}
