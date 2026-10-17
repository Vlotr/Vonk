# Configuration

Everything is set with environment variables (in Compose: under `environment:`, or in `.env`). Only `SECRET_KEY` and `BASE_URL` are required. The tool checks all of them at the start and stops with one line per problem, so you can fix them in one go; `docker compose logs app` shows the lines.

| Variable | Required | Default | What it does |
| --- | --- | --- | --- |
| `SECRET_KEY` | yes | | Protects sessions, invite and reset links, and API tokens. |
| `BASE_URL` | yes | | The address people and SDKs use to reach the tool. |
| `DATABASE_URL` | no | (SQLite) | Postgres instead of SQLite. |
| `MAIL_URL` | no | (no mail) | SMTP for invites, password resets and notifications. |
| `ADDR` | no | `:8000` | The address and port the tool listens on, inside the container. |
| `DATA_DIR` | no | `/data` | Where the database, backups and source maps live. |
| `LOG_LEVEL` | no | `info` | `debug`, `info`, `warn` or `error`. |
| `TZ` | no | UTC | The clock of the daily, weekly and monthly summaries. |
| `SOURCEMAP_CACHE_MB` | no | `64` | Memory for parsed source maps, in MiB. |
| `DEV_ALLOW_HTTP_WEBHOOKS` | no | `false` | **Development and tests only, never in production.** |

## SECRET_KEY

At least 16 characters; use a long random string: `openssl rand -hex 32`. The example values `change-me` and `verander-mij` are refused.

The database stores only keyed hashes (HMAC-SHA256) of session, invite and reset tokens and of API tokens, keyed from `SECRET_KEY`: a copy of the database alone is not enough to sign in. So **changing it** signs everyone out, makes open invite and reset links stop working, and voids every API token (make new ones in Settings → API tokens). Passwords keep working: they are argon2id hashes that need no key. Keep the secret with your other secrets, and in your backups' notes.

## BASE_URL

The root of the address, such as `https://monitor.example.com` or `http://192.168.1.20:8000`: `http://` or `https://`, a host, an optional port, and nothing after it (no path: the tool cannot run under `/monitor/`; no query, no user name or password). A trailing `/` is dropped.

The tool builds from it the DSNs and PostHog host that Setup and keys shows, the links in mails, and the address inside API tokens. With an `https://` address the session cookie is `__Host-monitor_session` and sent over HTTPS only. **Changing it** changes the DSNs (update your apps), voids the API tokens' address (make new ones), and signs everyone out when it moves between `http://` and `https://`.

## DATABASE_URL

Empty (the default): SQLite at `<DATA_DIR>/monitor.db`, in WAL mode. Nothing else to run.

`postgres://user:password@host:5432/dbname?sslmode=require` (or `postgresql://`): Postgres instead, tested with version 17 (11 or newer should work; only 17 was run). Anything else is refused. Give the tool an empty database that the user in the URL owns: it makes its tables at the first start and updates them at every start after an upgrade, as with SQLite. Notes:

