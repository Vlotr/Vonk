-- 0008_sourcemap_ids: ids of source map files and API tokens are never
-- used again (AUTOINCREMENT). The cache of parsed maps and the directories
-- of a token's chunks are named after them; with SQLite's plain row ids a
-- new row could get the id of a deleted one (another project's map, a
-- revoked token's chunks). SQLite cannot add AUTOINCREMENT to a table, so
-- both tables are made again with their rows; api_token_projects is made
-- again first, so dropping api_tokens deletes nothing through its foreign
-- key.

CREATE TABLE source_maps_new (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    project_id  BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    release     TEXT   NOT NULL DEFAULT '',
    dist        TEXT   NOT NULL DEFAULT '',
    file        TEXT   NOT NULL,
    kind        TEXT   NOT NULL,
    debug_id    TEXT   NOT NULL DEFAULT '',
    sourcemap   TEXT   NOT NULL DEFAULT '',
    bundle      TEXT   NOT NULL,
    path        TEXT   NOT NULL,
    size        BIGINT NOT NULL,
    uploaded_at BIGINT NOT NULL
);
INSERT INTO source_maps_new (id, project_id, release, dist, file, kind, debug_id, sourcemap, bundle, path, size, uploaded_at)
    SELECT id, project_id, release, dist, file, kind, debug_id, sourcemap, bundle, path, size, uploaded_at FROM source_maps;
DROP TABLE source_maps;
ALTER TABLE source_maps_new RENAME TO source_maps;

CREATE INDEX source_maps_project_debug_id ON source_maps (project_id, debug_id);
CREATE INDEX source_maps_project_release_file ON source_maps (project_id, release, file);
CREATE INDEX source_maps_project_bundle ON source_maps (project_id, bundle);
CREATE INDEX source_maps_project_uploaded ON source_maps (project_id, uploaded_at);

CREATE TABLE api_tokens_new (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT   NOT NULL,
    token_hash   TEXT   NOT NULL UNIQUE,
    hint         TEXT   NOT NULL DEFAULT '',
    created_by   BIGINT REFERENCES users (id) ON DELETE SET NULL,
    created_at   BIGINT NOT NULL,
    last_used_at BIGINT
);
INSERT INTO api_tokens_new (id, name, token_hash, hint, created_by, created_at, last_used_at)
    SELECT id, name, token_hash, hint, created_by, created_at, last_used_at FROM api_tokens;

CREATE TABLE api_token_projects_new (
    token_id   BIGINT NOT NULL REFERENCES api_tokens_new (id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    PRIMARY KEY (token_id, project_id)
);
INSERT INTO api_token_projects_new (token_id, project_id) SELECT token_id, project_id FROM api_token_projects;

DROP TABLE api_token_projects;
DROP TABLE api_tokens;
-- Renaming updates the foreign key of api_token_projects_new to api_tokens.
ALTER TABLE api_tokens_new RENAME TO api_tokens;
ALTER TABLE api_token_projects_new RENAME TO api_token_projects;

CREATE INDEX api_token_projects_project ON api_token_projects (project_id);
