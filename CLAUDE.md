# CLAUDE.md

A self-hosted monitoring tool: one Go binary that pretends to be Sentry (errors) and PostHog (page views, events), with a React dashboard built into the binary, shipped as one small container. The spec is `concept-monitoring-tool.md` (Dutch); the designs are in `Monitoring dashboard-png/`. Every judgment call is logged in `docs/decisions.md`.

## Commands

| Command | What it does |
| --- | --- |
| `make dev` | Go server on `:8000` plus the Vite dev server (hot reload) on `:5173`, which forwards `/ui/api` and `/health` to Go. Data in `./data`. Ctrl-C stops both. |
| `make test` | `go vet ./...`, `go test -race ./...`, web typecheck and Vitest. Run before every commit. |
| `make e2e` | Playwright end-to-end tests (placeholder until Task 3). |
| `make build` | Web build into `web/dist`, then `bin/monitor` with the dashboard embedded. |
| `make docker` | Image `monitor:dev` for this machine's platform. |
| `make clean` | Remove `bin/` and the web build. |

Focused runs while iterating: `go test -race ./internal/store/ -run TestCreateProject`, `cd web && npm test`.

Run the binary by hand:

```sh
SECRET_KEY=$(openssl rand -hex 32) BASE_URL=http://localhost:8000 DATA_DIR=./data bin/monitor serve
SECRET_KEY=... BASE_URL=http://localhost:8000 DATA_DIR=./data bin/monitor project create --name Webshop
ADDR=:8000 bin/monitor healthcheck   # exit 0 when /health answers 200
```

Container: `docker run -p 8000:8000 -e SECRET_KEY=... -e BASE_URL=http://localhost:8000 -e DEV_BASIC_AUTH=dev:some-password -v monitor-data:/data monitor:dev`. The image has no shell; run CLI commands with `docker exec <container> /monitor project create --name Webshop`.

## Capturing real SDK traffic

`tools/capture` is a tiny recorder: point any SDK or CLI at it and it writes each request to `NNNN-METHOD-path.json` (method, path, query, host, headers) and `NNNN-METHOD-path.body` (the body exactly as sent, still compressed). It answers `200 {}` (`-response` changes the body) and allows CORS from any origin, so browser SDKs work too. Use it to make test fixtures from real clients instead of hand-written ones.

```sh
go run ./tools/capture -addr 127.0.0.1:19999 -out /tmp/captured
# then use a DSN like http://5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c@127.0.0.1:19999/1 (any 32-hex key works)
```

The Sentry fixtures in `internal/sentry/testdata/` were made with the recipes in `internal/sentry/testdata/capture/` (SDK versions in `docs/decisions.md`). To redo them after an SDK update, copy a recipe to a temp directory and:

- PHP: `composer install`, then `SENTRY_DSN=<dsn> php -S 127.0.0.1:18091 public/index.php`, and `curl -X POST 'http://localhost:18091/checkout?coupon=SPRING&token=tok_abc123' -H 'Cookie: laravel_session=eyJpdiI6IjEyMyJ9; XSRF-TOKEN=abc123' -H 'Authorization: Bearer sk_live_abc123' -H 'X-Forwarded-For: 203.0.113.9' -H 'X-Real-IP: 203.0.113.9' --data 'email=jan@example.com&password=hunter2&card_number=4111111111111111'`. The body is `php-exception.envelope.gz`, the `.json` is `php-exception.headers.json`.
- Browser: `npm install && npx playwright install chromium && npm run build && SENTRY_DSN=<dsn> npm run capture`. The request whose body holds `"exception"` is `browser-typeerror.envelope`; the first session envelope is `browser-session.envelope`.

Then update the ids and values the tests expect (they name them).

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
internal/server/        router, dashboard guard, request logging, panic recovery, /health
internal/web/           serves the embedded SPA with index.html fallback
web/                    Vite + React + TypeScript dashboard
  embed.go              Go package monitor/web: embeds web/dist into the binary
  dist/.keep            committed placeholder so `go build` works before a web build
  src/brand.ts          the tool name, the only place it lives
tools/capture/          recorder for real SDK traffic (see "Capturing real SDK traffic")
docs/decisions.md       one line per judgment call
```

Packages still to come (see the plan): `internal/posthog`, `internal/ingest`, `internal/issues`, `internal/api`, `internal/auth`, `internal/mail`, `internal/alerts`, `internal/jobs`, `internal/routes`, `internal/bots`; `examples/`, `e2e/`.

## Conventions

- Simple, idiomatic Go; stdlib first (`net/http` routing, `log/slog`, `embed`, `database/sql`). The team is new to Go: clarity over cleverness. New dependencies need a line in `docs/decisions.md`. Approved: `modernc.org/sqlite`.
- Subcommands: `serve` (default), `healthcheck`, `project create`, later `admin reset-password`, `restore`, `delete-user-data`. One file per subcommand in `cmd/monitor/`. `run(args, getenv, stdout, stderr) int` keeps them testable.
- Times in the DB are unix milliseconds (`BIGINT`); JSON uses RFC 3339. JSON payloads are `TEXT`. Booleans are `BOOLEAN` with `TRUE`/`FALSE`.
- SQL stays in the subset SQLite and Postgres share: use `INSERT ... RETURNING id`, not `LastInsertId`. Placeholders are `?` for now (Task 11 adds a dialect helper).
- Migrations are append-only: never edit a committed file, add `NNNN_name.sql`. Every SQLite migration needs a Postgres twin from Task 11 on. Tracked in `schema_migrations`.
- All writes go through `db.write` (one connection, `BEGIN IMMEDIATE`); queries use `db.read` (read-only pool).
- Routing: public routes (SDK ingest, `/health`) go on the public mux in `internal/server`; everything else falls through the dashboard guard. Dashboard API under `/ui/api/`, camelCase JSON, errors as `{"error":"..."}`. Missing access and missing objects both return 404.
- Never store or log IP addresses. Request logs leave out the query string and the client address.
- Frontend: plain CSS, design tokens as CSS variables in `web/src/styles/tokens.css` (from Task 3), CSS modules per component, no UI kit, no chart library. Fonts bundled via `@fontsource`. Light theme only. Poll every 60 s. UI copy is English and matches the designs. The tool name shows as `[Tool name]`, defined only in `web/src/brand.ts`; Go never reads it.
- Tests first (TDD), next to the code (`*_test.go`, `*.test.ts(x)`). Tests use temp directories, never `/data`, and keep output clean.
- Small conventional commits on branch `build/v1`, each ending with `Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`. Never push; there is no remote.

## Gotchas

- `vite build` empties `web/dist`; a plugin in `web/vite.config.ts` writes `dist/.keep` back. A binary built without the web build answers dashboard pages with 503 and a hint.
- `go.mod` has `ignore ./web/node_modules` so `./...` never descends into npm packages.
- Without `DEV_BASIC_AUTH` the dashboard guard lets in only requests made directly from this machine: the TCP peer must be loopback and the request must carry no `Forwarded`, `X-Forwarded-For` or `X-Real-IP` header. A reverse proxy on the same host (or a container with `--network host`) connects from 127.0.0.1 too; the forwarding headers it adds are what gives it away, so a proxy that strips them would still get in. Forwarding headers are never trusted to let a request in. Behind Docker port mapping or any proxy, set `DEV_BASIC_AUTH`.
- The runtime image is distroless (no shell, no curl); the Docker `HEALTHCHECK` runs `/monitor healthcheck`.
