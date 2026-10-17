package sourcemaps

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

// A project's parsed map stays in the cache after the project is deleted.
// Another project's upload must never be answered with it, even when its
// rows get the same ids: the frames would map to the wrong files, and the
// deleted project's code would land in the other project's events.
func TestSymbolicate_CacheNeverServesADeletedProjectsMap(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	gone, err := env.db.CreateProject(ctx, store.NewProject{Name: "Gone"})
	if err != nil {
		t.Fatal(err)
	}
	env.storeBuild(t, gone.ID, buildA, "", true)
	if !env.svc.Symbolicate(ctx, gone.ID, browserEvent(t, buildA, "", true)) {
		t.Fatal("the first project's error was not symbolicated")
	}
	if err := env.db.DeleteProject(ctx, gone.ID); err != nil {
		t.Fatal(err)
	}

	env.storeBuild(t, env.project.ID, buildB, "", true)
	ev := browserEvent(t, buildB, "", true)
	if !env.svc.Symbolicate(ctx, env.project.ID, ev) {
		t.Fatal("the second project's error was not symbolicated")
	}
	wantCheckoutFrames(t, ev)
}

// Chunks belong to the token that sent them, also after it is revoked: a
// token made afterwards never finds them.
func TestUpload_NewTokenNeverGetsARevokedTokensChunks(t *testing.T) {
	env := newUploadEnv(t)
	ctx := context.Background()
	cli := loadRecorded(t, "sentry-cli")
	if rec := env.send(t, cli[2]); rec.Code != 200 { // the chunk, with the fixture token
		t.Fatalf("chunk upload = %d %s", rec.Code, rec.Body)
	}
	old, err := env.db.APITokenByHash(ctx, env.svc.hashToken(fixtureToken))
	if err != nil {
		t.Fatal(err)
	}
	if err := env.db.DeleteAPIToken(ctx, old.ID); err != nil {
		t.Fatal(err)
	}

	next := env.token(t, "sntrys_next", env.project.ID)
	if next.ID == old.ID {
		t.Errorf("the new token has the revoked token's id %d", old.ID)
	}
	rec := env.send(t, cli[3], func(r *http.Request) { r.Header.Set("Authorization", "Bearer sntrys_next") })
	if body := decodeBody(t, rec); body["state"] != "not_found" {
		t.Errorf("assemble with the new token = %v, want not_found: the chunk was the revoked token's", body)
	}
}

// RemoveTokenChunks (called when a token is revoked) deletes what the
// token had waiting.
func TestRemoveTokenChunks(t *testing.T) {
	env := newUploadEnv(t)
	if rec := env.send(t, loadRecorded(t, "sentry-cli")[2]); rec.Code != 200 {
		t.Fatalf("chunk upload = %d %s", rec.Code, rec.Body)
	}
	tok, _ := env.db.APITokenByHash(context.Background(), env.svc.hashToken(fixtureToken))
	if entries, _ := os.ReadDir(env.svc.chunkDir(tok.ID)); len(entries) != 1 {
		t.Fatalf("chunks before = %v", entries)
	}
	env.svc.RemoveTokenChunks(tok.ID)
	if _, err := os.Stat(env.svc.chunkDir(tok.ID)); !os.IsNotExist(err) {
		t.Errorf("the token's chunk directory is still there: %v", err)
	}
}

// denseMap is a map without sourcesContent whose mappings take far more
// memory parsed than on disk, like a large bundle's map without sources.
func denseMap(segments int) []byte {
	var mappings strings.Builder
	for i := range segments {
		if i > 0 {
			mappings.WriteString(",")
		}
		mappings.WriteString("CAAA")
	}
	data, _ := json.Marshal(map[string]any{"version": 3, "sources": []string{"src/a.ts"}, "names": []string{}, "mappings": mappings.String()})
	return data
}

