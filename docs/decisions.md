# Decisions

One line per judgment call: what was decided, and why. Newest task at the bottom.

## Task 1: skeleton

- `project.DSN` lives in a new package `internal/project` (with `TestDSN` next to it), to match the interface name in the plan; the plan listed `TestDSN` under the store tests.
- The dashboard is embedded by `web/embed.go` (Go package `monitor/web`), because `go:embed` cannot reach `../../web/dist` from `internal/web`; `internal/web` imports it as `dashboard`.
- `.gitignore` uses `/web/dist/*` plus `!/web/dist/.keep`; a plain `web/dist/` pattern would make the committed placeholder impossible to track.
- Vite empties `web/dist` on every build, so a small plugin in `web/vite.config.ts` writes `dist/.keep` back and `git status` stays clean.
- `go.mod` carries `ignore ./web/node_modules` so `go vet ./...` and `go test ./...` never descend into npm packages that ship `.go` files.
- `cmd/monitor` is split into `main.go` (dispatch), `serve.go`, `healthcheck.go` and `project.go`, one file per subcommand, plus `main_test.go`; the plan listed only `main.go`.
- Config reports every problem at once (`errors.Join`) so an operator fixes the environment in one pass.
- `SECRET_KEY` is refused when empty, equal to `verander-mij` or `change-me` (case-insensitive), or shorter than 16 characters.
- `BASE_URL` is normalized to `scheme://host[:port]` without a trailing slash; user info, query and fragment are refused as well as paths.
- `DATABASE_URL` must start with `postgres://` or `postgresql://`; `store.Open` refuses it with a clear message until Task 11 adds Postgres.
- `LOG_LEVEL` is parsed with `slog.Level` (debug, info, warn, error); `DEV_BASIC_AUTH` must be `user:password` with both parts non-empty.
- SQLite writer: one connection, WAL, `synchronous=NORMAL`, `foreign_keys=1`, `busy_timeout=5000`, `_txlock=immediate` (writes take the lock at BEGIN, so no upgrade deadlocks).
- SQLite readers: a separate pool of `max(4, NumCPU)` connections with `query_only=1`, so a write through the read pool fails loudly.
- Each migration runs in its own write transaction together with its `schema_migrations` row (`version`, `name`, `applied_at`), checked inside the transaction, so `serve` and a CLI command starting at once cannot apply a file twice.
- Schema uses `INTEGER PRIMARY KEY` ids (SQLite row id; the Postgres twin will use identity columns), `BOOLEAN ... DEFAULT FALSE`, and `INSERT ... RETURNING id` instead of `LastInsertId`, all of which work in both databases.
- `error_events` text columns default to `''`, not NULL; `user_id = ''` means no user, so "distinct non-empty user ids" is a plain filter.
- `NewProject.RetentionDays` 0 means the default of 90 days and `KeepForever` means no limit (stored as NULL, `Project.RetentionDays == nil`); `RateLimitPerMinute` 0 means 1000. The allowed retention choices are left to the Settings API (Task 5).
- Slugs are lowercase ASCII letters and digits joined by `-`; other characters are dropped (`Café` becomes `caf`), an empty result becomes `project`, and taken slugs get `-2`, `-3` and so on.
- Sentry key: 16 random bytes as hex (32 characters). PostHog token: `phc_` plus 32 random bytes in unpadded base64url (43 characters).
- `ListProjects` sorts by `lower(name)`, then id.
- `monitor project create` takes only `--name`; it creates the project with 90 days retention, tracking Laravel and browser errors but not page views (the Settings screen changes these later). Output is `Label: value` lines so scripts such as the e2e setup can parse it.
- Dashboard guard without `DEV_BASIC_AUTH`: only loopback TCP peers (127.0.0.0/8, ::1, IPv4-mapped loopback) get in; forwarding headers never let a request in because anyone can send them. Behind Docker port mapping the peer is the bridge gateway, so the container needs `DEV_BASIC_AUTH`; the 403 message says so. (Corrected in Task 2: a proxy on the same host also connects from loopback; see the Task 2 entry.)
- With `DEV_BASIC_AUTH` set, every client needs the credentials, loopback included; comparison is constant-time on SHA-256 digests.
- The guard is fail-closed: public routes (`/health`, `/api/...`, later PostHog) are registered on a public mux and everything else falls through the guard to the dashboard mux, so a new `/ui/api` route cannot skip it by accident.
- The SPA is registered on `/` instead of `GET /` because Go's mux refuses that pattern next to a method-less `/ui/api/`; the SPA handler answers other methods with 405 itself.
- `/api/` has a public JSON 404 catch-all so SDK traffic never receives HTML, before and after Task 2 adds the envelope route.
- SPA serving: `/assets/*` gets `Cache-Control: public, max-age=31536000, immutable` (Vite hashes names), `index.html` gets `no-cache`, missing paths under `/assets/` get 404 (Task 1 also 404'd any path with an extension; Task 2 dropped that), dotfiles such as `.keep` are never served, and a binary built without the web build answers pages with 503 and how to build.
- Request logging: `slog` text handler on stderr, one line per request with method, path, status, bytes and duration; no query string (SDKs can put keys there) and no client address; `/health` logs at debug because Docker calls it every 30 s.
- A panic-recovery middleware (500 plus a log line) was added beyond the brief, so a handler bug never drops the connection without a trace.
- `/health` pings the database with a 2 s timeout and answers `{"status":"ok","version":...,"db":"ok"}` or 503 with `"status":"error"`.
- HTTP server timeouts: read header 10 s, read and write 60 s, idle 120 s; on SIGINT or SIGTERM it waits up to 15 s for running requests.
- `monitor healthcheck` reads only `ADDR` (not the full configuration) and calls 127.0.0.1 for wildcard hosts, with a 3 s timeout.
- Docker build stages run on `$BUILDPLATFORM` and cross-compile with `GOOS`/`GOARCH`, so a multi-arch `buildx` build needs no emulation; base tags `node:24-alpine` and `golang:1.27-alpine` float within their minor version.
- The image copies in an empty `/data` owned by uid 65532 (distroless `nonroot`) so a fresh named volume is writable; `ADDR` and `DATA_DIR` defaults are set as `ENV`.
- Web dependencies (latest at the time): React 19.3, react-router 8.4, Vite 8.3, @vitejs/plugin-react 6.1, TypeScript 7.0, Vitest 5.0, @types/node 24 (matches Node 24). No other dependencies.
- The app shell uses a system font and approximate colors in CSS modules and `styles/global.css`; sampling exact tokens into `tokens.css` and bundling the font is Task 3's job.
- The page title is set at runtime from `brand.ts` (`document.title`), so `index.html` holds no copy of the name.
- The Vitest test renders `App` with `react-dom/server` in the node environment, so no jsdom or Testing Library dependency is needed yet.
- `make e2e` prints a placeholder until Task 3 adds Playwright; `make dev` builds `bin/monitor-dev` and runs it next to Vite with dev-only settings (`DEV_DATA_DIR=./data`, a fixed dev `SECRET_KEY`, `BASE_URL=http://localhost:8000`), all overridable.
- Image size (linux/arm64, `docker images`): 22.6 MB on disk, 5.6 MB compressed content; the binary alone is 11.5 MB.

## Task 2: Sentry ingest

Follow-ups from the Task 1 review:

- The loopback guard also refuses a loopback peer that sends `Forwarded`, `X-Forwarded-For` or `X-Real-IP`: such a request was relayed by a proxy on the same host (or a `--network host` container). The headers only ever make the guard stricter. A same-host proxy that strips all three still gets in; `DEV_BASIC_AUTH` is the real protection there, and Task 5 replaces the guard with logins.
- `BASE_URL` errors never echo user info: every message shows the value with anything before `@` in the host part replaced by `xxxxx`, a URL with user info is refused right after parsing, and a `url.Parse` failure is not wrapped (its `*url.Error` repeats the raw value).
- The SPA answers 404 only for missing files under `/assets/` and for hidden paths (`/.keep`); every other unknown path, dotted or not (`/releases/v1.2.3`, `/favicon.ico`), gets `index.html`.
- `monitor project create` refuses extra arguments (exit code 2) and suggests quoting the name.
- `DB.Ping` (and so `/health`) checks the write connection and the read pool.
- `serve` restores default signal handling after the first SIGINT/SIGTERM, so a second Ctrl-C ends the process during the 15 s shutdown.

Fixtures (Step 1):

- Fixtures captured on 2026-10-16 with `sentry/sentry` 4.34.0 (PHP 8.4.1, `php -S`, one POST with curl) and `@sentry/browser` 11.8.0 (Playwright 1.64.1, Chromium headless shell 156, bundled with esbuild 0.28.2). The recipes are in `internal/sentry/testdata/capture/`; CLAUDE.md says how to rerun them.
- The PHP fixture is a web request with `send_default_pii => true`, a cookie, `Authorization`, `X-Forwarded-For`/`X-Real-IP` and a form body with a password, so the scrub tests run on real data; `prefixes` and `in_app_exclude` are set the way sentry-laravel sets them. It is requested via `localhost` so `request.url` carries a host name, as a real site does, not an IP.
- The browser page is served by a real server on `localhost:5173`: Chrome's local network access rules block a Playwright-routed page from reaching the recorder on 127.0.0.1. Its URLs carry `card`, `iban` and `cvv` because the SDK already filters `token`- and `key`-like parameters itself. The session envelope it sent is kept as `browser-session.envelope`.
- What the fixtures show: sentry-php puts `event_id` only in the envelope header, sends no `level`, sends header values as lists, gzips, and writes items without `length`; the browser SDK authenticates with `?sentry_key=`, sends `text/plain` without compression, sends breadcrumbs as a bare list and `sdk.settings.infer_ip: "auto"` instead of an IP, and `{{auto}}` in session attributes.
- `tools/capture` answers CORS preflights for any origin and does not record them; it replies `200 {}` (or `-response`) to everything else.

Parser (Step 2):

- `ParseEnvelope` streams: event items up to the limit (1 MiB) are read, larger ones are discarded unread and counted in `Dropped`; other item types are discarded as they stream by and counted in `Skipped`.
- An event item that is not one JSON object makes the whole envelope bad (400), so the SDK learns about it; other item types are never parsed.
- Blank lines between items are tolerated, although the spec does not allow them. Header lines are capped at 64 KiB.
- Event ids are normalized to 32 lowercase hex characters (dashes removed); anything else counts as missing, and then the envelope header's id is used.
- `sentry.Event` reads its typed fields leniently from the raw JSON (decoded with `json.Number`, so stored numbers stay exact); a field of an unexpected type is left empty. `level` defaults to `error`, `warn` becomes `warning`, `critical` becomes `fatal`.
- The stored event JSON is the whole scrubbed event, encoded without HTML escaping.

Scrubbing (Step 5):

- The key and URL rules run over the whole event at any depth, not only the places the brief lists, so fields SDKs add later are covered too.
- Keys match ignoring case and `-`, `_`, `.` and spaces (`X-Api-Key` matches `api_key`). "credit card" in the brief's list is taken as two substrings, `credit` and `card` (stricter). Accepted false positives: `author` (auth), `className` (ssn).
- Query and fragment parameters are filtered in every string without whitespace that has a `?` or `#` (URLs and paths; `#access_token=` too); keys are percent-decoded before matching. Source lines in frames have whitespace and are left alone.
- A request body sent as text is filtered by key when it is JSON or a form, and replaced whole when it is other text that contains a sensitive word.
- Dropped IP fields: `user.ip_address`, the headers `X-Forwarded-For`, `X-Real-IP`, `Forwarded`, `CF-Connecting-IP`, `True-Client-IP`, `X-Client-IP`, `X-Cluster-Client-IP`, `Fastly-Client-IP`, `X-Original-Forwarded-For`, the same names plus `REMOTE_ADDR` and `SERVER_ADDR` in `request.env`, and every `{{auto}}` value. Free text (such as an exception message naming a database host) is not searched for IPs: those are server addresses, and a pattern would also hit versions like `Chrome/141.0.0.0`.

Grouping (Step 6):

- "Innermost exception" is the first entry of `exception.values`: Sentry lists the oldest cause first and the exception that was thrown last (sentry-php and the JS SDK both do). The title, message and culprit come from the same exception.
- "Top" frames are the last ones in Sentry's frame list, nearest to the throw. Without in-app frames, the top five of all frames are used.
- A fingerprint is 32 hex characters (the first 128 bits of SHA-256 over its parts). An SDK fingerprint is hashed too, so "used as-is" means only those values decide the group; `{{ default }}` (spaces and case ignored) inside it stands for the computed fingerprint.
- A frame counts as its module, or else its file normalized: a URL becomes its path (no host, query or fragment), backslashes become slashes, numeric directories become `<n>`, directories of 16+ hex characters become `<hash>`, and build hashes are removed from file names (`index-D8xk2Lp1.js` → `index.js`: a part after the first of 8+ characters with a digit, or a last part of exactly 8 mixed-case letters). Plus the function, with the line removed from PHP closure names (`{closure:/routes/web.php:42}`).
- Without a stack trace: the type plus the message with UUIDs → `<uuid>`, `0x…` and bare 8+ character hex containing a letter → `<hex>`, and digit runs → `<n>`. A log message without an exception is grouped by that normalized text.
- A log message event gets its first line as title; no exception and no message gives `<unlabeled event>`; an exception without type is titled `Error`. Titles are cut at 200 runes and messages at 1000, ending in `…`.
- The culprit is the top in-app frame, or the top frame when none is in-app: file name (else `abs_path`, else module) without query, plus `:line` when there is one.
- `Source`: the `sentry.php.laravel` SDK or a `composer:sentry/sentry-laravel` package → `laravel`; platform `javascript` → `browser` (Node, Bun and Deno SDKs report `node`); else the platform in lowercase; none → `other`. The PHP fixture (plain `sentry/sentry`) is therefore `php`.
