# Troubleshooting

First look at the log and the health check:

```sh
docker compose logs --tail 50 app
curl http://localhost:8000/health
```

Settings → Status (admins) shows the same health and more: version, uptime, database size, free disk, events stored in the last hour, events waiting to be written, the last backup, and recent admin alerts. `LOG_LEVEL=debug` logs every SDK request the tool accepts, not only the refused ones.

## The tool does not start

The log names every setting that is wrong, one line each, for example `SECRET_KEY is required: ...`, `BASE_URL "https://monitor.example.com/app" must be the root of a host ...` or `TZ "Amsterdam" is not a time zone ...`. See [Configuration](configuration.md). With Compose, `set SECRET_KEY in .env` means the `.env` file is missing or not next to `compose.yaml`.

`permission denied` under `/data`: the tool runs as user 65532. A named volume (as in the compose files) works by itself; a folder of the host (`./data:/data`) must be writable for that user: `sudo chown -R 65532:65532 data`.

`a newer version of the tool updated this database`: you started an older version on a database a newer one already updated. Start the newer version again, or go back as [Updating](updating.md#going-back-to-an-older-version) describes.

`the backup before the database update failed`: a new version updates the database at its start and copies it first; without room on the disk, or without write access to `DATA_DIR/backups`, it stops before changing anything. Make room and start again.

## /health and the status page

| Answer | Meaning |
| --- | --- |
| 200, `"status":"ok"` | All is well. |
| 200, `"status":"degraded"`, `"failing":["disk"]` | The disk under `DATA_DIR` has less than 10 % or 1 GiB free (or cannot be read). The tool works; admins get an alert. Make room, or shorten a project's retention. |
| 503, `"status":"error"` | The tool cannot work: the database does not answer, or the disk has less than 1 % or 256 MiB free. Docker marks the container unhealthy. |

On a laptop whose disk is nearly full, `degraded` is expected.

## Errors do not arrive

- Is the DSN's address reachable **from the app**? `localhost` in a DSN means the app's own machine or container. An app in another container needs `BASE_URL` set to an address it can reach (the server's name or IP), and its DSN taken from Setup and keys after that.
- Does the app send at all? `php artisan sentry:test` (Laravel) says whether the SDK sent the test. Then look in the tool's log: a refused request is logged with its status; with `LOG_LEVEL=debug` accepted ones too.
- From a browser: an HTTPS page cannot send to a plain `http://` tool (mixed content), and a Content Security Policy on your site must allow the tool's address in `connect-src`. Adblockers may block the requests: see [your own domain or path](https-caddy.md#against-adblockers-your-own-domain-or-path).
- The page refreshes once a minute; reload it to see new errors at once.

What the SDK endpoints answer:

| Status | Meaning |
| --- | --- |
| 200 | Accepted. Events are written within a second. |
| 400 | The request is not one the tool understands (the reason is in the body, and for Sentry SDKs in the `X-Sentry-Error` header). |
| 401 | Unknown key or project id (a DSN or PostHog token of another installation, or rotated keys). |
| 413 | Over 20 MiB, before or after decompression. |
| 429 | Too much at once: the project's events-per-minute limit (1000 unless changed in Edit project; `Retry-After`: until the next minute), or 10 000 events waiting to be written (`Retry-After: 5`). The SDKs wait and send again. |
| 503 | The database cannot be reached (Postgres restarting, for example), with `Retry-After: 60`: the tool cannot look up the project's key. |

During a database outage the events already accepted are not lost at once: the writer keeps the batch it is writing (up to 500 events, of up to 1 MiB each, on top of the queue's limits) and tries again for up to 2 minutes from the start of that write. After that the batch is dropped, admins get an alert ("events the database could not store"), and the next batch gets its own 2 minutes. New requests get 503 meanwhile, so the SDKs keep their events and send them again later.

Before it stores an event, the tool removes NUL characters from it (Postgres stores no NUL in text; a Laravel `QueryException` with a binary UUID in its message has them) and cuts user ids, releases and environments to 200 bytes. Should the database still refuse a value of one event (on Postgres an SQLSTATE of class 22 or 54), only that event is lost: the writer writes the rest of the batch in smaller parts, logs `the database refused a value of an event; the event is dropped` with the kind of event, its project and the error code (never the value), and admins get the "could not store" alert.

## Page views do not show

- Page views must be on for the project (Settings → Projects → Add page views), or the sidebar has no Pages.
- Pages and Events read hourly summaries that are rebuilt every minute: new page views show a minute or two after they arrive.
- `api_host` must be the tool's `BASE_URL` and the token the project's PostHog token. A refused token is logged with status 401.
- posthog-js sends nothing from a browser that looks automated (`navigator.webdriver`, or a headless browser's User-Agent). Test with a normal browser.
- Visitors who are not signed in count as new on every page load with `persistence: 'memory'` (see [SDK setup](sdk-setup.md#page-views-with-posthog-js)).
- "No time yet" on a page: nobody left it yet with a page leave or a next page view. posthog-js sends the page leave when the tab closes or navigates; a browser that is killed sends none.
- Bots (crawlers, headless browsers) are counted apart, in one line under the totals.

## Source maps are not used

- Use debug ids: run `sentry-cli sourcemaps inject` before the upload, or use `@sentry/vite-plugin`, which injects them. Matching by release and file URL works only when the error's release equals the upload's, and not, on the issue page, for errors stored before the upload whose file URL has a segment that looks like a hash or token (see [Source maps](sdk-setup.md#source-maps)).
- Upload before the build goes live: errors that arrive after the upload are stored with the original code.
- A map over a quarter of `SOURCEMAP_CACHE_MB` (16 MiB by default) is stored but not used: the log says "file too large to symbolicate with". Raise the setting.
- The vite plugin says the URL "doesn't match the host your Sentry credentials are for": its `url` must be exactly `BASE_URL`, which the token names. After changing `BASE_URL`, make a new token.
- `403` from the upload: the token may not upload to that project, or the project slug is wrong.

## Signing in

- After 5 wrong passwords in 15 minutes an account pauses for 15 minutes (the message says so); a password reset ends the pause. A browser gets at most 20 tries in 15 minutes, then 429.
- Behind a proxy, the tool counts sign-in tries per visitor only when the proxy adds `X-Forwarded-For` and connects from the same machine or a private network (Caddy in Docker does). Otherwise all visitors share the proxy's count of 20.
- Forgot the password: see [Accounts](sdk-setup.md#accounts). The only admin can always get a link on the server: `docker compose exec app /monitor admin reset-password --email you@example.com`.
- Signed out after changing `SECRET_KEY` or moving `BASE_URL` between http and https: expected; sign in again.

## Mail and Slack

- Mail: the log says `mail is on` with the host at the start; failed mails are logged with the server's answer. `smtp://` needs STARTTLS (use `smtps://` for port 465). The sender defaults to `monitor@<BASE_URL host>`, which your mail server may refuse; set `?from=` to an address it accepts.
- Slack: the webhook must start with `https://`. "Send test message" on the Alerts page says why a message did not arrive. New and returning issues are capped at 30 notifications per project and channel an hour.
- Summaries go out at 08:00 in `TZ` (UTC without it).

## Restore refused

The reason is on the page or printed by `monitor restore`: a damaged file, one that is not this tool's backup, one from a newer version ("Update the tool first"), or one whose rows refer to rows that are not there. See [Backup and restore](backup-restore.md#restore). A restore the start refused is kept as `restore-rejected-<time>.db` with the reason in the log; "Remove the file" on the Backup page (or `monitor restore --cancel`) removes it.

## Still stuck

Run with `LOG_LEVEL=debug`, reproduce the problem, and read the lines around it. Log lines never contain query strings, client addresses or the user id of the user activity page; error lines can quote your data (an error message from the database, a project name), so read a log before you share it.
