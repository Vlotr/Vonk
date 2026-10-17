package sourcemaps

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

// postChunks sends a chunk upload request with the fixture token.
func (u *uploadEnv) postChunks(t *testing.T, contentType string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	return u.send(t, recorded{Method: "POST", Path: "/api/0/organizations/o/chunk-upload/", body: body,
		Headers: http.Header{"Content-Type": {contentType}, "Authorization": {"Bearer " + fixtureToken}}})
}

// stagedFiles lists what the fixture token has in its chunk directory.
func (u *uploadEnv) stagedFiles(t *testing.T) []string {
	t.Helper()
	tok, err := u.db.APITokenByHash(context.Background(), u.svc.hashToken(fixtureToken))
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(u.svc.chunkDir(tok.ID))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// A chunk that unpacks to more than 8 MiB (a gzip bomb: 9 MiB of zeros are
// 9 KiB of gzip) is refused while it is read, and nothing is kept.
func TestUpload_GzipBombChunk(t *testing.T) {
	env := newUploadEnv(t)
	zeros := make([]byte, ChunkBytes+1<<20)
	contentType, body := chunkUpload(t, map[string][]byte{sha1Hex(zeros): zeros})
	if body.Len() > 64<<10 {
		t.Fatalf("the bomb is %d bytes on the wire; the test wants a small one", body.Len())
	}
	rec := env.postChunks(t, contentType, body.Bytes())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "at most 8 MiB once unpacked") {
		t.Errorf("gzip bomb = %d %s, want 400", rec.Code, rec.Body)
	}
	if files := env.stagedFiles(t); len(files) != 0 {
		t.Errorf("staged after a refused chunk: %v", files)
	}
}

// A bundle that unpacks to more than the limit (1 GiB; lowered here) is
// refused, and the files it unpacked so far are removed.
func TestUpload_ZipBombBundle(t *testing.T) {
	env := newUploadEnv(t)
	env.svc.maxExtract = 1 << 20
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.json")
	io.WriteString(w, `{"files":{"a.js.map":{"type":"source_map","url":"~/a.js.map"},"b.js.map":{"type":"source_map","url":"~/b.js.map"}}}`)
	for _, name := range []string{"a.js.map", "b.js.map"} {
		w, _ = zw.Create(name) // deflated: 768 KiB of zeros are a few KiB
		w.Write(make([]byte, 768<<10))
	}
	zw.Close()

	body := env.upload(t, buf.Bytes(), "shop@1.0.0")
	if body["state"] != "error" || body["detail"] != "The bundle unpacks to more than 1 MiB." {
		t.Errorf("assemble of a zip bomb = %v", body)
	}
	entries, _ := os.ReadDir(filepath.Join(env.svc.dir, fmt.Sprint(env.project.ID)))
	if len(entries) != 0 {
		t.Errorf("files left after the refused bundle: %d", len(entries))
	}
	if files, _ := env.db.SourceMapsUploadedBefore(context.Background(), env.project.ID, testNow.Add(time.Second), 10); len(files) != 0 {
		t.Errorf("stored %d files", len(files))
	}
}

// A manifest may list at most 10 000 files.
func TestUpload_ManifestFileLimit(t *testing.T) {
	env := newUploadEnv(t)
	files := map[string]any{}
	for i := range maxBundleFiles + 1 {
		files[fmt.Sprintf("_/_/%d.js.map", i)] = map[string]any{"type": "source_map", "url": fmt.Sprintf("~/%d.js.map", i)}
	}
	manifest, _ := json.Marshal(map[string]any{"files": files})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("manifest.json")
	w.Write(manifest)
	zw.Close()

	body := env.upload(t, buf.Bytes(), "shop@1.0.0")
	if body["state"] != "error" || !strings.Contains(fmt.Sprint(body["detail"]), "at most 10000 files") {
		t.Errorf("assemble of a bundle listing 10 001 files = %v", body)
	}
}

