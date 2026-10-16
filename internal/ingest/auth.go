package ingest

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"monitor/internal/store"
)

// errUnauthorized means the key is missing, unknown or belongs to another project.
var errUnauthorized = errors.New("unknown DSN key for this project")

// keyFromRequest returns the DSN key from the X-Sentry-Auth header (server
// SDKs) or the sentry_key query parameter (browser SDKs), or "".
func keyFromRequest(r *http.Request) string {
	if key := authField(r.Header.Get("X-Sentry-Auth"), "sentry_key"); key != "" {
		return key
	}
	return r.URL.Query().Get("sentry_key")
}

// authField reads one field of an auth header such as
// "Sentry sentry_version=7, sentry_client=sentry.php/4.34.0, sentry_key=abc".
func authField(header, name string) string {
	header = strings.TrimSpace(header)
	if len(header) > 7 && strings.EqualFold(header[:7], "sentry ") {
		header = header[7:]
	}
	for part := range strings.SplitSeq(header, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok && strings.TrimSpace(key) == name {
			return strings.Trim(strings.TrimSpace(value), `"`)
		}
	}
	return ""
}

// keyFromDSN returns the key of a DSN such as "https://abc@monitor.example.com/1", or "".
func keyFromDSN(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil || u.User == nil {
		return ""
	}
	return u.User.Username()
}

// authenticate returns the project that owns key, if it is the project in the
// URL. It returns errUnauthorized when it is not, or a database error.
func authenticate(ctx context.Context, db *store.DB, key string, projectID int64) (store.Project, error) {
	if key == "" {
		return store.Project{}, errUnauthorized
	}
	p, err := db.ProjectBySentryKey(ctx, key)
	if errors.Is(err, store.ErrNotFound) {
		return store.Project{}, errUnauthorized
	}
	if err != nil {
		return store.Project{}, err
	}
	if p.ID != projectID {
		return store.Project{}, errUnauthorized
	}
	return p, nil
}
