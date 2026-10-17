package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"monitor/internal/config"
	"monitor/internal/store"
	"monitor/internal/store/storetest"
)

// backupPage is the answer of GET /ui/api/settings/backup.
type backupPage struct {
	DatabaseKind   string `json:"databaseKind"`
	DatabaseBytes  int64  `json:"databaseBytes"`
	CanDownload    bool   `json:"canDownload"`
	MaxUploadBytes int64  `json:"maxUploadBytes"`
	BackupsDir     string `json:"backupsDir"`
	Backups        []struct {
		Name      string `json:"name"`
		Kind      string `json:"kind"`
		Version   int    `json:"version"`
		SizeBytes int64  `json:"sizeBytes"`
	} `json:"backups"`
	RestorePending *struct {
		SizeBytes int64 `json:"sizeBytes"`
	} `json:"restorePending"`
	RestoreRejected *struct {
		Name string `json:"name"`
	} `json:"restoreRejected"`
}

// download asks for a backup as the admin's browser does from the page.
func (e *testEnv) download(t *testing.T, site string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "/ui/api/backup", nil)
	req.AddCookie(e.admin.cookie)
	if site != "" {
		req.Header.Set("Sec-Fetch-Site", site)
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec.Result()
}

func skipOnPostgres(t *testing.T) {
	t.Helper()
	if storetest.Postgres() {
		t.Skip("backups are SQLite's; Postgres is covered by TestBackup_PostgresExplainsPgDump")
	}
}

