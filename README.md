# Monitoring tool (`monitor`)

A light, self-hosted tool for errors and page statistics. Your apps keep their existing SDKs: the tool pretends to be Sentry (errors from Laravel and the browser) and PostHog (page views). It runs as one Go binary with the dashboard built in, in one small container with all data on one volume. The name is not decided yet; the dashboard shows `[Tool name]`.

Status: early development (phase 4 of the plan). The first visitor creates the admin account; others join with an invite link, as admins or as members who see only the projects they are given. Projects are made in Settings, which shows the DSN and the setup steps. Point a Sentry SDK at that DSN: errors are stored, grouped into issues, and listed in the dashboard with their stack trace. Issues can be resolved, ignored and reopened; a resolved issue opens again when the error comes back. The All projects page shows which projects have new errors. Each project's Alerts page sends new issues, returning issues, error spikes and a daily, weekly or monthly summary by mail and to Slack; admins hear about problems of the tool itself, such as a disk that runs full. Old data is deleted after each project's retention, the database is backed up before every update, and Settings → Status and `/health` show how the tool is doing. Phase 3 adds page views: point posthog-js at the tool and each project's Pages screen shows its pages with views, visitors, the average time on a page and the errors on it (which lead to that page's issues), over 24 hours, 7 or 30 days, with bots counted apart; All projects shows the page views and time of the last 24 hours. Phase 4 starts with the user journey: when both SDKs get the same user id, an issue lists the pages its latest error's user saw in the half hour before it, and links to everything that user did; issues show the release they were first seen in (and the one they came back in), and the Events screen counts the app's own PostHog events (`posthog.capture('checkout_completed')`). Phase 4 ends with source maps: a production build uploads its maps with `sentry-cli` or `@sentry/vite-plugin` and an API token from Settings, and its minified errors then show the original files, functions and lines.

Tested with these SDKs, sending real errors to the tool (see `examples/` and `make e2e`):

| SDK | Version | Tested with |
| --- | --- | --- |
| `sentry/sentry-laravel` | 4.29.0 (on `sentry/sentry` 4.34.0) | Laravel 13.37, PHP 8.4 |
| `sentry/sentry` (plain PHP) | 4.34.0 | PHP 8.4 |
| `@sentry/browser` | 11.8.0 | Chromium (Playwright 1.64.1) |
| `posthog-js` | 1.438.12 | Chromium (Playwright 1.64.1) |
| `@sentry/cli` (source map upload) | 3.8.0 | Vite 8.3 builds |
| `@sentry/vite-plugin` (source map upload) | 11.8.0 (uploads with the `sentry` CLI 0.45.0) | Vite 8.3 |

## Development quick start

You need Go 1.27, Node 24 and npm. Docker is optional.

```sh
make dev
```

This starts the Go server on http://localhost:8000 and the dashboard with hot reload on http://localhost:5173 (open that one). Data goes to `./data`. The first time, the dashboard asks you to create the first account, an admin. Add projects in Settings → Projects; Setup and keys shows the DSN and the steps for Laravel and the browser.

A project can also be made on the command line (in a second terminal):

```sh
make build
SECRET_KEY=dev-only-secret-not-for-production BASE_URL=http://localhost:8000 DATA_DIR=./data \
  bin/monitor project create --name Webshop
```

Send it some errors with the examples: `examples/laravel/` (sentry-laravel) and `examples/browser/` (`@sentry/browser`), and page views with `examples/posthog/` (posthog-js and `@sentry/browser` with the same user id). Each has a README with the steps.

Run the tests:

```sh
make test   # Go and web unit tests
make e2e    # the real binary, the examples and the dashboard in Chromium (needs PHP and Composer)
```

`make e2e` saves `e2e/screenshots/phase1-issues.png`, `phase1-issue-detail.png`, `phase2-all-projects.png`, `phase2-settings.png`, `phase2-alerts.png`, `phase2-status.png`, `phase3-pages.png`, `phase4-journey.png`, `phase4-activity.png`, `phase4-events.png`, `phase4-sourcemaps.png` and `phase4-api-tokens.png`, the dashboard as it looked during the run.

