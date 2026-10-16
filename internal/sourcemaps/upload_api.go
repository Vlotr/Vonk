package sourcemaps

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// The upload API is the part of Sentry's /api/0/ that sentry-cli
// ("sourcemaps upload") and @sentry/vite-plugin call, recorded from both
// (internal/sourcemaps/testdata, docs/decisions.md):
//
//	GET  /api/0/organizations/{org}/                       the organization (the vite plugin looks up its region)
//	GET  /api/0/organizations/{org}/chunk-upload/          how to upload: chunk size, limits, where to
//	POST /api/0/organizations/{org}/chunk-upload/          chunks of a bundle (multipart, gzip)
//	POST /api/0/organizations/{org}/artifactbundle/assemble/  put a bundle together; asked again until "ok"
//	POST /api/0/organizations/{org}/releases/              create a release (the vite plugin)
//	PUT  /api/0/organizations/{org}/releases/{version}/    finalize it
//
// Every call needs an API token (Authorization: Bearer <token>); a token
// uploads only to its projects, which the assemble call names by slug.
// The organization in the path can be anything: this tool has none.
// Answers and errors are Sentry's shape, {"detail": "..."} for errors, so
// the tools show the message.

// Register adds the upload API to the public mux (it signs in with tokens,
// not sessions). Other paths under /api/0/ are left to the caller's JSON 404.
func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/0/organizations/{org}/{$}", s.withToken(s.organization))
	mux.HandleFunc("GET /api/0/organizations/{org}/chunk-upload/{$}", s.withToken(s.chunkOptions))
	mux.HandleFunc("POST /api/0/organizations/{org}/chunk-upload/{$}", s.withToken(s.uploadChunks))
	mux.HandleFunc("POST /api/0/organizations/{org}/artifactbundle/assemble/{$}", s.withToken(s.assemble))
	mux.HandleFunc("POST /api/0/organizations/{org}/releases/{$}", s.withToken(s.createRelease))
	mux.HandleFunc("PUT /api/0/organizations/{org}/releases/{version}/{$}", s.withToken(s.updateRelease))
}

// TokenPrefix starts every API token, as Sentry's organization tokens do.
const TokenPrefix = "sntrys_"

// NewToken returns a new API token in the format of Sentry's organization
// tokens: "sntrys_", a base64 JSON payload with the address of this tool,
// "_" and a random secret. sentry-cli and the vite plugin read the address
// (and the organization) from the payload, so a token is all they need.
// hint is the token's last four characters.
func NewToken(baseURL string, now time.Time) (token, hint string, err error) {
	payload, err := json.Marshal(map[string]any{
		"iat":        float64(now.UnixMilli()) / 1000,
		"url":        baseURL,
		"region_url": baseURL,
		"org":        "monitor",
	})
	if err != nil {
		return "", "", err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", "", err
	}
	token = TokenPrefix + base64.StdEncoding.EncodeToString(payload) + "_" + base64.RawStdEncoding.EncodeToString(secret)
	return token, token[len(token)-4:], nil
}

// withToken signs a request in with its bearer token: 401 without one or
// with one that does not exist (or was deleted).
func (s *Service) withToken(next func(http.ResponseWriter, *http.Request, store.APIToken)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
		token = strings.TrimSpace(token)
		if !strings.EqualFold(scheme, "Bearer") || token == "" {
			detail(w, http.StatusUnauthorized, "Sign in with an API token from Settings → API tokens: Authorization: Bearer <token>.")
			return
		}
		tok, err := s.db.APITokenByHash(r.Context(), s.hashToken(token))
		if errors.Is(err, store.ErrNotFound) {
			detail(w, http.StatusUnauthorized, "This API token does not exist. Make one in Settings → API tokens.")
			return
		}
		if err != nil {
			s.unavailable(w, r, err)
			return
		}
		if err := s.db.TouchAPIToken(r.Context(), tok.ID, s.now()); err != nil {
			s.log.Warn("sourcemaps: note token use", "token", tok.ID, "err", err)
		}
		next(w, r, tok)
	}
}