func TestBackup_DownloadIsValidDB(t *testing.T) {
	skipOnPostgres(t)
	env := newTestEnv(t)
	env.project(t, "Webshop")

	res := env.download(t, "same-origin")
	body := readAll(t, res)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("download = %d %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/vnd.sqlite3" {
		t.Errorf("Content-Type = %q", ct)
	}
	cd := res.Header.Get("Content-Disposition")
	if !strings.HasPrefix(cd, `attachment; filename="monitor-backup-20261016T123000Z`) || !strings.HasSuffix(cd, `.db"`) {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if res.Header.Get("Content-Length") != strconv.Itoa(len(body)) {
		t.Errorf("Content-Length = %s for %d bytes", res.Header.Get("Content-Length"), len(body))
	}
	path := filepath.Join(t.TempDir(), "download.db")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckBackupFile(context.Background(), path); err != nil {
		t.Errorf("the download is no backup: %v", err)
	}
	copyDB, err := store.Open(context.Background(), config.Config{DataDir: filepath.Dir(path), SQLitePath: path})
	if err != nil {
		t.Fatal(err)
	}
	defer copyDB.Close()
	if list, err := copyDB.ListProjects(context.Background()); err != nil || len(list) != 1 || list[0].Name != "Webshop" {
		t.Errorf("projects in the download = %+v, %v", list, err)
	}
	if leftovers := tempFiles(t, env.dir); len(leftovers) != 0 {
		t.Errorf("the download left %v in DATA_DIR", leftovers)
	}

	// Typed in the address bar: no Sec-Fetch-Site of another site.
	if res := env.download(t, "none"); res.StatusCode != http.StatusOK {
		t.Errorf("typed address = %d", res.StatusCode)
	}
	// A link on another site cannot make an admin's browser copy the database.
	res = env.download(t, "cross-site")
	if body := readAll(t, res); res.StatusCode != http.StatusForbidden || bytes.HasPrefix(body, []byte("SQLite")) {
		t.Errorf("from another site = %d %.40q, want 403", res.StatusCode, body)
	}

	// Without room on the disk for the copy: refused with a reason.
	env.health.DiskUsage = func(string) (uint64, uint64, error) { return 300 << 20, 100 << 30, nil }
	res = env.download(t, "same-origin")
	if body := readAll(t, res); res.StatusCode != http.StatusInsufficientStorage || !strings.Contains(string(body), "disk space") {
		t.Errorf("full disk = %d %s, want 507 naming the disk space", res.StatusCode, body)
	}
	page := getJSON[backupPage](t, env, "/ui/api/settings/backup")
	if page.CanDownload || page.MaxUploadBytes != 0 {
		t.Errorf("full disk: page says canDownload %v, maxUploadBytes %d", page.CanDownload, page.MaxUploadBytes)
	}
}

func TestBackup_PostgresExplainsPgDump(t *testing.T) {
	if !storetest.Postgres() {
		t.Skip("needs Postgres: make test-postgres")
	}
	env := newTestEnv(t)
	res := env.download(t, "same-origin")
	if body := readAll(t, res); res.StatusCode != http.StatusConflict || !strings.Contains(string(body), "pg_dump") {
		t.Errorf("download on Postgres = %d %s, want 409 naming pg_dump", res.StatusCode, body)
	}
	rec := env.send(t, env.admin, "POST", "/ui/api/settings/backup/restore", "application/octet-stream", "SQLite format 3\x00")
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "pg_restore") {
		t.Errorf("upload on Postgres = %d %s, want 409 naming pg_restore", rec.Code, rec.Body)
	}
	page := getJSON[backupPage](t, env, "/ui/api/settings/backup")
	if page.DatabaseKind != "postgres" || page.CanDownload || len(page.Backups) != 0 || page.RestorePending != nil {
		t.Errorf("page on Postgres = %+v", page)
	}
}

func TestRestoreUpload_StagesChecksAndCancels(t *testing.T) {
	skipOnPostgres(t)
	env := newTestEnv(t)
	env.project(t, "Webshop")
	backup := readAll(t, env.download(t, "same-origin"))
	// An automatic backup from before an update, as the start makes them.
	if err := os.MkdirAll(filepath.Join(env.dir, "backups"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(env.dir, "backups", "pre-migrate-0007-20261015T080000Z.db"), backup, 0o600); err != nil {
		t.Fatal(err)
	}

	page := getJSON[backupPage](t, env, "/ui/api/settings/backup")
	if page.DatabaseKind != "sqlite" || !page.CanDownload || page.RestorePending != nil || page.BackupsDir != filepath.Join(env.dir, "backups") {
		t.Errorf("page = %+v", page)
	}
	if len(page.Backups) != 1 || page.Backups[0].Kind != "pre-migrate" || page.Backups[0].Version != 7 || page.Backups[0].SizeBytes != int64(len(backup)) {
		t.Errorf("backups = %+v", page.Backups)
	}

	upload := func(contentType string, body []byte) *httpResult {
		rec := env.send(t, env.admin, "POST", "/ui/api/settings/backup/restore", contentType, string(body))
		return &httpResult{code: rec.Code, body: rec.Body.String()}
	}
	res := upload("application/octet-stream", backup)
	if res.code != http.StatusOK {
		t.Fatalf("upload = %d %s", res.code, res.body)
	}
	var staged backupPage
	if err := json.Unmarshal([]byte(res.body), &staged); err != nil || staged.RestorePending == nil || staged.RestorePending.SizeBytes != int64(len(backup)) {
		t.Errorf("upload answer = %s (%v), want the page with the pending restore", res.body, err)
	}
	if _, err := os.Stat(filepath.Join(env.dir, store.RestorePendingFile)); err != nil {
		t.Errorf("nothing staged: %v", err)
	}

	// Refused uploads say why and leave the pending one alone.
	damaged := append([]byte(nil), backup[:len(backup)/2]...)
	for _, tc := range []struct {
		name, contentType string
		body              []byte
		code              int
		says              string
	}{
		{"damaged", "application/octet-stream", damaged, http.StatusBadRequest, "damaged"},
		{"not a database", "application/octet-stream", []byte("hello"), http.StatusBadRequest, "not a SQLite database"},
		{"as JSON", "application/json", []byte(`{}`), http.StatusUnsupportedMediaType, "application/octet-stream"},
		{"as a form", "multipart/form-data; boundary=x", backup, http.StatusUnsupportedMediaType, "application/octet-stream"},
	} {
		if res := upload(tc.contentType, tc.body); res.code != tc.code || !strings.Contains(res.body, tc.says) {
			t.Errorf("%s: %d %s, want %d saying %q", tc.name, res.code, res.body, tc.code, tc.says)
		}
	}
	env.api.maxRestoreBytes = 1000
	if res := upload("application/octet-stream", backup); res.code != http.StatusRequestEntityTooLarge || !strings.Contains(res.body, "too large") {
		t.Errorf("over the limit: %d %s, want 413", res.code, res.body)
	}
	if p, _ := store.PendingRestoreIn(env.dir); p == nil || p.SizeBytes != int64(len(backup)) {
		t.Errorf("the pending restore after refused uploads = %+v, want the first upload", p)
	}
	if leftovers := tempFiles(t, env.dir); len(leftovers) != 0 {
		t.Errorf("refused uploads left %v", leftovers)
	}

	// Cancelled: nothing pending.
	rec := env.as(t, env.admin, "DELETE", "/ui/api/settings/backup/restore", "")
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), `"restorePending":{`) {
		t.Errorf("cancel = %d %s", rec.Code, rec.Body)
	}
	if p, _ := store.PendingRestoreIn(env.dir); p != nil {
		t.Errorf("still pending after cancelling: %+v", p)
	}
}

type httpResult struct {
	code int
	body string
}

// tempFiles lists the files under way that the store names .tmp-* in dir.
func tempFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			names = append(names, e.Name())
		}
	}
	return names
}

func readAll(t *testing.T, res *http.Response) []byte {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}
