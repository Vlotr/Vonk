package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"monitor/internal/auth"
	"monitor/internal/config"
	"monitor/internal/store"
)

// adminCommand runs "monitor admin <subcommand>". Today that is only
// reset-password: how an admin who forgot the password gets back in.
func adminCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "reset-password" {
		fmt.Fprintf(stderr, "monitor: expected \"admin reset-password --email EMAIL\"\n\n%s", usage)
		return 2
	}

	flags := flag.NewFlagSet("admin reset-password", flag.ContinueOnError)
	flags.SetOutput(stderr)
	email := flags.String("email", "", "the address of the account, for example jan@example.com (required)")
	if err := flags.Parse(args[1:]); err != nil {
		return 2
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "monitor: admin reset-password takes only --email, got extra arguments %q\n", flags.Args())
		return 2
	}
	if strings.TrimSpace(*email) == "" {
		fmt.Fprintln(stderr, "monitor: admin reset-password needs --email, for example: monitor admin reset-password --email jan@example.com")
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

	u, err := db.UserByEmail(ctx, *email)
	if errors.Is(err, store.ErrNotFound) {
		fmt.Fprintf(stderr, "monitor: there is no account with the email %s\n", store.NormalizeEmail(*email))
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "monitor: %v\n", err)
		return 1
	}
	// Same SECRET_KEY as the server, so the server recognizes the link.
	accounts := auth.New(auth.Options{
		DB:        db,
		SecretKey: cfg.SecretKey,
		BaseURL:   cfg.BaseURL,
		Logger:    slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel})),
	})
	link, err := accounts.ResetLink(ctx, u.ID, false)
	if err != nil {
		fmt.Fprintf(stderr, "monitor: make reset link: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "Password reset link for %s (%s)\n", u.Email, u.Role)
	fmt.Fprintf(stdout, "Link:     %s\n", link.URL)
	fmt.Fprintf(stdout, "Expires:  %s (it works once)\n", link.ExpiresAt.UTC().Format("2006-01-02 15:04 UTC"))
	fmt.Fprintln(stdout, "Open it in a browser and choose a new password. That signs the account out everywhere else.")
	return 0
}
