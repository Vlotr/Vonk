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
- Committed fixtures are sanitized, not byte-identical to what the SDK sent (controller ruling 24): `capture/sanitize.mjs` replaces the local app directory with `/var/www/webshop` and `contexts.os` with neutral Linux values, recompresses gzip bodies and fixes `Content-Length`/`bodySize`, and refuses to write while the user or host name remains. The SDK's structure, fields and encoding stay as sent. The browser fixture needed no change.

Parser (Step 2):

- `ParseEnvelope` streams: event items up to the limit (1 MiB) are read, larger ones are discarded unread and counted in `Dropped`; other item types are discarded as they stream by and counted in `Skipped`.
- An event item that is not one JSON object makes the whole envelope bad (400), so the SDK learns about it; other item types are never parsed.
- Blank lines between items are tolerated, although the spec does not allow them. Header lines are capped at 64 KiB.
- Event ids are normalized to 32 lowercase hex characters (dashes removed); anything else counts as missing, and then the envelope header's id is used.
- `sentry.Event` reads its typed fields leniently from the raw JSON (decoded with `json.Number`, so stored numbers stay exact); a field of an unexpected type is left empty. `level` defaults to `error`, `warn` becomes `warning`, `critical` becomes `fatal`.
- The stored event JSON is the whole scrubbed event, encoded without HTML escaping.

Scrubbing (Step 5):

- The key and URL rules run over the whole event at any depth, not only the places the brief lists, so fields SDKs add later are covered too. Two parts are left out: the SDK's `fingerprint`, which decides grouping and is computed after scrubbing (`"auth-failure"` contains `auth`), and the frames' source lines (`pre_context`, `context_line`, `post_context`), which are code.
- The [key, value] pair rule applies only to the fields SDKs send as pair lists: `request.headers`, `request.query_string`, `request.env`, `request.data` and `tags` (cookies are dropped). Elsewhere a two-item list such as `["card-declined", "checkout"]` is just a list. IP headers and env variables are dropped in both the object and the pair-list shape.
- Keys match ignoring case and `-`, `_`, `.` and spaces (`X-Api-Key` matches `api_key`). "credit card" in the brief's list is taken as two substrings, `credit` and `card` (stricter). Accepted false positives: `author` (auth), `className` (ssn).
- Query and fragment parameters are filtered in every string without whitespace that has a `?` or `#` (URLs and paths; `#access_token=` too); keys are percent-decoded before matching. Source lines in frames have whitespace and are left alone.
- A request body sent as text is filtered by key when it is JSON or a form, and replaced whole when it is other text that contains a sensitive word.
- Dropped IP fields: `user.ip_address`, the headers `X-Forwarded-For`, `X-Real-IP`, `Forwarded`, `CF-Connecting-IP`, `True-Client-IP`, `X-Client-IP`, `X-Cluster-Client-IP`, `Fastly-Client-IP`, `X-Original-Forwarded-For`, the same names plus `REMOTE_ADDR` and `SERVER_ADDR` in `request.env`, and every `{{auto}}` value. Free text (such as an exception message naming a database host) is not searched for IPs: those are server addresses, and a pattern would also hit versions like `Chrome/141.0.0.0`.

Grouping (Step 6):

