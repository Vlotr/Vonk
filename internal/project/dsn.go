// Package project holds project helpers that are not database queries.
package project

import (
	"net/url"
	"strconv"

	"monitor/internal/store"
)

// DSN returns the Sentry DSN for p: the base URL with the project's key as the
// user and the project id as the path, for example
// "https://<key>@monitor.example.com/1". baseURL must be the root of a host;
// config.Load guarantees that. DSN returns "" when baseURL cannot be parsed.
func DSN(baseURL string, p store.Project) string {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" {
		return ""
	}
	dsn := url.URL{
		Scheme: base.Scheme,
		User:   url.User(p.SentryKey),
		Host:   base.Host,
		Path:   "/" + strconv.FormatInt(p.ID, 10),
	}
	return dsn.String()
}
