package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"monitor/internal/config"
	"monitor/internal/issues"
	"monitor/internal/store"
)

// deleteUserDataCommand runs "monitor delete-user-data --user-id ID
// [--project ID]...": everything stored with that user id (the id the
// SDKs send) goes, in the projects named or in every project. The log gets
// one line with the number of rows, never the user id.
func deleteUserDataCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("delete-user-data", flag.ContinueOnError)
	flags.SetOutput(stderr)
	userID := flags.String("user-id", "", "the user id as the SDKs send it: Sentry's user.id, PostHog's distinct_id (required)")
	var projects projectIDs
	flags.Var(&projects, "project", "a project id to delete it in; repeat it for more; leave it out for every project")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "monitor: delete-user-data takes only --user-id and --project, got extra arguments %q\n", flags.Args())
		return 2
	}
	if strings.TrimSpace(*userID) == "" {
		fmt.Fprintln(stderr, "monitor: delete-user-data needs --user-id, for example: monitor delete-user-data --user-id 42")
		return 2
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		printConfigError(stderr, err)
		return 1
	}
	ctx := context.Background()
	db, err := openDatabase(ctx, cfg, func(path string, warning error) {
		if path != "" {
			fmt.Fprintf(stderr, "monitor: backed up the database to %s before updating it\n", path)
		}
		if warning != nil {
			fmt.Fprintf(stderr, "monitor: warning: %v\n", warning)
		}
	})
	if err != nil {
		fmt.Fprintf(stderr, "monitor: %v\n", err)
		return 1
	}
	defer db.Close()

	deleted, err := db.DeleteUserData(ctx, store.UserDataRequest{UserID: *userID, ProjectIDs: projects.ids, Describe: issues.EventText})
	var unknown *store.UnknownProjectError
	if errors.As(err, &unknown) {
		fmt.Fprintf(stderr, "monitor: there is no project %d; nothing was deleted\n", unknown.ID)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "monitor: delete the user's data: %v\n", err)
		return 1
	}
	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	logger.Info("privacy: deleted the data of a user id on request", "by", "command-line", "projects", deleted.Projects,
		"rows", deleted.Rows(), "error_events", deleted.ErrorEvents, "pageviews", deleted.Pageviews, "pageleaves", deleted.Pageleaves,
		"custom_events", deleted.CustomEvents, "issues_deleted", deleted.IssuesDeleted)

	fmt.Fprintf(stdout, "Deleted the data of that user id in %s:\n", count(int64(deleted.Projects), "project", "projects"))
	fmt.Fprintf(stdout, "  %s, %s, %s, %s\n", count(deleted.ErrorEvents, "error event", "error events"),
		count(deleted.Pageviews, "page view", "page views"), count(deleted.Pageleaves, "page leave", "page leaves"),
		count(deleted.CustomEvents, "custom event", "custom events"))
	fmt.Fprintf(stdout, "  %s left without events removed, %s recounted\n", count(deleted.IssuesDeleted, "issue", "issues"),
		count(deleted.IssuesUpdated, "issue", "issues"))
	fmt.Fprintln(stdout, "The page and event statistics drop the user within a minute while the server runs (or at its next start).")
	fmt.Fprintln(stdout, "Backups made before now still hold the data until they are replaced.")
	return 0
}

// projectIDs collects repeated --project flags.
type projectIDs struct{ ids []int64 }

func (p *projectIDs) String() string { return fmt.Sprint(p.ids) }

func (p *projectIDs) Set(s string) error {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id <= 0 {
		return errors.New("a project id is a positive number, as monitor project create prints it")
	}
	p.ids = append(p.ids, id)
	return nil
}

// count writes n with the singular or plural word.
func count(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.FormatInt(n, 10) + " " + many
}
