-- 0004_alerts: notification settings per project and channel, each
-- project's Slack webhook, what alerts last sent (cooldowns and digest
-- periods), and settings of the whole tool (the admins' Slack webhook).

-- The project's Slack incoming webhook; '' when it has none. It is a
-- secret: the API only ever shows it masked.
ALTER TABLE projects ADD COLUMN slack_webhook_url TEXT NOT NULL DEFAULT '';

-- One row per project, kind and channel. A kind without rows uses the
-- defaults in internal/store/alerts.go.
CREATE TABLE alerts (
    id         INTEGER PRIMARY KEY,
    project_id BIGINT  NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- new_issue, regression, spike or digest.
    kind       TEXT    NOT NULL,
    -- email or slack.
    channel    TEXT    NOT NULL,
    enabled    BOOLEAN NOT NULL,
    -- spike: fire at this many times the usual hourly rate of errors.
    threshold  INTEGER,
    -- digest: daily, weekly or monthly.
    frequency  TEXT    NOT NULL DEFAULT '',
    UNIQUE (project_id, kind, channel)
);

-- What an alert last did, by name: "spike:<project id>",
-- "digest:<project id>:<frequency>", "admin:<kind>".
CREATE TABLE alert_state (
    name       TEXT   PRIMARY KEY,
    -- NULL for the tool's own (admin) alerts; a deleted project takes its rows along.
    project_id BIGINT REFERENCES projects (id) ON DELETE CASCADE,
    -- The period a digest last covered, or the text of the last admin alert.
    value      TEXT   NOT NULL DEFAULT '',
    sent_at    BIGINT NOT NULL
);

-- Settings of the whole tool, by name, such as admin_slack_webhook_url.
CREATE TABLE settings (
    name       TEXT   PRIMARY KEY,
    value      TEXT   NOT NULL,
    updated_at BIGINT NOT NULL
);
