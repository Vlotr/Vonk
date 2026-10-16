-- 0002_issue_status: when a resolved issue came back, and indexes for the
-- issue list filters and the All projects overview.

-- Set by the ingest writer when an event arrives for a resolved issue and
-- opens it again; NULL for an issue that never came back.
ALTER TABLE issues ADD COLUMN regressed_at BIGINT;

-- The issue list shows one status of one project at a time.
CREATE INDEX issues_project_status_last_seen ON issues (project_id, status, last_seen);

-- Events per project and hour for the overview (and, later, retention).
CREATE INDEX error_events_project_timestamp ON error_events (project_id, timestamp);
