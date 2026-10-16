# CLAUDE.md

A self-hosted monitoring tool: one Go binary that pretends to be Sentry (errors) and PostHog (page views, events), with a React dashboard built into the binary, shipped as one small container. The spec is `concept-monitoring-tool.md` (Dutch); the designs are in `Monitoring dashboard-png/`. Every judgment call is logged in `docs/decisions.md`.

## Commands

| Command | What it does |
| --- | --- |
| `make dev` | Go server on `:8000` plus the Vite dev server (hot reload) on `:5173`, which forwards `/ui/api` and `/health` to Go. Data in `./data`. Ctrl-C stops both. |
| `make test` | `go vet ./...`, `go test -race ./...`, web typecheck and Vitest. Run before every commit. |
| `make e2e` | Playwright end-to-end tests in `e2e/`: builds the binary, starts it with the Laravel and browser examples, checks the dashboard in Chromium and saves screenshots. Needs PHP 8.3+ and Composer. |
| `make build` | Web build into `web/dist`, then `bin/monitor` with the dashboard embedded. |
| `make docker` | Image `monitor:dev` for this machine's platform. |
| `make clean` | Remove `bin/` and the web build. |

Focused runs while iterating: `go test -race ./internal/store/ -run TestCreateProject`, `go test ./internal/api/`, `cd web && npm test`, `cd e2e && npx playwright test` (after `make build` once).

Run the binary by hand:

```sh
SECRET_KEY=$(openssl rand -hex 32) BASE_URL=http://localhost:8000 DATA_DIR=./data bin/monitor serve
SECRET_KEY=... BASE_URL=http://localhost:8000 DATA_DIR=./data bin/monitor project create --name Webshop
ADDR=:8000 bin/monitor healthcheck   # exit 0 when /health answers 200
```

Container: `docker run -p 8000:8000 -e SECRET_KEY=... -e BASE_URL=http://localhost:8000 -e DEV_BASIC_AUTH=dev:some-password -v monitor-data:/data monitor:dev`. The image has no shell; run CLI commands with `docker exec <container> /monitor project create --name Webshop`.

## Capturing real SDK traffic

`tools/capture` is a tiny recorder: point any SDK or CLI at it and it writes each request to `NNNN-METHOD-path.json` (method, path, query, host, headers) and `NNNN-METHOD-path.body` (the body exactly as sent, still compressed; sanitize it before committing, see below). It answers `200 {}` (`-response` changes the body) and allows CORS from any origin, so browser SDKs work too. Use it to make test fixtures from real clients instead of hand-written ones.

```sh
go run ./tools/capture -addr 127.0.0.1:19999 -out /tmp/captured
# then use a DSN like http://5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c@127.0.0.1:19999/1 (any 32-hex key works)
```

The Sentry fixtures in `internal/sentry/testdata/` were made with the recipes in `internal/sentry/testdata/capture/` (SDK versions in `docs/decisions.md`). To redo them after an SDK update, copy a recipe to a temp directory and:

