package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"monitor/internal/config"
	"monitor/internal/project"
	"monitor/internal/store"
)

// projectCommand runs "monitor project <subcommand>".
func projectCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "create" {
		fmt.Fprintf(stderr, "monitor: expected \"project create\"\n\n%s", usage)
		return 2
	}

	flags := flag.NewFlagSet("project create", flag.ContinueOnError)
	flags.SetOutput(stderr)
	name := flags.String("name", "", "project name, for example Webshop (required)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "monitor: project create takes only --name, got extra arguments %q; quote a name with spaces: --name \"Customer portal\"\n", flags.Args())
		return 2
	}
	if strings.TrimSpace(*name) == "" {
		fmt.Fprintln(stderr, "monitor: project create needs --name, for example: monitor project create --name Webshop")
		return 2
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		printConfigError(stderr, err)
		return 1
	}

	ctx := context.Background()
	db, err := openDatabase(ctx, cfg, func(path string, pruneErr error) {
		fmt.Fprintf(stderr, "monitor: backed up the database to %s before updating it\n", path)
		if pruneErr != nil {
			fmt.Fprintf(stderr, "monitor: warning: %v\n", pruneErr)
		}
	})
	if err != nil {
		fmt.Fprintf(stderr, "monitor: %v\n", err)
		return 1
	}
	defer db.Close()

	// Defaults until the Settings screen exists: keep data 90 days and track
	// Laravel and browser errors.
	p, err := db.CreateProject(ctx, store.NewProject{
		Name:          *name,
		TracksLaravel: true,
		TracksBrowser: true,
	})
	if err != nil {
		fmt.Fprintf(stderr, "monitor: create project: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Created project %s\n", p.Name)
	fmt.Fprintf(stdout, "ID:            %d\n", p.ID)
	fmt.Fprintf(stdout, "Slug:          %s\n", p.Slug)
	fmt.Fprintf(stdout, "DSN:           %s\n", project.DSN(cfg.BaseURL, p))
	fmt.Fprintf(stdout, "PostHog token: %s\n", p.PosthogToken)
	return 0
}
