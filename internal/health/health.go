// Package health says whether the tool itself works: the database answers,
// the disk under DATA_DIR has room, and how much waits in the ingest queue.
// /health, the status page in Settings and the admin alerts all read the
// same Report.
package health

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"monitor/internal/httpjson"
	"monitor/internal/ingest"
	"monitor/internal/store"
)

// The disk is low below 10 percent free or below 1 GiB free.
const (
	lowDiskPercent = 10
	lowDiskBytes   = 1 << 30
)

// DiskLow reports whether free bytes out of total are too few: under 10
// percent, or under 1 GiB.
func DiskLow(free, total uint64) bool {
	return free < lowDiskBytes || free*100 < total*lowDiskPercent
}

// Report is the state of the tool at one moment.
type Report struct {
	// Status is "ok", or "error" when any check failed.
	Status  string `json:"status"`
	Version string `json:"version"`
	// DB is "ok" or "error".
	DB string `json:"db"`
	// Disk is "ok", "low" (see DiskLow) or "error" (it could not be read).
	Disk           string `json:"disk"`
	DiskFreeBytes  uint64 `json:"diskFreeBytes"`
	DiskTotalBytes uint64 `json:"diskTotalBytes"`
	// QueueDepth is how many events wait for the ingest writer.
	QueueDepth int `json:"queueDepth"`
	// Failing names the checks that failed ("db", "disk"); empty when all is well.
	Failing []string `json:"failing,omitempty"`

	// DBError and DiskError say what went wrong, for the log and admin
	// alerts; /health leaves them out.
	DBError   string `json:"-"`
	DiskError string `json:"-"`
}

// OK reports whether every check passed.
func (r Report) OK() bool {
	return len(r.Failing) == 0
}

// Checker runs the checks.
type Checker struct {
	DB *store.DB
	// DataDir is the directory whose file system is checked for space.
	DataDir string
	Queue   *ingest.Queue
	Version string
	// DiskUsage returns free and total bytes of the file system holding a
	// path; nil means the real one. Tests set it.
	DiskUsage func(path string) (free, total uint64, err error)
	Log       *slog.Logger
}

// Check runs every check, each within 2 seconds.
func (c *Checker) Check(ctx context.Context) Report {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	r := Report{Status: "ok", Version: c.Version, DB: "ok", Disk: "ok"}
	if c.Queue != nil {
		r.QueueDepth = c.Queue.Len()
	}
	if err := c.DB.Ping(ctx); err != nil {
		r.DB, r.DBError = "error", err.Error()
		r.Failing = append(r.Failing, "db")
	}

	usage := c.DiskUsage
	if usage == nil {
		usage = diskUsage
	}
	free, total, err := usage(c.DataDir)
	switch {
	case err != nil:
		r.Disk, r.DiskError = "error", err.Error()
		r.Failing = append(r.Failing, "disk")
	default:
		r.DiskFreeBytes, r.DiskTotalBytes = free, total
		if DiskLow(free, total) {
			r.Disk = "low"
			r.Failing = append(r.Failing, "disk")
		}
	}
	if !r.OK() {
		r.Status = "error"
	}
	return r
}

// Handler answers GET /health: 200 with the Report when every check
// passes, else 503 with the failing checks.
func Handler(c *Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := c.Check(r.Context())
		status := http.StatusOK
		if !report.OK() {
			status = http.StatusServiceUnavailable
			// Docker asks every 30 s; the database is worth a line each
			// time, the disk is the admin alert's job.
			if report.DBError != "" {
				c.Log.Error("health check: database", "err", report.DBError)
			}
		}
		httpjson.Write(w, status, report)
	}
}