- PHP: `composer install`, then `SENTRY_DSN=<dsn> php -S 127.0.0.1:18091 public/index.php`, and `curl -X POST 'http://localhost:18091/checkout?coupon=SPRING&token=tok_abc123' -H 'User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36' -H 'Cookie: laravel_session=eyJpdiI6IjEyMyJ9; XSRF-TOKEN=abc123' -H 'Authorization: Bearer sk_live_abc123' -H 'X-Forwarded-For: 203.0.113.9' -H 'X-Real-IP: 203.0.113.9' --data 'email=jan@example.com&password=hunter2&card_number=4111111111111111'`. The User-Agent is the one in the committed fixture (a visitor's Chrome on macOS, which the issue page tests read); curl's own would change those tests. The recorded request becomes `php-exception.envelope.gz` and `php-exception.headers.json`.
- Browser: `npm install && npx playwright install chromium && npm run build && SENTRY_DSN=<dsn> npm run capture`. The request whose body holds `"exception"` becomes `browser-typeerror.envelope`; the first session envelope is `browser-session.envelope`. `browser-rejection.envelope` came from the "Reject a promise" button of `examples/browser` (served by Vite, DSN pointing at the recorder). Sanitizing the session envelope also writes `browser-session.headers.json`; that file is not committed (no test reads it), so delete it.

Never commit a recording as it is: it contains this machine's paths (with your user name), host name and kernel build. Sanitize each one into `internal/sentry/testdata/`:

```sh
node internal/sentry/testdata/capture/sanitize.mjs \
  /tmp/captured/0001-POST-api-1-envelope.body /tmp/captured/0001-POST-api-1-envelope.json \
  internal/sentry/testdata/php-exception <php-app-dir>
```

`<php-app-dir>` is the real path of the recipe copy the PHP server ran from (`pwd -P` in it, which resolves `/tmp` to `/private/tmp` on macOS); leave it out for the browser. The script replaces it with `/var/www/webshop`, sets `contexts.os` to neutral Linux values, keeps gzip bodies gzip, fixes `Content-Length`/`bodySize`, and refuses to write while your user name (as a path segment), host name (as a whole host token, in the case the system reports it) or the app directory is still in the body. Then update the ids and values the tests expect (they name them). `node --test internal/sentry/testdata/capture/sanitize.test.mjs` (part of `make test`) tests that leak check.

## Settings (environment variables)

| Variable | Default | Notes |
| --- | --- | --- |
| `SECRET_KEY` | required | At least 16 characters; the example values `verander-mij` and `change-me` are refused. |
| `BASE_URL` | required | Root of a host, such as `https://monitor.example.com`. No subpath. DSNs are built from it. |
| `DATABASE_URL` | empty | `postgres://...` selects Postgres (not supported until Task 11; startup refuses it). Empty: SQLite at `<DATA_DIR>/monitor.db`. |
| `MAIL_URL` | empty | SMTP; read but unused until Task 5. |
| `ADDR` | `:8000` | Listen address. |
| `DATA_DIR` | `/data` | SQLite file and later backups. |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. |
| `DEV_BASIC_AUTH` | empty | `user:password`. Temporary dashboard guard (Tasks 1 to 4, removed in Task 5). Without it, only requests made directly from this machine (loopback, no proxy headers) reach the dashboard, so in Docker or behind a proxy you need it. |
| `TZ` | UTC | Schedules (later tasks). |

## Layout

```
cmd/monitor/            the binary: main.go (command dispatch), serve.go, healthcheck.go, project.go
internal/config/        Load(getenv): reads and validates the settings
internal/store/         DB (one write connection + read pool), migrations, queries
  migrations/sqlite/    NNNN_name.sql, embedded, applied in order at startup
internal/project/       project helpers that are not queries (DSN)
internal/sentry/        Sentry envelopes and events: ParseEnvelope, Event, Fingerprint, Summarize, Source
  testdata/             real SDK requests (fixtures) and the recipes that made them (capture/)
internal/scrub/         removes sensitive values and IP addresses before storage
internal/ingest/        SDK endpoints: Sentry envelope handler, auth, CORS, rate limit, queue, batch writer
internal/issues/        Hooks the writer calls on new and regressed issues (NoHooks until Task 6)
internal/api/           dashboard JSON API under /ui/api/ (projects, issues, issue detail, User-Agent names)
internal/server/        router, dashboard guard, request logging, panic recovery, /health
internal/web/           serves the embedded SPA with index.html fallback
web/                    Vite + React + TypeScript dashboard
  embed.go              Go package monitor/web: embeds web/dist into the binary
  dist/.keep            committed placeholder so `go build` works before a web build
  src/brand.ts          the tool name, the only place it lives
  src/styles/tokens.css every color, font, radius and spacing value, sampled from the designs
  src/api/client.ts     typed calls to /ui/api
  src/lib/              pure helpers with Vitest tests (format, levels, frames) and usePolling
  src/layout/           AppShell (loads the projects), Sidebar
  src/pages/            ProjectsPage, IssuesPage, IssueDetailPage, NotFoundPage
  src/components/       HourBars, StackTrace, Tag, Icons, LoadError
examples/laravel/       Laravel 13 app with sentry/sentry-laravel: /boom and /boom-other fail on purpose
examples/browser/       Vite page with @sentry/browser, DSN from ?dsn=: a TypeError and an unhandled rejection
e2e/                    Playwright: global-setup.ts starts everything, phase1.spec.ts, screenshots/
tools/capture/          recorder for real SDK traffic (see "Capturing real SDK traffic")
docs/decisions.md       one line per judgment call
```

Packages still to come (see the plan): `internal/posthog`, `internal/auth`, `internal/mail`, `internal/alerts`, `internal/jobs`, `internal/routes`, `internal/bots`.

## How Sentry ingest works

SDKs send envelopes to `POST /api/<project id>/envelope/` (DSN `http(s)://<key>@<host>/<project id>`). The handler in `internal/ingest` authenticates the key (`X-Sentry-Auth`, `?sentry_key=` or the envelope's `dsn`), decompresses (gzip, deflate; 20 MiB limit before and after), parses with `sentry.ParseEnvelope` (event items up to 1 MiB, other item types skipped), applies the project's events-per-minute limit, scrubs each event with `scrub.Event` and puts it on the bounded `ingest.Queue` (10 000 events or 256 MiB of event JSON). It never waits: a full queue answers 429 with `Retry-After: 5`. One `ingest.Writer` takes events off the queue and writes them in batches (500 or every second) through `store.SaveErrorEvents`, which groups them into issues by `sentry.Fingerprint` and skips repeated event ids. Bad traffic gets a 4xx, never a 5xx; only a failing database gives 503.

Try it by hand: `bin/monitor project create --name Webshop`, then point an SDK at the printed DSN, or replay a fixture: `curl -X POST http://localhost:8000/api/1/envelope/ -H "X-Sentry-Auth: Sentry sentry_key=<key>" -H 'Content-Encoding: gzip' --data-binary @internal/sentry/testdata/php-exception.envelope.gz`. The issues then show in the dashboard under the project's Issues.

## The dashboard

Routes: `/` (projects), `/projects/:id/issues`, `/projects/:id/issues/:issueId`. Pages poll their data every 60 s (`usePolling`); the shell loads the project list for the sidebar, which shows the project of the URL (or the last one visited, or the first).

API (`internal/api`, registered on the dashboard mux, so behind the guard):

- `GET /ui/api/projects` → `[{id, name, slug, sources, openIssues}]`
- `GET /ui/api/projects/{id}/issues?status=open&limit=25&offset=0` → `{project, counts, new24h, total, issues: [{id, type, message, source, culprit, events, users, firstSeen, lastSeen, isNew, hourly}]}`. `hourly` is 24 clock hours (UTC), oldest first, the current hour last.
- `GET /ui/api/issues/{id}` → `{issue: {…, status, projectId}, hourly, latestEvent}`. `latestEvent.frames` is the main exception's trace with the most recent call first and at most three lines of code on each side of the error line; optional fields (user, browser, os, environment, server, runtime, url, release, country) are left out when the event lacks them. Browser and system come from the request's User-Agent header.

The API returns exception types in full (`Illuminate\Database\QueryException`); the UI shortens them with `shortType` and shows the full name in the stack trace and as a tooltip. An issue whose exception came in as an unhandled promise rejection is titled "Unhandled rejection" (set by `sentry.Summarize` at ingest; the fingerprint still uses the exception type). Culprits are `basename:line` for server platforms and the URL's path for browsers (`assets/main.js:21`).

The look comes from the PNGs: Instrument Sans and JetBrains Mono (bundled with `@fontsource-variable`), sizes and weights fitted to the designs, all values in `tokens.css`. To compare a change with a design, render the page at 1360 px wide and device scale 2 (the PNGs' size) and put the two side by side; `make e2e` takes its screenshots at that size.

## Conventions

- Simple, idiomatic Go; stdlib first (`net/http` routing, `log/slog`, `embed`, `database/sql`). The team is new to Go: clarity over cleverness. New dependencies need a line in `docs/decisions.md`. Approved: `modernc.org/sqlite`.
- Subcommands: `serve` (default), `healthcheck`, `project create`, later `admin reset-password`, `restore`, `delete-user-data`. One file per subcommand in `cmd/monitor/`. `run(args, getenv, stdout, stderr) int` keeps them testable.
- Times in the DB are unix milliseconds (`BIGINT`); JSON uses RFC 3339. JSON payloads are `TEXT`. Booleans are `BOOLEAN` with `TRUE`/`FALSE`.
- SQL stays in the subset SQLite and Postgres share: use `INSERT ... RETURNING id`, not `LastInsertId`. Placeholders are `?` for now (Task 11 adds a dialect helper).
- Migrations are append-only: never edit a committed file, add `NNNN_name.sql`. Every SQLite migration needs a Postgres twin from Task 11 on. Tracked in `schema_migrations`.
- All writes go through `db.write` (one connection, `BEGIN IMMEDIATE`); queries use `db.read` (read-only pool).
- Routing: public routes (SDK ingest, `/health`) go on the public mux in `internal/server`; everything else falls through the dashboard guard. Dashboard API under `/ui/api/`, camelCase JSON, errors as `{"error":"..."}`. Missing access and missing objects both return 404.
- Never store or log IP addresses. Request logs leave out the query string and the client address. Everything an SDK sends goes through `internal/scrub` before it is queued or stored.
- Frontend: plain CSS, design tokens as CSS variables in `web/src/styles/tokens.css`, CSS modules per component, no UI kit, no chart library. No color, font, radius or line-height literal outside `tokens.css`. Fonts bundled via `@fontsource-variable`. Light theme only. Poll every 60 s. UI copy is English and matches the designs; show only data that exists (no placeholders, no fake numbers). The tool name shows as `[Tool name]`, defined only in `web/src/brand.ts`; Go never reads it.
- Tests first (TDD), next to the code (`*_test.go`, `*.test.ts(x)`). Tests use temp directories, never `/data`, and keep output clean.
- Small conventional commits on branch `build/v1`, each ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push; there is no remote.

## Gotchas

- `vite build` empties `web/dist`; a plugin in `web/vite.config.ts` writes `dist/.keep` back. A binary built without the web build answers dashboard pages with 503 and a hint.
- `go.mod` ignores `./web/node_modules`, `./e2e/node_modules` and `./examples`, so `./...` never descends into npm or Composer packages.
- Without `DEV_BASIC_AUTH` the dashboard guard lets in only requests made directly from this machine: the TCP peer must be loopback and the request must carry no `Forwarded`, `X-Forwarded-For` or `X-Real-IP` header. A reverse proxy on the same host (or a container with `--network host`) connects from 127.0.0.1 too; the forwarding headers it adds are what gives it away, so a proxy that strips them would still get in. Forwarding headers are never trusted to let a request in. Behind Docker port mapping or any proxy, set `DEV_BASIC_AUTH`.
- The runtime image is distroless (no shell, no curl); the Docker `HEALTHCHECK` runs `/monitor healthcheck`.
- Accepted SDK requests are logged at debug (`LOG_LEVEL=debug` to see them); refused ones (401, 413, 429, ...) at info.
- Only the ingest writer should write ingested data, and code inside a `db.write` transaction must use the `*sql.Tx`, never `db.write` itself: the write pool has one connection, so that would wait forever.
- `make e2e` rewrites `e2e/screenshots/*.png` on every run (relative times and data differ). Commit them when the dashboard changed on purpose; otherwise `git checkout e2e/screenshots`. Process output of a run is in `e2e/.logs/`.
- The e2e setup starts each process in its own process group and stops the group, because `php artisan serve` runs `php -S` as a child. It passes the DSN to Laravel as an environment variable with `--no-reload` (without it, artisan serve drops variables that are not in `.env`) and sets `SENTRY_SERVER_NAME=web-1` so screenshots never show this machine's host name.
- The stack trace marks the error line with `aria-current="true"`; the e2e test finds it that way.
- `go test -race ./internal/ingest/` takes about 17 s: `TestIngest_ConcurrentLoad` sends 10 000 envelopes over HTTP. `go test ./internal/sentry/ -run '^$' -fuzz FuzzParseEnvelope -fuzztime 60s` fuzzes parse, scrub and grouping.
