package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Kinds of uploaded files.
const (
	SourceMapKind      = "source_map"
	MinifiedSourceKind = "minified_source"
)

// SourceMapFile is one file of an uploaded artifact bundle: a source map
// or the minified file it maps. The content is on disk at Path, relative to
// the source map directory (internal/sourcemaps keeps it).
type SourceMapFile struct {
	ID        int64
	ProjectID int64
	Release   string
	Dist      string
	// File is the URL the bundle gives, such as "~/assets/index-D8xk2Lp1.js.map".
	File    string
	Kind    string // SourceMapKind or MinifiedSourceKind
	DebugID string // lowercase, with dashes; "" without one
	// Sourcemap is the map a minified file points to, relative to File.
	Sourcemap  string
	Bundle     string // the SHA-1 of the bundle it came in
	Path       string
	Size       int64
	UploadedAt time.Time
}

const sourceMapColumns = `id, project_id, release, dist, file, kind, debug_id, sourcemap, bundle, path, size, uploaded_at`

func scanSourceMap(s scanner) (SourceMapFile, error) {
	var (
		f        SourceMapFile
		uploaded int64
	)
	err := s.Scan(&f.ID, &f.ProjectID, &f.Release, &f.Dist, &f.File, &f.Kind, &f.DebugID, &f.Sourcemap,
		&f.Bundle, &f.Path, &f.Size, &uploaded)
	if errors.Is(err, sql.ErrNoRows) {
		return SourceMapFile{}, ErrNotFound
	}
	if err != nil {
		return SourceMapFile{}, err
	}
	f.UploadedAt = time.UnixMilli(uploaded).UTC()
	return f, nil
}

