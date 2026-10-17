package sourcemaps

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"monitor/internal/config"
	"monitor/internal/sentry"
	"monitor/internal/store"
	"monitor/internal/store/storetest"
)

// The builds in testdata/build-a and testdata/build-b are the recipe's
// app (testdata/capture/src) built by Vite with its source maps, debug ids
// injected by sentry-cli. Build b (VITE_BANNER=1) has more code before the
// checkout, so its file name, minified names and positions all differ. The
// frames below are the stacks Chromium gave for the "Throw a TypeError"
// button.
type build struct {
	dir     string
	file    string // the minified file's name
	debugID string
	frames  []testFrame // oldest call first, as Sentry lists them
}

type testFrame struct {
	function  string
	line, col int
}

var (
	buildA = build{dir: "build-a", file: "index-BM13fAI6.js", debugID: "a157b193-d5a9-5480-b001-80dc5d43e6b4", frames: []testFrame{
		{"HTMLButtonElement.<anonymous>", 3, 888}, {"t", 3, 770}, {"e", 3, 740},
	}}
	buildB = build{dir: "build-b", file: "index-DqFrKOVy.js", debugID: "7d847f0d-c2d1-5022-a176-4cd6231fca64", frames: []testFrame{
		{"HTMLButtonElement.<anonymous>", 3, 1079}, {"r", 3, 957}, {"n", 3, 927},
	}}
)

const pageOrigin = "http://localhost:4173"

var testNow = time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)

