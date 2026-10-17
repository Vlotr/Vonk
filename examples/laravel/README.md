# Laravel example

A fresh Laravel app with the official [`sentry/sentry-laravel`](https://docs.sentry.io/platforms/php/guides/laravel/) SDK, pointed at the monitoring tool instead of sentry.io. Two routes fail on purpose:

| Route | Throws | In the dashboard |
| --- | --- | --- |
| `/boom` | `RuntimeException('Checkout failed')` in `CheckoutController::store` | one issue; every visit adds an event |
| `/boom-other` | `InvalidArgumentException('Coupon code is not valid')` | an issue of its own |

Tested with Laravel 13.37, `sentry/sentry-laravel` 4.29.0 (`sentry/sentry` 4.34.0) and PHP 8.4.

## Run it

You need PHP 8.3 or newer and Composer. Start the monitoring tool first and create a project; `monitor project create` prints its DSN (see the main README).

```sh
cd examples/laravel
composer install
cp .env.example .env
php artisan key:generate
```

Put the DSN in `.env`:

```sh
SENTRY_LARAVEL_DSN=http://<key>@localhost:8000/1
```

Then serve the app and break it:

```sh
php artisan serve --port=8080
curl http://localhost:8080/boom          # twice, if you like
curl http://localhost:8080/boom-other
```

Open the dashboard (`http://localhost:5173` with `make dev`) and choose Issues: `RuntimeException` with its event count, `InvalidArgumentException` next to it, both with source Laravel. Errors show up within a second; the page refreshes every minute, or reload it.

Visit the routes in a browser instead of with curl and the issue page also shows your browser and system, taken from the request's User-Agent header.

## What was changed from a fresh `composer create-project laravel/laravel`

- `composer require sentry/sentry-laravel`, and `php artisan vendor:publish --provider="Sentry\Laravel\ServiceProvider"` for `config/sentry.php`.
- `bootstrap/app.php`: `Integration::handles($exceptions)`, so unhandled exceptions are reported.
- `config/sentry.php`: `server_name` reads `SENTRY_SERVER_NAME` (default: this machine's host name); the end-to-end tests set a neutral name because their screenshots are committed.
- `app/Http/Controllers/CheckoutController.php` and the two routes in `routes/web.php`.
- `.env.example`: `SENTRY_LARAVEL_DSN`, and file sessions and cache (plus the sync queue) so the app runs without a database.
- Removed the skeleton's `AGENTS.md` and `CLAUDE.md` (setup instructions for coding agents that do not apply inside this repository).

`vendor/` and `.env` are not committed.