// TouchSourceMapBundle marks a bundle as uploaded again, now, when the
// project already stored it (the bundle with this checksum, for this
// release and dist), and reports whether it did: uploading a bundle again
// counts as an upload for retention. The same files for another release
// are another upload: they get rows of their own (SaveSourceMapFiles), or
// lookups by that release would not find them.
func (db *DB) TouchSourceMapBundle(ctx context.Context, projectID int64, bundle, release, dist string, now time.Time) (bool, error) {
	res, err := db.write.ExecContext(ctx, "UPDATE source_maps SET uploaded_at = ? WHERE project_id = ? AND bundle = ? AND release = ? AND dist = ?",
		now.UnixMilli(), projectID, bundle, release, dist)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// SaveSourceMapFiles stores the rows of one uploaded bundle in one
// transaction. Files of a project that already has the bundle for the
// file's release and dist are skipped (an upload sent twice at once); the
// result says which files were stored. Files of projects that no longer
// exist give ErrUnknownProject.
func (db *DB) SaveSourceMapFiles(ctx context.Context, files []SourceMapFile) ([]SourceMapFile, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() // no-op after Commit

	// The same bundle saved at the same moment waits here, then finds it stored.
	if err := tx.lock(ctx, lockSourceMapBundles); err != nil {
		return nil, err
	}
	type upload struct {
		projectID             int64
		bundle, release, dist string
	}
	known := map[upload]bool{} // the bundle was there before this call
	var stored []SourceMapFile
	for _, f := range files {
		key := upload{f.ProjectID, f.Bundle, f.Release, f.Dist}
		had, checked := known[key]
		if !checked {
			if _, err := existingProjects(ctx, tx, []int64{f.ProjectID}); err != nil {
				return nil, err
			}
			err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM source_maps WHERE project_id = ? AND bundle = ? AND release = ? AND dist = ?)",
				f.ProjectID, f.Bundle, f.Release, f.Dist).Scan(&had)
			if err != nil {
				return nil, err
			}
			known[key] = had
		}
		if had {
			continue
		}
		f.UploadedAt = msTime(f.UploadedAt)
		f.DebugID = strings.ToLower(f.DebugID)
		err := tx.QueryRowContext(ctx, `
			INSERT INTO source_maps (project_id, release, dist, file, kind, debug_id, sourcemap, bundle, path, size, uploaded_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
			f.ProjectID, f.Release, f.Dist, f.File, f.Kind, f.DebugID, f.Sourcemap, f.Bundle, f.Path, f.Size,
			f.UploadedAt.UnixMilli()).Scan(&f.ID)
		if err != nil {
			return nil, fmt.Errorf("insert source map file %s: %w", f.File, err)
		}
		stored = append(stored, f)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if len(stored) > 0 {
		db.sourceMapsVersion.Add(1)
	}
	return stored, nil
}

// HasSourceMaps reports whether a project has any uploaded file.
func (db *DB) HasSourceMaps(ctx context.Context, projectID int64) (bool, error) {
	var has bool
	err := db.read.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM source_maps WHERE project_id = ?)", projectID).Scan(&has)
	return has, err
}

// SourceMapsVersion changes after every change to the stored source map
// files this process made (SaveSourceMapFiles, DeleteSourceMapFiles): a
// cache of what HasSourceMaps said knows when to ask again. It moves after
// the commit, so whoever reads it before asking never keeps an answer from
// before a change it already saw.
func (db *DB) SourceMapsVersion() int64 {
	return db.sourceMapsVersion.Load()
}

// SourceMapByDebugID returns the newest file of a kind with this debug id
// in a project, or ErrNotFound.
func (db *DB) SourceMapByDebugID(ctx context.Context, projectID int64, debugID, kind string) (SourceMapFile, error) {
	return scanSourceMap(db.read.QueryRowContext(ctx, "SELECT "+sourceMapColumns+` FROM source_maps
		WHERE project_id = ? AND debug_id = ? AND kind = ? ORDER BY uploaded_at DESC, id DESC LIMIT 1`,
		projectID, strings.ToLower(debugID), kind))
}

// SourceMapByFile returns the newest file of a kind uploaded for a release
// under one of these URLs (in the order given: the first that exists
// wins), or ErrNotFound.
func (db *DB) SourceMapByFile(ctx context.Context, projectID int64, release, kind string, files ...string) (SourceMapFile, error) {
	for _, file := range files {
		f, err := scanSourceMap(db.read.QueryRowContext(ctx, "SELECT "+sourceMapColumns+` FROM source_maps
			WHERE project_id = ? AND release = ? AND file = ? AND kind = ? ORDER BY uploaded_at DESC, id DESC LIMIT 1`,
			projectID, release, file, kind))
		if !errors.Is(err, ErrNotFound) {
			return f, err
		}
	}
	return SourceMapFile{}, ErrNotFound
}

// SourceMapsUploadedBefore returns at most limit of a project's files
// uploaded before before, for retention.
func (db *DB) SourceMapsUploadedBefore(ctx context.Context, projectID int64, before time.Time, limit int) ([]SourceMapFile, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT "+sourceMapColumns+` FROM source_maps
		WHERE project_id = ? AND uploaded_at < ? ORDER BY id LIMIT ?`, projectID, before.UnixMilli(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var files []SourceMapFile
	for rows.Next() {
		f, err := scanSourceMap(rows)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// DeleteSourceMapFiles deletes the rows with these ids and returns how many it deleted.
func (db *DB) DeleteSourceMapFiles(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() // no-op after Commit
	var total int64
	for _, id := range ids {
		res, err := tx.ExecContext(ctx, "DELETE FROM source_maps WHERE id = ?", id)
		if err != nil {
			return 0, err
		}
		n, _ := res.RowsAffected()
		total += n
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	db.sourceMapsVersion.Add(1)
	return total, nil
}

// SourceMapPaths returns the path of every stored file, for the sweep that
// removes files nothing points to any more.
func (db *DB) SourceMapPaths(ctx context.Context) (map[string]bool, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT path FROM source_maps")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := map[string]bool{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		paths[p] = true
	}
	return paths, rows.Err()
}
