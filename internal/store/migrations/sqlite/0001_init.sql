-- 0001_init: projects, grouped errors (issues) and single error events.
--
-- Conventions: times are unix milliseconds (BIGINT), JSON payloads are TEXT,
-- and the SQL stays in the subset SQLite and Postgres share. "id INTEGER
-- PRIMARY KEY" is SQLite's auto-incrementing row id; the Postgres twin of this
-- file uses an identity column instead.

CREATE TABLE projects (
    id                    INTEGER PRIMARY KEY,
    name                  TEXT    NOT NULL,
    slug                  TEXT    NOT NULL UNIQUE,
    sentry_key            TEXT    NOT NULL UNIQUE,
    posthog_token         TEXT    NOT NULL UNIQUE,
    -- NULL keeps data forever.
    retention_days        INTEGER,
    tracks_laravel        BOOLEAN NOT NULL DEFAULT FALSE,
    tracks_browser        BOOLEAN NOT NULL DEFAULT FALSE,
    tracks_pageviews      BOOLEAN NOT NULL DEFAULT FALSE,
    rate_limit_per_minute INTEGER NOT NULL DEFAULT 1000,
    created_at            BIGINT  NOT NULL
);

CREATE TABLE issues (
    id          INTEGER PRIMARY KEY,
    project_id  BIGINT  NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    fingerprint TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    message     TEXT    NOT NULL DEFAULT '',
    culprit     TEXT    NOT NULL DEFAULT '',
    -- laravel, browser or the SDK platform.
    source      TEXT    NOT NULL DEFAULT '',
    level       TEXT    NOT NULL DEFAULT 'error',
    -- open, resolved or ignored.
    status      TEXT    NOT NULL DEFAULT 'open',
    first_seen  BIGINT  NOT NULL,
    last_seen   BIGINT  NOT NULL,
    resolved_at BIGINT,
    times_seen  BIGINT  NOT NULL DEFAULT 0,
    UNIQUE (project_id, fingerprint)
);

CREATE INDEX issues_project_last_seen ON issues (project_id, last_seen);

CREATE TABLE error_events (
    id          INTEGER PRIMARY KEY,
    issue_id    BIGINT NOT NULL REFERENCES issues (id) ON DELETE CASCADE,
    project_id  BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    event_id    TEXT   NOT NULL,
    timestamp   BIGINT NOT NULL,
    received_at BIGINT NOT NULL,
    -- Empty when the SDK sent no user id.
    user_id     TEXT   NOT NULL DEFAULT '',
    url         TEXT   NOT NULL DEFAULT '',
    route       TEXT   NOT NULL DEFAULT '',
    release     TEXT   NOT NULL DEFAULT '',
    environment TEXT   NOT NULL DEFAULT '',
    platform    TEXT   NOT NULL DEFAULT '',
    -- The scrubbed event as JSON.
    data        TEXT   NOT NULL,
    UNIQUE (project_id, event_id)
);

CREATE INDEX error_events_issue_timestamp ON error_events (issue_id, timestamp);
CREATE INDEX error_events_project_user_timestamp ON error_events (project_id, user_id, timestamp);
