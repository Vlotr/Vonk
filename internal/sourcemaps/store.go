// Package sourcemaps makes minified JavaScript errors readable. It speaks
// the part of Sentry's API that sentry-cli and @sentry/vite-plugin use to
// upload source maps (upload_api.go), keeps the uploaded files on disk with
// their rows in source_maps (this file), and maps the frames of a stack
// trace back to the original files, lines and functions (symbolicate.go).
//
// Files live under <DATA_DIR>/sourcemaps/:
//
//	chunks/<token id>/<sha1>  pieces of a bundle being uploaded, kept an hour
//	tmp/                      bundles while they are put together
//	<project id>/<name>       the files of stored bundles
package sourcemaps

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor/internal/store"
)

// Limits of the upload API.
const (
	// MaxBundleBytes is the largest bundle (a zip of minified files and
	// their maps) a client may upload.
	MaxBundleBytes = 100 << 20
	// ChunkBytes is the size of the pieces clients upload a bundle in.
	ChunkBytes = 8 << 20
	// maxRequestBytes is how much one chunk upload request may carry.
	maxRequestBytes = 32 << 20
	// maxExtractBytes is how much a bundle may unpack to: zip compresses
	// source maps several times, but a zip bomb stops here.
	maxExtractBytes = 1 << 30
	// maxStagedBytes is how much one token may have in chunks at once.
	maxStagedBytes = 256 << 20
	// chunkTTL is how long chunks of a bundle that was never put together are kept.
	chunkTTL = time.Hour
)

