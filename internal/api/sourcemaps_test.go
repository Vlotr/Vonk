package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"monitor/internal/sentry"
	"monitor/internal/sourcemaps"
	"monitor/internal/store"
)

// An event stored before its source map was uploaded shows the original
// code on the issue page once the map is there (ruling 15): the page
// symbolicates it when it reads it. The stored event keeps its minified
// frames.
func TestIssueDetail_SymbolicatesEventsStoredBeforeTheirMap(t *testing.T) {
	env := newTestEnv(t)
	p := env.project(t, "Webshop")
	svc := sourcemaps.New(sourcemaps.Options{DB: env.db, Dir: filepath.Join(env.dir, "sourcemaps"), BaseURL: "https://monitor.example.com",
		HashToken: env.auth.HashToken, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), Now: func() time.Time { return testNow }})
	env.api.maps = svc

	// The fixture build's TypeError (internal/sourcemaps/testdata/build-a), sent before the upload.
	const debugID = "a157b193-d5a9-5480-b001-80dc5d43e6b4"
	file := "https://shop.example.com/assets/index-BM13fAI6.js"
	raw, _ := json.Marshal(map[string]any{
		"event_id": "0123456789abcdef0123456789abcdef", "platform": "javascript",
		"exception": map[string]any{"values": []any{map[string]any{"type": "TypeError", "value": "Cannot read properties of undefined (reading 'price')",
			"stacktrace": map[string]any{"frames": []any{
				map[string]any{"filename": file, "function": "HTMLButtonElement.<anonymous>", "in_app": true, "lineno": 3, "colno": 888},
				map[string]any{"filename": file, "function": "t", "in_app": true, "lineno": 3, "colno": 770},
				map[string]any{"filename": file, "function": "e", "in_app": true, "lineno": 3, "colno": 740},
			}}}}},
		"debug_meta": map[string]any{"images": []any{map[string]any{"type": "sourcemap", "code_file": file, "debug_id": debugID}}},
	})
	ev, err := sentry.DecodeEvent(raw)
	if err != nil {
		t.Fatal(err)
	}
	env.save(t, p.ID, ev)
	list, _ := env.db.ListIssues(context.Background(), p.ID)
	if len(list) != 1 || list[0].Culprit != "assets/index-BM13fAI6.js:3" {
		t.Fatalf("issues = %+v, want one with the minified culprit", list)
	}
	path := fmt.Sprintf("/ui/api/issues/%d", list[0].ID)
	if top := getJSON[detailResponse](t, env, path).LatestEvent.Frames[0]; top.Function != "e" {
		t.Fatalf("before the upload the top frame is %+v, want the minified one", top)
	}

	// The upload: the build's files as an upload stores them.
	var files []store.SourceMapFile
	for name, kind := range map[string]string{"index-BM13fAI6.js": store.MinifiedSourceKind, "index-BM13fAI6.js.map": store.SourceMapKind} {
		data, err := os.ReadFile(filepath.Join("..", "sourcemaps", "testdata", "build-a", name))
		if err != nil {
			t.Fatal(err)
		}
		rel := fmt.Sprintf("%d/%024x", p.ID, len(files)+1) // the shape uploads get: 24 hex digits
		os.MkdirAll(filepath.Join(env.dir, "sourcemaps", fmt.Sprint(p.ID)), 0o750)
		if err := os.WriteFile(filepath.Join(env.dir, "sourcemaps", rel), data, 0o640); err != nil {
			t.Fatal(err)
		}
		files = append(files, store.SourceMapFile{ProjectID: p.ID, File: "~/assets/" + name, Kind: kind, DebugID: debugID,
			Bundle: "b", Path: rel, Size: int64(len(data)), UploadedAt: testNow})
	}
	if _, err := env.db.SaveSourceMapFiles(context.Background(), files); err != nil {
		t.Fatal(err)
	}

	frames := getJSON[detailResponse](t, env, path).LatestEvent.Frames
	top := frames[0]
	if top.File != "https://shop.example.com/src/checkout.ts" || top.Function != "showPrice" || top.Line != 9 ||
		top.ContextLine != "  label.textContent = product!.price.toFixed(2)" || len(top.PreContext) != 3 {
		t.Errorf("top frame = %+v, want showPrice in src/checkout.ts", top)
	}
	if frames[1].Function != "onCheckoutClick" || frames[2].File != "https://shop.example.com/src/main.ts" {
		t.Errorf("frames = %+v", frames)
	}
	stored, _ := env.db.LatestErrorEvent(context.Background(), list[0].ID)
	if storedEv, _ := sentry.DecodeEvent([]byte(stored.Data)); sentry.MainException(storedEv).Frames[2].Function != "e" {
		t.Error("reading the issue changed the stored event")
	}
}
