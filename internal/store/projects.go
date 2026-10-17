package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Defaults for new projects.
const (
	DefaultRetentionDays      = 90
	DefaultRateLimitPerMinute = 1000
)

// Project is one monitored app.
type Project struct {
	ID   int64
	Name string
	Slug string
	// SentryKey is the public key in the project's Sentry DSN.
	SentryKey string
	// PosthogToken is the project API key PostHog SDKs send ("phc_...").
	PosthogToken string
	// RetentionDays is how long data is kept; nil keeps it forever.
	RetentionDays      *int
	TracksLaravel      bool
	TracksBrowser      bool
	TracksPageviews    bool
	RateLimitPerMinute int
	CreatedAt          time.Time
	// RoutePatterns are the project's route patterns ("/products/:slug"),
	// tried before the automatic rules of routes.Normalize.
	RoutePatterns []string
}

// NewProject holds the choices for a project that is about to be created.
type NewProject struct {
	Name string
	// RetentionDays is how long data is kept; 0 means DefaultRetentionDays.
	RetentionDays int
	// KeepForever keeps data without a limit and overrides RetentionDays.
	KeepForever        bool
	TracksLaravel      bool
	TracksBrowser      bool
	TracksPageviews    bool
	RateLimitPerMinute int // 0 means DefaultRateLimitPerMinute
}

// projectColumns lists the columns scanProject expects, in order.
const projectColumns = `id, name, slug, sentry_key, posthog_token, retention_days,
	tracks_laravel, tracks_browser, tracks_pageviews, rate_limit_per_minute, created_at, route_patterns`

// CreateProject stores a new project with fresh keys and a unique slug.
func (db *DB) CreateProject(ctx context.Context, np NewProject) (Project, error) {
	name := strings.TrimSpace(np.Name)
	if name == "" {
		return Project{}, errors.New("project name is required")
	}
	if np.RetentionDays < 0 || np.RateLimitPerMinute < 0 {
		return Project{}, errors.New("retention and rate limit cannot be negative")
	}

	sentryKey, err := newSentryKey()
	if err != nil {
		return Project{}, err
	}
	posthogToken, err := newPosthogToken()
	if err != nil {
		return Project{}, err
	}

	p := Project{
		Name:               name,
		SentryKey:          sentryKey,
		PosthogToken:       posthogToken,
		TracksLaravel:      np.TracksLaravel,
		TracksBrowser:      np.TracksBrowser,
		TracksPageviews:    np.TracksPageviews,
		RateLimitPerMinute: np.RateLimitPerMinute,
		RoutePatterns:      []string{},
		// Stored as unix milliseconds; truncate now so the value we return
		// equals the value a later read returns.
		CreatedAt: time.UnixMilli(time.Now().UnixMilli()).UTC(),
	}
	if !np.KeepForever {
		days := np.RetentionDays
		if days == 0 {
			days = DefaultRetentionDays
		}
		p.RetentionDays = &days
	}
	if p.RateLimitPerMinute == 0 {
		p.RateLimitPerMinute = DefaultRateLimitPerMinute
	}

	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback() // no-op after Commit

	// A project with the same name made at the same moment waits here, then
	// sees this one's slug.
	if err := tx.lock(ctx, lockProjectSlugs); err != nil {
		return Project{}, err
	}
	p.Slug, err = uniqueSlug(ctx, tx, slugify(name))
	if err != nil {
		return Project{}, err
	}

	err = tx.QueryRowContext(ctx, `
		INSERT INTO projects (name, slug, sentry_key, posthog_token, retention_days,
			tracks_laravel, tracks_browser, tracks_pageviews, rate_limit_per_minute, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		RETURNING id`,
		p.Name, p.Slug, p.SentryKey, p.PosthogToken, p.RetentionDays,
		p.TracksLaravel, p.TracksBrowser, p.TracksPageviews, p.RateLimitPerMinute, p.CreatedAt.UnixMilli(),
	).Scan(&p.ID)
	if err != nil {
		return Project{}, fmt.Errorf("insert project: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	return p, nil
}

// ProjectByID returns the project with this id, or ErrNotFound.
func (db *DB) ProjectByID(ctx context.Context, id int64) (Project, error) {
	row := db.read.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id)
	return scanProject(row)
}

// ProjectBySlug returns the project with this slug, or ErrNotFound.
func (db *DB) ProjectBySlug(ctx context.Context, slug string) (Project, error) {
	row := db.read.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE slug = ?", slug)
	return scanProject(row)
}

// ProjectByPosthogToken returns the project with this PostHog token, or ErrNotFound.
func (db *DB) ProjectByPosthogToken(ctx context.Context, token string) (Project, error) {
	row := db.read.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE posthog_token = ?", token)
	return scanProject(row)
}

// MaxRoutePatterns is how many route patterns a project has at most, and
// MaxRoutePatternChars how many characters one may have.
const (
	MaxRoutePatterns     = 100
	MaxRoutePatternChars = 300
)

// routePatternsJSON returns the route_patterns column for patterns: blank
// ones left out, the others trimmed, or an error when there are too many
// or one is too long.
func routePatternsJSON(patterns []string) (string, error) {
	clean := []string{}
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if utf8.RuneCountInString(p) > MaxRoutePatternChars {
			return "", fmt.Errorf("a route pattern is longer than %d characters", MaxRoutePatternChars)
		}
		clean = append(clean, p)
	}
	if len(clean) > MaxRoutePatterns {
		return "", fmt.Errorf("at most %d route patterns", MaxRoutePatterns)
	}
	data, err := json.Marshal(clean)
	return string(data), err
}