// Options configure a Service.
type Options struct {
	DB *store.DB
	// Dir is <DATA_DIR>/sourcemaps.
	Dir string
	// BaseURL is BASE_URL: tokens name it, and clients upload chunks to it.
	BaseURL string
	// HashToken hashes an API token as the database stores it (auth.Service.HashToken).
	HashToken func(string) string
	Logger    *slog.Logger
	// CacheBytes is how much memory parsed files may take; 0 means DefaultCacheBytes.
	CacheBytes int64
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service stores uploaded source maps and symbolicates events with them.
type Service struct {
	db        *store.DB
	dir       string
	baseURL   string
	hashToken func(string) string
	log       *slog.Logger
	now       func() time.Time
	cache     *cache

	// maxBundle is MaxBundleBytes; tests lower it.
	maxBundle int64

	mu         sync.Mutex
	assembling map[string]bool // bundles being put together, by checksum
}

// New returns a Service. Its directories are made when the first upload comes in.
func New(o Options) *Service {
	s := &Service{db: o.DB, dir: o.Dir, baseURL: o.BaseURL, hashToken: o.HashToken, log: o.Logger, now: o.Now,
		maxBundle: MaxBundleBytes, assembling: map[string]bool{}}
	if s.now == nil {
		s.now = time.Now
	}
	size := o.CacheBytes
	if size <= 0 {
		size = DefaultCacheBytes
	}
	s.cache = newCache(size)
	return s
}

// chunkDir is where a token's chunks wait to be put together.
func (s *Service) chunkDir(tokenID int64) string {
	return filepath.Join(s.dir, "chunks", strconv.FormatInt(tokenID, 10))
}

// saveChunk stores one chunk of a token's upload, after checking that its
// SHA-1 is the checksum the client named it by, and returns its size. r is
// the chunk as it was sent, already decompressed; it may be at most ChunkBytes.
func (s *Service) saveChunk(tokenID int64, checksum string, r io.Reader) (int64, error) {
	dir := s.chunkDir(tokenID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, "upload-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename

	sum := sha1.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(r, ChunkBytes+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if n > ChunkBytes {
		return 0, errBadUpload(fmt.Sprintf("A chunk can be at most %d bytes.", ChunkBytes))
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != checksum {
		return 0, errBadUpload(fmt.Sprintf("The chunk named %s has the checksum %s.", checksum, got))
	}
	return n, os.Rename(tmp.Name(), filepath.Join(dir, checksum))
}

// stagedBytes returns how many bytes of chunks a token has waiting.
func (s *Service) stagedBytes(tokenID int64) int64 {
	entries, err := os.ReadDir(s.chunkDir(tokenID))
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// errBadUpload is a problem with what the client sent; its text is shown to people.
type errBadUpload string

func (e errBadUpload) Error() string { return string(e) }

// bundleRequest is what a client asks to put together: the checksum of
// the whole bundle, its chunks in order, and where it goes.
type bundleRequest struct {
	Checksum string
	Chunks   []string
	Projects []store.Project
	Release  string
	Dist     string
}

// missingChunks returns the chunks of the request the token has not
// uploaded yet, and the size of those it has.
func (s *Service) missingChunks(tokenID int64, chunks []string) (missing []string, size int64) {
	missing = []string{}
	for _, c := range chunks {
		info, err := os.Stat(filepath.Join(s.chunkDir(tokenID), c))
		if err != nil {
			missing = append(missing, c)
			continue
		}
		size += info.Size()
	}
	return missing, size
}

// assembleBundle puts a bundle together from the token's chunks, checks
// its checksum, and stores its minified files and source maps for each
// project. The chunks are removed afterwards.
func (s *Service) assembleBundle(ctx context.Context, tokenID int64, req bundleRequest) error {
	tmpDir := filepath.Join(s.dir, "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return err
	}
	bundle, err := os.CreateTemp(tmpDir, "bundle-*.zip")
	if err != nil {
		return err
	}
	defer os.Remove(bundle.Name())
	defer bundle.Close()

	sum := sha1.New()
	for _, c := range req.Chunks {
		if err := appendFile(io.MultiWriter(bundle, sum), filepath.Join(s.chunkDir(tokenID), c)); err != nil {
			return err
		}
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != req.Checksum {
		return errBadUpload(fmt.Sprintf("The chunks make a bundle with checksum %s, not %s.", got, req.Checksum))
	}
	size, err := bundle.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(bundle, size)
	if err != nil {
		return errBadUpload("The bundle is not a zip file.")
	}
	files, err := s.unpackBundle(zr, req)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errBadUpload("The bundle has no minified files or source maps.")
	}
	stored, err := s.db.SaveSourceMapFiles(ctx, files)
	if err != nil {
		removeFiles(s.dir, files)
		return err
	}
	if len(stored) < len(files) { // the same bundle was stored in between
		keep := map[string]bool{}
		for _, f := range stored {
			keep[f.Path] = true
		}
		var extra []store.SourceMapFile
		for _, f := range files {
			if !keep[f.Path] {
				extra = append(extra, f)
			}
		}
		removeFiles(s.dir, extra)
	}
	for _, c := range req.Chunks {
		os.Remove(filepath.Join(s.chunkDir(tokenID), c))
	}
	s.log.Info("sourcemaps: bundle stored", "files", len(stored), "release", req.Release, "bytes", size)
	return nil
}

// appendFile copies the file at name to w.
func appendFile(w io.Writer, name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// manifest is the manifest.json of an artifact bundle.
type manifest struct {
	Files map[string]struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	} `json:"files"`
	Release string `json:"release"`
	Dist    string `json:"dist"`
}

// unpackBundle writes the bundle's minified files and source maps to each
// project's directory and returns their rows (not stored yet).
func (s *Service) unpackBundle(zr *zip.Reader, req bundleRequest) ([]store.SourceMapFile, error) {
	var m manifest
	if err := readJSONEntry(zr, "manifest.json", &m); err != nil {
		return nil, err
	}
	release, dist := req.Release, req.Dist
	if release == "" {
		release, dist = m.Release, m.Dist
	}

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	var (
		files     []store.SourceMapFile
		extracted int64
	)
	for name, info := range m.Files {
		kind := ""
		switch info.Type {
		case "source_map":
			kind = store.SourceMapKind
		case "minified_source", "source":
			kind = store.MinifiedSourceKind
		default:
			continue // indexed RAM bundles and other kinds have no use here
		}
		entry := entries[name]
		if entry == nil || info.URL == "" {
			continue
		}
		for _, p := range req.Projects {
			rel, n, err := s.extract(entry, p.ID, maxExtractBytes-extracted)
			if err != nil {
				removeFiles(s.dir, files)
				return nil, err
			}
			extracted += n
			files = append(files, store.SourceMapFile{
				ProjectID:  p.ID,
				Release:    release,
				Dist:       dist,
				File:       info.URL,
				Kind:       kind,
				DebugID:    normalizeDebugID(header(info.Headers, "debug-id")),
				Sourcemap:  header(info.Headers, "sourcemap", "x-sourcemap"),
				Bundle:     req.Checksum,
				Path:       rel,
				Size:       n,
				UploadedAt: s.now(),
			})
		}
	}
	return files, nil
}

// extract writes one zip entry to the project's directory and returns its
// path relative to the source map directory and its size. It fails when
// the entry unpacks to more than limit bytes.
func (s *Service) extract(entry *zip.File, projectID int64, limit int64) (string, int64, error) {
	dir := filepath.Join(s.dir, strconv.FormatInt(projectID, 10))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", 0, err
	}
	rc, err := entry.Open()
	if err != nil {
		return "", 0, errBadUpload(fmt.Sprintf("The bundle's file %s cannot be read.", entry.Name))
	}
	defer rc.Close()
	name := randomName()
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		return "", 0, err
	}
	n, err := io.Copy(out, io.LimitReader(rc, limit+1))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	rel := path.Join(strconv.FormatInt(projectID, 10), name)
	if err == nil && n > limit {
		err = errBadUpload(fmt.Sprintf("The bundle unpacks to more than %d bytes.", int64(maxExtractBytes)))
	}
	var bad errBadUpload
	if err != nil && !errors.As(err, &bad) && !isDiskError(err) {
		// Reading the entry failed (a bad checksum, a broken deflate stream).
		err = errBadUpload(fmt.Sprintf("The bundle's file %s is damaged.", entry.Name))
	}
	if err != nil {
		os.Remove(filepath.Join(dir, name))
		return "", 0, err
	}
	return rel, n, nil
}

// isDiskError reports whether err came from writing to the disk rather than
// from reading the zip.
func isDiskError(err error) bool {
	var pathErr *fs.PathError
	return errors.As(err, &pathErr)
}

// readJSONEntry decodes a small JSON file of the bundle.
func readJSONEntry(zr *zip.Reader, name string, v any) error {
	f, err := zr.Open(name)
	if err != nil {
		return errBadUpload("The bundle has no " + name + ".")
	}
	defer f.Close()
	if err := json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(v); err != nil {
		return errBadUpload("The bundle's " + name + " is not valid JSON.")
	}
	return nil
}

// header returns the first of names in headers, ignoring case.
func header(headers map[string]string, names ...string) string {
	for _, name := range names {
		for k, v := range headers {
			if strings.EqualFold(k, name) {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// normalizeDebugID returns a debug id lowercase with dashes
// (c5bc0f5b-2a6f-50f4-aafe-19e667444d31); SDKs and tools write it either way.
func normalizeDebugID(id string) string {
	id = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
	if len(id) < 32 {
		return ""
	}
	id = id[:32] // an appendix ("...-0") is not part of the UUID
	if _, err := hex.DecodeString(id); err != nil {
		return ""
	}
	return id[:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:]
}

func randomName() string {
	b := make([]byte, 12)
	rand.Read(b) // never fails; see crypto/rand
	return hex.EncodeToString(b)
}

// removeFiles deletes the files of rows that will not be stored.
func removeFiles(dir string, files []store.SourceMapFile) {
	for _, f := range files {
		os.Remove(filepath.Join(dir, filepath.FromSlash(f.Path)))
	}
}

// readFile returns a stored file's content.
func (s *Service) readFile(f store.SourceMapFile) ([]byte, error) {
	return os.ReadFile(filepath.Join(s.dir, filepath.FromSlash(f.Path)))
}

// DeleteUploadedBefore deletes at most limit of a project's files uploaded
// before cutoff (retention counts from the upload), from disk and from the
// database, and returns how many it deleted.
func (s *Service) DeleteUploadedBefore(ctx context.Context, projectID int64, cutoff time.Time, limit int) (int64, error) {
	files, err := s.db.SourceMapsUploadedBefore(ctx, projectID, cutoff, limit)
	if err != nil || len(files) == 0 {
		return 0, err
	}
	ids := make([]int64, len(files))
	for i, f := range files {
		ids[i] = f.ID
		if err := os.Remove(filepath.Join(s.dir, filepath.FromSlash(f.Path))); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	return s.db.DeleteSourceMapFiles(ctx, ids)
}

// CleanUp removes what uploads left behind: chunks and half-assembled
// bundles older than an hour, and files no row points to any more (the
// files of deleted projects). The sweep job runs it every hour.
func (s *Service) CleanUp(ctx context.Context, now time.Time) {
	removeOld := func(dir string) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if info, err := e.Info(); err == nil && !e.IsDir() && now.Sub(info.ModTime()) > chunkTTL {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
		os.Remove(dir) // only when empty
	}
	tokens, _ := os.ReadDir(filepath.Join(s.dir, "chunks"))
	for _, t := range tokens {
		removeOld(filepath.Join(s.dir, "chunks", t.Name()))
	}
	removeOld(filepath.Join(s.dir, "tmp"))

	known, err := s.db.SourceMapPaths(ctx)
	if err != nil {
		s.log.Warn("sourcemaps: clean up", "err", err)
		return
	}
	projects, _ := os.ReadDir(s.dir)
	for _, p := range projects {
		if _, err := strconv.ParseInt(p.Name(), 10, 64); err != nil || !p.IsDir() {
			continue // chunks/, tmp/
		}
		dir := filepath.Join(s.dir, p.Name())
		files, _ := os.ReadDir(dir)
		for _, f := range files {
			info, err := f.Info()
			// A file written in the last hour may be waiting for its row.
			if err != nil || known[path.Join(p.Name(), f.Name())] || now.Sub(info.ModTime()) < chunkTTL {
				continue
			}
			os.Remove(filepath.Join(dir, f.Name()))
		}
		os.Remove(dir) // only when empty
	}
}
