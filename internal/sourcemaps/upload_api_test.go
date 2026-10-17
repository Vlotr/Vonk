package sourcemaps

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"monitor/internal/store"
)

// The fixtures in testdata/sentry-cli and testdata/vite-plugin are the
// requests sentry-cli 3.8.0 ("sourcemaps upload") and @sentry/vite-plugin
// 11.8.0 sent while they uploaded build a of the recipe to this API
// (tools/capture -forward), with the token replaced by fixtureToken.
const fixtureToken = "sntrys_fixture"

// recorded is one request of a fixture.
type recorded struct {
	name    string
	Method  string      `json:"method"`
	Path    string      `json:"path"`
	Query   string      `json:"query"`
	Headers http.Header `json:"headers"`
	body    []byte
}

func loadRecorded(t *testing.T, dir string) []recorded {
	t.Helper()
	names, err := filepath.Glob(filepath.Join("testdata", dir, "*.request.json"))
	if err != nil || len(names) == 0 {
		t.Fatalf("no fixtures in testdata/%s: %v", dir, err)
	}
	slices.Sort(names)
	var out []recorded
	for _, name := range names {
		data, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var r recorded
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatal(err)
		}
		r.name = filepath.Base(strings.TrimSuffix(name, ".request.json"))
		r.body, _ = os.ReadFile(strings.TrimSuffix(name, ".request.json") + ".body")
		out = append(out, r)
	}
	return out
}

// uploadEnv is the test environment with the upload API on a mux, as the
// server mounts it, and an API token (fixtureToken) for the project.
type uploadEnv struct {
	*testEnv
	mux   *http.ServeMux
	admin store.User
}

func newUploadEnv(t *testing.T) *uploadEnv {
	t.Helper()
	env := newTestEnv(t)
	admin, err := env.db.CreateFirstAdmin(context.Background(), "admin@example.com", "not-a-real-hash", testNow)
	if err != nil {
		t.Fatal(err)
	}
	u := &uploadEnv{testEnv: env, mux: http.NewServeMux(), admin: admin}
	u.token(t, fixtureToken, env.project.ID)
	env.svc.Register(u.mux)
	u.mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { detail(w, http.StatusNotFound, "not found") })
	return u
}

// token stores an API token for projects.
func (u *uploadEnv) token(t *testing.T, token string, projectIDs ...int64) store.APIToken {
	t.Helper()
	tok, err := u.db.CreateAPIToken(context.Background(), store.NewAPIToken{Name: token, TokenHash: u.svc.hashToken(token),
		ProjectIDs: projectIDs, CreatedBy: u.admin.ID, Now: testNow})
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

// send replays a recorded request, with change applied, and returns the answer.
func (u *uploadEnv) send(t *testing.T, r recorded, change ...func(*http.Request)) *httptest.ResponseRecorder {
	t.Helper()
	target := r.Path
	if r.Query != "" {
		target += "?" + r.Query
	}
	req := httptest.NewRequest(r.Method, target, bytes.NewReader(r.body))
	for name, values := range r.Headers {
		for _, v := range values {
			req.Header.Add(name, v)
		}
	}
	for _, c := range change {
		c(req)
	}
	rec := httptest.NewRecorder()
	u.mux.ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer %d is not a JSON object: %s", rec.Code, rec.Body)
	}
	return body
}

// Both clients start by asking how to upload. These are the fields they
// cannot do without (each one taken away made sentry-cli or the vite
// plugin fail; docs/decisions.md), with the limits of this tool.
func TestUpload_ChunkOptions(t *testing.T) {
	env := newUploadEnv(t)
	for _, dir := range []string{"sentry-cli", "vite-plugin"} {
		for _, r := range loadRecorded(t, dir) {
			if r.Method != http.MethodGet || !strings.HasSuffix(r.Path, "/chunk-upload/") {
				continue
			}
			rec := env.send(t, r)
			if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("%s/%s = %d %s", dir, r.name, rec.Code, rec.Body)
			}
			body := decodeBody(t, rec)
			want := map[string]any{
				"url":              "http://monitor.test/api/0/organizations/monitor/chunk-upload/",
				"chunkSize":        float64(8 << 20),
				"chunksPerRequest": float64(64),
				"maxRequestSize":   float64(32 << 20),
				"maxFileSize":      float64(100 << 20),
				"hashAlgorithm":    "sha1",
			}
			for k, v := range want {
				if body[k] != v {
					t.Errorf("%s: %s = %v, want %v", dir, k, body[k], v)
				}
			}
			if c, _ := body["concurrency"].(float64); c < 1 {
				t.Errorf("%s: concurrency = %v", dir, body["concurrency"])
			}
			if !slices.Contains(body["compression"].([]any), any("gzip")) || !slices.Contains(body["accept"].([]any), any("artifact_bundles")) {
				t.Errorf("%s: compression %v, accept %v", dir, body["compression"], body["accept"])
			}
		}
	}

	// The vite plugin first looks the organization up; any slug is one.
	r := loadRecorded(t, "vite-plugin")[0]
	if rec := env.send(t, r); rec.Code != http.StatusOK || decodeBody(t, rec)["slug"] != "monitor" {
		t.Errorf("organization = %d %s", rec.Code, rec.Body)
	}
}

