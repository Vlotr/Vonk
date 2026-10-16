# Monitoring tool (`monitor`)

A light, self-hosted tool for errors and page statistics. Your apps keep their existing SDKs: the tool pretends to be Sentry (errors from Laravel and the browser) and PostHog (page views). It runs as one Go binary with the dashboard built in, in one small container with all data on one volume. The name is not decided yet; the dashboard shows `[Tool name]`.

Status: early development (phase 2 of the plan). The first visitor creates the admin account; others join with an invite link, as admins or as members who see only the projects they are given. Projects are made in Settings, which shows the DSN and the setup steps. Point a Sentry SDK at that DSN: errors are stored, grouped into issues, and listed in the dashboard with their stack trace. Issues can be resolved, ignored and reopened; a resolved issue opens again when the error comes back. The All projects page shows which projects have new errors. Each project's Alerts page sends new issues, returning issues, error spikes and a daily, weekly or monthly summary by mail and to Slack; admins hear about problems of the tool itself, such as a disk that runs full. Old data is deleted after each project's retention, the database is backed up before every update, and Settings → Status and `/health` show how the tool is doing. Page views come next.

Tested with these SDKs, sending real errors to the tool (see `examples/` and `make e2e`):

| SDK | Version | Tested with |
| --- | --- | --- |
| `sentry/sentry-laravel` | 4.29.0 (on `sentry/sentry` 4.34.0) | Laravel 13.37, PHP 8.4 |
| `sentry/sentry` (plain PHP) | 4.34.0 | PHP 8.4 |
| `@sentry/browser` | 11.8.0 | Chromium (Playwright 1.64.1) |

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

Send it some errors with the examples: `examples/laravel/` (sentry-laravel) and `examples/browser/` (`@sentry/browser`). Each has a README with the steps.

Run the tests:

```sh
make test   # Go and web unit tests
make e2e    # the real binary, both examples and the dashboard in Chromium (needs PHP and Composer)
```

`make e2e` saves `e2e/screenshots/phase1-issues.png`, `phase1-issue-detail.png`, `phase2-all-projects.png`, `phase2-settings.png`, `phase2-alerts.png` and `phase2-status.png`, the dashboard as it looked during the run.

## Run the container

With Docker Compose and `compose.yaml` (one container, all data on one volume):

```sh
make docker                                  # builds the image monitor:dev
export SECRET_KEY="$(openssl rand -hex 32)"  # keep it: changing it signs everyone out
docker compose up -d
curl http://localhost:8000/health
```

The compose file sets `TZ=Europe/Amsterdam`, so summaries arrive at 08:00 Amsterdam time; set `BASE_URL` (default `http://localhost:8000`) to the address people and SDKs use, and uncomment `MAIL_URL` to send mail. Data lives in the volume `data` and survives `docker compose restart` and image updates; before a database update the tool copies the database to `/data/backups/`.

Or with plain Docker:

```sh
make docker
docker run -p 8000:8000 \
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
| `DATABASE_URL` | no | | Postgres instead of SQLite (coming later). |
| `MAIL_URL` | no | | SMTP for invites, reset links and notifications: `smtp://user:pass@host:587` (STARTTLS) or `smtps://user:pass@host:465`, optionally `?from=alerts@example.com` (default `monitor@<BASE_URL host>`). Without it, notifications go to Slack only. |
| `ADDR` | no | `:8000` | Listen address. |
| `DATA_DIR` | no | `/data` | Where the SQLite database and its backups (`backups/`) live. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error`. |
| `TZ` | no | UTC | Time zone of the schedules: summaries go out at 08:00 there, for example `Europe/Amsterdam`. |
| `DEV_ALLOW_HTTP_WEBHOOKS` | no | `false` | For development and tests only, never in production: `true` lets Slack webhooks use plain http (a fake Slack on your machine). Webhooks must otherwise be https. |

## Alerts and health

- Per project, on its Alerts page (admins change it; members who see the project read it): new issue, returning issue, spike (at least 5 times the usual hourly number of errors by default, and 10 or more; at most once in 6 hours) and a summary (daily, weekly on Mondays or monthly on the 1st, at 08:00 in `TZ`), each by email and to Slack. Mail goes to everyone who may see the project; Slack to the project's incoming webhook, which must start with `https://` ("Send test message" checks it). At most 30 notifications per project and channel an hour; then one message says how many more were held back.
- For admins: a mail (and a message on the admins' Slack webhook, set in Settings → Status) when the disk under `DATA_DIR` has less than 10 % or 1 GiB free, when events could not be stored, or when events were turned away because the tool could not keep up; at most once in 12 hours each.
- `GET /health` answers 200 with status `ok`, or `degraded` naming what fails while the tool still works (a disk under 10 % or 1 GiB free, or one that cannot be read); 503 with status `error` only when it cannot work (the database fails, or the disk has under 1 % or 256 MiB free). The answer has the version, the free disk space and the queue depth; the Docker health check uses it. Settings → Status shows the same and more: uptime, database size, events in the last hour, the last backup and the recent admin alerts.
- Retention: once a day each project's data older than its "Keep data for" is deleted; "Indefinitely" keeps everything.
- Updates: when a new version changes the database, it first copies it to `DATA_DIR/backups/pre-migrate-<version>-<time>.db` (the five newest are kept) and refuses to start if that copy fails.

The tool speaks plain HTTP. Put a reverse proxy such as Caddy in front of it for HTTPS; with an `https://` `BASE_URL` the session cookie is sent over HTTPS only.

See `CLAUDE.md` for the project layout and conventions, and `docs/decisions.md` for the choices made along the way.
