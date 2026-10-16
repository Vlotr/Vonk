# Monitoring tool (`monitor`)

A light, self-hosted tool for errors and page statistics. Your apps keep their existing SDKs: the tool pretends to be Sentry (errors from Laravel and the browser) and PostHog (page views). It runs as one Go binary with the dashboard built in, in one small container with all data on one volume. The name is not decided yet; the dashboard shows `[Tool name]`.

Status: early development (phase 2 of the plan). The first visitor creates the admin account; others join with an invite link, as admins or as members who see only the projects they are given. Projects are made in Settings, which shows the DSN and the setup steps. Point a Sentry SDK at that DSN: errors are stored, grouped into issues, and listed in the dashboard with their stack trace. Issues can be resolved, ignored and reopened; a resolved issue opens again when the error comes back. The All projects page shows which projects have new errors. Alerts and page views come next.

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

`make e2e` saves `e2e/screenshots/phase1-issues.png`, `phase1-issue-detail.png`, `phase2-all-projects.png` and `phase2-settings.png`, the dashboard as it looked during the run.

## Run the container

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
| `MAIL_URL` | no | | SMTP for invite and reset mails (and later notifications): `smtp://user:pass@host:587` (STARTTLS) or `smtps://user:pass@host:465`, optionally `?from=alerts@example.com` (default `monitor@<BASE_URL host>`). |
| `ADDR` | no | `:8000` | Listen address. |
| `DATA_DIR` | no | `/data` | Where the SQLite database lives. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error`. |

The tool speaks plain HTTP. Put a reverse proxy such as Caddy in front of it for HTTPS; with an `https://` `BASE_URL` the session cookie is sent over HTTPS only.

See `CLAUDE.md` for the project layout and conventions, and `docs/decisions.md` for the choices made along the way.
