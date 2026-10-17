package api

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"monitor/internal/auth"
	"monitor/internal/health"
	"monitor/internal/httpjson"
	"monitor/internal/store"
)

// Settings → Backup, for admins (SQLite only; Postgres is backed up with
// pg_dump and restored with pg_restore):
//
//	GET    /ui/api/backup                   the whole database as a file to save (a VACUUM INTO snapshot)
//	HEAD   /ui/api/backup                   would the download work now? (its checks, no copy; the page asks first)
//	GET    /ui/api/settings/backup          what the page shows: the database, the automatic backups, a pending restore
//	POST   /ui/api/settings/backup/restore  the body (application/octet-stream) is a backup to restore at the next start
//	DELETE /ui/api/settings/backup/restore  cancel the pending restore
//
// A restore never swaps the database while the tool runs: the upload is
// checked and waits as <DATA_DIR>/restore-pending.db, and the next start
// moves the current database to backups/ and puts the backup in its place
// (store.ApplyPendingRestore).

// defaultMaxRestoreBytes caps a restore upload; the free disk space caps
// it further (health.Room).
const defaultMaxRestoreBytes = 64 << 30

// transferTimeout is how long a download or an upload of a backup may
// take: the server's read and write timeouts (60 s) are for API calls,
// not for files of gigabytes.
const transferTimeout = 2 * time.Hour

// Answers on Postgres.
const (
	postgresBackupText  = "The database is Postgres, which the tool does not back up: back it up with pg_dump."
	postgresRestoreText = "The database is Postgres, which the tool does not restore: restore a pg_dump backup with pg_restore."
)

