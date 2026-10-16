// Package web serves the dashboard: a single-page React app built into the binary.
package web

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"

	dashboard "monitor/web"
)

// notBuiltMessage is shown when the binary was built without the dashboard.
const notBuiltMessage = "The dashboard is not built into this binary. Run `make build`, or use `make dev` during development.\n"

// Handler serves the embedded dashboard build (web/dist).
func Handler() http.Handler {
	files, err := fs.Sub(dashboard.Dist, "dist")
	if err != nil {
		panic(err) // cannot happen: "dist" is a fixed directory inside the embed
	}
	return NewHandler(files)
}

// NewHandler serves the dashboard files in files. Files that exist are served
// as they are. A missing file under /assets/ gets a 404, so a missing script
// is never answered with HTML; so do hidden paths such as /.keep. Every other
// path, such as /projects/1/issues or /releases/v1.2.3, gets index.html so
// the React router can show it: client routes may contain dots.
func NewHandler(files fs.FS) http.Handler {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		index = nil // not built; see notBuiltMessage
	}
	fileServer := http.FileServerFS(files)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		isAsset := strings.HasPrefix(name, "assets/")

		if name != "index.html" && !hidden(name) && isFile(files, name) {
			if isAsset {
				// Vite puts a content hash in every asset name, so they never change.
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			fileServer.ServeHTTP(w, r)
			return
		}
		if isAsset || hidden(name) {
			http.NotFound(w, r)
			return
		}

		if index == nil {
			http.Error(w, notBuiltMessage, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeContent(w, r, "index.html", time.Time{}, bytes.NewReader(index))
	})
}

// isFile reports whether name is a regular file in files.
func isFile(files fs.FS, name string) bool {
	if name == "" || name == "." {
		return false
	}
	info, err := fs.Stat(files, name)
	return err == nil && !info.IsDir()
}

// hidden reports whether any part of name starts with a dot, like the
// dist/.keep placeholder. Such files are never served.
func hidden(name string) bool {
	for part := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}