## Run the container

With Docker Compose and `compose.yaml` (one container, all data on one volume):

```sh
make docker                                  # builds the image monitor:dev
export SECRET_KEY="$(openssl rand -hex 32)"  # keep it: changing it signs everyone out
docker compose up -d
curl http://localhost:8000/health
```

The compose file sets `TZ=Europe/Amsterdam`, so summaries arrive at 08:00 Amsterdam time; set `BASE_URL` (default `http://localhost:8000`) to the address people and SDKs use, and uncomment `MAIL_URL` to send mail. Data lives in the volume `data` and survives `docker compose restart` and image updates; before a database update the tool copies the database to `/data/backups/`. It also sets `stop_grace_period: 30s`: on a stop the tool lets running requests finish (up to 15 s), writes the events it has already accepted and gives notifications 10 s, and Docker's default of 10 s would cut that short.

Or with plain Docker (`--stop-timeout 30` for the same reason):

```sh
make docker
docker run -p 8000:8000 --stop-timeout 30 \
  -e SECRET_KEY="$(openssl rand -hex 32)" \
  -e BASE_URL=http://localhost:8000 \
  -v monitor-data:/data \
  monitor:dev
curl http://localhost:8000/health
```

Then open http://localhost:8000 and create the first account right away: until it exists, anyone who reaches the tool can create it. That account is an admin; everyone else joins with an invite from Settings → Users. The SDK endpoints and `/health` need no account.

Forgot your password? With `MAIL_URL` set, the login page mails a reset link. Without mail, an admin makes a reset link for a member in Settings → Users, and an admin gets one on the server: `docker exec <container> /monitor admin reset-password --email you@example.com`.

## Settings