// The recorded uploads, replayed: the client is told which chunk is
// missing, sends it, and the second assemble call stores the bundle's
// minified file and source map, which then symbolicate the build's errors.
// The vite plugin also creates and finalizes a release.
func TestUpload_AssembleStoresFiles(t *testing.T) {
	env := newUploadEnv(t)
	ctx := context.Background()

	cli := loadRecorded(t, "sentry-cli")
	var states []string
	for _, r := range cli {
		rec := env.send(t, r)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", r.name, rec.Code, rec.Body)
		}
		if strings.HasSuffix(r.Path, "/assemble/") {
			body := decodeBody(t, rec)
			states = append(states, body["state"].(string))
			if body["state"] == "not_found" {
				var req assembleRequest
				json.Unmarshal(r.body, &req)
				if fmt.Sprint(body["missingChunks"]) != fmt.Sprint(req.Chunks) {
					t.Errorf("missing chunks = %v, want %v", body["missingChunks"], req.Chunks)
				}
			}
		}
	}
	if fmt.Sprint(states) != "[not_found ok]" {
		t.Errorf("assemble states = %v, want [not_found ok]", states)
	}
	m, err := env.db.SourceMapByDebugID(ctx, env.project.ID, buildA.debugID, store.SourceMapKind)
	if err != nil || m.File != "~/assets/index-BM13fAI6.js.map" || m.Release != "shop@1.0.0" {
		t.Fatalf("stored map = %+v, %v", m, err)
	}
	min, err := env.db.SourceMapByDebugID(ctx, env.project.ID, buildA.debugID, store.MinifiedSourceKind)
	if err != nil || min.Sourcemap != "index-BM13fAI6.js.map" || min.File != "~/assets/index-BM13fAI6.js" {
		t.Errorf("stored minified file = %+v, %v", min, err)
	}
	if data, err := env.svc.readFile(m); err != nil || !bytes.Contains(data, []byte(`"sourcesContent"`)) {
		t.Errorf("map on disk: %d bytes, %v", len(data), err)
	}
	if entries, _ := os.ReadDir(env.svc.chunkDir(1)); len(entries) != 0 {
		t.Errorf("chunks left after the bundle was stored: %v", entries)
	}
	if !env.svc.Symbolicate(ctx, env.project.ID, browserEvent(t, buildA, "", true)) {
		t.Error("the uploaded files do not symbolicate the build's error")
	}

	// The same upload again finds the bundle stored: "ok" at once, also
	// with the project named twice.
	twice := cli[1]
	twice.body = bytes.Replace(cli[1].body, []byte(`["webshop"]`), []byte(`["webshop","webshop"]`), 1)
	if body := decodeBody(t, env.send(t, twice)); body["state"] != "ok" {
		t.Errorf("assemble of a stored bundle = %v", body)
	}

	vite := loadRecorded(t, "vite-plugin")
	states = nil
	for _, r := range vite {
		rec := env.send(t, r)
		want := http.StatusOK
		if r.Method == http.MethodPost && strings.HasSuffix(r.Path, "/releases/") {
			want = http.StatusCreated
		}
		if rec.Code != want {
			t.Fatalf("%s = %d %s, want %d", r.name, rec.Code, rec.Body, want)
		}
		if strings.HasSuffix(r.Path, "/assemble/") {
			states = append(states, decodeBody(t, rec)["state"].(string))
		}
	}
	if fmt.Sprint(states) != "[not_found ok]" {
		t.Errorf("vite plugin assemble states = %v", states)
	}
	stored, err := env.db.SourceMapsUploadedBefore(ctx, env.project.ID, testNow.Add(time.Second), 10)
	if err != nil || len(stored) != 4 {
		t.Fatalf("stored files = %d, %v; want 2 per upload", len(stored), err)
	}
	for _, f := range stored[2:] {
		if f.Release != "shop@1.1.0" || f.DebugID == "" || !strings.HasPrefix(f.File, "~/") {
			t.Errorf("vite plugin file = %+v", f)
		}
	}
}

