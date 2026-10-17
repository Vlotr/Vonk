-- 0005_pages: page views from PostHog SDKs, the page leaves that close
-- them, their hourly summaries (which the dashboard reads), and each
-- project's route patterns.

-- Route patterns such as "/products/:slug", as a JSON list of strings.
-- They apply to data that comes in after they are set.
ALTER TABLE projects ADD COLUMN route_patterns TEXT NOT NULL DEFAULT '[]';

-- One row per $pageview.
CREATE TABLE pageviews (
    id                     INTEGER PRIMARY KEY,
    project_id             BIGINT  NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- $pageview_id (posthog-js: the uuid of the $pageview event), so an
    -- SDK that sends the same page view again stores it once.
    pageview_id            TEXT    NOT NULL,
    -- $session_id and $window_id (one browser tab); '' when the SDK sent none.
    session_id             TEXT    NOT NULL DEFAULT '',
    window_id              TEXT    NOT NULL DEFAULT '',
    -- distinct_id: anonymous until the app calls identify.
    user_id                TEXT    NOT NULL DEFAULT '',
    route                  TEXT    NOT NULL,
    -- The URL's path, without query and fragment.
    path                   TEXT    NOT NULL DEFAULT '',
    start                  BIGINT  NOT NULL,
    -- Time on the page: until its $pageleave or the next $pageview in the
    -- same tab, whichever came first. NULL while open, and when the time
    -- is not plausible (negative, or more than 6 hours).
    duration_ms            BIGINT,
    -- TRUE until something closes it: "the user left the page".
    left_without_pageleave BOOLEAN NOT NULL DEFAULT TRUE,
    -- Bots are stored but counted apart.
    is_bot                 BOOLEAN NOT NULL DEFAULT FALSE,
    UNIQUE (project_id, pageview_id)
);

CREATE INDEX pageviews_project_start ON pageviews (project_id, start);
-- The page view before and after another one in the same tab.
CREATE INDEX pageviews_project_tab_start ON pageviews (project_id, session_id, window_id, start);

-- One row per $pageleave, kept so a page view that arrives after its
-- leave is still closed by it.
CREATE TABLE pageleaves (
    id          INTEGER PRIMARY KEY,
    project_id  BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- The uuid of the $pageleave event, so a resent one is stored once.
    event_id    TEXT   NOT NULL,
    -- The page view it closes ($pageview_id); '' when the SDK did not
    -- say, and then it closes the latest page view of its tab before it.
    pageview_id TEXT   NOT NULL DEFAULT '',
    session_id  TEXT   NOT NULL DEFAULT '',
    window_id   TEXT   NOT NULL DEFAULT '',
    at          BIGINT NOT NULL,
    UNIQUE (project_id, event_id)
);

CREATE INDEX pageleaves_project_pageview ON pageleaves (project_id, pageview_id, session_id, window_id, at);
CREATE INDEX pageleaves_project_at ON pageleaves (project_id, at);

-- Per project, route and clock hour (UTC): what the dashboard shows of
-- pages. Rebuilt by the aggregation job from pageviews and error_events.
CREATE TABLE hourly_stats (
    id                INTEGER PRIMARY KEY,
    project_id        BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    route             TEXT   NOT NULL,
    -- The hour's start, unix milliseconds.
    hour              BIGINT NOT NULL,
    -- Page views by people (bots are only in bots).
    views             BIGINT NOT NULL DEFAULT 0,
    -- The people's user ids as a mergeable sketch (internal/hll), so
    -- visitors of a day or week can be counted from the hours.
    visitors_hll      BLOB   NOT NULL,
    -- The page views with a duration (closed, plausible) and their sum.
    total_duration_ms BIGINT NOT NULL DEFAULT 0,
    duration_count    BIGINT NOT NULL DEFAULT 0,
    bots              BIGINT NOT NULL DEFAULT 0,
    -- Error events on this route in this hour.
    errors            BIGINT NOT NULL DEFAULT 0,
    UNIQUE (project_id, hour, route)
);

-- Hours whose summary is out of date: data came in for them (late data
-- too) since the aggregation job last rebuilt them.
CREATE TABLE stats_dirty_hours (
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    hour       BIGINT NOT NULL,
    PRIMARY KEY (project_id, hour)
);
