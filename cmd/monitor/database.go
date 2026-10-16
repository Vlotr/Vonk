package main

import (
	"context"
	"fmt"

	"monitor/internal/config"
	"monitor/internal/store"
)

// openDatabase opens the database and brings it up to date, as every
// command does at its start. When the update would change a database that
// has data, a backup comes first (<DATA_DIR>/backups/pre-migrate-…); if
// the backup fails, the update does not run and the command stops.
// backedUp hears the backup's path.
func openDatabase(ctx context.Context, cfg config.Config, backedUp func(path string)) (*store.DB, error) {
	db, err := store.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	path, err := db.BackupBeforeMigrate(ctx)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("the backup before the database update failed, so the update did not run and nothing was changed: %w. "+
			"Make room on the disk of DATA_DIR (%s) or fix its permissions, then start again", err, cfg.DataDir)
	}
	if path != "" {
		backedUp(path)
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
