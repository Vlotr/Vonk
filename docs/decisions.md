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
- `route` stays empty until Task 7 fills it. (Task 7: filled from the request URL with `routes.Normalize`.)
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
- `isNew` and `new24h` mean first seen at most 24 h ago; `new24h` and `total` count the requested status. (Changed in Task 4: `new24h` counts the whole project's open issues on every tab, and `total` counts the requested status within the filters; see the Task 4 issue list lines.)
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
- Newest issues are the open issues first seen in the last 24 hours across projects, most recently seen first, at most five (controller ruling 28: the times in the design are last-seen times, so the order is by last seen; an older new issue that is still busy can sit above a newer one). None: "No new issues in the last 24 hours." and no "See all issues" link, which otherwise opens the issue list of the first row's project. (Corrected in Task 6: the first version of this line claimed the rule reproduces the design's rows; the design's data does not say when its issues were first seen.)
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

## Task 5: Users, access and project settings

Schema and store:

- Invites keep their projects in an extra table `invite_projects` (foreign keys with `ON DELETE CASCADE`), not a JSON column: SQLite reuses the row id of the newest row after a delete, so a stale project id in an old invite could otherwise grant a later project. `project_access` cascades the same way.
- Addresses are stored trimmed and lowercase; one account per address. An invite for an address that has an account is refused (409); a new invite for an address replaces its unused earlier ones, and a new reset link replaces the user's unused earlier ones, so only the newest link works.
- Making a member admin deletes their `project_access` rows (admins see every project); making an admin a member gives the projects sent along (none when none are sent).
- The last-admin rule (no removing or demoting the last admin), single-use invites and reset links, and "setup only while there is no account" are all checked inside the write transaction, so two requests at once cannot both win.
- Renaming a project keeps its slug. Store queries live in `internal/store/{users,sessions,invites,resets}.go`; projects gained `UpdateProject`, `RotateProjectKeys` (both keys at once), `DeleteProject` and `ProjectIDs`.

Passwords, sessions and CSRF:

- argon2id with OWASP's settings (19 MiB, 2 passes, 1 thread, 16-byte salt, 32-byte key) in PHC form; verification reads the settings from the hash and refuses hashes asking for more than 64 MiB or 16 passes. At most 4 hashes run at once, so a burst of logins cannot run the container out of memory. Dependency `golang.org/x/crypto` v0.58.0 (approved in the plan; it moved `golang.org/x/sys` to v0.49.0).
- Passwords: 10 to 256 characters, counted as characters; no other rules. A login for an unknown address is checked against a dummy hash and answers the same 401 text.
- Session, invite and reset tokens are 32 random bytes (URL-safe base64). The database keeps an HMAC-SHA256 of each, keyed with a key derived from `SECRET_KEY`: a copy of (or a write to) the database is not enough to sign in, and changing `SECRET_KEY` signs everyone out and voids open links.
- Cookie `monitor_session`, or `__Host-monitor_session` when `BASE_URL` is https (the prefix makes browsers insist on Secure, Path=/ and no Domain). Sliding expiry: a session lasts 30 days after its last use; the expiry and cookie are renewed at most once an hour, so minute polls do not write. An expired or unknown cookie is cleared. Expired sessions are deleted whenever someone signs in.
- The CSRF token is random per session and stored as is (it is useless without the cookie); compared in constant time; required on every non-GET request behind `RequireUser`, pages included.
- The sign-in endpoints have no session yet: they take JSON only (`httpjson.Read`, 415 otherwise), and `http.CrossOriginProtection` (Go 1.25+) refuses changes sent from other sites on the whole dashboard (not on ingest, which browser SDKs post to cross-site). BASE_URL is added as a trusted origin for proxies that change the Host header.
- Dashboard answers carry `X-Frame-Options: DENY`, `Content-Security-Policy: frame-ancestors 'none'`, `X-Content-Type-Options: nosniff` and `Referrer-Policy: same-origin`; API answers `Cache-Control: no-store`. A full Content-Security-Policy is left out: it needs a review of inline styles first.
- Wrong current password on the account page: 400 (a 401 would make the dashboard think the session ended) and it counts as a failed sign-in. Changing the password signs out the other sessions and keeps this one.
- After accepting an invite or using a reset link, the person is signed in at once.

Throttle and links:

- Login throttle: a sliding window per key that keeps only the newest 5 or 20 times, swept when a limiter holds 10 000 keys. A successful sign-in (or password reset) clears the address's count, not the client's. The client is the TCP peer, or the last `X-Forwarded-For` hop when the peer is loopback or a private address (a reverse proxy such as Caddy in Docker); from a public peer the header is ignored, since a client could write it to escape the limit. Keys are SHA-256 of a random per-process salt and the address, so no address is kept.
- "Forgot password" has the same limits (every request counts) and answers 429 the same way for any address; a malformed address gets 400. It answers at once and mails in the background, so its timing does not tell which addresses have an account; without mail it answers the same and sends nothing.
- Unknown invite or reset tokens answer 404, expired or used ones 410. Links carry the token after `#token=`, which browsers never send: no server, proxy or Referer sees it. The pages post it to `POST /ui/api/invite/check` and `/ui/api/password/reset/check` (not a GET with the token in the URL).
- Admin-made reset links are for members only (the spec: admins reset themselves on the server); for an admin the route answers 400 naming `monitor admin reset-password`. They are also mailed when mail is on (ruling 10). The CLI works for any account and only prints.

Access:

- `auth.User` carries the set of project ids its holder may see, read once per request by `RequireUser` (every id for an admin), so `CanAccessProject` and `AccessibleProjectIDs` need no database and no error. Their `ctx` is unused, kept for the planned signature. A request without a user (a route mounted outside `RequireUser` by mistake) may see nothing.
- Admin-only API routes answer 404 to members, like a missing object, through `adminOnly` in `internal/api`.
- The status route checks the status first (400 for any id), then looks the issue up and checks access (404), then changes it.
- An admin may remove or demote themselves while another admin exists; the UI does not offer Remove on your own row.
- `auth.Service.NewSession` is exported so tests in `api` and `server` act as a user without hashing passwords (160 ms each under `-race`); `internal/auth` tests lower the argon2 cost in `TestMain` instead.
- Test placement: `TestReset_AdminLinkForMember` and `TestLastAdmin_CannotBeRemovedOrDemoted` are in `internal/api/users_test.go` (the routes live there), `TestCLI_AdminResetPassword` in `cmd/monitor/admin_test.go`; the brief listed them under `internal/auth`. Extra files beyond the brief: `internal/auth/{auth,links}.go`, `internal/testutil/smtp.go`.

Mail:

- `smtp://` needs STARTTLS, except to localhost or a loopback address (a relay on this machine); `smtps://` speaks TLS from the start; TLS 1.2 or newer. Ports default to 587 and 465. Only `?from=` is accepted (other settings are refused, to catch typos) and it must be one address. `config.Load` parses `MAIL_URL` at startup (so a bad value stops the start with the other problems) and keeps the parsed `mail.Config`; errors never repeat the password.
- `mail.Parse` has no `BASE_URL`, so `mail.NewSender(cfg, baseURL)` applies the default sender `monitor@<BASE_URL host>`.
- Messages are plain text in UTF-8, quoted-printable, with a Q-encoded subject and a Message-ID at the sender's domain; a line break in To or Subject is refused. Send gives up after 30 s by default; invite and reset mails use 20 s. A failed invite or reset mail is logged and reported as `mailed: false`; the link is still shown. Mail texts name `BASE_URL`, never the tool name (Go does not read it).
- The fake SMTP server for tests is `internal/testutil/smtp.go` (STARTTLS, implicit TLS or plain, with httptest's certificate for 127.0.0.1), for Task 6 to reuse.

Settings API:

- Retention: 30, 90, 180 or 365 days, or `null` for indefinitely (400 otherwise); a new project keeps 90 days unless told otherwise. Names are trimmed, 1 to 100 characters. Tracking choices not sent are off (the form sends Laravel and browser on by default, as the design shows).
- `PATCH` changes only the fields that are sent; `retentionDays: null` (sent) means indefinitely, a missing field leaves it.
- The project list carries no keys; the single project (Setup and keys) adds `dsn`, `posthogToken` and `posthogHost` (= BASE_URL). Delete is `DELETE` without a body; the typed confirmation is in the UI.
- `GET /ui/api/settings/users` lists every account (with a member's project ids) and the pending invites; links are only ever in the answer that made them.

Dashboard:

- Settings has three pages, Projects, Users and Account; their links sit in the "Settings" line above the headline, where the design has only the word. The sidebar's Settings (at its bottom, as designed) opens Projects for admins and Account for members, who see no other Settings page (and get "Page not found" on their URLs). Sign out is a button on the Account page.
- Edit opens the form card in place of New project (`?edit=<id>`), with Keys (rotate after a confirmation) and Delete project (enabled once the name is typed) under it. Add project is `?new`; with no projects yet the form is open.
- Sizes fitted to Settings.png at 1360 × 2 (within 1 px): labels and the note 14 px, checkbox labels 15 px, native checkboxes 20 px in the primary button's dark (`accent-color`), fields 44 px with a `#b9c2cc` border (new token `--color-input-border`), the open form card 20 px padding with a dark border (`--color-border-strong`), table rows 69.5 px, row buttons 40 px with 14 px text, chips 6 px apart, the dashed "Add page views" chip 32 px high.
- "Add page views" (Settings) and "Add page tracking" (All projects, admins only) turn page views on and open Setup and keys at its page views card; members see "No page tracking yet" there. The All projects empty state links admins to Settings and tells members to ask an admin.
- Setup and keys also shows the `Integration::handles($exceptions)` line for `bootstrap/app.php` and `php artisan sentry:test`, besides the brief's two Laravel lines, and labels the PostHog token and host; code boxes turn JetBrains Mono's ligatures off, so `->` and `://` look as they are copied.
- "Created Sep 2, 2026" always shows the year, as the design does (`formatFullDate`); "Joined …" on the users list likewise.
- The sign-in pages (no design) are one 420 px column on the page background: brand, headline, sentence, and the form in a card. The login page sends people on to `?next=` only when it is a path on this site and not a sign-in page (`safeNext`).
- The shell loads `GET /ui/api/me` (polled with the other data) for the role and the CSRF token; a 401 from the API sends the browser to `/login?next=<here>`. The logo moved to `components/Logo.tsx` for the sign-in pages.

End-to-end:

- `global-setup.ts` creates the first admin on the setup page in Chromium and stores its session (`ADMIN_STATE`) for the specs, instead of `DEV_BASIC_AUTH`. `phase2-access.spec.ts` deletes the project it creates, because `phase2-issues` counts the two example projects. New screenshot `phase2-settings.png` (Settings → Projects with the New project form open).

Fix round 1 (review of Task 5):

- The login throttle reserves each try before the password check (`throttle.begin`: check and count in one step, both limits under one lock), so simultaneous requests cannot all get past the limit; before, every request that arrived before the first recorded failure was checked (60 at once: 60 checked). A sign-in that works gives its try back (`succeeded`: the address starts over, the client's try is removed); a failed one keeps it. A try whose request then fails for another reason (the database, the client leaving) also stays counted. The forgot-password throttle uses the same reservation.
- Controller ruling 30: behind a trusted proxy (loopback or private peer) the client is the right-most `X-Forwarded-For` entry that is not itself loopback or private; the left-most entries, which the client writes, are ignored, and an entry that is not an address ends the walk (the last proxy seen counts). All header lines are read as one list. IPv6 addresses count per /64.
- Controller ruling 30: login answers 400 for anything that is not one valid address of at most 254 characters, before the throttle sees it. Address keys are salted SHA-256 hashes like client keys, so a key never holds the address and never grows with it. Each limiter tracks at most 10 000 keys; a new key in a full limiter looks at 32 keys (Go visits a map in random order), drops those whose tries all left the window, and else the one used longest ago. No call scans every key any more.
- Controller ruling 30a: removing an admin or making them a member deletes the unused invites they made, in the same transaction (they have seen those links and could come back through one). Used invites stay for the record.

Fix round 2 (re-review of Task 5):

- (Replaced in fix round 3 by ruling 32: the split below made 401 versus 429 an account oracle once the capped store was flooded, so the claim that both stores "answer alike" did not hold.) Controller ruling 31: the per-address limit has two stores. Addresses of existing accounts are counted in one that never evicts while a key is inside its window (bounded by the number of accounts); login, the account page and "forgot password" look the account up before the throttle step. Other addresses go to the capped store (10 000 keys, sampling eviction). In one shared store, a locked-out account's key (refused tries add no events, so it looks oldest) was the first a flood of throwaway addresses pushed out, which lifted its limit. Both stores count 5 tries per 15 minutes and answer alike. The per-client store stays capped: an attacker with enough addresses to flood it has that many clients anyway.
- A right current password on the account page gives its try back and starts that address's count over, as a sign-in does (`succeeded`).

Fix round 3 (re-review of Task 5):

- Controller ruling 32 replaces ruling 31: a bounded per-address limit that answers 429 cannot avoid telling accounts from other addresses (flood the cap and 401 means "no account", 429 "account"). So the per-address limit is a silent per-account lockout: after 5 wrong tries in 15 minutes an account answers every sign-in exactly like a wrong password (401, the same text, the dummy hash for the same work), even the right password, until the oldest of those tries is 15 minutes old. Locked tries add no try, so the lock never stretches while it is hammered. There is no store for addresses without an account: they are counted per client only. The account store needs no cap (one key per account) and drops expired keys at most once a window, so keys of deleted accounts do not stay.
- The per-client limit (20 in 15 minutes, reserved before the password check, right-most untrusted `X-Forwarded-For`, IPv6 /64) is the only one that answers 429 on the sign-in page; it counts every try alike.
- The 401 text: "The email or password is wrong. After 5 wrong tries, signing in to an account pauses for 15 minutes; resetting its password ends the pause." A reset by mail, by an admin's link or with the CLI ends the lock (`forget`).
- Forgot password: at most 5 links per account in 15 minutes; after that the request is answered the same and sends nothing. The per-client limit answers 429 as for sign-ins.
- On the account page (signed in, your own account) a locked account answers 429 with the wait: it tells nothing the user does not know. A right current password gives the try back (`succeeded`) and starts the count over.
- Tests count the real password checks through `Service.verify` (VerifyPassword by default).

## Task 6: Alerts, retention, health and pre-migration backups

Follow-ups from the Task 4 and Task 5 reviews:

- A failed load (new filters, or a poll) keeps the data on screen, sets `error` and ends `stale`, since nothing newer is on its way; the issue list then says "Could not load; showing older results." above the rows. The list remembers the tab it was loaded for (`LoadedIssues.status`), so rows from an older tab keep their own buttons and the optimistic counts move the right tab.
- `usePolling` starts no load once its page is gone (a status change that settles after leaving the page).
- `loadIssues` and `withIssueMoved` moved to `web/src/lib/issueList.ts` (tested with a fake API: requests of at most 100, stopping at the total, duplicates shown once); the shared optimistic status change is `lib/useStatusChange.ts`.
- `usePolling` and `copyText` are tested in a DOM: dev dependency `happy-dom` 20.14.5 (released mid-September), per test file with `// @vitest-environment happy-dom`, rendering with React's own `createRoot` and `act`; no Testing Library.
- `copyText` reports whether the copy worked (the clipboard promise, or `execCommand`'s answer); the copy-link button shows its tick only then, and otherwise says "The link could not be copied" as its label. The Copy buttons of code boxes do the same.
- The CSRF token: the sign-in answers (setup, login, invite, reset), which already carry `csrfToken`, set it at once; a change made before any token is known (right after a page load) first asks `GET /ui/api/me` itself, once for all changes waiting.
- Scrubbing: when a URL's host part (up to the first `/`) has no `@` but a `:` that is not followed by a port, the password holds an unencoded `/` (`postgres://app:pa/ss@db/x`) and the user info runs to the last `@` before whitespace. A password whose part before the `/` is digits only (`app:12/ab@db`) still looks like a port and is missed.
- `CreateInvite` checks inside its write transaction that the admin who makes the invite is still an admin (`store.ErrNotAdmin`, answered 404 like any admin route for a member); a creator id of 0, which only tests use, skips the check.
- `sanitize.mjs` compares the real paths of `process.argv[1]` and itself, so it runs through a symlinked path (macOS `/tmp`).
- Setup and keys (Task 5) shows the Laravel steps only when the project tracks Laravel errors, the browser steps only when it tracks browser errors, and the PostHog token and host only when page views are on (otherwise a "Turn on page views" button); the kinds it does not track are named in one line linking to Edit project. A page full of steps for things the project does not collect would suggest it does.
- `SettingsUsersPage.tsx` keeps the page and its tables; the invite and edit forms (with the role select and project checkboxes) are `pages/UserForms.tsx`, the card that shows a new link once is `components/LinkCard.tsx`.

Schema (migration 0004):

- `alerts` has one row per project, kind and channel, as the spec lists it; the spike threshold and the summary frequency are stored on both channel rows of their kind (the API keeps them equal). A project without rows uses `store.DefaultAlertSettings`: new issue, returning issue and spike on for email and Slack, the summary off (weekly). Slack needs a webhook and mail needs `MAIL_URL` before anything goes out.
- The project's webhook is `projects.slack_webhook_url` ('' for none); the admins' webhook is the `settings` row `admin_slack_webhook_url` (ruling 18). Columns are called `name`, not `key`, which is a keyword in some SQL.
- `alert_state` is keyed by a name (`spike:<id>`, `digest:<id>:<frequency>`, `admin:<kind>`) with a nullable project id that cascades, so a deleted project takes its state along. `ClaimCooldown` and `ClaimPeriod` read and write in one write transaction; an alert is recorded before its messages are queued, so it is never sent twice, and with in-memory delivery (ruling 11) a crash in between loses it.

Notifications:

- Delivery: 3 tries in all (ruling 11's "3 attempts"; the brief's "3 retries" read the same way), pausing 5 s and 30 s, each try at most 30 s; 4 workers; at most 1000 issue events and 1000 messages wait. A full event queue drops the notification with a warning instead of blocking the ingest writer, which calls the hooks.
- Cap: 30 notifications per project and channel in the hour that starts with the first of them; a mail to several people is one notification. Only new and returning issues count: spikes have their 6 h cooldown and summaries go out once per period. When the hour has passed, one "N more notifications were suppressed" message goes out on that channel (a job checks every minute, and a notification that opens the next hour sends it first). The counts are in memory and start over after a restart.
- Mail goes to everyone who may see the project when it is sent (ruling 10: every admin and the members with access); admin alerts go to admins only. Subjects are "[Webshop] New issue: RuntimeException: Checkout failed" (cut at 150 characters); bodies link to the issue and to the project's Alerts page and name `BASE_URL`, never the tool's name.
- Slack messages use mrkdwn (`<url|label>`, with `&`, `<` and `>` escaped). The client follows no redirects; its errors never contain the URL (`*url.Error` is unwrapped) and pass on Slack's short error word ("Slack answered 404 (no_service).").
- (Changed in fix round 1, ruling 33/36: https only, see below.) A webhook must be an http(s) URL with a host and no user name or password, at most 500 characters; other hosts than hooks.slack.com and plain http are allowed for Mattermost and the like on a local network. Masked as `scheme://host/…/` plus its last four characters (`…` alone when the rest is 8 characters or fewer).
- (Replaced in fix round 1 by rulings 33 and 35: only admins change alerts.) Everyone who may see a project may change its alerts and webhook and send the test message (the brief makes only the status page and the admins' webhook admin-only). So a member can make the server post the fixed test text to any http(s) address and learn only whether it answered 2xx; accepted for a team tool.
- The test message is sent at once (10 s), to the URL typed in (before saving) or the saved one; a failure answers 400 "The test message did not arrive. …", not 502, so the page shows the reason.

Spike (ruling 9):

- The last hour is events with a timestamp in [now − 1 h, now + 1 h) (an hour ahead counts, as on the dashboard). The usual rate is the events of the 168 hours before that hour divided by 168, also for a project younger than a week (it then fires more easily; the minimum of 10 errors guards). It fires at threshold × usual and at least 10 errors, at most once in 6 hours per project (both channels together). The threshold is a whole number from 2 to 100, default 5. The message names the three busiest issues of the hour.

Summaries:

- The job runs daily at 08:00 in `TZ` (`jobs.DailyAt` keeps the local time across daylight saving changes). Daily covers the calendar day before in `TZ`, weekly (Mondays) Monday to Sunday before, monthly (the 1st) the month before. The period's first day is claimed per project and frequency, so a period goes out once, across restarts too. A server that is down at 08:00 skips that day's summaries (no catch-up later).
- A summary has the errors, the number of new issues with up to 5 of them, the 5 busiest issues and the issues open now. (Task 7 adds page views, visitors and the average time for projects that track page views.)

Admin alerts:

- Checked every minute: a low disk (the health check), stored-event losses of the writer, and events the queue turned away. The ingest counters run since the start; the first check takes their values then as the baseline. Each kind alerts at most once in 12 hours (`alert_state`, or in memory when the database itself fails). Its one-line summary is kept and shown on the status page as the last alert of that kind ("recent admin alerts"). Subjects name the `BASE_URL` host, so several installations can be told apart.
- A disk that cannot be read makes `/health` fail (`disk: error`) but sends no admin alert; only a low disk does. (Fix round 1: an unreadable disk is degraded, 200; a low or critical disk alerts.)

Health:

- (Changed in fix round 1 by ruling 34: 503 only when the tool cannot work; see below.) `/health` answers `{status, version, db, disk, diskFreeBytes, diskTotalBytes, queueDepth, failing}`, 503 when any check fails. The disk is low under 10 % free or under 1 GiB free (the brief's "or"), counting the space this process may use (`Bavail`, via `statfs`); systems without `statfs` report the disk as `error`. A database error is logged on every call; the disk is the admin alert's job.
- (Replaced in fix round 1 by ruling 34.) On a nearly full disk `/health` answers 503 and Docker marks the container unhealthy; the compose file restarts only on exit, so that changes nothing but the status.

Retention, backups and jobs:

- Retention runs daily, first a minute after the start: per project with a retention, events with a timestamp older than now − days, then issues last seen before that (with their remaining events), each in batches of 5000 rows, one transaction per batch. It goes by the event's timestamp, which the index covers; an issue with a newer event stays, and its counter (`times_seen`) still includes the deleted events.
- Every command that opens the database (`serve`, `project create`, `admin reset-password`) backs it up first when migrations are pending and `schema_migrations` has rows (`cmd/monitor/database.go`). The copy is `VACUUM INTO <DATA_DIR>/backups/pre-migrate-<version>-<UTC time>.db` (`-2` when the name is taken), named after the version before the update. The five newest pre-migration backups are kept (by the time in the name); other files there are left alone. A failed backup stops the command before anything changes, naming `DATA_DIR`; a failed migration names the backup.
- One scheduler goroutine runs the jobs one after another (retention every 24 h, spike check every 5 min, summaries at 08:00 in `TZ`, suppressed-count messages and admin checks every minute). A job that was due several times (a machine that slept) runs once; the scheduler looks at the clock at least every minute; a panic is logged and the rest carry on.
- Shutdown order: the HTTP server, the jobs, the writer (its last batch still notifies), the notifier (10 s for messages under way), the database.
- `TZ` is read by `config.Load` (a leading `:` is dropped; an unknown zone stops the start); without it schedules use UTC. The zone data comes from the distroless image (checked with `TZ=Europe/Amsterdam` in the container).

Dashboard:

- The sidebar's Alerts (bell icon, as in the designs) sits under Issues; Pages and Events come in between later. `/projects/:id/alerts` is for everyone who sees the project; Settings → Status (`/settings/status`) for admins, linked between Users and Account.
- The Alerts page headline is a conclusion: "Webshop sends alerts by email and Slack" (or "by email", "to Slack", "no alerts"), counting a channel only when it can deliver (mail on, a webhook saved). Checkboxes save at once, optimistically like status changes; without `MAIL_URL` the email boxes keep their saved state but are disabled, under "Email is off. Set MAIL_URL to send email."; the threshold saves on leaving the field or Enter.
- The Status page headline names the problem ("Everything works", "The disk is almost full", "The database does not answer", "2 problems need attention"; fix round 1 adds "Disk space is running low" for a low disk and keeps "almost full" for a critical one), the sentence under it gives the version, the uptime and what `/health` answers. A strip of numbers in the style of the issue page (database size, disk free, events in the last hour, waiting to be stored), then Backups and Alerts for admins (the last alert of each kind, and the admins' webhook).
- "Events in the last hour" counts what this process stored (a per-minute counter in the writer), from the start after a restart: counting by receive time in the database would need a new index or a full scan every minute, and the event timestamp can be days old. The database size is the SQLite file plus its WAL.
- `internal/api/status.go` (the issue status route) is now `issue_status.go`, so `status.go` holds the status page as the brief names it. Extra files beyond the brief: `internal/store/{alerts,summary,retention}.go`, `internal/health/disk_{unix,other}.go`, `internal/ingest/counter.go`, `cmd/monitor/database.go`, `web/src/components/SlackWebhook.tsx`, `web/src/lib/{alerts,status}.ts`.
- e2e: `phase2-status.spec.ts` (last in file order, so it changes nothing the others count) uses a fake Slack on 127.0.0.1 and the real sentry-laravel SDK for the returning issue; new screenshots `phase2-alerts.png` and `phase2-status.png`, whose headline depends on the disk of the machine running it.

Container:

- `compose.yaml` follows the spec's example with the local image `monitor:dev`, `TZ: Europe/Amsterdam` and `restart: unless-stopped`; `SECRET_KEY` comes from the environment (`${SECRET_KEY:?…}`), because the example's `verander-mij` is refused at startup; `BASE_URL` defaults to `http://localhost:8000`.

Fix round 1 (review of Task 6):

- Controller rulings 33 and 35: changing a project's alerts (`PATCH /ui/api/projects/{id}/alerts`) and sending its Slack test message are admin-only, through `adminOnly` (404 for members, as on every admin route); reading them (`GET`) stays open to everyone who sees the project, with the webhook masked. `TestAccessMatrix` lists both change routes as admin-only, with their "nothing changed" checks. The Alerts page is read-only for members (and until the signed-in user is known): disabled checkboxes, threshold and frequency, the masked webhook alone ("No Slack webhook yet." without one), and one line "Only admins can change alerts."
- Controller rulings 33 and 36: webhook URLs must be https ("The webhook URL must start with https://, so the messages are encrypted on their way."). `DEV_ALLOW_HTTP_WEBHOOKS=true` allows plain http for local fakes (the e2e setup and the container check set it); `serve` then logs a warning, and it is never for production. Besides the check on saving and testing, the Slack client itself refuses an http webhook without the flag, so one saved while the flag was on is not used once it is off. The flag is parsed with `strconv.ParseBool`; another value stops the start.
- Controller ruling 34: `/health` answers 503 (status `error`) only when the tool cannot work: the database check fails, or the disk is critical (under 1 % or under 256 MiB free). A low disk (under 10 % or 1 GiB) or one that cannot be read answers 200 with status `degraded` and `disk` named in `failing`; the admin alert goes out for low and critical. Docker's health check therefore stays healthy on a low disk. The status API reports `status` (`ok`, `degraded`, `error`) instead of a `healthy` boolean; the page says "The /health check answers OK, with status degraded, until this is fixed." for a degraded tool.
- A change to a project's alerts is read, applied and written in one write transaction (`store.UpdateAlertSettings` with a change function, reading through the transaction): two PATCHes at once (a checkbox and the threshold field) run one after the other and both stay. The body is checked before the transaction (threshold, frequency, webhook), so the change itself cannot fail. `SaveAlertSettings` is the same transaction with a change that replaces everything.

## Task 7: PostHog ingest and page statistics

Follow-ups from the Task 6 review:

- `Notifier.Close` runs once (`sync.Once`; `serve` calls it on the normal path and again deferred). After the wait it stops the goroutines and counts whatever they left in the queues as done, so `busy` reaches zero and the goroutine that waited on it ends; the second call returns at once.
- `queueDelivery` never waits: with 1000 messages waiting (workers stuck on a slow mail server or webhook) the next one is dropped with a warning that names the channel, project and kind, as `queueIssue` does. The scheduler goroutine (summaries, suppressed counts, admin alerts) and the dispatcher keep going.
- `compose.yaml` sets `stop_grace_period: 30s` (and the README tells `docker run` users `--stop-timeout 30`): the shutdown budget is 15 s for requests plus the writer's last batch plus 10 s for notifications, and Docker's default of 10 s would kill the process while it writes events it already answered 200 for. The budgets stay as they are.
- A failed removal of old pre-migration backups no longer stops the start: `BackupBeforeMigrate` returns the new backup's path with `store.ErrOldBackupsKept`, and `openDatabase` reports it as a warning (`serve` logs it; the CLI commands print it) and migrates. Any other backup error still stops the command.
- The summary job counts open issues once for all projects (`CountOpenIssuesPerProject` before the loop), not once per project.
- Scrubbing: a URL whose host is a bracketed IPv6 address (`http://[::1]:8080/x?u=a@b`) keeps its colons; only a user-info password before the `@` is filtered (`http://app:[Filtered]@[::1]:5432/x`).
- `CreateInvite` always checks that the creator is still an admin; the "creator 0 skips the check" escape hatch for tests is gone, and the test helpers pass a real admin's id. `TestInvites_AdminDemotedMeanwhile` demotes the admin between the sign-in check and the handler and expects 404 and no invite.
- `internal/alerts/notifier.go` keeps the Notifier, its queues and delivery; the hourly cap and the suppressed-count message are in `cap.go`, the issue messages and their helpers in `messages.go`. `internal/api` and `internal/server` take `alerts.SlackPoster` instead of a copy of the interface.


Fixtures (Step 1):

- Captured on 2026-10-16 with `posthog-js` 1.438.12 (the latest, released the day before), Playwright 1.64.1 (Chromium headless shell 156) and esbuild 0.28.2; the recipe is in `internal/posthog/testdata/capture/`. The page uses the setup Task 8's Setup page should give: `persistence: 'memory'` (no cookies), `capture_pageview: 'history_change'`, `capture_pageleave: true`, `disable_session_recording: true`, `api_host` = the monitor.
- What posthog-js called, in order: `GET /array/<token>/config.js` (the remote config as a script; `GET /array/<token>/config` is its JSON fallback), `POST /e/` (the first `$pageview`, sent before the config named `/i/v0/e/`), `GET /static/1.438.12/surveys.js` (loaded even with surveys off), `POST /i/v0/e/` for every later event (the history page view, `$identify`, `custom`, `$pageleave` on closing the page), and `POST /flags/?v=2` after identify (a gzip body with the distinct id and person properties). Not called with these answers: `/decide/`, `/batch/`, `/capture/`, `/s/` (recording off); they are served for other SDKs and older versions.
- Every capture body is `{"api_key", "batch": [one event], "sent_at"}`, gzip, sent as `text/plain` with no `Content-Encoding` and no query string: posthog-js 1.438 no longer adds `?compression=gzip-js&ver=…`, so gzip is told by the body's first bytes. The leave goes by fetch keepalive the same way. Ids: a `$pageview`'s `$pageview_id` is its uuid and `$prev_pageview_id` the tab's previous page view; a `$pageleave` carries the left page's `$pageview_id` (and the same in `$prev_pageview_id`) and posthog-js's own `$prev_pageview_duration`. Every event has `$session_id` and `$window_id`, the token in `properties.token`, `distinct_id` in the properties, and no `$ip`.
- The recorder answers the configuration endpoints the way the monitor does (new `-responses` flag of `tools/capture`): posthog-js picks its compression from the remote config's `supportedCompression`, and with the recorder's plain `{}` it would have sent uncompressed JSON. `responses.json` is checked against `posthog.RemoteConfig` by `TestPosthog_ConfigMatchesTheCapture`.
- The capture page sets a visitor's Chrome User-Agent and hides `navigator.webdriver` and `navigator.userAgentData`: posthog-js sends nothing from a browser it takes for a bot (Task 8's e2e needs the same).
- The bodies are committed exactly as sent: nothing in them named this machine. The request files keep method, path, query and headers and drop the receive time. The leak check moved to `tools/capture/leaks.mjs` (the Sentry sanitizer imports it), the PostHog sanitizer uses it, and `tools/capture/fixtures.test.mjs` (in `make test`) runs it over every committed fixture.

Decoding and the endpoints (Step 2):

- `/flags/` answers exactly `{"flags":{},"errorsWhileComputingFlags":false,"requestId":"<random uuid>"}`. The remote config turns everything off that this tool does not collect: `sessionRecording`, `surveys`, `heatmaps`, `captureDeadClicks`, `capturePerformance` false, `autocapture_opt_out` true, error tracking and console logs off, `hasFeatureFlags` false (posthog-js then skips `/flags/` on load; it still asks after identify), `analytics.endpoint` `/i/v0/e/` and `supportedCompression` `["gzip","gzip-js"]`. `/decide/` answers that config and empty flags under the old and new names. Config and config.js are cached 5 minutes and served for any well-formed token (they hold nothing per project); a malformed token gets 404, and the token is written into the script as a JSON string.
- `/static/…` gets an empty script (200): posthog-js asks for `surveys.js` even with surveys off, and an empty script costs a request less than a failed one logs. `/s/` (recordings) gets 200 and is dropped unread. Unknown paths under the PostHog prefixes get `200 {}` and a debug line, so an SDK that adds an endpoint stays quiet. Each prefix is served with and without its trailing slash, because Go's redirect would turn a POST into a GET.
- Capture answers `{"status":"Ok"}`, as PostHog's capture service. An event belongs to the project of its own `properties.token` (or `api_key`/`token` on the event), else to the body's `api_key`/`token`; one unknown or missing token refuses the whole request with 401, so nothing is stored half.
- Bodies: `Content-Encoding` gzip or deflate is undone first (the Sentry handler's reader, 20 MiB before and after); then a form (`data=`, with `compression` from the query or the form), gzip (`compression=gzip-js`/`gzip`, or by its first bytes), base64 (`compression=base64`, or a body that is neither JSON nor gzip; spaces count as `+`), JSON. Anything else is 400 (`lz64`, brotli, a body without events, an event that is not an object); an event over 1 MiB or without a name is dropped alone. Ids are cut at 200 bytes, URLs at 2000; the stored path at 1000.
- Rate limit: the project's `rate_limit_per_minute` with a limiter of its own (not Sentry's), so a busy site's page views never take its errors' budget; it counts every event of a request (identify and custom events too). Then the same queue and writer as Sentry events (429 with `Retry-After: 5` when full).
- Only `$pageview` and `$pageleave` are queued; other events are accepted and not stored until Task 9 stores custom events.
- The time of an event follows PostHog: the receive time minus `offset`, or the receive time moved by `timestamp − sent_at` (the client's clock error cancels out), or `timestamp`, or the receive time; then the writer's clamp (an hour ahead, 30 days back).
- Scrubbing (`scrub.Properties`): IP fields are dropped at any depth (`$ip`, `ip`, `ip_address` and the IP headers' names, `{{auto}}` values), then the key and URL rules apply. The fields page views need (`$session_id`, `$window_id`, the page view ids, `$current_url`) are read before scrubbing, because `$session_id` matches the `session` key rule. Page views store the URL's path only (no query or fragment).
- Bots: the request's User-Agent, `$raw_user_agent` (an SDK relaying a visitor's events from a server sends its own User-Agent), or `$browser_type: "bot"` (what posthog-js sends with `opt_out_useragent_filter`). The generic words match anywhere in the User-Agent; "Cubot" (a phone brand) is exempt.
- Accepted PostHog requests are logged at debug and refused ones at info, like Sentry's. "Events stored in the last hour" and the writer's lost-event counts include page views and leaves.

Time on page (Step 3):

- A page view is closed by its `$pageleave` (by `$pageview_id`) and by the next `$pageview` in the same tab, whichever is first (ruling 12). The tab is `$session_id` plus `$window_id`: with posthog-js's default persistence several tabs share a session, and another tab's page view must not end this one, so `pageviews` has a `window_id` column beyond the brief's. A leave without `$pageview_id` (other SDKs) closes the latest page view of its tab before it; events without a session pair by id only.
- Arrival order does not matter: every closer can only shorten a duration (the minimum of the plausible ones), each new page view looks for closers already stored, and each new event updates the page view it closes. Leaves are kept in `pageleaves` (an implementation table, ruling 18), so a page view that arrives after its leave is still closed; retention clears them. `TestDurations_AnyOrderMatchesReference` compares random visits in random orders and batches with a computation from the whole visit.
- A duration under 0 or over 6 hours is dropped (NULL) but the page view counts as closed (not "left without a page leave"); exactly 6 hours counts; a later plausible closer still gives it a time. Page views with the same start in one tab do not close each other.
- A page view is stored once per `$pageview_id` (else its uuid), a leave once per event uuid.

Routes (Step 4):

- Normalize strips query and fragment, scheme and host when given a URL, empty segments and the trailing slash, tries the project's patterns in order (read like paths, so the leading slash is optional; `:name` is one segment; literal segments compare exactly; the first match wins; blank lines are skipped), then per segment: digits → `:id`, UUID → `:uuid`, 16+ hex → `:hash`, and `:token` for 20+ characters of letters, digits, `-` and `_` with a part of 8+ characters between dashes that mixes letters and digits (so slugs like `summer-sale-2026-shoes` stay). Routes are cut at 300 bytes. What the rules get wrong, a pattern fixes.
- Patterns live in `projects.route_patterns` (a JSON list; `store.SetRoutePatterns` keeps at most 100 of at most 300 characters; Task 8 adds the form). They apply when data comes in, so a change leaves stored routes as they were (ruling 14).
- Sentry events get their route from `request.url`, else a `url` tag; events without either keep `''`.

Aggregation (Step 5):

- Dirty hours are rows in `stats_dirty_hours`, written in the same transaction as the page view, leave or routed error that changes the hour (late data too), so a restart loses none. The job rebuilds an hour and clears its mark in one write transaction; a failed hour keeps its mark for the next run.
- "Upserted idempotently": `RecomputeHour` replaces the hour's rows (delete and insert in one transaction), which also removes routes that lost their data. `hourly_stats` has an `id` column so retention can delete in batches.
- Visitor sketches are our own (no dependency): a 32-bit hash (FNV-1a 64 with murmur3's finalizer) per user id, kept as an exact sorted set up to 4096 visitors (4 bytes each), then a HyperLogLog with 2^14 one-byte registers (16 KiB, about 0.8 % error) with linear counting for small counts. `TestHLL_MergeAccuracy` is within 2 % (worst 1.3 % over 50 seeds).
- Visitors are the user ids (`distinct_id`) of people; page views without one count as views, not visitors. Bots count only in `bots`.
- Errors per route count `error_events` by their timestamp, routes `''` left out, for every project (not only those tracking page views). Hours are UTC clock hours, as the dashboard's bars; a summary's period in `TZ` takes the hours that start in it (zones with half-hour offsets are 30 minutes off).
- The current hour of every project is rebuilt every minute, as the brief says, even without a mark.
- `store.PageStats` (a project's totals, routes sorted by views and views per hour over a range) and `store.PageviewTotals` (the All projects columns) are the queries Task 8's API is meant to use; the perf test measures them.
- Retention deletes page views by start, leaves by time, and the hourly rows (and dirty marks) of hours before the cutoff's hour, each in batches.
- Summaries add "Page views", "Visitors" and "Average time on page" ("1m 15s") for projects that track page views, from the hourly rows of the period; the line is left out when no page view has a time.

Performance (Step 6):

- No index on `error_events.route` after all: the issues filter finds events through their issue, and errors per route per hour use the `(project_id, timestamp)` index.
- Files beyond the brief's list: `internal/hll` (the sketches), `internal/store/{pageviews,pages}.go` (the queries live in `store`), `internal/alerts/{cap,messages}.go` (the review's split), `tools/capture/{leaks,fixtures.test}.mjs` and the PostHog recipe's `sanitize.mjs` and `responses.json`.
- `make perf` (`internal/jobs/perf_test.go`, build tag `perf`) runs the brief's load in a temp database that the test deletes; results in `docs/perf.md`: writer 50 400 events/s, aggregation 1.8 s, 30-day pages query at most 188 ms, overview 4 ms (24 h). All targets met.

## Task 8: Pages screen, page views on the overview, errors per page

Follow-ups from the Task 7 review:

- Controller ruling 38: page addresses are stored without the secrets in their path. `routes.Path` (PostHog's `pageviews.path`) and `routes.MaskURL` (Sentry's `request.url` and `url` tag, through `scrub.Event`, so the stored URL column and the event JSON agree) mask segments that are a UUID (`:uuid`), 16+ hex characters (`:hash`) or a token (`:token`), with `routes.Normalize`'s own detection; digits and slugs stay, so the raw-path preview still names the page. Laravel's `/reset-password/{64 hex}` is stored as `/reset-password/:hash`. Breadcrumbs and other strings that hold a URL are not masked (their query parameters are still scrubbed by key); the ruling names the stored path and URL.
- `MaskURL` leaves scheme, host, query, fragment and the encoding of other segments as sent; `Path` decodes its segments for reading. A page view's route is now computed from its stored (masked) path, so the preview on Edit project shows exactly what ingest does.
- `routes.Normalize` compares segments decoded: each segment is percent-decoded and `%` and `/` are encoded again, so `/zoeken/caf%C3%A9` (Sentry) and `/zoeken/café` (PostHog) are one route, an encoded slash never adds a segment, and a route normalizes to itself. A segment with an invalid escape (`100%`) stays as it is. Patterns are read the same way.
- Routes (300 bytes) and stored paths (1000 bytes) are cut at a character boundary (`routes.cut`, like `posthog.cut`); the PostHog handler no longer cuts paths itself.
- The full-queue answer and the failed project lookup are shared by both ingest handlers (`internal/ingest/refusals.go`, with the log prefix `sentry` or `posthog`).
- A refused request spends nothing: both handlers check the queue's room for the whole request first (`Queue.HasRoom`), then count every project's events at once (`RateLimiter.AllowAll`, all or none), then queue all items at once (`Queue.TryEnqueueAll`, all or none, under a mutex for producers); when the queue filled up between the check and the add, the budgets go back (`RateLimiter.Return`, only to the minute they were counted in). So the queue is checked before the rate limit: a request that meets both gets Retry-After 5, not the seconds to the next minute. The Sentry handler now scrubs after the checks instead of before the queue, so a refused envelope costs no scrubbing.
- `TestNotifier_FullDeliveryQueueDrops` waits until each of the 4 workers started its post before filling the queue, so exactly 3 messages are dropped every run (passed 30 times in a row under `-race`).
- The leak check (`tools/capture/leaks.mjs`) does not look for user or host names that are ordinary words of the recordings (app, www, var, web, mac, chrome, desktop, npm, in any case) or shorter than 3 characters: such a name cannot be told from the words, so the guard would fail on every fixture.
- New tests for the review's gaps: `TestAggregate_CloserInALaterHour` (a 10:59 view closed at 11:01; the leave marks only the view's hour, which is all that changes), the 30-day overview is asserted under 300 ms in `perf_test.go`, and `TestStartup_WarnsAndContinuesWhenOldBackupsStay` runs `project create` through `openDatabase` with a backup whose old copies stay (`backupBeforeMigrate` is a package variable so the test can make that failure).
- The Alerts page waits for `/ui/api/me` (as `AdminOnly` does) before it shows controls or "Only admins can change alerts.", so an admin never sees the page read-only first; when `/me` fails, the page keeps waiting until the shell's next poll.

Pages API (Step 1):

- `GET /ui/api/projects/{id}/pages` answers for any project the user sees, whether or not it tracks page views (tokens are accepted either way); `project.tracksPageviews` lets the screen word its empty state. The default range is `7d`, as the design shows; another value than `24h`, `7d` or `30d` is 400.
- 24 hours are the 24 clock hours (UTC) that end with the current one, as the overview's error bars; 7 and 30 days are calendar days that end with today, a bar each, in the browser's time zone (`tz`, an IANA name such as `Europe/Amsterdam`; UTC when missing, longer than 64 characters or unknown), so the bars' labels ("Oct 16") are the viewer's days. Days are made of UTC hours (a half-hour zone is 30 minutes off, as the summaries are). Every bar is in the answer, empty ones with 0. Totals, pages and bots cover exactly the bars' hours.
- The pages list keeps the routes with views or bots (carry-over 10), sorted by views then route, at most 100; a route that only bots saw is listed with 0 views. Its errors are the route's error events in the range; the issue list's `route` filter (ruling 19) shows their issues, whatever their status.
- Carry-over 10's visitors for the totals come from `store.PageStats` (its totals merge every hour's sketch), which the Pages API uses; `store.PageviewTotals`, the overview's query, stays without visitors because the overview shows none and its 30-day run must stay under 300 ms without merging sketches.
- The overview fills `pageviews24h` (0 without data) and `avgTimeOnPageMs` (null while no page view has a time) for projects that track page views, over the 24 clock hours that end with the current one; projects that do not track them keep null ("Add page tracking"), even when their token sent page views.
- `GET /ui/api/projects` adds `tracksPageviews`, so the sidebar shows Pages only for projects that track them (between Issues and Alerts, the design's order).
- Route patterns: PATCH `/ui/api/settings/projects/{id}` takes `routePatterns` (blank lines dropped, at most 100 of 300 characters; "A route pattern can have at most 300 characters." / "A project can have at most 100 route patterns."), saved after the other settings in a second write; the project answers carry them. Bodies with patterns may be 64 KiB.
- `POST /ui/api/settings/projects/{id}/route-preview` (admins) answers up to 20 recent paths with the route each gets under the patterns sent, without saving them, so the preview uses the server's own `routes.Normalize`. The paths are the distinct paths of the latest 500 page views and the latest 500 error URLs (newest first), passed through `routes.Path` (masked even when stored before ruling 38), at most 3 per first segment so one busy section leaves room for the others. It reads raw page views (by the start index), the only API that does, and never for statistics.

Pages screen (Step 2):

- The headline names the page where visitors stay longest: the longest average time among pages with at least 1 % of the range's views (a page seen twice says little), or among all pages with a time when none has that many. The sentence under it: "It is also the page with the most errors." when that page has the most errors of the listed pages, "The most errors happen on /cart." when another page does, "No page had errors." otherwise, then "Webshop, last 7 days." Variants: "/ gets the most views" when no page has a time yet ("Times on a page show up once visitors leave it."), "No page views yet" (with the period and when to expect them, or "Page views are off for Webshop."), and "Only bots visited Webshop".
- Without any page view or bot the screen shows one card with what to do: admins get a link to Setup and keys (or to turn page views on), members are told an admin has the steps.
- Times read as the Pages design writes them: "38s" under a minute, "1m 21s", and "1h 05m" from an hour (durations go up to 6 hours); the overview keeps its own "0m 48s" form, as its design does. A page without a time shows an empty bar and "No time yet"; totals without a time say "Nobody left a page yet".
- The time bars are relative to the longest average time in the table (the design's /checkout fills its bar). Bars of the chart are relative to the busiest bar, the latest one darker; a week's 7 bars get their numbers above them and every label, 24 hours and 30 days (narrower bars) only tooltips and every third hour or fifth day labelled, counted back from the last bar.
- The period is in the URL (`?range=24h`, `?range=30d`; none for 7 days), so a reload or a shared link keeps it; while another period loads the old numbers stay, dimmed.
- Bots are one muted line under the totals: "312 bot visits not counted".
- New tokens: `--color-errors-link` (the errors column's links, sampled `#a8370a`), `--radius-chart-bar` (5 px top corners), `--chart-bar-width` (56 px), `--chart-bar-height` (170 px), `--time-bar-height` (10 px).

Setup and keys:

- The posthog-js snippet is exactly the setup Task 7 recorded the fixtures with (`persistence: 'memory'`, `capture_pageview: 'history_change'`, `capture_pageleave: true`, `disable_session_recording: true`, `api_host` = `BASE_URL`), checked by a Vitest test. The note under it explains the cookieless trade-off and quotes posthog-js's console warning ("PostHog will mint a new distinct ID on every page load"), which this tool can ignore: it has no PostHog person limit.
- "The same user in errors and page views" (shown for projects that track page views) gives the browser `posthog.identify(String(user.id))` with `Sentry.setUser({ id: String(user.id) })` (and `posthog.reset()` / `Sentry.setUser(null)` on sign-out), and Laravel a listener on `Authenticated` that sets only the user id on the Sentry scope: sentry-laravel's own user capture needs `send_default_pii`, which also sends the email and more.
- Edit project has the route patterns field (one per line) with the live preview (300 ms after typing stops) and the line that patterns apply to new data only (ruling 14); they save with "Save changes". The field and preview live in `web/src/pages/RoutePatternsField.tsx`, beyond the brief's file list, to keep `SettingsProjectsPage.tsx` its size.

Example and e2e (Step 3):

- `examples/posthog/` is a Vite shop of three real pages (`/`, `/products/`, `/checkout/`); product links change the address with `history.pushState` (`/products/blue-shoes`), so it has full page loads (page leaves) and history page views. The keys come from `VITE_POSTHOG_TOKEN`, `VITE_POSTHOG_HOST` and `VITE_SENTRY_DSN` when Vite starts (a multi-page site cannot carry them in `?dsn=` as the browser example does). "Sign in as user 42" identifies both SDKs and is kept in `sessionStorage`, as a session would.
- posthog-js drops events when `navigator.userAgent` names a bot, so `phase3-pages`' bot is a browser whose requests say Googlebot while its page sees a Chrome `navigator.userAgent`: real posthog-js traffic that the monitor marks by the request's User-Agent. A crawler that runs posthog-js unfiltered would look the same.
- `phase3-pages` runs last (file order) and turns on Webshop's page views with "Add page views", so the earlier specs still see "Add page tracking"; it waits for the aggregation job instead of a test hook, up to two and a half minutes.