func TestUpload_TokenScopes(t *testing.T) {
	env := newUploadEnv(t)
	ctx := context.Background()
	cli := loadRecorded(t, "sentry-cli")
	options, assemble, chunk := cli[0], cli[1], cli[2]
	bearer := func(token string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+token) }
	}

	// No token, or one that does not exist: 401 with a reason the tools show.
	for name, change := range map[string]func(*http.Request){
		"no token":      func(r *http.Request) { r.Header.Del("Authorization") },
		"unknown token": bearer("sntrys_unknown"),
		"not bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Basic "+fixtureToken) },
	} {
		for _, r := range []recorded{options, assemble, chunk} {
			rec := env.send(t, r, change)
			if rec.Code != http.StatusUnauthorized || decodeBody(t, rec)["detail"] == nil {
				t.Errorf("%s: %s = %d %s, want 401 with a detail", name, r.name, rec.Code, rec.Body)
			}
		}
	}

	// A token for another project (or a project that does not exist) gets
	// 403 for this one, and stores nothing.
	portal, err := env.db.CreateProject(ctx, store.NewProject{Name: "Portal"})
	if err != nil {
		t.Fatal(err)
	}
	env.token(t, "sntrys_portal", portal.ID)
	if rec := env.send(t, chunk, bearer("sntrys_portal")); rec.Code != http.StatusOK {
		t.Fatalf("chunk upload with another project's token = %d %s (chunks belong to no project)", rec.Code, rec.Body)
	}
	rec := env.send(t, assemble, bearer("sntrys_portal"))
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `cannot upload to the project \"webshop\"`) {
		t.Errorf("assemble for a project the token lacks = %d %s, want 403", rec.Code, rec.Body)
	}
	unknown := assemble
	unknown.body = bytes.Replace(assemble.body, []byte(`"webshop"`), []byte(`"nope"`), 1)
	if rec := env.send(t, unknown); rec.Code != http.StatusForbidden {
		t.Errorf("assemble for an unknown project = %d %s, want 403", rec.Code, rec.Body)
	}
	if files, _ := env.db.SourceMapsUploadedBefore(ctx, portal.ID, testNow.Add(time.Second), 10); len(files) != 0 {
		t.Errorf("refused uploads stored %d files", len(files))
	}
	// Chunks belong to the token that sent them: the project's own token
	// does not find the other token's chunk.
	if body := decodeBody(t, env.send(t, assemble)); body["state"] != "not_found" {
		t.Errorf("assemble with another token's chunks = %v, want not_found", body)
	}

	// Any organization slug is accepted; the project is matched by slug.
	acme := options
	acme.Path = "/api/0/organizations/acme/chunk-upload/"
	if body := decodeBody(t, env.send(t, acme)); body["url"] != "http://monitor.test/api/0/organizations/acme/chunk-upload/" {
		t.Errorf("chunk options for org acme = %v", body)
	}

	// The vite plugin's release creation names projects too.
	vite := loadRecorded(t, "vite-plugin")
	if rec := env.send(t, vite[1], bearer("sntrys_portal")); rec.Code != http.StatusForbidden {
		t.Errorf("release for a project the token lacks = %d %s, want 403", rec.Code, rec.Body)
	}

	// A deleted token stops working at once.
	tok, _ := env.db.APITokenByHash(ctx, env.svc.hashToken("sntrys_portal"))
	if err := env.db.DeleteAPIToken(ctx, tok.ID); err != nil {
		t.Fatal(err)
	}
	if rec := env.send(t, options, bearer("sntrys_portal")); rec.Code != http.StatusUnauthorized {
		t.Errorf("deleted token = %d, want 401", rec.Code)
	}
	// Using a token is noted.
	if tok, _ := env.db.APITokenByHash(ctx, env.svc.hashToken(fixtureToken)); tok.LastUsedAt == nil {
		t.Error("the token's last use was not noted")
	}
}