type testEnv struct {
	db      *store.DB
	svc     *Service
	project store.Project
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, SQLitePath: filepath.Join(dir, "monitor.db"), DatabaseURL: storetest.DatabaseURL(t)}
	db, err := store.Open(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	p, err := db.CreateProject(context.Background(), store.NewProject{Name: "Webshop", TracksBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	svc := New(Options{DB: db, Dir: filepath.Join(dir, "sourcemaps"), BaseURL: "http://monitor.test",
		HashToken: func(s string) string { return "hash:" + s }, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now: func() time.Time { return testNow }})
	return &testEnv{db: db, svc: svc, project: p}
}

// storeBuild stores a build's minified file and source map for the
// project, as an upload would: with their debug id when withDebugID, and
// for release.
func (e *testEnv) storeBuild(t *testing.T, projectID int64, b build, release string, withDebugID bool) {
	t.Helper()
	debugID := ""
	if withDebugID {
		debugID = b.debugID
	}
	var files []store.SourceMapFile
	for _, f := range []struct{ name, kind, sourcemap string }{
		{b.file, store.MinifiedSourceKind, b.file + ".map"},
		{b.file + ".map", store.SourceMapKind, ""},
	} {
		data, err := os.ReadFile(filepath.Join("testdata", b.dir, f.name))
		if err != nil {
			t.Fatal(err)
		}
		rel := path.Join(strconv.FormatInt(projectID, 10), randomName()) // as extract names them
		if err := os.MkdirAll(filepath.Join(e.svc.dir, path.Dir(rel)), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(e.svc.dir, rel), data, 0o640); err != nil {
			t.Fatal(err)
		}
		files = append(files, store.SourceMapFile{ProjectID: projectID, Release: release, File: "~/assets/" + f.name,
			Kind: f.kind, DebugID: debugID, Sourcemap: f.sourcemap, Bundle: b.dir + release, Path: rel,
			Size: int64(len(data)), UploadedAt: testNow})
	}
	if _, err := e.db.SaveSourceMapFiles(context.Background(), files); err != nil {
		t.Fatal(err)
	}
}

// browserEvent is a TypeError as @sentry/browser sends it from a build:
// its frames, and with debug ids the image that names the build's debug id.
func browserEvent(t *testing.T, b build, release string, withDebugID bool) *sentry.Event {
	t.Helper()
	url := pageOrigin + "/assets/" + b.file
	var frames []map[string]any
	for _, f := range b.frames {
		frames = append(frames, map[string]any{"filename": url, "function": f.function, "in_app": true, "lineno": f.line, "colno": f.col})
	}
	raw := map[string]any{
		"event_id": "0123456789abcdef0123456789abcdef",
		"platform": "javascript",
		"release":  release,
		"exception": map[string]any{"values": []any{map[string]any{
			"type":       "TypeError",
			"value":      "Cannot read properties of undefined (reading 'price')",
			"mechanism":  map[string]any{"type": "auto.browser.browserapierrors.addEventListener", "handled": false},
			"stacktrace": map[string]any{"frames": frames},
		}}},
	}
	if withDebugID {
		raw["debug_meta"] = map[string]any{"images": []any{map[string]any{"type": "sourcemap", "code_file": url, "debug_id": b.debugID}}}
	}
	data, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := sentry.DecodeEvent(data)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

// wantCheckoutFrames checks that the frames are the original TypeScript:
// the click handler in main.ts, onCheckoutClick and showPrice in checkout.ts.
func wantCheckoutFrames(t *testing.T, ev *sentry.Event) {
	t.Helper()
	frames := sentry.MainException(ev).Frames
	type got struct {
		File     string
		Function string
		Line     int
	}
	var have []got
	for _, f := range frames {
		have = append(have, got{f.Filename, f.Function, f.Lineno})
	}
	want := []got{
		{pageOrigin + "/src/main.ts", "HTMLButtonElement.<anonymous>", 7},
		{pageOrigin + "/src/checkout.ts", "onCheckoutClick", 14},
		{pageOrigin + "/src/checkout.ts", "showPrice", 9},
	}
	if !reflect.DeepEqual(have, want) {
		t.Fatalf("frames =\n%+v\nwant\n%+v", have, want)
	}
	top := frames[2]
	if top.ContextLine != "  label.textContent = product!.price.toFixed(2)" {
		t.Errorf("context line = %q", top.ContextLine)
	}
	if len(top.PreContext) != 5 || top.PreContext[4] != "  const label = document.querySelector<HTMLElement>('#price')!" ||
		len(top.PostContext) != 5 || top.PostContext[0] != "}" {
		t.Errorf("pre = %q, post = %q", top.PreContext, top.PostContext)
	}
	if !top.InApp {
		t.Error("app code is not in-app any more")
	}
}

func TestSymbolicate_DebugID(t *testing.T) {
	env := newTestEnv(t)
	env.storeBuild(t, env.project.ID, buildA, "", true)
	ev := browserEvent(t, buildA, "", true)

	if !env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
		t.Fatal("Symbolicate = false, want true")
	}
	wantCheckoutFrames(t, ev)

	// The minified frames are kept as Sentry keeps them, and each mapped
	// frame says which map it came from.
	exc := ev.Raw["exception"].(map[string]any)["values"].([]any)[0].(map[string]any)
	raw := exc["raw_stacktrace"].(map[string]any)["frames"].([]any)
	if fn := raw[2].(map[string]any)["function"]; fn != "e" {
		t.Errorf("raw top frame function = %v, want the minified e", fn)
	}
	data := exc["stacktrace"].(map[string]any)["frames"].([]any)[2].(map[string]any)["data"].(map[string]any)
	if data["sourcemap"] != "~/assets/index-BM13fAI6.js.map" || data["resolved_with"] != "debug-id" {
		t.Errorf("frame data = %v", data)
	}

	// A second call finds the event done and changes nothing.
	before, _ := ev.JSON()
	if env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
		t.Error("second Symbolicate = true, want false")
	}
	if after, _ := ev.JSON(); string(after) != string(before) {
		t.Error("second Symbolicate changed the event")
	}
}

func TestSymbolicate_ReleaseAbsPath(t *testing.T) {
	env := newTestEnv(t)
	env.storeBuild(t, env.project.ID, buildA, "shop@1.0.0", false)

	ev := browserEvent(t, buildA, "shop@1.0.0", false)
	if !env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
		t.Fatal("Symbolicate = false, want true")
	}
	wantCheckoutFrames(t, ev)
	exc := ev.Raw["exception"].(map[string]any)["values"].([]any)[0].(map[string]any)
	data := exc["stacktrace"].(map[string]any)["frames"].([]any)[2].(map[string]any)["data"].(map[string]any)
	if data["resolved_with"] != "release" {
		t.Errorf("resolved with %v, want release", data["resolved_with"])
	}

	// Another release has no maps.
	other := browserEvent(t, buildA, "shop@2.0.0", false)
	if env.svc.Symbolicate(context.Background(), env.project.ID, other) {
		t.Error("an event of another release was symbolicated")
	}
}

