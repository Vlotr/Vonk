package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"monitor/internal/config"
	"monitor/internal/store"
)

// restoreCommand runs "monitor restore FILE" and "monitor restore --cancel".
// It never touches the live database: it checks the backup and stages it
// as <DATA_DIR>/restore-pending.db, and the next start of the server makes
// it the database (store.ApplyPendingRestore). SQLite only.
func restoreCommand(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	cancel := flags.Bool("cancel", false, "remove the backup that waits for the next start")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if (*cancel && flags.NArg() != 0) || (!*cancel && flags.NArg() != 1) {
		fmt.Fprintln(stderr, "monitor: expected \"monitor restore FILE\" (a backup to restore at the next start) or \"monitor restore --cancel\"")
		return 2
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		printConfigError(stderr, err)
		return 1
	}
	if cfg.DatabaseURL != "" {
		fmt.Fprintln(stderr, "monitor: the database is Postgres (DATABASE_URL), which the tool does not restore; restore a pg_dump backup with pg_restore")
		return 1
	}
	if *cancel {
		removed, err := store.CancelRestore(cfg.DataDir)
		if err != nil {
			fmt.Fprintf(stderr, "monitor: cancel the restore: %v\n", err)
			return 1
		}
		if removed {
			fmt.Fprintln(stdout, "The restore is cancelled: the next start keeps the database as it is.")
		} else {
			fmt.Fprintln(stdout, "No restore was waiting.")
		}
		return 0
	}

	file, err := os.Open(flags.Arg(0))
	if err != nil {
		fmt.Fprintf(stderr, "monitor: %v\n", err)
		return 1
	}
	defer file.Close()
	pending, err := store.StageRestore(context.Background(), cfg.DataDir, file)
	var refused *store.BackupError
	if errors.As(err, &refused) {
		fmt.Fprintf(stderr, "monitor: %s Nothing was changed.\n", refused.Reason)
		return 1
	}
	if err != nil {
		fmt.Fprintf(stderr, "monitor: stage the restore: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "The backup is checked (database version %d) and waits as %s.\n", pending.Version,
		filepath.Join(cfg.DataDir, store.RestorePendingFile))
	fmt.Fprintf(stdout, "Restart the server to finish the restore: at its start it moves the current database to %s and puts the backup in its place.\n",
		filepath.Join(cfg.DataDir, "backups"))
	fmt.Fprintln(stdout, "What is stored until then is not in the restored database. To cancel: monitor restore --cancel")
	return 0
}