- Without `connect_timeout` in the URL a new connection gives up after 5 seconds. pgx, the driver, also reads the usual `PG*` variables (`PGPASSWORD`, `PGSSLMODE`, `PGCONNECT_TIMEOUT`, ...) for what the URL leaves out.
- The URL is never logged; the start line says `database=postgres`.
- The tool makes no backups of Postgres, also not before an update: it says so in the log at every start. Back it up with `pg_dump` (see [Backup and restore](backup-restore.md#postgres)).
- Run one `monitor serve` per database. Commands such as `docker compose exec app /monitor project create` next to it are fine.
- `DATA_DIR` is still used, for uploaded source maps.
- There is no tool to move data from SQLite to Postgres: a new Postgres database starts empty.

## MAIL_URL

`smtp://user:password@mail.example.com:587` (STARTTLS, required except to a server on `localhost`) or `smtps://user:password@mail.example.com:465` (TLS from the start). The port defaults to 587 and 465. Add `?from=alerts@example.com` for the sender; the default is `monitor@<the host of BASE_URL>`. Percent-encode special characters in the user name and password (`@` is `%40`). Other options are refused, to catch typos.

With mail on: invites and admin-made reset links are also mailed, "Forgot password" on the sign-in page works, and notifications go by mail to everyone who may see the project (and admin alerts to admins). Without it: invite and reset links are shown to the admin to pass on, and notifications go to Slack only. An admin who forgot their password gets a link on the server: `docker compose exec app /monitor admin reset-password --email you@example.com`.

## ADDR and DATA_DIR

`ADDR` is the listen address inside the container (`:8000`); change the published port in Compose instead (`ports: ["9000:8000"]`). `monitor healthcheck` (the image's health check) reads it too.

`DATA_DIR` is `/data` in the image, a volume. Keep it on a disk with room: the tool alerts admins below 10 % or 1 GiB free, and `/health` fails below 1 % or 256 MiB. What is in it:

| Path | What |
| --- | --- |
| `monitor.db` (with `-wal` and `-shm`) | the SQLite database (not with Postgres) |
| `backups/pre-migrate-<version>-<time>.db` | made before a database update; the five newest are kept |
| `backups/pre-restore-<version>-<time>.db` | the database a restore replaced; the five newest are kept |
| `backups/orphaned-<time>.db-wal` | a leftover write-ahead log a restore moved aside (rare: a start that stopped halfway) |
| `restore-pending.db` | a checked backup that becomes the database at the next start ([restore](backup-restore.md)) |
| `restore-rejected-<time>.db` | a pending backup the start refused; the server log says why |
| `sourcemaps/` | uploaded source maps and minified files, and chunks of uploads under way |
| `.tmp-*` | a backup download or upload under way; the start removes leftovers |

The container runs as user 65532 (`nonroot`). A named volume (as in the compose files) gets the right owner by itself. With a folder of the host (`./data:/data`), make it writable for that user first: `sudo chown 65532:65532 data`.

## LOG_LEVEL

`info` logs one line per dashboard request, refused SDK requests (401, 413, 429, ...), jobs and problems. `debug` adds every accepted SDK request and `/health` call; use it to see whether an SDK reaches the tool. Logs go to standard error (`docker compose logs app`). Request lines carry the method, path, status, size and duration, never the query string or the client's address, and the path of the user activity page has the user id masked (`/projects/1/users/:userId`).

## TZ

An IANA zone such as `Europe/Amsterdam`. Summaries go out at 08:00 on this clock (daily; weekly on Mondays; monthly on the 1st). An unknown zone stops the start. Without it, UTC. The dashboard shows times in each viewer's own browser zone whatever this is.

## SOURCEMAP_CACHE_MB

Memory (MiB, a whole number from 1 to 65536) for parsed source maps and minified files. One source map is used up to a quarter of it: 16 MiB with the default of 64. A larger map is stored but not used, and the log says so once ("file too large to symbolicate with"); raise this setting for it.

## Development and tests only

None of these belong in production.

- `DEV_ALLOW_HTTP_WEBHOOKS=true` lets Slack webhooks use plain `http://`, for a fake Slack on your machine. The tool logs a warning at every start while it is on. Webhooks must otherwise be `https://`, so messages are encrypted on their way.
- `DEV_BASIC_AUTH` is no longer read (accounts replaced it); the tool warns when it is set.
- Read by the tests and the Makefile, not by the tool: `TEST_DATABASE_URL` (Go tests against Postgres), `E2E_DATABASE_URL` (the end-to-end tests against Postgres), `POSTGRES_IMAGE` (`make test-postgres`), and `DEV_DATA_DIR`, `DEV_SECRET_KEY`, `DEV_BASE_URL` (`make dev`, whose fixed secret is for your machine only).

## Limits that are not settings

- Each project accepts 1000 events a minute from Sentry SDKs and 1000 from PostHog SDKs; more get 429 with `Retry-After` (the SDKs wait and send again).
- Requests are at most 20 MiB (before and after decompression); one event at most 1 MiB.
- Up to 10 000 events (or 256 MiB) wait to be written; when that is full, ingest answers 429 with `Retry-After: 5`.
- Source map bundles up to 100 MiB.
