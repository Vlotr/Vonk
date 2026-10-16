// Package web embeds the built dashboard so the Go binary can serve it.
//
// The Vite app in this directory builds into dist/. A committed placeholder,
// dist/.keep, keeps `go build` working before the dashboard has ever been
// built; such a binary answers dashboard pages with a "not built" message.
// internal/web serves these files.
package web

import "embed"

// Dist holds the contents of dist/ (index.html, assets/ and the placeholder).
//
//go:embed all:dist
var Dist embed.FS
