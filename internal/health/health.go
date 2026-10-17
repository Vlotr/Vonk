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

// Disk thresholds (ruling 34). Low: under 10 percent or 1 GiB free, the
// tool still works but should get room soon (degraded, an admin alert).
// Critical: under 1 percent or 256 MiB free, writes are about to fail (503).
const (
	lowDiskPercent      = 10
	lowDiskBytes        = 1 << 30
	criticalDiskPercent = 1
	criticalDiskBytes   = 256 << 20
)

// DiskLow reports whether free bytes out of total are too few: under 10
// percent, or under 1 GiB.
func DiskLow(free, total uint64) bool {
	return free < lowDiskBytes || free*100 < total*lowDiskPercent
}

// DiskCritical reports whether the disk is all but full: under 1 percent,
// or under 256 MiB free.
func DiskCritical(free, total uint64) bool {
	return free < criticalDiskBytes || free*100 < total*criticalDiskPercent
}

// Room returns how many bytes can still be written to a disk with free
// of total bytes free before it is critical (DiskCritical): what a backup
// or an upload may take.
func Room(free, total uint64) uint64 {
	reserve := max(uint64(criticalDiskBytes), (total*criticalDiskPercent+99)/100)
	if free <= reserve {
		return 0
	}
	return free - reserve
}

// Report is the state of the tool at one moment.
type Report struct {
	// Status is "ok"; "degraded" when a check failed but the tool still
	// works (a low or unreadable disk); "error" when it cannot work (the
	// database fails, or the disk is critical). /health answers 503 only
	// for "error".
	Status  string `json:"status"`
	Version string `json:"version"`
	// DB is "ok" or "error".
	DB string `json:"db"`
	// Disk is "ok", "low" (see DiskLow), "critical" (see DiskCritical) or
	// "error" (it could not be read).
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

// Down reports whether the tool cannot work: /health answers 503.
func (r Report) Down() bool {
	return r.Status == "error"
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

// DiskSpace returns the free and total bytes of the file system under DataDir.
func (c *Checker) DiskSpace() (free, total uint64, err error) {
	if c.DiskUsage != nil {
		return c.DiskUsage(c.DataDir)
	}
	return diskUsage(c.DataDir)
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

	free, total, err := c.DiskSpace()
	switch {
	case err != nil:
		r.Disk, r.DiskError = "error", err.Error()
	case DiskCritical(free, total):
		r.Disk = "critical"
	case DiskLow(free, total):
		r.Disk = "low"
	}
	if err == nil {
		r.DiskFreeBytes, r.DiskTotalBytes = free, total
	}
	if r.Disk != "ok" {
		r.Failing = append(r.Failing, "disk")
	}

	switch {
	case r.DB != "ok" || r.Disk == "critical":
		r.Status = "error"
	case !r.OK():
		r.Status = "degraded"
	}
	return r
}

// Handler answers GET /health with the Report: 200 while the tool works
// (status "ok" or "degraded", with the failing checks named), 503 when it
// cannot (status "error").
func Handler(c *Checker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		report := c.Check(r.Context())
		status := http.StatusOK
		if report.Down() {
			status = http.StatusServiceUnavailable
		}
		// Docker asks every 30 s; the database is worth a line each time,
		// the disk is the admin alert's job.
		if report.DBError != "" {
			c.Log.Error("health check: database", "err", report.DBError)
		}
		httpjson.Write(w, status, report)
	}
}