// chunkUpload is a multipart request with the chunks gzip'ed, as both clients send them.
func chunkUpload(t *testing.T, chunks map[string][]byte) (string, *bytes.Buffer) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for sum, data := range chunks {
		part, err := mw.CreateFormFile("file_gzip", sum)
		if err != nil {
			t.Fatal(err)
		}
		zw := gzip.NewWriter(part)
		zw.Write(data)
		zw.Close()
	}
	mw.Close()
	return mw.FormDataContentType(), &body
}

func sha1Hex(data []byte) string {
	sum := sha1.Sum(data)
	return hex.EncodeToString(sum[:])
}

// upload sends a bundle in chunks of ChunkBytes and asks for it to be put
// together for the projects (Webshop when none), as the clients do, and
// returns the assemble answer.
func (u *uploadEnv) upload(t *testing.T, bundle []byte, release string, projects ...string) map[string]any {
	t.Helper()
	var sums []string
	for start := 0; start < len(bundle); start += ChunkBytes {
		chunk := bundle[start:min(start+ChunkBytes, len(bundle))]
		sum := sha1Hex(chunk)
		sums = append(sums, sum)
		contentType, body := chunkUpload(t, map[string][]byte{sum: chunk})
		rec := u.send(t, recorded{Method: "POST", Path: "/api/0/organizations/o/chunk-upload/", body: body.Bytes(),
			Headers: http.Header{"Content-Type": {contentType}, "Authorization": {"Bearer " + fixtureToken}}})
		if rec.Code != http.StatusOK {
			t.Fatalf("chunk upload = %d %s", rec.Code, rec.Body)
		}
	}
	if len(projects) == 0 {
		projects = []string{"webshop"}
	}
	req, _ := json.Marshal(map[string]any{"checksum": sha1Hex(bundle), "chunks": sums, "projects": projects, "version": release})
	rec := u.send(t, recorded{Method: "POST", Path: "/api/0/organizations/o/artifactbundle/assemble/", body: req,
		Headers: http.Header{"Content-Type": {"application/json"}, "Authorization": {"Bearer " + fixtureToken}}})
	if rec.Code != http.StatusOK {
		t.Fatalf("assemble = %d %s", rec.Code, rec.Body)
	}
	return decodeBody(t, rec)
}

// bundleZip is an artifact bundle with one source map of size bytes.
func bundleZip(t *testing.T, size int) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.CreateHeader(&zip.FileHeader{Name: "manifest.json", Method: zip.Store})
	io.WriteString(w, `{"files":{"_/_/big.js.map":{"type":"source_map","url":"~/big.js.map","headers":{"debug-id":"0123456789ABCDEF0123456789ABCDEF"}}}}`)
	w, _ = zw.CreateHeader(&zip.FileHeader{Name: "_/_/big.js.map", Method: zip.Store}) // stored, as the vite plugin does
	filler := bytes.Repeat([]byte("abcdefghijklmnopqrstuvwxyz012345"), size/32+1)
	w.Write(filler[:size])
	zw.Close()
	return buf.Bytes()
}

// Bundles may be up to 100 MiB, uploaded in chunks of 8 MiB that are put
// together in order; a larger one is refused with the reason.
func TestUpload_BundleInSeveralChunks(t *testing.T) {
	env := newUploadEnv(t)
	bundle := bundleZip(t, ChunkBytes+ChunkBytes/2) // two chunks
	if body := env.upload(t, bundle, "big@1", "webshop", "webshop"); body["state"] != "ok" {
		t.Fatalf("assemble = %v", body)
	}
	if files, _ := env.db.SourceMapsUploadedBefore(context.Background(), env.project.ID, testNow.Add(time.Second), 10); len(files) != 1 {
		t.Errorf("stored %d files for a bundle of one map, the project named twice", len(files))
	}
	f, err := env.db.SourceMapByDebugID(context.Background(), env.project.ID, "01234567-89ab-cdef-0123-456789abcdef", store.SourceMapKind)
	if err != nil || f.Size != int64(ChunkBytes+ChunkBytes/2) || f.Release != "big@1" {
		t.Errorf("stored = %+v, %v", f, err)
	}

	env.svc.maxBundle = 1 << 20
	body := env.upload(t, bundleZip(t, 2<<20), "big@2")
	if body["state"] != "error" || body["detail"] != "Bundles can be at most 1 MiB." {
		t.Errorf("assemble of a bundle over the limit = %v", body)
	}
}