// organization answers the lookup of the organization in the path. This
// tool has none, so every slug is one; the vite plugin needs no more.
func (s *Service) organization(w http.ResponseWriter, r *http.Request, _ store.APIToken) {
	org := r.PathValue("org")
	write(w, http.StatusOK, map[string]any{"id": "1", "slug": org, "name": org})
}

// chunkOptions tells the client how to upload. Both clients need url,
// chunkSize, chunksPerRequest, maxRequestSize, concurrency and
// hashAlgorithm; the vite plugin also compression. "artifact_bundles" in
// accept makes older sentry-cli versions upload bundles too.
func (s *Service) chunkOptions(w http.ResponseWriter, r *http.Request, _ store.APIToken) {
	write(w, http.StatusOK, map[string]any{
		"url":              s.baseURL + "/api/0/organizations/" + url.PathEscape(r.PathValue("org")) + "/chunk-upload/",
		"chunkSize":        s.maxChunk,
		"chunksPerRequest": 64,
		"maxFileSize":      s.maxBundle,
		"maxRequestSize":   maxRequestBytes,
		"concurrency":      4,
		"hashAlgorithm":    "sha1",
		"compression":      []string{"gzip"},
		"accept":           []string{"artifact_bundles", "sources"},
	})
}

var checksumPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// uploadChunks stores the chunks of a multipart request: fields "file_gzip"
// (gzip, as both clients send them) or "file", each named by its SHA-1.
func (s *Service) uploadChunks(w http.ResponseWriter, r *http.Request, tok store.APIToken) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes+1<<20) // room for the multipart framing
	mr, err := r.MultipartReader()
	if err != nil {
		detail(w, http.StatusBadRequest, "Send chunks as multipart/form-data.")
		return
	}
	stored := 0
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			s.uploadError(w, r, requestError{err})
			return
		}
		if err := s.storePart(tok.ID, part); err != nil {
			s.uploadError(w, r, err)
			return
		}
		stored++
	}
	if stored == 0 {
		detail(w, http.StatusBadRequest, "The request has no chunks.")
		return
	}
	write(w, http.StatusOK, map[string]any{})
}

// storePart stores one part of a chunk upload.
func (s *Service) storePart(tokenID int64, part *multipart.Part) error {
	defer part.Close()
	field := part.FormName()
	if field != "file" && field != "file_gzip" {
		return nil // Sentry ignores other fields too
	}
	checksum := strings.ToLower(part.FileName())
	if !checksumPattern.MatchString(checksum) {
		return errBadUpload("Each chunk must be named by its SHA-1 checksum.")
	}
	release, ok := s.reserveChunk(tokenID)
	if !ok {
		return errBadUpload("Too many chunks wait to be put together. Try again in an hour.")
	}
	defer release()
	var r io.Reader = requestReader{part}
	if field == "file_gzip" {
		zr, err := gzip.NewReader(r)
		if errors.As(err, new(requestError)) {
			return err
		}
		if err != nil {
			return errBadUpload("A file_gzip chunk is not gzip.")
		}
		defer zr.Close()
		r = requestReader{zr} // a broken gzip stream is the client's too
	}
	_, err := s.saveChunk(tokenID, checksum, r)
	return err
}

// requestError is a failure to read what the client sent (a body cut
// short, a broken multipart frame or gzip stream), as opposed to one of
// the disk: the client gets a 400 for it (uploadError).
type requestError struct{ err error }

func (e requestError) Error() string { return "reading the upload: " + e.err.Error() }
func (e requestError) Unwrap() error { return e.err }

// requestReader reads what the client sent and marks its errors as requestErrors.
type requestReader struct{ r io.Reader }

func (r requestReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if err != nil && err != io.EOF {
		err = requestError{err}
	}
	return n, err
}

// assembleRequest is the body of an assemble call.
type assembleRequest struct {
	Checksum string   `json:"checksum"`
	Chunks   []string `json:"chunks"`
	Projects []string `json:"projects"`
	Version  string   `json:"version"`
	Dist     string   `json:"dist"`
}