// ProjectBySentryKey returns the project that owns this DSN key, or ErrNotFound.
func (db *DB) ProjectBySentryKey(ctx context.Context, key string) (Project, error) {
	row := db.read.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE sentry_key = ?", key)
	return scanProject(row)
}

// ListProjects returns all projects sorted by name.
func (db *DB) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := db.read.QueryContext(ctx, "SELECT "+projectColumns+" FROM projects ORDER BY lower(name), id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

// scanner is the Scan method shared by *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func scanProject(s scanner) (Project, error) {
	var (
		p         Project
		retention sql.NullInt64
		createdAt int64
		patterns  string
	)
	err := s.Scan(&p.ID, &p.Name, &p.Slug, &p.SentryKey, &p.PosthogToken, &retention,
		&p.TracksLaravel, &p.TracksBrowser, &p.TracksPageviews, &p.RateLimitPerMinute, &createdAt, &patterns)
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, err
	}
	if retention.Valid {
		days := int(retention.Int64)
		p.RetentionDays = &days
	}
	p.CreatedAt = time.UnixMilli(createdAt).UTC()
	if err := json.Unmarshal([]byte(patterns), &p.RoutePatterns); err != nil {
		return Project{}, fmt.Errorf("project %d: route patterns: %w", p.ID, err)
	}
	if p.RoutePatterns == nil {
		p.RoutePatterns = []string{}
	}
	return p, nil
}

// slugify turns a name into lowercase ASCII words joined by dashes:
// "Customer portal" becomes "customer-portal".
func slugify(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
		} else {
			dash = true
		}
	}
	if b.Len() == 0 {
		return "project"
	}
	return b.String()
}

// uniqueSlug returns base, or base-2, base-3 and so on when base is taken.
func uniqueSlug(ctx context.Context, tx *writeTx, base string) (string, error) {
	for n := 1; ; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		var taken int
		err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM projects WHERE slug = ?", candidate).Scan(&taken)
		if err != nil {
			return "", err
		}
		if taken == 0 {
			return candidate, nil
		}
	}
}

// newSentryKey returns 32 lowercase hex characters, the format Sentry uses for DSN keys.
func newSentryKey() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// newPosthogToken returns "phc_" followed by 43 url-safe characters.
func newPosthogToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "phc_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// ProjectIDs returns the ids of all projects, sorted.
func (db *DB) ProjectIDs(ctx context.Context) ([]int64, error) {
	return queryIDs(ctx, db.read, "SELECT id FROM projects ORDER BY id")
}