func TestSymbolicate_MissingMapLeavesFrames(t *testing.T) {
	env := newTestEnv(t)
	// Maps of another project are no use.
	other, err := env.db.CreateProject(context.Background(), store.NewProject{Name: "Portal"})
	if err != nil {
		t.Fatal(err)
	}
	env.storeBuild(t, other.ID, buildA, "shop@1.0.0", true)

	for name, ev := range map[string]*sentry.Event{
		"debug id": browserEvent(t, buildA, "shop@1.0.0", true),
		"release":  browserEvent(t, buildA, "shop@1.0.0", false),
	} {
		before, _ := ev.JSON()
		if env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
			t.Errorf("%s: Symbolicate = true without a map", name)
		}
		if after, _ := ev.JSON(); string(after) != string(before) {
			t.Errorf("%s: frames changed without a map:\n%s", name, after)
		}
	}

	// A frame of a script without a map (a third party's) stays as it is
	// next to frames that have one.
	env.storeBuild(t, env.project.ID, buildA, "", true)
	ev := browserEvent(t, buildA, "", true)
	frames := ev.Raw["exception"].(map[string]any)["values"].([]any)[0].(map[string]any)["stacktrace"].(map[string]any)
	widget := map[string]any{"filename": "https://widgets.example.com/chat.js", "function": "a", "in_app": true,
		"lineno": json.Number("1"), "colno": json.Number("50")}
	frames["frames"] = append([]any{widget}, frames["frames"].([]any)...)
	ev.Refresh()
	if !env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
		t.Fatal("Symbolicate = false, want true")
	}
	got := sentry.MainException(ev).Frames
	if got[0].Filename != "https://widgets.example.com/chat.js" || got[0].Function != "a" || got[0].Colno != 50 {
		t.Errorf("third-party frame = %+v", got[0])
	}
	if got[3].Function != "showPrice" {
		t.Errorf("top frame = %+v", got[3])
	}
}

// A browser error groups into one issue across builds once source maps
// exist: the fingerprint uses the original files and function names, which
// do not change with the minified names, positions or the file's hash.
func TestFingerprint_StableAcrossBuildsWithMaps(t *testing.T) {
	env := newTestEnv(t)
	evA := browserEvent(t, buildA, "shop@1.0.0", true)
	evB := browserEvent(t, buildB, "shop@1.1.0", true)
	if sentry.Fingerprint(evA) == sentry.Fingerprint(evB) {
		t.Fatal("the minified builds already group together; the test proves nothing")
	}

	env.storeBuild(t, env.project.ID, buildA, "shop@1.0.0", true)
	env.storeBuild(t, env.project.ID, buildB, "shop@1.1.0", true)
	for _, ev := range []*sentry.Event{evA, evB} {
		if !env.svc.Symbolicate(context.Background(), env.project.ID, ev) {
			t.Fatal("Symbolicate = false, want true")
		}
	}
	wantCheckoutFrames(t, evB)
	if a, b := sentry.Fingerprint(evA), sentry.Fingerprint(evB); a != b {
		t.Errorf("fingerprints differ across builds with maps: %s, %s", a, b)
	}
	if a, b := sentry.Summarize(evA).Culprit, sentry.Summarize(evB).Culprit; a != "src/checkout.ts:9" || b != a {
		t.Errorf("culprits = %q, %q, want src/checkout.ts:9", a, b)
	}
}

// A project that never uploaded a map costs no lookups: once that is
// known, Symbolicate does not even ask the database (closed here, so any
// lookup would fail and be logged) until a map is stored.
func TestSymbolicate_ProjectWithoutMapsSkipsLookups(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var logs bytes.Buffer
	env.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	if env.svc.Symbolicate(ctx, env.project.ID, browserEvent(t, buildA, "shop@1.0.0", true)) {
		t.Fatal("symbolicated without a map")
	}
	env.db.Close()
	for _, ev := range []*sentry.Event{browserEvent(t, buildA, "shop@1.0.0", true), browserEvent(t, buildB, "shop@1.1.0", false)} {
		if env.svc.Symbolicate(ctx, env.project.ID, ev) {
			t.Error("symbolicated without a map")
		}
	}
	if logs.Len() != 0 {
		t.Errorf("Symbolicate looked something up for a project without maps:\n%s", logs.String())
	}
}

// The cache's size (SOURCEMAP_CACHE_MB) sets the largest map that is used:
// a quarter of it.
func TestSymbolicate_CacheSizeSetsTheLargestMap(t *testing.T) {
	info, err := os.Stat(filepath.Join("testdata", buildA.dir, buildA.file+".map"))
	if err != nil {
		t.Fatal(err)
	}
	for cacheBytes, want := range map[int64]bool{4 * info.Size(): true, 4*info.Size() - 4: false} {
		env := newTestEnv(t)
		env.svc = New(Options{DB: env.db, Dir: env.svc.dir, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
			CacheBytes: cacheBytes, Now: func() time.Time { return testNow }})
		env.storeBuild(t, env.project.ID, buildA, "", true)
		if got := env.svc.Symbolicate(context.Background(), env.project.ID, browserEvent(t, buildA, "", true)); got != want {
			t.Errorf("cache of %d bytes for a map of %d: symbolicated = %v, want %v", cacheBytes, info.Size(), got, want)
		}
	}
}