// assemble puts a bundle together once all its chunks are there and
// answers its state: "not_found" with the chunks still missing, "ok" once
// it is stored (also when it was stored before), "assembling" while
// another request is putting it together, or "error" with the reason.
func (s *Service) assemble(w http.ResponseWriter, r *http.Request, tok store.APIToken) {
	var req assembleRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		detail(w, http.StatusBadRequest, "The body must be JSON with checksum, chunks and projects.")
		return
	}
	req.Checksum = strings.ToLower(req.Checksum)
	if !checksumPattern.MatchString(req.Checksum) || len(req.Chunks) == 0 || int64(len(req.Chunks)) > s.maxBundle/s.maxChunk+1 {
		detail(w, http.StatusBadRequest, "The body needs the bundle's SHA-1 checksum and its chunks.")
		return
	}
	for i, c := range req.Chunks {
		req.Chunks[i] = strings.ToLower(c)
		if !checksumPattern.MatchString(req.Chunks[i]) {
			detail(w, http.StatusBadRequest, "Chunks are named by their SHA-1 checksums.")
			return
		}
	}
	projects, ok := s.tokenProjects(w, r, tok, req.Projects)
	if !ok {
		return
	}

	// A bundle every project has already, for this release and dist, is
	// done: clients upload the same build again, and ask again after an
	// "ok" when they wait for it. The same files for another release are
	// stored again, for that release.
	done := true
	for _, p := range projects {
		has, err := s.db.TouchSourceMapBundle(r.Context(), p.ID, req.Checksum, req.Version, req.Dist, s.now())
		if err != nil {
			s.unavailable(w, r, err)
			return
		}
		done = done && has
	}
	if done {
		writeState(w, "ok", nil, "")
		return
	}

	missing, size := s.missingChunks(tok.ID, req.Chunks)
	if len(missing) > 0 {
		writeState(w, "not_found", missing, "")
		return
	}
	if size > s.maxBundle {
		writeState(w, "error", nil, "Bundles can be at most "+sizeText(s.maxBundle)+".")
		return
	}

	// One token's request at a time per bundle: putting it together
	// removes the token's chunks. Another token has chunks of its own.
	key := assembly{tok.ID, req.Checksum}
	s.mu.Lock()
	busy := s.assembling[key]
	s.assembling[key] = true
	s.mu.Unlock()
	if busy {
		writeState(w, "assembling", nil, "")
		return
	}
	defer func() {
		s.mu.Lock()
		delete(s.assembling, key)
		s.mu.Unlock()
	}()

	err := s.assembleBundle(r.Context(), tok.ID, bundleRequest{Checksum: req.Checksum, Chunks: req.Chunks,
		Projects: projects, Release: req.Version, Dist: req.Dist})
	var bad errBadUpload
	switch {
	case errors.As(err, &bad):
		writeState(w, "error", nil, bad.Error())
	case err != nil:
		s.log.Error("sourcemaps: store bundle", "err", err)
		writeState(w, "error", nil, "The bundle could not be stored. The server log says why.")
	default:
		writeState(w, "ok", nil, "")
	}
}

// tokenProjects returns the projects named by slug, when the token may
// upload to all of them. Otherwise it answers 400 (none named) or 403 (a
// project the token does not have, which is also what an unknown slug gets,
// so a token cannot find out which other projects exist) and returns false.
func (s *Service) tokenProjects(w http.ResponseWriter, r *http.Request, tok store.APIToken, slugs []string) ([]store.Project, bool) {
	if len(slugs) == 0 {
		detail(w, http.StatusBadRequest, "Name the project to upload to (sentry-cli: --project, vite plugin: project).")
		return nil, false
	}
	var projects []store.Project
	for _, slug := range slugs {
		if slices.ContainsFunc(projects, func(p store.Project) bool { return p.Slug == slug }) {
			continue // named twice: stored once
		}
		p, err := s.db.ProjectBySlug(r.Context(), slug)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			s.unavailable(w, r, err)
			return nil, false
		}
		if err != nil || !slices.Contains(tok.ProjectIDs, p.ID) {
			detail(w, http.StatusForbidden, fmt.Sprintf("This API token cannot upload to the project %q. "+
				"Use the slug of a project the token was made for; Settings → API tokens lists them.", slug))
			return nil, false
		}
		projects = append(projects, p)
	}
	return projects, true
}