type backupFileJSON struct {
	Name string `json:"name"`
	// Kind is "pre-migrate" (made before a database update) or
	// "pre-restore" (the database a restore replaced).
	Kind      string    `json:"kind"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	SizeBytes int64     `json:"sizeBytes"`
}

type pendingRestoreJSON struct {
	SizeBytes int64     `json:"sizeBytes"`
	StagedAt  time.Time `json:"stagedAt"`
}

type rejectedRestoreJSON struct {
	Name       string    `json:"name"`
	RejectedAt time.Time `json:"rejectedAt"`
}

type backupPageJSON struct {
	// DatabaseKind is "sqlite" or "postgres"; on Postgres the page only
	// explains pg_dump and pg_restore, and the rest is empty.
	DatabaseKind  string `json:"databaseKind"`
	DatabaseBytes int64  `json:"databaseBytes"`
	DiskFreeBytes uint64 `json:"diskFreeBytes"`
	// CanDownload is false when the disk has no room for the copy a download makes.
	CanDownload bool `json:"canDownload"`
	// MaxUploadBytes is the largest backup an upload may be now.
	MaxUploadBytes int64 `json:"maxUploadBytes"`
	// BackupsDir is where the automatic backups are, on the server.
	BackupsDir string           `json:"backupsDir"`
	Backups    []backupFileJSON `json:"backups"`
	// RestorePending is the backup that waits for the next start; null when none.
	RestorePending *pendingRestoreJSON `json:"restorePending"`
	// RestoreRejected is the newest restore a start refused; null when none.
	RestoreRejected *rejectedRestoreJSON `json:"restoreRejected"`
}

// room returns how many bytes a backup or an upload may write to DATA_DIR's
// disk now, and its free bytes; ok is false when the disk cannot be read.
func (h *Handler) room() (room, free uint64, ok bool) {
	free, total, err := h.health.DiskSpace()
	if err != nil {
		return 0, 0, false
	}
	return health.Room(free, total), free, true
}

// maxUpload is the largest restore upload the disk and the cap allow now.
func (h *Handler) maxUpload() int64 {
	limit := h.maxRestoreBytes
	if room, _, ok := h.room(); ok && room < uint64(limit) {
		limit = int64(room)
	}
	return limit
}

func (h *Handler) backupPage(w http.ResponseWriter, r *http.Request) {
	out, err := h.backupPageData(r)
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	httpjson.Write(w, http.StatusOK, out)
}

func (h *Handler) backupPageData(r *http.Request) (backupPageJSON, error) {
	out := backupPageJSON{DatabaseKind: "sqlite", Backups: []backupFileJSON{}}
	if h.db.Postgres() {
		out.DatabaseKind = "postgres"
		return out, nil
	}
	dataDir := h.db.DataDir()
	out.DatabaseBytes = h.db.SizeBytes(r.Context())
	out.BackupsDir = filepath.Join(dataDir, "backups")
	room, free, ok := h.room()
	out.DiskFreeBytes = free
	out.CanDownload = !ok || uint64(out.DatabaseBytes) <= room
	out.MaxUploadBytes = h.maxUpload()

	backups, err := h.db.Backups()
	if err != nil {
		return out, err
	}
	for _, b := range backups {
		out.Backups = append(out.Backups, backupFileJSON{Name: b.Name, Kind: b.Kind, Version: b.Version, CreatedAt: b.CreatedAt, SizeBytes: b.SizeBytes})
	}
	pending, err := store.PendingRestoreIn(dataDir)
	if err != nil {
		return out, err
	}
	if pending != nil {
		out.RestorePending = &pendingRestoreJSON{SizeBytes: pending.SizeBytes, StagedAt: pending.StagedAt.UTC()}
	}
	rejected, err := store.RejectedRestores(dataDir)
	if err != nil {
		return out, err
	}
	if len(rejected) > 0 {
		out.RestoreRejected = &rejectedRestoreJSON{Name: rejected[0].Name, RejectedAt: rejected[0].RejectedAt.UTC()}
	}
	return out, nil
}

// backupDownload answers GET /ui/api/backup with a snapshot of the whole
// database as a file to save. Only one snapshot is made at a time, and only
// when the disk has room for it; the copy is gone once it is sent. HEAD
// (which the mux routes here too) runs the same checks and stops before
// the copy: the page asks with it first, so a refusal shows on the page
// instead of arriving as a downloaded file of JSON.
func (h *Handler) backupDownload(w http.ResponseWriter, r *http.Request) {
	if h.db.Postgres() {
		httpjson.Error(w, http.StatusConflict, postgresBackupText)
		return
	}
	// The page links here, so the browser goes here itself (a GET); a
	// link on another site must not make an admin's browser copy the
	// database. "none" is an address typed in.
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		httpjson.Error(w, http.StatusForbidden, "requests from other sites are refused")
		return
	}
	size := h.db.SizeBytes(r.Context())
	if room, _, ok := h.room(); ok && uint64(size) > room {
		httpjson.Error(w, http.StatusInsufficientStorage, fmt.Sprintf(
			"Not enough free disk space for a backup: making one needs about %s next to the database. Free up disk space first.", humanBytes(size)))
		return
	}
	select {
	case h.snapshots <- struct{}{}:
	default:
		httpjson.Error(w, http.StatusConflict, "A backup is being made already. Try again when it is done.")
		return
	}
	if r.Method == http.MethodHead {
		<-h.snapshots
		w.WriteHeader(http.StatusOK)
		return
	}
	f, err := h.db.Snapshot(r.Context())
	<-h.snapshots
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		h.internalError(w, r, err)
		return
	}

	name := "monitor-backup-" + h.now().UTC().Format("20060102T150405Z") + ".db"
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(info.Size(), 10))
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(transferTimeout)) // not every writer has deadlines
	w.WriteHeader(http.StatusOK)
	sent, err := io.Copy(w, f)
	admin := auth.UserFrom(r.Context()).ID
	if err != nil {
		h.log.Info("backup download stopped before the end", "admin", admin, "bytes", sent, "of", info.Size(), "err", err)
		return
	}
	h.log.Info("backup downloaded", "admin", admin, "bytes", sent)
}

// restoreUpload answers POST /ui/api/settings/backup/restore: the body is
// a backup file, which is streamed to DATA_DIR, checked and staged for the
// next start. It answers the page's data, now with the pending restore.
func (h *Handler) restoreUpload(w http.ResponseWriter, r *http.Request) {
	if h.db.Postgres() {
		httpjson.Error(w, http.StatusConflict, postgresRestoreText)
		return
	}
	// The file as it is: a browser sends this type to another site only
	// after a CORS preflight, as it does for JSON (see httpjson.Read).
	if mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); mediaType != "application/octet-stream" {
		httpjson.Error(w, http.StatusUnsupportedMediaType, "Send the backup file itself, with Content-Type: application/octet-stream.")
		return
	}
	limit := h.maxUpload()
	tooLarge := func() {
		httpjson.Error(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("The file is too large: at most %s fits now "+
			"(the limit is %s, and the disk must keep room to work). Nothing was changed.", humanBytes(limit), humanBytes(h.maxRestoreBytes)))
	}
	if r.ContentLength > limit {
		tooLarge()
		return
	}
	select {
	case h.restores <- struct{}{}:
	default:
		httpjson.Error(w, http.StatusConflict, "Another backup is being uploaded. Try again when it is done.")
		return
	}
	defer func() { <-h.restores }()

	rc := http.NewResponseController(w)
	_ = rc.SetReadDeadline(time.Now().Add(transferTimeout)) // not every writer has deadlines
	_ = rc.SetWriteDeadline(time.Now().Add(transferTimeout))
	pending, err := store.StageRestore(r.Context(), h.db.DataDir(), http.MaxBytesReader(w, r.Body, limit))
	var (
		over    *http.MaxBytesError
		refused *store.BackupError
	)
	switch {
	case errors.As(err, &over):
		tooLarge()
		return
	case errors.As(err, &refused):
		httpjson.Error(w, http.StatusBadRequest, refused.Reason+" Nothing was changed.")
		return
	case err != nil && httpjson.ClientGone(r):
		h.log.Debug("restore upload: the client went away", "err", err)
		return
	case err != nil:
		h.internalError(w, r, err)
		return
	}
	h.log.Info("restore staged: the backup becomes the database at the next start", "admin", auth.UserFrom(r.Context()).ID,
		"bytes", pending.SizeBytes, "version", pending.Version)
	h.backupPage(w, r)
}

// cancelRestore answers DELETE /ui/api/settings/backup/restore: the
// pending restore (and any refused one) is removed.
func (h *Handler) cancelRestore(w http.ResponseWriter, r *http.Request) {
	if h.db.Postgres() {
		httpjson.Error(w, http.StatusConflict, postgresRestoreText)
		return
	}
	removed, err := store.CancelRestore(h.db.DataDir())
	if err != nil {
		h.internalError(w, r, err)
		return
	}
	if removed {
		h.log.Info("restore cancelled", "admin", auth.UserFrom(r.Context()).ID)
	}
	h.backupPage(w, r)
}

// humanBytes writes a size as people read it: "512 B", "1.4 MiB", "64 GiB".
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, exp := float64(n)/unit, 0
	for value >= unit && exp < 4 {
		value /= unit
		exp++
	}
	return strconv.FormatFloat(float64(int64(value*10+0.5))/10, 'f', -1, 64) + " " + string("KMGTP"[exp]) + "iB"
}