- The main exception is the last entry of `exception.values` (controller ruling 23; the brief's "innermost" was a plan error): Sentry lists the oldest cause first and the exception that was finally thrown last (sentry-php and the JS SDK both do), and the design titles issues after Laravel's outer `QueryException`, not the `PDOException` it wraps. It drives the fingerprint, title, message and culprit.
- "Top" frames are the last ones in Sentry's frame list, nearest to the throw. Without in-app frames, the top five of all frames are used.
- A fingerprint is 32 hex characters (the first 128 bits of SHA-256 over its parts). An SDK fingerprint is hashed too, so "used as-is" means only those values decide the group; `{{ default }}` (spaces and case ignored) inside it stands for the computed fingerprint.
- A frame counts as its module, or else its file normalized: a URL becomes its path (no host, query or fragment), backslashes become slashes, numeric directories become `<n>`, directories of 16+ hex characters become `<hash>`, and build hashes are removed from file names (`index-D8xk2Lp1.js` → `index.js`: a part after the first of 8+ characters with a digit, or a last part of exactly 8 mixed-case letters). Plus the function, with the line removed from PHP closure names (`{closure:/routes/web.php:42}`).
- Without a stack trace: the type plus the message with UUIDs → `<uuid>`, `0x…` and bare 8+ character hex containing a letter → `<hex>`, and digit runs → `<n>`. A log message without an exception is grouped by that normalized text.
- A log message event gets its first line as title; no exception and no message gives `<unlabeled event>`; an exception without type is titled `Error`. Titles are cut at 200 runes and messages at 1000, ending in `…`.
- The culprit is the top in-app frame, or the top frame when none is in-app: file name (else `abs_path`, else module) without query, plus `:line` when there is one.
- `Source`: the `sentry.php.laravel` SDK or a `composer:sentry/sentry-laravel` package → `laravel`; platform `javascript` → `browser` (Node, Bun and Deno SDKs report `node`); else the platform in lowercase; none → `other`. The PHP fixture (plain `sentry/sentry`) is therefore `php`.

Envelope endpoint (Step 3):

- One route, `/api/{projectID}/envelope/{$}`, answers POST and OPTIONS itself (other methods get a JSON 405). `/api/1/envelope` without the slash gets Go's 307 to the slash form, which keeps the method and body.
- The key is taken from `X-Sentry-Auth` (`sentry_key=`, with or without the `Sentry ` prefix), then `?sentry_key=`, then the envelope header `dsn`. A header or query key is checked before the body is read. A missing or unknown key, the key of another project, or a project id that is not a number gives 401.
- A failing project lookup (a database error, not bad traffic) gives 503 with `Retry-After: 60` and an error log line; it is the only 5xx the endpoint sends.
- Accepted encodings: none, `identity`, `gzip`/`x-gzip`, and `deflate` (zlib as HTTP specifies, or raw deflate when there is no zlib header). Anything else (`br`, `zstd`, chained encodings) and corrupt or truncated data gives 400.
- The 20 MiB limit applies to the body as sent and after decompression; going over gives 413 and stops reading, so a zip bomb costs at most 20 MiB of inflating.
- A 400 repeats its reason in `X-Sentry-Error`, as Sentry does; every error body is `{"error":"..."}`.
- CORS: `Access-Control-Allow-Origin: *` on every answer, errors included, plus `Access-Control-Expose-Headers: x-sentry-error, x-sentry-rate-limits, retry-after` so browser SDKs can read the back-off headers; the preflight allows `POST, OPTIONS` and the headers `content-type, content-encoding, x-sentry-auth, authorization, sentry-trace, baggage`, cached for a day.
- An accepted envelope gets `200 {"id":"<event id>"}`, one without events (sessions, client reports) `200 {}`; events without an id get a random one.
- Events are scrubbed in the handler, before they are queued; grouping, encoding and the timestamp clamp happen in the writer.
- Envelopes without events are authenticated but neither rate-limited nor queued.
- Accepted SDK requests are logged at debug, like `/health`; refused ones stay at info, so a busy site does not fill the log.
- The project's tracking choices (Laravel, browser) are not enforced at ingest yet.

Rate limit and queue (Step 4):

- The rate limit is a fixed window per clock minute and per project, counting events (not envelopes), with the project's `rate_limit_per_minute` (default 1000). An envelope that does not fit is refused whole; `Retry-After` and `X-Sentry-Rate-Limits: <s>::project` give the seconds until the next minute (rounded up, at least 1). Counts live in memory and start over after a restart.
- The queue is a buffered channel of 10 000 events. When it is full the handler answers 429 with `Retry-After: 5` (no `X-Sentry-Rate-Limits`); events of the same envelope that were already queued stay. The "queue is full" warning is logged at most once per 10 s.
- The rate limit is checked before queueing, so an event refused for a full queue still counts toward the minute.

Writer (Step 7):

- One goroutine flushes when 500 events are waiting or on a 1 s ticker; on `Close` (or when its context ends) it first takes everything left in the queue.
- `store.SaveErrorEvents` writes one batch in one transaction: per event a duplicate check on (project, event id), then the issue (read, then insert or update; safe because there is one writer), then the event row. Issues touched in a batch are kept in memory for the rest of it.
- Out-of-order events keep the earliest `first_seen` and the latest `last_seen`; title, message, culprit and level follow the newest event.
- Events of a project deleted while they were queued are skipped and counted (warning), instead of failing the batch on the foreign key.
- A batch that fails is retried after 0.2 s, 1 s and 5 s (SQLite already waits up to 5 s for a lock in each attempt), then logged as an error and dropped.
- Timestamps: missing, or more than 1 h after the receive time, become the receive time; older timestamps are kept (SDKs send late).
- `route` stays empty until Task 7 fills it.
- `OnIssueCreated` runs once per new issue after the commit, on the writer goroutine, with the issue as it is after the batch.
- `Close` without `Run` writes the queue itself; `Run` and `Close` may each be called twice.
- No new migration: the Task 1 unique keys on `issues (project_id, fingerprint)` and `error_events (project_id, event_id)` carry grouping and deduplication.
- `store` gained `Issue`, `IssueByID`, `ListIssues` (all issues of a project, newest first), `ErrorEventByEventID` and `CountErrorEvents`, used by the tests; Task 3 builds the dashboard queries.

Wiring (Step 8):

- `serve` runs the writer with its own context, not the signal context, so it keeps writing while running requests finish; shutdown order is HTTP server, then `writer.Close`, then the database.
- `issues.Hooks` lives in `internal/issues` with a no-op `NoHooks`, used by `serve` until Task 6.
- Measured on this Mac (`TestIngest_ConcurrentLoad`, 50 goroutines × 200 envelopes over HTTP with 4 readers running): sent in about 0.2 s and all written within 0.8 s; with `-race` about 1 s and 10 s.

## Task 3: Phase 1 dashboard, examples and end-to-end proof

Follow-ups from earlier reviews:

- The two identical JSON 404 handlers in `internal/server` are now one, `notFoundJSON`.
- Every color, font, radius and line height of the dashboard, the Task 1 shell included, is a variable in `web/src/styles/tokens.css`; no CSS module has a literal for them.
- The e2e test checks that both Laravel issues carry the source tag "Laravel" (sentry-laravel detection proven with the real SDK, not only in unit tests).

Design tokens (Step 1):

- Sans: Instrument Sans (`@fontsource-variable/instrument-sans` 5.3.0). Picked by rendering 16 candidate families over crops of the PNGs at the same cap height: its glyphs, x-height to cap-height ratio (0.71) and ink coverage match. Weights were fitted by ink coverage: headlines and numbers 600, nav 500 (600 when active), brand 700, tags 500 (New 600), buttons 600.
- Mono: JetBrains Mono (`@fontsource-variable/jetbrains-mono` 5.3.0); its `->` ligature is the arrow in the design's code, and its width matches at 13.5 px. Code 13.5 px, culprits 13 px, frame paths and mono values 14 px.
- Fitting sizes by text width at letter-spacing 0 gave whole pixels (34, 22, 19, 18, 17, 16, 15, 14, 13, 12), so those are the sizes; only the headline has -0.01em. The search box placeholder in Issues.png is Chrome's default #757575, so the designs were most likely rendered from HTML.
- Variable fonts: one file per script covers weights 400 to 700. All subsets are bundled (about 125 KB in the binary); browsers fetch only Latin (`unicode-range`).
- Tags are pills 18.5 px high (12 px text at normal line height plus 2 px padding); the outlined tags on the issue page spend 1 px of the padding on the border. The error line in the stack trace is semibold, as in the design.
- Hover backgrounds on table rows and nav items are not in the static designs; they use existing tokens.

API (Step 2):

- `GET /ui/api/projects` also returns `openIssues` (beyond the plan's shape), so the sidebar badge needs no second request. `sources` follow the project's tracking choices, Laravel first.
- Hourly buckets are clock hours in UTC: the current, unfinished hour is the last of 24, so an event 3 h ago is in bucket 20. Events up to an hour ahead (a client clock that is off) count in the current hour.
- `isNew` and `new24h` mean first seen at most 24 h ago; `new24h` and `total` count the requested status.
- Query parameters are checked before the project lookup, so a bad request gets 400 whether or not the project exists. `limit` is 1 to 100 (default 25), `offset` 0 or more. Missing, non-numeric and unknown ids get 404.
- Wrong methods on the API routes fall through to the method-less `/ui/api/` catch-all and get its JSON 404 (`{"error":"not found"}`), not Go's plain-text 405; the dashboard never sends them. (Corrected in Task 4; the first version of this line said 405.)
- `users` is `COUNT(DISTINCT user_id)` over the issue's events at query time; a counter can replace it if it gets slow.
- The detail's `latestEvent` is the newest by timestamp. Its frames are those of the exception that names and groups the issue (`sentry.MainException`, now exported), the most recent call first, with at most three lines of code before and after the error line (the design shows three; SDKs send five). The detail's `issue` object repeats `hourly`, like the list.
- Browser and system are parsed from the request's User-Agent header (major versions; macOS and Windows without one, because browsers freeze them in the header), else from Sentry's `browser` and `client_os` contexts. Never from `contexts.os`: server SDKs put the server's system there. Country only from `user.geo.country_code`, since IP addresses are not stored.
- `runtime` is `{name, version}`, so the Details card can show "PHP 8.3" as the design does.
- A stored event that cannot be decoded still gives `latestEvent` with id and time, and a warning in the log.
- Store queries for the dashboard live in `internal/store/dashboard.go`; `scanIssue` accepts extra columns and error events share one scan helper.
- `internal/api/event.go` (newest event) and `useragent.go` were added next to the plan's `api.go`, `projects.go` and `issues.go`.

Frontend (Step 3):

- Task 3 leaves out Task 4's controls: status tabs, source and sort selects, search, Resolve, Ignore, copy link, Show more. It keeps the breadcrumbs ("All projects", "All projects / Webshop issues") and the "Showing 6 of 14 open issues" footer text, which need no API and keep the designs' layout.
- `/` is a projects list (name, source tags, open issues) with the headline "2 projects have open issues" (one, none and no-projects variants) and "Updated HH:MM. New data comes in every minute."; Task 4 replaces it with the All projects overview.
- The sidebar shows the project of the URL, else the last one visited, else the first by name. The badge is hidden at 0 open issues.
- Headline variants: "No open issues in Webshop"; "1 open issue …"; under it "It is new in the last 24 hours." / "It was first seen more than 24 hours ago." for one issue, "None of them are new …", "1 of them is new …", "6 of them are new …", and for none "New errors show up here within a minute after your app sends them."
- Times: "just now", "4 min ago", "3 h ago", "2 d ago", then a date; First seen as "Oct 15, 21:06" in local time, with the year when it is not this year.
- Bar levels: 0 without events, else quarters of the row's busiest hour (`ceil(count / max * 4)`), so any event shows.
- Stack trace: framework frames (not in-app) are hidden behind "14 framework lines hidden. Show them." and the "Show framework code" / "Hide framework code" button; when no frame is in-app, all are shown. The first in-app frame with source opens; others open on click. Functions show the method after `::` (full name as tooltip); files lose their leading slash and URLs show their path.
- The message under an issue's name is clamped to two lines and a long release is cut with an ellipsis, both with the full text as a tooltip; the stack trace shows the whole message.
- An issue whose project is not the one in the URL is "Issue not found". The error line carries `aria-current="true"`, for assistive technology and as the e2e test's hook.

Examples (Step 4):

- Laravel 13.37 from `composer create-project`, `sentry/sentry-laravel` 4.29.0. `.env.example` uses file sessions and cache and the sync queue, so the example needs no database. `config/sentry.php` reads `server_name` from `SENTRY_SERVER_NAME`. The skeleton's `AGENTS.md` and `CLAUDE.md` (setup instructions for coding agents, including a `curl | bash` installer) were removed. `composer.lock` is committed.
- `/boom` throws in `CheckoutController::store`, `/boom-other` in `CheckoutController::applyCoupon`.
- The browser example runs on the Vite dev server, so its frames point at readable `src/main.js` until source maps (Task 10). `@sentry/browser` is pinned at 11.8.0, the fixture's version.

End-to-end (Step 5):

- `e2e/` is its own npm package with `@playwright/test` 1.64.1 (the version Task 2 used) and `@types/node`; `make e2e` installs them and Chromium. The global setup builds with `make build` and installs the examples' packages when they are missing.
- The Laravel routes are opened with `page.goto`, so the events carry a real browser's User-Agent. The dashboard page is reloaded until the rows appear, since it polls only once a minute.
- Screenshots are taken at 1360 × 960 with device scale 2 (the PNGs' size), full page, and committed as the phase 1 proof. The setup sets server name `web-1` and release `example@1.0.0`.
- Compared with design-equivalent mock data (scratch, not committed), everything shared with the designs lines up within 1 px. Left out on purpose: Task 4's controls, "Before this error" (Task 9), and Pages, Events, Alerts and Settings in the sidebar. Without the Resolve column, the issue column takes the free width, so the bars sit further right than in Issues.png.

Fix round 1 (review of Task 3):

- The issue page keys the stack trace by issue, not by event, and remembers opened frames by `file:line`: the minute poll often brings a newer event of the same issue, and that used to fold the framework frames and close opened frames again.
- Controller ruling 26: an issue whose main exception has mechanism `onunhandledrejection` (or `….onunhandledrejection`, as `@sentry/browser` 11 sends: `auto.browser.global_handlers.onunhandledrejection`) is titled "Unhandled rejection". The rule is in `sentry.Summarize`, so the stored title and every place that shows it agree; the fingerprint is unchanged and the real type stays in the stack trace. New fixture `browser-rejection.envelope`, captured from the browser example.
- Controller ruling 27: for platform `javascript` the culprit is the frame URL's path without origin, query, fragment and leading slash (`assets/index-D8xk2Lp1.js:8301`, `src/main.js:21`); other platforms keep the file name. This replaces the Task 2 rule for browsers only; grouping inputs are unchanged.

## Task 4: Issue workflow, issue detail and the All projects overview

Follow-ups from the Task 2 and Task 3 reviews:

- `BASE_URL` errors hide everything between `://` (or the start) and the last `@` of the whole value, so a password with an unencoded `#`, `?` or `/` is hidden too; a path or query with an `@` after the host is hidden along with it, which is fine for an error message.
- The scrubber replaces the password of every `scheme://user:password@host` in any string, free text included (`connecting to amqp://guest:…@queue failed`); the user name stays, so a DSN's public key (`https://key@host/1`) is kept. The host part runs to the next `/` or whitespace and the last `@` in it ends the user info. Accepted false positive: a URL without a path whose query holds an `@` and that has a port (`https://host:8080?email=a@b.com`) loses everything between the port and that `@`.
- A request body sent as JSON text that is a list of `[key, value]` pairs gets the pair rule, like the same list sent as JSON.
- Timestamps more than 30 days before the receive time become the receive time, like those more than an hour ahead; `"timestamp": 1` no longer makes an issue first seen in 1970.
- The ingest queue is also full at 256 MiB of queued event JSON (`sentry.Event.Size`, the bytes the event was decoded from, added up when queued and released when the writer takes it). Decoded events take more memory than their JSON; the cap is an approximation that keeps 10 000 large events from taking gigabytes. The "queue is full" warning names both limits.
- `writeJSON`/`writeError` live once, in `internal/httpjson` (`Write`, `Error`), used by `api`, `server` and `ingest`.
- `sanitize.mjs` looks for the user name only as a whole path segment and for the host name (and its first label) only as a whole host token, both in the exact case the system reports them; `localhost` is never a leak. A host called `mac` therefore no longer matches `Mac OS X` in a User-Agent. The check is exported as `findLeaks` and tested by `sanitize.test.mjs` with `node --test`, which `make test` runs.
- The PHP capture recipe sends the fixture's Chrome-on-macOS User-Agent, and CLAUDE.md says the session envelope's headers file is not committed.
- `tokens.css` keeps every color, font, radius and line height, the spacing steps and the sizes several components share (new: card title padding, row padding, table header height, the gap under names, the issue list's bar width, the issue page's side column, the list footer height); one-off nudges for a single element stay in its CSS module, and the header now says so.
- The users count of an issue is one SQL fragment (`usersColumn` in `internal/store/dashboard.go`), still computed at query time: a distinct count over the issue's events for every row a query reads (for the whole filtered list when sorting by users). A counter column can replace it when it shows up as slow.
- Dates with a time use the same style as dates alone: "Oct 15, 21:06", and "Dec 31, 2025, 23:59" in another year.
- The e2e setup listens for `'error'` on every process it starts: a missing `php` or `npx` fails setup with a clear message and teardown stops what already runs (before, Node threw on the unhandled event and left the monitor running). `waitFor` also fails at once when its process stopped.
- The Details card shows the runtime version as major.minor ("PHP 8.4" for 8.4.1), with the full version as a tooltip.
- Long code lines already scrolled inside the code box, not the page; with headless Chromium and macOS overlay scrollbars nothing showed that, so the clipped line looked like overflow. The code box now has a CSS-only shadow at its right edge while code is hidden there (a `local` background covers it once the end is in view); lines still do not wrap.
- A stack frame is remembered by file, line and function plus how many frames before it have the same three, so recursive frames each keep their own open or closed state across polls.

Status and regressions (Step 1):

- A resolved issue reopens when an event arrives that was received at or after `resolved_at` (receive time, not the event's own timestamp, which clients set): an event that was still waiting in the queue when someone clicked Resolve counts but does not undo the click. A resolved issue without `resolved_at` reopens on any event. Reopening sets `regressed_at` to the receive time and calls `OnIssueRegressed` once per regression, after the commit; reopening by hand sets neither.
- Setting the status an issue already has changes nothing, so a double click on Resolve keeps the first `resolved_at`. Opening or ignoring clears `resolved_at`.
- `POST /ui/api/issues/{id}/status` takes only `Content-Type: application/json` (415 otherwise) and a body of at most 1 KiB: until logins and CSRF tokens arrive (Task 5), that keeps another site from making a visitor's browser post a form here, since a JSON request needs a CORS preflight this API never answers. It answers `{id, projectId, status}`. The handler is in `internal/api/status.go`, next to the plan's `issues.go`.
- `issues.Service` takes the clock as an argument; `api.New` passes the handler's clock, so API tests control both.
- Migration 0002 also adds the indexes `issues (project_id, status, last_seen)` for the list and `error_events (project_id, timestamp)` for the overview (and Task 6's retention). No index on `error_events.route` yet: the column stays empty until Task 7, which can add one against real data.

Issue list queries (Step 2):

- `source` accepts `laravel` and `browser` (400 otherwise), the choices of the Source select; issues from other platforms (plain `php`, `node`) appear under All sources only.
- `q` matches part of the title, message or culprit, or of the URL of any of the issue's events (`EXISTS` over the events, using the issue index), ignoring case with `LOWER(...) LIKE ? ESCAPE '\'`; `%`, `_` and `\` in the search are literal. Go lowercases only ASCII letters, the same as SQLite's `LOWER`, so a non-ASCII search still finds its exact spelling; the Postgres twin (Task 11) must keep that. `q` is trimmed and at most 200 characters (400 above).
- `route` keeps issues with at least one event on exactly that route (controller ruling 19: this task owns the filter, Task 8 links to it).
- The status counts (`counts`, the tabs) and `total` follow the filters, so the tabs say how many issues of each status match; the headline numbers do not: `openIssues` (new field) and `new24h` describe the whole project's open issues on every tab. `new24h` used to count the requested status; only the open tab used it.
- Sorts: `last_seen` (default), `first_seen` (newest first), `events`, `users`, all descending, ties by last seen and then id so pages never overlap.

All projects overview:

- `newIssues24h` counts open issues first seen in the last 24 hours, the rule of the "New" tag and the issue list headline; a new issue that was resolved or ignored is not counted. `errors24h` counts every event of the last 24 hours whatever its issue's status (ignored issues keep counting). The bars cover the 24 clock hours that end now, so events from the oldest partial hour count in `errors24h` but not in a bar.
- Projects are ordered by new issues, then errors, then name (the design's order fits: 6 new / 399, 1 new / 42, none / 3, none / 0).
- Newest issues are the open issues first seen in the last 24 hours across projects, most recently seen first, at most five; with the Issues.png data that gives the design's rows (QueryException 4 min ago, TypeError 12 min ago, then Customer portal's ValidationException 2 h ago). None: "No new issues in the last 24 hours." and no "See all issues" link, which otherwise opens the issue list of the first row's project.
- The overview queries take a list of project ids, so Task 5 can pass only the projects a member may see.
- The bars of all rows share one scale, the busiest hour of the whole table: in the design a project with 3 errors stays light next to one with 399.
- "Add page tracking" spans both page-view columns and is text styled like the design's link: neither Settings (Task 5) nor page tracking (Task 8) exists yet. `formatDuration` ("1m 12s", "2m 05s") is ready for the average time column.
- The headline is kept to 16em with a 39px line height, which breaks it after "in the" as the design does; a one-line headline keeps 1.22.

Dashboard (Step 3):

- Status tabs are buttons with `aria-pressed` in a group labelled Status. Tab, source and sort changes add a history entry; the search replaces it. Unknown values in the URL fall back to the defaults; defaults are left out of the URL.
- Search runs 300 ms after typing pauses (Enter runs it at once); the URL keeps the text as typed (spaces too), the API trims it; the box takes at most 200 characters.
- "Show more" opens 25 more issues. The page loads the first 25 × pages issues (in requests of at most 100), so the minute poll refreshes everything shown; filters start at one page again. The page count is not in the URL.
- Resolve (and Reopen on the Resolved and Ignored tabs, which the design does not show) is optimistic: the row leaves and the counts move at once, polls wait until the request is done (`usePolling.mutate`), then the list and the sidebar reload; a failure shows one line ("… could not be resolved. Try again.") and the reload brings the row back. While the rows of the previous tab or filters are still on screen (dimmed, during the next load) their buttons wait.
- The issue page shows Resolve and Ignore for an open issue, Reopen and Ignore for a resolved one, Reopen and Resolve for an ignored one, plus an outlined "Resolved" or "Ignored" tag. The copy-link button copies the page's address (clipboard API; a selected text field where the browser has none, such as plain HTTP from another host) and shows a tick for 2 s.
- A route filter in the URL shows as "Only errors on the page /checkout. Show all pages", which the design does not have; without it the smaller counts would be unexplained.
- Sizes fitted to the designs: row buttons 40 px with 14 px text, Show more and the issue page buttons 44 px with 15 px text and 17 px padding (the designs use 16 and 18); selects 121 and 130 px wide with a thin arrow of our own, because the browser's native arrow is bolder than the design's; status tabs in a box with 8 px corners, the active tab and the selects with 6 px. A shared `components/Button.module.css` replaces the stack trace's own button style.
- Compared with design-equivalent data (scratch, not committed) at 1360 × 2: Issues, Issue detail and All projects line up within 1 px except the All projects rows, 1 px taller each (line boxes of 20 and 19 px where the design has 19.5 and 18.5), and the Last seen column, set to the design's 109 px.
- Task 3's interim projects list (`ProjectsPage`) is gone; `/` is the All projects page.
- Below about 580 px wide the page scrolls sideways (the fixed 220 px sidebar plus the status tabs); the designs are desktop only.

End-to-end (Step 4):

- The e2e setup also creates "Customer portal"; phase 2 points the browser example at its DSN, so All projects shows two projects with new errors from real SDKs, as the design does. `phase2-issues.spec.ts` makes its own errors and does not depend on phase 1's.