// createRelease answers the vite plugin's release creation. Releases come
// from the events themselves here, so nothing is stored; the token must
// still have the projects it names.
func (s *Service) createRelease(w http.ResponseWriter, r *http.Request, tok store.APIToken) {
	var req struct {
		Version  string   `json:"version"`
		Projects []string `json:"projects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil || req.Version == "" {
		detail(w, http.StatusBadRequest, "The body must be JSON with the release's version.")
		return
	}
	projects, ok := s.tokenProjects(w, r, tok, req.Projects)
	if !ok {
		return
	}
	slugs := []map[string]string{}
	for _, p := range projects {
		slugs = append(slugs, map[string]string{"slug": p.Slug, "name": p.Name})
	}
	write(w, http.StatusCreated, map[string]any{
		"version":      req.Version,
		"shortVersion": req.Version,
		"projects":     slugs,
		"dateCreated":  s.now().UTC().Format(time.RFC3339),
		"dateReleased": nil,
	})
}

// updateRelease answers the vite plugin's "finalize" of a release.
func (s *Service) updateRelease(w http.ResponseWriter, r *http.Request, _ store.APIToken) {
	var req struct {
		DateReleased *string `json:"dateReleased"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	version := r.PathValue("version")
	write(w, http.StatusOK, map[string]any{
		"version":      version,
		"shortVersion": version,
		"dateReleased": req.DateReleased,
	})
}

// uploadError answers a failed chunk upload: 413 over the size limit, 400
// for what the client got wrong or sent damaged, else a server problem
// (the disk).
func (s *Service) uploadError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		tooLarge *http.MaxBytesError
		bad      errBadUpload
		damaged  requestError
	)
	switch {
	case errors.As(err, &tooLarge):
		detail(w, http.StatusRequestEntityTooLarge, "A chunk upload can be at most "+sizeText(maxRequestBytes)+".")
	case errors.As(err, &bad):
		detail(w, http.StatusBadRequest, bad.Error())
	case errors.As(err, &damaged):
		detail(w, http.StatusBadRequest, "The upload is damaged or cut short: its multipart body or a chunk's gzip.")
	default:
		s.log.Error("sourcemaps: store chunk", "err", err)
		detail(w, http.StatusInternalServerError, "The chunk could not be stored. The server log says why.")
	}
}

// unavailable answers 503 when the database fails.
func (s *Service) unavailable(w http.ResponseWriter, r *http.Request, err error) {
	if httpjson.ClientGone(r) {
		s.log.Debug("sourcemaps: the client went away", "path", r.URL.Path, "err", err)
		return
	}
	s.log.Error("sourcemaps: upload API", "path", r.URL.Path, "err", err)
	w.Header().Set("Retry-After", "60")
	detail(w, http.StatusServiceUnavailable, "The database is not available. Try again in a minute.")
}

// writeState answers an assemble call.
func writeState(w http.ResponseWriter, state string, missing []string, why string) {
	if missing == nil {
		missing = []string{}
	}
	body := map[string]any{"state": state, "missingChunks": missing, "detail": nil}
	if why != "" {
		body["detail"] = why
	}
	write(w, http.StatusOK, body)
}

// detail answers an error the way Sentry's API does, which the tools show.
func detail(w http.ResponseWriter, status int, message string) {
	write(w, status, map[string]string{"detail": message})
}

func write(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// sizeText writes a size limit for people: "100 MiB", or bytes below a MiB.
func sizeText(n int64) string {
	if n >= 1<<20 && n%(1<<20) == 0 {
		return fmt.Sprintf("%d MiB", n>>20)
	}
	return fmt.Sprintf("%d bytes", n)
}