// A map too large for the cache is read and parsed once and then known as
// too large, not parsed again for every event on the ingest path; one
// larger than a quarter of the cache is not even read.
func TestSymbolicate_TooLargeMapIsReadOnce(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var logs bytes.Buffer
	env.svc.log = slog.New(slog.NewTextHandler(&logs, nil))

	data := denseMap(2000) // about 10 KB on disk, 48 KB of mappings parsed
	reads := map[string]int{}
	var mu sync.Mutex
	env.svc.readAll = func(name string) ([]byte, error) {
		mu.Lock()
		reads[filepath.Base(name)]++
		mu.Unlock()
		return os.ReadFile(name)
	}
	env.svc.cache = newCache(int64(len(data)) * 4) // the file fits a quarter; parsed, it does not fit at all

	upload := func(name, debugID string, content []byte) {
		dir := filepath.Join(env.svc.dir, "1")
		os.MkdirAll(dir, 0o750)
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o640); err != nil {
			t.Fatal(err)
		}
		_, err := env.db.SaveSourceMapFiles(ctx, []store.SourceMapFile{{ProjectID: env.project.ID, File: "~/" + name, Kind: store.SourceMapKind,
			DebugID: debugID, Bundle: name, Path: "1/" + name, Size: int64(len(content)), UploadedAt: testNow}})
		if err != nil {
			t.Fatal(err)
		}
	}
	upload("dense.js.map", "11111111-1111-1111-1111-111111111111", data)
	upload("huge.js.map", "22222222-2222-2222-2222-222222222222", denseMap(10000)) // over a quarter of the cache

	event := func(file, debugID string) *sentry.Event {
		raw, _ := json.Marshal(map[string]any{"platform": "javascript",
			"exception":  map[string]any{"values": []any{map[string]any{"type": "TypeError", "stacktrace": map[string]any{"frames": []any{map[string]any{"filename": "https://shop.example.com/" + file, "function": "a", "lineno": 1, "colno": 5}}}}}},
			"debug_meta": map[string]any{"images": []any{map[string]any{"type": "sourcemap", "code_file": "https://shop.example.com/" + file, "debug_id": debugID}}}})
		ev, err := sentry.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		return ev
	}
	for range 5 {
		if env.svc.Symbolicate(ctx, env.project.ID, event("dense.js", "11111111-1111-1111-1111-111111111111")) {
			t.Error("an event was symbolicated with a map too large for the cache")
		}
		env.svc.Symbolicate(ctx, env.project.ID, event("huge.js", "22222222-2222-2222-2222-222222222222"))
	}
	if reads["dense.js.map"] != 1 || reads["huge.js.map"] != 0 {
		t.Errorf("reads = %v, want the dense map once and the huge one never", reads)
	}
	if n := strings.Count(logs.String(), "too large"); n != 2 {
		t.Errorf("logged %d times that a map is too large, want once per map:\n%s", n, logs.String())
	}
}

// The cache knows a file by its path, a random name, not by its row id:
// two rows with one id (as plain SQLite row ids allowed) get their own maps.
func TestParsedMap_KnowsFilesByPath(t *testing.T) {
	env := newTestEnv(t)
	row := func(path string, b build) store.SourceMapFile {
		data, err := os.ReadFile(filepath.Join("testdata", b.dir, b.file+".map"))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(env.svc.dir, filepath.Dir(path)), 0o750)
		if err := os.WriteFile(filepath.Join(env.svc.dir, path), data, 0o640); err != nil {
			t.Fatal(err)
		}
		return store.SourceMapFile{ID: 5, Path: path, Size: int64(len(data))}
	}
	a, err := env.svc.parsedMap(row("1/a", buildA))
	if err != nil {
		t.Fatal(err)
	}
	b, err := env.svc.parsedMap(row("2/b", buildB))
	if err != nil {
		t.Fatal(err)
	}
	if a == b || len(b.sources) != 3 {
		t.Errorf("the second file got the first one's map: %v", b.sources)
	}
}

// After a restore the database can name files that are not on disk (a
// backup holds the rows, not the files): each missing file is logged once
// and then known as unusable, not looked for again for every event.
func TestSymbolicate_MissingFileIsLoggedOnce(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var logs bytes.Buffer
	env.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	env.storeBuild(t, env.project.ID, buildA, "", true)
	if err := os.RemoveAll(filepath.Join(env.svc.dir, "1")); err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if env.svc.Symbolicate(ctx, env.project.ID, browserEvent(t, buildA, "", true)) {
			t.Fatal("symbolicated without the map's file")
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 1 || !strings.Contains(logs.String(), "missing") {
		t.Errorf("want one warning that the file is missing, got:\n%s", logs.String())
	}
}