// ProjectSettings are the choices an admin can change on a project.
type ProjectSettings struct {
	Name string
	// RetentionDays is how long data is kept; nil keeps it forever.
	RetentionDays   *int
	TracksLaravel   bool
	TracksBrowser   bool
	TracksPageviews bool
	// RoutePatterns replace the project's route patterns (blank ones are
	// left out; at most MaxRoutePatterns of at most MaxRoutePatternChars);
	// nil leaves them as they are. They apply to data that comes in from
	// now on.
	RoutePatterns *[]string
}

// UpdateProject replaces a project's settings (and its route patterns,
// when s has them) in one write, and returns the project as it is
// afterwards, or ErrNotFound. When anything is refused nothing changes.
// The slug stays as it was created.
func (db *DB) UpdateProject(ctx context.Context, id int64, s ProjectSettings) (Project, error) {
	return db.ChangeProject(ctx, id, func(old *ProjectSettings) error {
		*old = s
		return nil
	})
}

// ChangeProject changes a project's settings in one write transaction: it
// locks the project's row (FOR NO KEY UPDATE: the change leaves its id,
// slug and keys alone, so ingest batches that keep the project go on),
// reads its settings, lets change edit them, and writes them back, with
// the route patterns when change sets them (RoutePatterns is nil going
// in). Two changes at once therefore run one after the other and never
// undo each other (as UpdateAlertSettings). It returns the project as
// stored, ErrNotFound, or the error change returned, and writes nothing
// then. change must not touch the database.
func (db *DB) ChangeProject(ctx context.Context, id int64, change func(*ProjectSettings) error) (Project, error) {
	tx, err := db.write.BeginTx(ctx)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback() // no-op after Commit
	p, err := scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?"+tx.forNoKeyUpdate(), id))
	if err != nil {
		return Project{}, err
	}
	s := ProjectSettings{Name: p.Name, RetentionDays: p.RetentionDays,
		TracksLaravel: p.TracksLaravel, TracksBrowser: p.TracksBrowser, TracksPageviews: p.TracksPageviews}
	if err := change(&s); err != nil {
		return Project{}, err
	}

	name := strings.TrimSpace(s.Name)
	if name == "" {
		return Project{}, errors.New("project name is required")
	}
	if s.RetentionDays != nil && *s.RetentionDays <= 0 {
		return Project{}, errors.New("retention must be a positive number of days")
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE projects SET name = ?, retention_days = ?, tracks_laravel = ?, tracks_browser = ?, tracks_pageviews = ?
		WHERE id = ?`,
		name, s.RetentionDays, s.TracksLaravel, s.TracksBrowser, s.TracksPageviews, id); err != nil {
		return Project{}, err
	}
	if s.RoutePatterns != nil {
		patterns, err := routePatternsJSON(*s.RoutePatterns)
		if err != nil {
			return Project{}, err
		}
		if _, err := tx.ExecContext(ctx, "UPDATE projects SET route_patterns = ? WHERE id = ?", patterns, id); err != nil {
			return Project{}, err
		}
	}
	p, err = scanProject(tx.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id))
	if err != nil {
		return Project{}, err
	}
	return p, tx.Commit()
}

// RotateProjectKeys gives a project a new Sentry key and PostHog token. The
// old ones stop working at once. It returns the project with its new keys,
// or ErrNotFound.
func (db *DB) RotateProjectKeys(ctx context.Context, id int64) (Project, error) {
	sentryKey, err := newSentryKey()
	if err != nil {
		return Project{}, err
	}
	posthogToken, err := newPosthogToken()
	if err != nil {
		return Project{}, err
	}
	res, err := db.write.ExecContext(ctx, "UPDATE projects SET sentry_key = ?, posthog_token = ? WHERE id = ?",
		sentryKey, posthogToken, id)
	if err != nil {
		return Project{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Project{}, ErrNotFound
	}
	return db.projectByIDForWrite(ctx, id)
}

// projectByIDForWrite reads a project through the write connection, so it
// sees a change that was just committed there.
func (db *DB) projectByIDForWrite(ctx context.Context, id int64) (Project, error) {
	return scanProject(db.write.QueryRowContext(ctx, "SELECT "+projectColumns+" FROM projects WHERE id = ?", id))
}

// DeleteProject removes a project with all its issues, events and access
// rows, or returns ErrNotFound.
func (db *DB) DeleteProject(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, "DELETE FROM projects WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
