package main

import (
	"context"
	"errors"
	"fmt"

	"monitor/internal/config"
	"monitor/internal/store"
)

// backupBeforeMigrate is store's DB.BackupBeforeMigrate; tests replace it
// to make a failure that the disk cannot.
var backupBeforeMigrate = (*store.DB).BackupBeforeMigrate

// openDatabase opens the database and brings it up to date, as every
// command does at its start. When the update would change a SQLite
// database that has data, a backup comes first
// (<DATA_DIR>/backups/pre-migrate-…); if the backup fails, the update does
// not run and the command stops. backedUp hears the backup's path ("" when
// none was made) and a warning (nil without one): why old backups could
// not be removed, or that an update of a Postgres database runs without a
// backup (store.ErrNoBackups). Those are worth a warning, not a stop.
func openDatabase(ctx context.Context, cfg config.Config, backedUp func(path string, warning error)) (*store.DB, error) {
	db, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	path, err := backupBeforeMigrate(db, ctx)
	var warning error
	if errors.Is(err, store.ErrOldBackupsKept) || errors.Is(err, store.ErrNoBackups) {
		warning, err = err, nil
	}
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("the backup before the database update failed, so the update did not run and nothing was changed: %w. "+
			"Make room on the disk of DATA_DIR (%s) or fix its permissions, then start again", err, cfg.DataDir)
	}
	if path != "" || warning != nil {
		backedUp(path, warning)
	}
	if err := db.Migrate(ctx); err != nil {
		db.Close()
		if path != "" {
			return nil, fmt.Errorf("database migration failed: %w (the database as it was before is in %s)", err, path)
		}
		return nil, fmt.Errorf("database migration failed: %w", err)
	}
	return db, nil
}
