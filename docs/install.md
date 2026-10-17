# Install with Docker Compose

This gets the tool running on one machine in about ten minutes: start the container, create the first account, add a project and send it a first error. HTTPS comes after that, from a reverse proxy ([HTTPS with Caddy](https-caddy.md)).

You need a Linux or macOS machine, x86-64 (amd64) or 64-bit ARM (arm64), with Docker and Docker Compose v2 (`docker compose version` prints 2.x), and `openssl` and `curl`. The tool needs little: the image is about 21 MB (8 MB to download), and the container used under 50 MB of memory in our tests.

> **The image.** Each release is published as `ghcr.io/OWNER/monitor`, where `OWNER` is the GitHub account that publishes the project: replace `OWNER` below with it. The tag `1` follows every 1.x release; `1.2.3` stays on one. To build the image yourself instead, run `make docker` in a checkout of the repository; it makes `monitor:dev`, which you then put in `image:`.

## 1. Make a folder with two files

```sh
mkdir monitor && cd monitor
```

`compose.yaml`:

```yaml
services:
  app:
    image: ghcr.io/OWNER/monitor:1
    ports: ["8000:8000"]
    environment:
      SECRET_KEY: ${SECRET_KEY:?set SECRET_KEY in .env}
      BASE_URL: ${BASE_URL:?set BASE_URL in .env}
      # Summaries go out at 08:00 on this clock.
      TZ: Europe/Amsterdam
      # MAIL_URL: smtp://user:password@mail.example.com:587
    volumes: ["data:/data"]
    restart: unless-stopped
    # On a stop the tool finishes running requests and writes the events it
    # accepted; Docker's default of 10 s can cut that short.
    stop_grace_period: 30s
volumes:
  data:
```

`.env`, with a random secret (keep it: changing it signs everyone out) and the address people and SDKs will use to reach the tool:

```sh
printf 'SECRET_KEY=%s\nBASE_URL=http://localhost:8000\n' "$(openssl rand -hex 32)" > .env
chmod 600 .env
```

`BASE_URL` is the root of a host, without a path. On a server, put its address there (`http://203.0.113.10:8000`, or `https://monitor.example.com` once [Caddy](https-caddy.md) is in front). DSNs and links in mails are made from it. Every setting is in [Configuration](configuration.md).

## 2. Start it

```sh
docker compose up -d
curl http://localhost:8000/health
```

`/health` answers `{"status":"ok",...}` once the tool is up (a second or two). `"degraded"` with `"failing":["disk"]` means the disk has less than 10 % or 1 GiB free: the tool works, but make room. If it does not answer, `docker compose logs app` says why; a wrong setting stops the start with a line naming it.

## 3. Create the first account

Open `BASE_URL` (http://localhost:8000) in a browser. The tool shows "Create the first account": an email address and a password of at least 10 characters. This account is an admin.

Do this right after the start: until the first account exists, anyone who can reach the tool can create it (the log warns about that every start until then). After it, the page is gone; others join with an invite link from Settings → Users.

## 4. Add a project

The first page, All projects, says "No projects yet" with a link: Add your first project (or Settings → Projects → Add project). Give it a name (such as "Webshop"), how long to keep data (90 days unless you choose otherwise) and what to track, then Create project. The tool opens "Set up Webshop" with the project's DSN, such as `http://5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c@localhost:8000/1`, and the setup steps for Laravel, the browser and page views. Later, Settings → Projects → Setup and keys shows the same page.

On the command line instead: `docker compose exec app /monitor project create --name Webshop` prints the DSN and the PostHog token. (The image has no shell; commands run as `/monitor ...`.)

## 5. Send a first error

Pick one of these. The error shows under the project's Issues within a second or two (the page itself refreshes every minute, or reload it).

**With curl, no SDK needed.** Put the DSN in a variable and post one event the way the Sentry SDKs do:

```sh
DSN='http://5f2b6a0c7d3e4f1a8b9c0d1e2f3a4b5c@localhost:8000/1'   # your project's DSN
printf '%s\n' "{\"dsn\":\"$DSN\"}" '{"type":"event"}' \
  '{"platform":"php","exception":{"values":[{"type":"RuntimeException","value":"Hello from the install guide"}]}}' |
  curl -sS --data-binary @- "$(echo "$DSN" | sed -E 's#//[^@]*@#//#; s#/([0-9]+)$#/api/\1/envelope/#')"
```

It answers `{"id":"..."}`, and Issues shows `RuntimeException: Hello from the install guide`.

**From your Laravel app** (sentry-laravel, Laravel 11 or newer): `composer require sentry/sentry-laravel`, add `Integration::handles($exceptions);` to `bootstrap/app.php` as Setup and keys shows, put `SENTRY_LARAVEL_DSN=<the DSN>` in the app's `.env`, and run `php artisan sentry:test`. The details are in [SDK setup](sdk-setup.md#laravel).

**With the Laravel example** from this repository (PHP 8.3 or newer and Composer), with `DSN` set as above, in `examples/laravel` (the `SENTRY_LARAVEL_DSN` line added last in `.env` counts):

```sh
composer install
cp .env.example .env
php artisan key:generate
echo "SENTRY_LARAVEL_DSN=$DSN" >> .env
php artisan serve --port=8080
```

Then, in a second terminal, `curl http://localhost:8080/boom`: Issues shows `RuntimeException: Checkout failed`, source Laravel. Open http://localhost:8080/boom in a browser instead and the issue also shows your browser and system.

The DSN's address must be one the app can reach: an app in another container or on another machine needs `BASE_URL` set to an address it can reach, not `localhost`.

## Next

- [HTTPS with Caddy](https-caddy.md): put the tool on a domain with a certificate. Do this before you send real data from the internet.
- [SDK setup](sdk-setup.md): errors from Laravel and the browser, page views and custom events with posthog-js, source maps, and the tested SDK versions.
- [Configuration](configuration.md): mail (`MAIL_URL`), Postgres (`DATABASE_URL`) and every other setting.
- [Backup and restore](backup-restore.md) and [Updating](updating.md).
- [Privacy](privacy.md): what is stored, and deleting a person's data on request.
- [Troubleshooting](troubleshooting.md).

## Without Compose

The same with `docker run` (`--stop-timeout 30` for the same reason as `stop_grace_period`). Keep the secret in a file first: every later `docker run` (after an update) needs the same one.

```sh
openssl rand -hex 32 > monitor-secret && chmod 600 monitor-secret
docker run -d --name monitor --restart unless-stopped -p 8000:8000 --stop-timeout 30 \
  -e SECRET_KEY="$(cat monitor-secret)" -e BASE_URL=http://localhost:8000 \
  -v monitor-data:/data ghcr.io/OWNER/monitor:1
```

Commands run as `docker exec monitor /monitor project create --name Webshop`.

The tool is also one binary (`monitor`, built with `make build`) that runs without Docker: `SECRET_KEY=... BASE_URL=... DATA_DIR=/var/lib/monitor monitor serve`.
