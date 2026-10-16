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
- Dashboard guard without `DEV_BASIC_AUTH`: only loopback TCP peers (127.0.0.0/8, ::1, IPv4-mapped loopback) get in; `X-Forwarded-For` is ignored because anyone can send it. Behind Docker port mapping the peer is the bridge gateway, so the container needs `DEV_BASIC_AUTH`; the 403 message says so.
- With `DEV_BASIC_AUTH` set, every client needs the credentials, loopback included; comparison is constant-time on SHA-256 digests.
- The guard is fail-closed: public routes (`/health`, `/api/...`, later PostHog) are registered on a public mux and everything else falls through the guard to the dashboard mux, so a new `/ui/api` route cannot skip it by accident.
- The SPA is registered on `/` instead of `GET /` because Go's mux refuses that pattern next to a method-less `/ui/api/`; the SPA handler answers other methods with 405 itself.
- `/api/` has a public JSON 404 catch-all so SDK traffic never receives HTML, before and after Task 2 adds the envelope route.
- SPA serving: `/assets/*` gets `Cache-Control: public, max-age=31536000, immutable` (Vite hashes names), `index.html` gets `no-cache`, missing paths under `/assets/` or with an extension get 404, dotfiles such as `.keep` are never served, and a binary built without the web build answers pages with 503 and how to build.
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
