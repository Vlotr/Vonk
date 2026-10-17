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
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"monitor/internal/store"
)

// Limits of the upload API. New puts them in the Service's fields, which
// tests lower.
const (
	// MaxBundleBytes is the largest bundle (a zip of minified files and
	// their maps) a client may upload.
	MaxBundleBytes = 100 << 20
	// ChunkBytes is the size of the pieces clients upload a bundle in, and
	// the most one chunk may be once its gzip is undone: a gzip bomb stops here.
	ChunkBytes = 8 << 20
	// maxRequestBytes is how much one chunk upload request may carry.
	maxRequestBytes = 32 << 20
	// maxExtractBytes is how much a bundle may unpack to: zip compresses
	// source maps several times, but a zip bomb stops here.
	maxExtractBytes = 1 << 30
	// maxStagedBytes is how much one token may have in chunks at once.
	maxStagedBytes = 256 << 20
	// maxBundleFiles is how many files a bundle's manifest may list.
	maxBundleFiles = 10_000
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
	// CacheBytes is how much memory parsed files may take
	// (SOURCEMAP_CACHE_MB); 0 means DefaultCacheBytes. One file is used up
	// to a quarter of it.
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

	// The limits of uploads (see the constants); tests lower them.
	maxBundle  int64 // MaxBundleBytes
	maxChunk   int64 // ChunkBytes
	maxStaged  int64 // maxStagedBytes
	maxExtract int64 // maxExtractBytes
	// readAll reads a stored file: os.ReadFile; tests count the reads.
	readAll func(name string) ([]byte, error)

	mu         sync.Mutex
	assembling map[assembly]bool   // bundles being put together
	reserved   map[int64]int64     // per token: room taken by chunks being written (reserveChunk)
	hasMaps    map[int64]mapsKnown // per project: whether it has source maps (projectHasMaps)

	badPaths sync.Map // ids of rows whose path pathOf refused, logged once
}

// assembly is one token's request to put a bundle together.
type assembly struct {
	tokenID  int64
	checksum string
}

// New returns a Service. Its directories are made when the first upload comes in.
func New(o Options) *Service {
	s := &Service{db: o.DB, dir: o.Dir, baseURL: o.BaseURL, hashToken: o.HashToken, log: o.Logger, now: o.Now,
		maxBundle: MaxBundleBytes, maxChunk: ChunkBytes, maxStaged: maxStagedBytes, maxExtract: maxExtractBytes,
		assembling: map[assembly]bool{}, reserved: map[int64]int64{}, hasMaps: map[int64]mapsKnown{}, readAll: os.ReadFile}
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
// the chunk as it was sent, already decompressed; it may be at most maxChunk.
func (s *Service) saveChunk(tokenID int64, checksum string, r io.Reader) (int64, error) {
	dir := s.chunkDir(tokenID)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(dir, chunkTempPrefix+"*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name()) // no-op after the rename

	sum := sha1.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), io.LimitReader(r, s.maxChunk+1))
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if n > s.maxChunk {
		return 0, errBadUpload("A chunk can be at most " + sizeText(s.maxChunk) + " once unpacked.")
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != checksum {
		return 0, errBadUpload(fmt.Sprintf("The chunk named %s has the checksum %s.", checksum, got))
	}
	return n, os.Rename(tmp.Name(), filepath.Join(dir, checksum))
}

// chunkTempPrefix starts the name of a chunk while it is being written.
const chunkTempPrefix = "upload-"

// stagedBytes returns how many bytes of chunks a token has waiting (the
// ones being written count in reserved instead).
func (s *Service) stagedBytes(tokenID int64) int64 {
	entries, err := os.ReadDir(s.chunkDir(tokenID))
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), chunkTempPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	return total
}