// Damaged uploads are refused with a 4xx, never stored, never a 5xx.
func TestUpload_RefusesDamagedUploads(t *testing.T) {
	env := newUploadEnv(t)
	auth := http.Header{"Authorization": {"Bearer " + fixtureToken}}
	post := func(path, contentType string, body []byte) *httptest.ResponseRecorder {
		h := auth.Clone()
		h.Set("Content-Type", contentType)
		return env.send(t, recorded{Method: "POST", Path: path, Headers: h, body: body})
	}
	chunks := "/api/0/organizations/o/chunk-upload/"

	contentType, body := chunkUpload(t, map[string][]byte{sha1Hex([]byte("other")): []byte("chunk")})
	if rec := post(chunks, contentType, body.Bytes()); rec.Code != http.StatusBadRequest {
		t.Errorf("chunk with a wrong checksum = %d %s", rec.Code, rec.Body)
	}
	if rec := post(chunks, "multipart/form-data; boundary=x", []byte("--x\r\nContent-Disposition: form-data; name=\"file_gzip\"; filename=\""+
		sha1Hex([]byte("a"))+"\"\r\n\r\nnot gzip\r\n--x--\r\n")); rec.Code != http.StatusBadRequest {
		t.Errorf("chunk that is not gzip = %d %s", rec.Code, rec.Body)
	}
	if rec := post(chunks, "application/json", []byte("{}")); rec.Code != http.StatusBadRequest {
		t.Errorf("chunks not as multipart = %d %s", rec.Code, rec.Body)
	}
	for _, bad := range []string{`{`, `{"checksum":"abc","chunks":["abc"],"projects":["webshop"]}`, `{"checksum":"` + sha1Hex([]byte("x")) + `","chunks":[],"projects":["webshop"]}`} {
		if rec := post("/api/0/organizations/o/artifactbundle/assemble/", "application/json", []byte(bad)); rec.Code != http.StatusBadRequest {
			t.Errorf("assemble %s = %d %s", bad, rec.Code, rec.Body)
		}
	}

	// A bundle that is not a zip, and one without a manifest: state "error".
	for name, bundle := range map[string][]byte{"not a zip": []byte("just text"), "no manifest": func() []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		w, _ := zw.Create("a.js.map")
		w.Write([]byte("{}"))
		zw.Close()
		return buf.Bytes()
	}()} {
		if body := env.upload(t, bundle, ""); body["state"] != "error" || body["detail"] == nil {
			t.Errorf("%s: assemble = %v, want an error with the reason", name, body)
		}
	}
	if files, _ := env.db.SourceMapsUploadedBefore(context.Background(), env.project.ID, testNow.Add(time.Second), 10); len(files) != 0 {
		t.Errorf("damaged uploads stored %d files", len(files))
	}
}

func TestNewToken_SentryOrganizationFormat(t *testing.T) {
	token, hint, err := NewToken("https://monitor.example.com", testNow)
	if err != nil {
		t.Fatal(err)
	}
	payload, secret, ok := strings.Cut(strings.TrimPrefix(token, TokenPrefix), "_")
	if !strings.HasPrefix(token, TokenPrefix) || !ok || strings.Contains(secret, "_") || hint != token[len(token)-4:] {
		t.Fatalf("token = %q, hint %q", token, hint)
	}
	var fields struct {
		Iat       float64 `json:"iat"`
		URL       string  `json:"url"`
		RegionURL string  `json:"region_url"`
		Org       string  `json:"org"`
	}
	data, _ := base64Std(payload)
	if err := json.Unmarshal(data, &fields); err != nil || fields.URL != "https://monitor.example.com" || fields.RegionURL != fields.URL ||
		fields.Org == "" || fields.Iat != float64(testNow.Unix()) {
		t.Errorf("payload = %s, %v", data, err)
	}
	if other, _, _ := NewToken("https://monitor.example.com", testNow); other == token {
		t.Error("two tokens are the same")
	}
}

func base64Std(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}
