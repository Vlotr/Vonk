-- 0003_users: accounts, which projects members see, sessions, invites and
-- password resets.
--
-- Tokens (session cookies, invite and reset links) are never stored as they
-- are: token_hash is an HMAC-SHA256 of the token keyed with SECRET_KEY, so a
-- copy of the database alone cannot be used to sign in.

CREATE TABLE users (
    id            INTEGER PRIMARY KEY,
    -- Trimmed and lowercase, so one address has one account.
    email         TEXT    NOT NULL UNIQUE,
    -- argon2id in PHC form: $argon2id$v=19$m=...,t=...,p=...$salt$hash
    password_hash TEXT    NOT NULL,
    -- admin or member.
    role          TEXT    NOT NULL,
    created_at    BIGINT  NOT NULL
);

-- The projects a member sees. Admins see every project and have no rows here.
CREATE TABLE project_access (
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    PRIMARY KEY (user_id, project_id)
);

CREATE INDEX project_access_project ON project_access (project_id);

CREATE TABLE sessions (
    id         INTEGER PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT   NOT NULL UNIQUE,
    -- Sent back by the dashboard in X-CSRF-Token on every change.
    csrf_token TEXT   NOT NULL,
    created_at BIGINT NOT NULL,
    -- Moves forward while the session is used (sliding expiry).
    expires_at BIGINT NOT NULL
);

CREATE INDEX sessions_user ON sessions (user_id);

CREATE TABLE invites (
    id         INTEGER PRIMARY KEY,
    -- Trimmed and lowercase, like users.email.
    email      TEXT   NOT NULL,
    role       TEXT   NOT NULL,
    token_hash TEXT   NOT NULL UNIQUE,
    created_by BIGINT REFERENCES users (id) ON DELETE SET NULL,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    -- Set when the invite was accepted; an invite works once.
    used_at    BIGINT
);

-- The projects an invited member will see once the invite is accepted.
CREATE TABLE invite_projects (
    invite_id  BIGINT NOT NULL REFERENCES invites (id) ON DELETE CASCADE,
    project_id BIGINT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    PRIMARY KEY (invite_id, project_id)
);

CREATE TABLE password_resets (
    id         INTEGER PRIMARY KEY,
    user_id    BIGINT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT   NOT NULL UNIQUE,
    created_at BIGINT NOT NULL,
    expires_at BIGINT NOT NULL,
    -- Set when the link was used; a reset link works once.
    used_at    BIGINT
);

CREATE INDEX password_resets_user ON password_resets (user_id);