// reserveChunk takes room for one chunk of a token's upload before it is
// written, so requests sent at the same moment cannot together stage more
// than maxStaged; release gives the room back once the chunk is on disk
// (where stagedBytes counts it) or failed. ok is false when the token has
// too much waiting already.
func (s *Service) reserveChunk(tokenID int64) (release func(), ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stagedBytes(tokenID)+s.reserved[tokenID]+s.maxChunk > s.maxStaged {
		return nil, false
	}
	s.reserved[tokenID] += s.maxChunk
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.reserved[tokenID] -= s.maxChunk; s.reserved[tokenID] <= 0 {
			delete(s.reserved, tokenID)
		}
	}, true
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
		s.removeFiles(files)
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
		s.removeFiles(extra)
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
	if len(m.Files) > maxBundleFiles {
		return nil, errBadUpload(fmt.Sprintf("A bundle can list at most %d files; this one lists %d.", maxBundleFiles, len(m.Files)))
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
			rel, n, err := s.extract(entry, p.ID, s.maxExtract-extracted)
			if err != nil {
				s.removeFiles(files)
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
		err = errBadUpload("The bundle unpacks to more than " + sizeText(s.maxExtract) + ".")
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

// storedName is the path of a file the tool stored, relative to the
// source map directory: the project's id and a random name (extract).
var storedName = regexp.MustCompile(`^([0-9]+)/[0-9a-f]{24}$`)

// errNotOurFile is what reading a row whose path is not a stored name gives.
var errNotOurFile = errors.New("the row names a path that is not a file the tool made")

// pathOf returns where a row's file is on disk. A path that is not a name
// the tool makes, in the row's project's directory, is refused (false): a
// restored backup could name any file ("../monitor.db"), which must never
// be read or removed. Such a row is logged once.
func (s *Service) pathOf(f store.SourceMapFile) (string, bool) {
	if m := storedName.FindStringSubmatch(f.Path); m != nil && m[1] == strconv.FormatInt(f.ProjectID, 10) {
		return filepath.Join(s.dir, filepath.FromSlash(f.Path)), true
	}
	if _, logged := s.badPaths.LoadOrStore(f.ID, true); !logged {
		s.log.Warn("sourcemaps: a source map row names a path that is not a file the tool made; it is never read or removed",
			"project", f.ProjectID, "row", f.ID)
	}
	return "", false
}

// removeFiles deletes the files of rows that will not be stored.
func (s *Service) removeFiles(files []store.SourceMapFile) {
	for _, f := range files {
		if name, ok := s.pathOf(f); ok {
			os.Remove(name)
		}
	}
}

// readFile returns a stored file's content.
func (s *Service) readFile(f store.SourceMapFile) ([]byte, error) {
	name, ok := s.pathOf(f)
	if !ok {
		return nil, errNotOurFile
	}
	return s.readAll(name)
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
		name, ok := s.pathOf(f)
		if !ok {
			continue // the row goes; the file it names stays
		}
		if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return 0, err
		}
	}
	return s.db.DeleteSourceMapFiles(ctx, projectID, ids)
}

// CleanUp removes what uploads left behind: chunks and half-assembled
// bundles older than an hour, the chunk directories of tokens that no
// longer exist, and files no row points to any more (the files of deleted
// projects). The sweep job runs it every hour.
//
// A token's chunks are removed when it is revoked (RemoveTokenChunks), but
// a directory can outlive its token: one revoked before that existed, or
// while the server was down. Ids of tokens are never used again on
// Postgres or since migration 0008 on SQLite, but 0008 started counting at
// the highest id left then, so an id deleted before it can come back once:
// removing the directories of missing tokens keeps such a token from
// finding another token's chunks.
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
	// The directories are listed before the tokens are read: a token made
	// in between has no directory in the list yet.
	tokens, _ := os.ReadDir(filepath.Join(s.dir, "chunks"))
	exist, err := s.db.APITokenIDs(ctx)
	if err != nil {
		s.log.Warn("sourcemaps: clean up", "err", err)
		exist = nil // keep every token's directory; only old chunks go
	}
	for _, t := range tokens {
		dir := filepath.Join(s.dir, "chunks", t.Name())
		if id, err := strconv.ParseInt(t.Name(), 10, 64); err == nil && t.IsDir() && exist != nil && !exist[id] {
			if err := os.RemoveAll(dir); err != nil {
				s.log.Warn("sourcemaps: remove the chunks of a token that no longer exists", "token", id, "err", err)
			}
			continue
		}
		removeOld(dir)
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

// RemoveTokenChunks deletes the chunks a token had waiting, when it is
// revoked: nobody can put them together any more.
func (s *Service) RemoveTokenChunks(tokenID int64) {
	if err := os.RemoveAll(s.chunkDir(tokenID)); err != nil {
		s.log.Warn("sourcemaps: remove a revoked token's chunks", "token", tokenID, "err", err)
	}
}