// Room for chunks is taken before they are written, so requests sent at
// the same moment cannot together go over what a token may stage.
func TestUpload_StagedRoomIsReserved(t *testing.T) {
	env := newUploadEnv(t)
	env.svc.maxChunk, env.svc.maxStaged = 1024, 2048
	first, ok1 := env.svc.reserveChunk(1)
	_, ok2 := env.svc.reserveChunk(1)
	if _, ok3 := env.svc.reserveChunk(1); !ok1 || !ok2 || ok3 {
		t.Fatalf("reservations = %v %v %v, want two and no third", ok1, ok2, ok3)
	}
	if _, ok := env.svc.reserveChunk(2); !ok {
		t.Error("another token's room is taken too")
	}
	first()
	if _, ok := env.svc.reserveChunk(1); !ok {
		t.Error("room given back is not free again")
	}

	// Over HTTP: chunks on disk count, and the next one is refused.
	env.svc.reserved = map[int64]int64{}
	for i := range 3 {
		data := bytes.Repeat([]byte{byte('a' + i)}, 1000)
		contentType, body := chunkUpload(t, map[string][]byte{sha1Hex(data): data})
		rec := env.postChunks(t, contentType, body.Bytes())
		switch {
		case i < 2 && rec.Code != http.StatusOK:
			t.Errorf("chunk %d = %d %s", i, rec.Code, rec.Body)
		case i == 2 && (rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Too many chunks wait")):
			t.Errorf("chunk over the staged limit = %d %s, want 400", rec.Code, rec.Body)
		}
	}
	if n := len(env.svc.reserved); n != 0 {
		t.Errorf("reservations left after the requests: %v", env.svc.reserved)
	}
}

// What the client sent damaged is a 400; a failing disk is the server's
// problem, a 500. Told apart by where the error came from, not its text.
func TestUpload_DamagedBodyOrDisk(t *testing.T) {
	env := newUploadEnv(t)
	sum := sha1Hex([]byte("chunk"))
	head := "--x\r\nContent-Disposition: form-data; name=\"file_gzip\"; filename=\"" + sum + "\"\r\n\r\n"
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(bytes.Repeat([]byte("chunk"), 1000))
	zw.Close()
	broken := append([]byte{}, gz.Bytes()[:10]...) // the gzip header, then garbage
	broken = append(broken, bytes.Repeat([]byte{0xff}, 64)...)

	for name, body := range map[string]string{
		"cut short":          head + string(gz.Bytes()[:gz.Len()/2]),
		"no closing frame":   head + gz.String(),
		"broken gzip stream": head + string(broken) + "\r\n--x--\r\n",
	} {
		rec := env.postChunks(t, "multipart/form-data; boundary=x", []byte(body))
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "damaged or cut short") {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}

	// The chunk directory cannot be made: a file has the name.
	var logs bytes.Buffer
	env.svc.log = slog.New(slog.NewTextHandler(&logs, nil))
	chunks := filepath.Join(env.svc.dir, "chunks")
	if err := os.RemoveAll(chunks); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(env.svc.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chunks, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := []byte("chunk")
	contentType, body := chunkUpload(t, map[string][]byte{sha1Hex(data): data})
	if rec := env.postChunks(t, contentType, body.Bytes()); rec.Code != http.StatusInternalServerError {
		t.Errorf("disk failure = %d %s, want 500", rec.Code, rec.Body)
	}
	if !strings.Contains(logs.String(), "store chunk") {
		t.Errorf("the disk failure is not logged:\n%s", logs.String())
	}
}

// The same bundle uploaded for another release is stored for that release
// too (lookups by release and URL find it); for the same release again it
// is "ok" at once.
func TestUpload_SameBundleForAnotherRelease(t *testing.T) {
	env := newUploadEnv(t)
	ctx := context.Background()
	bundle := bundleZip(t, 100)
	for _, release := range []string{"shop@1.0.0", "shop@1.1.0", "shop@1.1.0"} {
		if body := env.upload(t, bundle, release); body["state"] != "ok" {
			t.Fatalf("%s: assemble = %v", release, body)
		}
	}
	files, err := env.db.SourceMapsUploadedBefore(ctx, env.project.ID, testNow.Add(time.Second), 10)
	if err != nil || len(files) != 2 {
		t.Fatalf("stored %d files, %v; want one per release", len(files), err)
	}
	for _, release := range []string{"shop@1.0.0", "shop@1.1.0"} {
		if _, err := env.db.SourceMapByFile(ctx, env.project.ID, release, store.SourceMapKind, "~/big.js.map"); err != nil {
			t.Errorf("the map of %s: %v", release, err)
		}
	}
}

// Putting a bundle together is one request at a time per token: another
// token assembling the same bundle (its own chunks) does not hold it up.
func TestUpload_AssemblingIsPerToken(t *testing.T) {
	env := newUploadEnv(t)
	other := env.token(t, "sntrys_other", env.project.ID)
	mine, err := env.db.APITokenByHash(context.Background(), env.svc.hashToken(fixtureToken))
	if err != nil {
		t.Fatal(err)
	}

	bundle := bundleZip(t, 100)
	env.svc.assembling[assembly{other.ID, sha1Hex(bundle)}] = true
	if body := env.upload(t, bundle, "shop@1.0.0"); body["state"] != "ok" {
		t.Errorf("assemble while another token assembles the bundle = %v, want ok", body)
	}

	second := bundleZip(t, 200)
	env.svc.assembling[assembly{mine.ID, sha1Hex(second)}] = true
	if body := env.upload(t, second, "shop@1.0.0"); body["state"] != "assembling" {
		t.Errorf("assemble while this token assembles the bundle = %v, want assembling", body)
	}
}

// The hourly clean-up removes the chunk directories of tokens that no
// longer exist, also fresh ones, and keeps the directories of tokens that do.
func TestCleanUp_RemovesChunksOfMissingTokens(t *testing.T) {
	env := newUploadEnv(t)
	tok, err := env.db.APITokenByHash(context.Background(), env.svc.hashToken(fixtureToken))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{tok.ID, 9999} {
		dir := env.svc.chunkDir(id)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, sha1Hex([]byte("x"))), []byte("x"), 0o640); err != nil {
			t.Fatal(err)
		}
	}
	env.svc.CleanUp(context.Background(), time.Now())
	if _, err := os.Stat(env.svc.chunkDir(9999)); !os.IsNotExist(err) {
		t.Errorf("the chunks of a token that does not exist are still there: %v", err)
	}
	if files := env.stagedFiles(t); len(files) != 1 {
		t.Errorf("the existing token's fresh chunk = %v, want it kept", files)
	}
}