| Variable | Required | Default | Purpose |
| --- | --- | --- | --- |
| `SECRET_KEY` | yes | | Protects sessions and keys. At least 16 characters. |
| `BASE_URL` | yes | | Public address of the tool, the root of a host, such as `https://monitor.example.com`. |
| `DATABASE_URL` | no | | Postgres instead of SQLite, such as `postgres://monitor:secret@db.example.com:5432/monitor?sslmode=require` (or `postgresql://`). See "Postgres" below. |
| `MAIL_URL` | no | | SMTP for invites, reset links and notifications: `smtp://user:pass@host:587` (STARTTLS) or `smtps://user:pass@host:465`, optionally `?from=alerts@example.com` (default `monitor@<BASE_URL host>`). Without it, notifications go to Slack only. |
| `ADDR` | no | `:8000` | Listen address. |
| `DATA_DIR` | no | `/data` | Where the SQLite database, its backups (`backups/`) and uploaded source maps (`sourcemaps/`) live. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error`. |
| `TZ` | no | UTC | Time zone of the schedules: summaries go out at 08:00 there, for example `Europe/Amsterdam`. |
| `SOURCEMAP_CACHE_MB` | no | `64` | Memory in MiB for parsed source maps. One source map is used up to a quarter of it (16 MiB with the default); raise it for apps with larger maps. |
| `DEV_ALLOW_HTTP_WEBHOOKS` | no | `false` | For development and tests only, never in production: `true` lets Slack webhooks use plain http (a fake Slack on your machine). Webhooks must otherwise be https. |

## Postgres

SQLite in `DATA_DIR` is the default and needs nothing else. For a large installation, point `DATABASE_URL` at an empty Postgres database (tested with version 17) that the user in the URL owns: the tool makes its tables at the first start and updates them at every start after an upgrade, as with SQLite. Run one `monitor serve` per database; commands such as `docker exec <container> /monitor project create` work next to it. While Postgres restarts or fails over (up to 2 minutes), the tool keeps the events it accepted and asks the SDKs to wait (429 with `Retry-After`); `/health` reports the database as failing meanwhile.

The tool makes no backups of a Postgres database, also not before an update: it says so in the log at every start and on Settings → Status. Back it up with your database host's backups or with `pg_dump`, also before upgrading the tool:

```sh
pg_dump --format=custom --file=monitor.dump "$DATABASE_URL"
pg_restore --clean --dbname="$DATABASE_URL" monitor.dump   # to put it back
```

`DATA_DIR` is still needed for uploaded source maps. There is no tool to move data from SQLite to Postgres; a new Postgres database starts empty.

## Page views (PostHog)

Turn on page views for the project (Settings → Projects → Add page views), then point posthog-js at the tool: `api_host` is your `BASE_URL`, the token is the project's PostHog token (Setup and keys shows both and this snippet). Without cookies:

```js
posthog.init('<project token>', {
  api_host: 'https://monitor.example.com',
  persistence: 'memory',
  capture_pageview: 'history_change',
  capture_pageleave: true,
  disable_session_recording: true,
})
```

The tool answers the endpoints posthog-js calls (`/i/v0/e/`, `/e/`, `/batch/`, `/flags/`, `/decide/`, `/array/<token>/config.js`, …) with no feature flags and recording, surveys, autocapture and heatmaps off. Time on a page runs from its page view to its page leave or the next page view in the same tab; a visit that never closed counts as a view but not in the average time. Known bots (by User-Agent) are counted apart. IP addresses are not stored. Ids, UUIDs, hashes and tokens in paths are grouped into one route automatically (`/orders/1234` counts as `/orders/:id`), and route patterns per project (Edit project, one per line, such as `/products/:slug`) group the rest, for data that comes in after they are saved; Edit project previews recent paths with their routes. UUIDs, hashes and tokens in a page's address are masked before anything is stored, in page view paths and in an error's URL, Referer header and breadcrumbs (a password reset link keeps no token); numbers and words stay.

With `persistence: 'memory'` posthog-js warns in the browser console that it mints a new distinct ID on every page load: visitors who are not signed in count as new on each page load. Give both SDKs your user id after sign-in (`posthog.identify(String(user.id))` and `Sentry.setUser({ id: String(user.id) })`; Setup and keys has the Laravel side too) so signed-in visitors count once and their errors and page views match.

## User journey, releases and custom events

- Before this error: give both SDKs the same user id (see above). The issue page then lists the page views of the latest event's user in the 30 minutes before it (at most 10, oldest first, with the time on each; bots left out) and the error as the last row. Without a user id on the latest event the card is not shown. "See all activity of this user" opens that user's page views, errors and custom events, the newest first.
- Releases: an issue's Release is the `release` of the event that created it; when a resolved issue comes back in another release, the issue page says "Came back in …". The release links to the issues with an event in that release.
- Custom events: every PostHog event whose name does not start with `$` is stored with its page, its user and the properties the app sent (scrubbed like page views; the SDK's own `$` properties are left out). The Events screen shows how often each happened and by how many users over 24 hours, 7 or 30 days, with a chart per event, from hourly summaries rebuilt every minute; bots are counted apart. It is in the sidebar for projects that track page views and for projects that send events from a server (posthog-node or another PostHog SDK with the project's token, which Setup and keys shows).

## Source maps

Errors from a minified production build point at `assets/index-D8xk2Lp1.js:1`. Upload the build's source maps and they show your own files, functions and lines (`src/checkout.ts`, `showPrice`, line 9, with the code around it). The tool speaks the part of Sentry's API that `sentry-cli` and `@sentry/vite-plugin` use, so they work as they do with Sentry:

1. Settings → API tokens → New token: a name and the projects it may upload to. The token is shown once (only a hash is stored), with the setup below filled in. Give it to your build as a secret.
2. With `@sentry/vite-plugin` (`npm install --save-dev @sentry/vite-plugin`), in `vite.config.ts`:

   ```js
   import { sentryVitePlugin } from '@sentry/vite-plugin'
   import { defineConfig } from 'vite'

   export default defineConfig({
     build: { sourcemap: 'hidden' },
     plugins: [
       sentryVitePlugin({
         url: 'https://monitor.example.com', // your BASE_URL
         org: 'monitor',                     // any word: the tool has no organizations
         project: 'webshop',                 // the project's slug
         authToken: process.env.SENTRY_AUTH_TOKEN,
         sourcemaps: { filesToDeleteAfterUpload: ['./dist/**/*.map'] },
         telemetry: false,
       }),
     ],
   })
   ```

   Build with `SENTRY_AUTH_TOKEN=<token>` and `SENTRY_CLI_NO_TELEMETRY=1` in the environment (the plugin's Sentry CLI otherwise sends its own error reports to sentry.io).
3. Or with `sentry-cli`, after the build: `npx @sentry/cli sourcemaps inject ./dist` and `npx @sentry/cli sourcemaps upload --project webshop ./dist`, with the token in `SENTRY_AUTH_TOKEN`. The token carries the tool's address, so no `--url` or `--org` is needed.

Maps are matched by the debug id the tools put in each file (and the browser SDK reports with each error), or else by release and file URL. Errors that arrive after the upload are stored with the original frames, so they group by your own code and stay one issue across builds; errors stored before the upload show the original code on their issue page. Tokens name the tool's `BASE_URL`: the tools must use that address, and after changing `BASE_URL` make new tokens. A token uploads only to its projects (403 for others); revoking it stops it at once. Bundles may be up to 100 MiB. One source map is used up to a quarter of `SOURCEMAP_CACHE_MB`, the memory the tool keeps parsed maps in: 16 MiB with the default of 64. A larger one is stored but not used, and the server log says so ("file too large to symbolicate with"); raise `SOURCEMAP_CACHE_MB` for it. The same build uploaded again for another release is stored for that release too. Source maps are kept for the project's retention, counted from their upload.

## Alerts and health

- Per project, on its Alerts page (admins change it; members who see the project read it): new issue, returning issue (with the release it came back in), spike (at least 5 times the usual hourly number of errors by default, and 10 or more; at most once in 6 hours) and a summary (daily, weekly on Mondays or monthly on the 1st, at 08:00 in `TZ`), each by email and to Slack. Mail goes to everyone who may see the project; Slack to the project's incoming webhook, which must start with `https://` ("Send test message" checks it). At most 30 notifications per project and channel an hour; then one message says how many more were held back.
- For admins: a mail (and a message on the admins' Slack webhook, set in Settings → Status) when the disk under `DATA_DIR` has less than 10 % or 1 GiB free, when events could not be stored, or when events were turned away because the tool could not keep up; at most once in 12 hours each.
- `GET /health` answers 200 with status `ok`, or `degraded` naming what fails while the tool still works (a disk under 10 % or 1 GiB free, or one that cannot be read); 503 with status `error` only when it cannot work (the database fails, or the disk has under 1 % or 256 MiB free). The answer has the version, the free disk space and the queue depth; the Docker health check uses it. Settings → Status shows the same and more: uptime, database size, events in the last hour, the last backup (or, on Postgres, the `pg_dump` command) and the recent admin alerts.
- Retention: once a day each project's data older than its "Keep data for" is deleted (errors, page views, custom events and their hourly statistics, and source maps uploaded before it); "Indefinitely" keeps everything.
- Updates: when a new version changes the database, it first copies it to `DATA_DIR/backups/pre-migrate-<version>-<time>.db` (the five newest are kept) and refuses to start if that copy fails. On Postgres it warns instead: make a `pg_dump` before you upgrade.

The tool speaks plain HTTP. Put a reverse proxy such as Caddy in front of it for HTTPS; with an `https://` `BASE_URL` the session cookie is sent over HTTPS only.

See `CLAUDE.md` for the project layout and conventions, and `docs/decisions.md` for the choices made along the way.
