package sourcemaps

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

// A row whose path is not a name the tool made (a crafted backup, restored)
// never leads outside the source map directory: symbolication does not
// read the file it names, retention deletes the row but not that file, and
// the row is logged once.
func TestSourceMapRows_PathsStayInTheirDirectory(t *testing.T) {
	env := newTestEnv(t)
	ctx := context.Background()
	var logs bytes.Buffer
	env.svc.log = slog.New(slog.NewTextHandler(&logs, nil))

	// The live database's neighbour in DATA_DIR, named from the source map directory.
	victim := filepath.Join(filepath.Dir(env.svc.dir), "monitor-victim.db")
	if err := os.WriteFile(victim, []byte(`{"version":3,"sources":[],"names":[],"mappings":""}`), 0o600); err != nil {
		t.Fatal(err)
	}
	const debugID = "33333333-3333-3333-3333-333333333333"
	old := testNow.AddDate(-2, 0, 0)
	for i, p := range []string{"../monitor-victim.db", "1/../../monitor-victim.db", "/etc/hosts"} {
		if _, err := env.db.SaveSourceMapFiles(ctx, []store.SourceMapFile{{ProjectID: env.project.ID, File: "~/assets/x.js.map",
			Kind: store.SourceMapKind, DebugID: debugID, Bundle: "crafted" + string(rune('a'+i)), Path: p, Size: 10, UploadedAt: old}}); err != nil {
			t.Fatal(err)
		}
	}

	raw, _ := json.Marshal(map[string]any{"platform": "javascript",
		"exception":  map[string]any{"values": []any{map[string]any{"type": "TypeError", "stacktrace": map[string]any{"frames": []any{map[string]any{"filename": "https://shop.example.com/assets/x.js", "function": "a", "lineno": 1, "colno": 5}}}}}},
		"debug_meta": map[string]any{"images": []any{map[string]any{"type": "sourcemap", "code_file": "https://shop.example.com/assets/x.js", "debug_id": debugID}}}})
	reads := 0
	env.svc.readAll = func(name string) ([]byte, error) {
		reads++
		return os.ReadFile(name)
	}
	for range 3 {
		ev, err := sentry.DecodeEvent(raw)
		if err != nil {
			t.Fatal(err)
		}
		if env.svc.Symbolicate(ctx, env.project.ID, ev) {
			t.Error("symbolicated with a file outside the source map directory")
		}
	}
	if reads != 0 {
		t.Errorf("read %d files for rows whose paths leave the directory", reads)
	}

	n, err := env.svc.DeleteUploadedBefore(ctx, env.project.ID, testNow, 100)
	if err != nil || n != 3 {
		t.Errorf("retention deleted %d rows, %v; want the 3 rows", n, err)
	}
	if _, err := os.Stat(victim); err != nil {
		t.Errorf("retention removed a file outside the source map directory: %v", err)
	}
	if _, err := os.Stat("/etc/hosts"); err != nil {
		t.Errorf("/etc/hosts: %v", err)
	}
	if got := strings.Count(logs.String(), "not a file the tool made"); got != 3 {
		t.Errorf("want one warning per row, got %d:\n%s", got, logs.String())
	}
}
