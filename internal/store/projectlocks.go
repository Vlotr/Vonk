package store

import (
	"context"
	"database/sql"
	"errors"
)

// Deleting a project (DeleteProject) locks the project's row first and
// then, through ON DELETE CASCADE, every row of its tables. On Postgres a
// transaction that takes those locks the other way round, a row of the
// project first and the project later (a foreign key check on an insert
// waits for the project's row), can deadlock with the deletion, which
// Postgres then undoes. So every write transaction that inserts, updates
// or deletes rows of a project's tables (issues, events, page views and
// leaves, summaries and dirty marks, custom events, alerts and their
// state, source maps, member access, invite and token projects) first
// keeps the project with one of the functions below, before its first
// such write. Keeping (FOR KEY SHARE) holds off a deletion until the
// commit and nothing else: writers of the project's own row and other
// keepers go on. A deletion then waits for the transaction, or the
// transaction waits for the deletion and finds the project gone. On SQLite
// the write transaction holds the whole database anyway.

// projectExists reports whether the project exists, and keeps it until
// the transaction ends.
func projectExists(ctx context.Context, tx *writeTx, id int64) (bool, error) {
	var one int
	err := tx.QueryRowContext(ctx, "SELECT 1 FROM projects WHERE id = ?"+tx.forKeyShare(), id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// existingProjects keeps the projects (in id order) and returns their ids
// sorted and without repeats, or ErrUnknownProject when one of them does
// not exist.
func existingProjects(ctx context.Context, tx *writeTx, projectIDs []int64) ([]int64, error) {
	ids := uniqueSorted(projectIDs)
	for _, id := range ids {
		exists, err := projectExists(ctx, tx, id)
		if err != nil {
			return nil, err
		}
		if !exists {
			return nil, &UnknownProjectError{ID: id}
		}
	}
	return ids, nil
}

// keepAllProjects keeps every project until the transaction ends. Changes
// of accounts take it: removing a user, a role or an invite deletes access
// and invite rows of projects that are only known by reading those rows,
// and these changes are rare enough that holding off the deletion of any
// project for a moment costs nothing. A no-op on SQLite.
func keepAllProjects(ctx context.Context, tx *writeTx) error {
	if tx.d != postgresDialect {
		return nil
	}
	_, err := queryIDs(ctx, tx, "SELECT id FROM projects ORDER BY id"+tx.forKeyShare())
	return err
}

// lockAccountChange takes the accounts lock (dialect.go) and then keeps
// every project: the order every change of accounts that writes project
// rows (member access, invite and token projects) takes its locks in.
func lockAccountChange(ctx context.Context, tx *writeTx) error {
	if err := tx.lock(ctx, lockAccounts); err != nil {
		return err
	}
	return keepAllProjects(ctx, tx)
}

// inProject runs write in a write transaction that first keeps the
// project, and returns what write returns; 0 and no error when the project
// is gone (nothing of it is left to write).
func (db *DB) inProject(ctx context.Context, projectID int64, write func(tx *writeTx) (int64, error)) (int64, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // no-op after Commit
	exists, err := projectExists(ctx, tx, projectID)
	if err != nil || !exists {
		return 0, err
	}
	n, err := write(tx)
	if err != nil {
		return 0, err
	}
	return n, tx.Commit()
}
