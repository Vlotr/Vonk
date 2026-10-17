# Accounts, projects and SDK setup

Your apps keep the official SDKs: the tool answers like Sentry (errors) and like PostHog (page views and events). This page covers who can see what, how projects work, and how to point each SDK at the tool.

## Accounts

- The first account is made on the setup page right after the [install](install.md#3-create-the-first-account); it is an admin. After that, the setup page is gone.
- Others join with an invite: Settings → Users → Invite, with their email address, a role and (for members) their projects. The link works once, for 7 days. With `MAIL_URL` it is mailed too; without it, pass the link on yourself.
- **Admins** see every project and Settings. **Members** see only the projects they are given, and get "not found" for everything else; they cannot change alerts. The last admin cannot be removed or made a member.
- Forgot password: with mail, the sign-in page mails a link. Without mail, an admin makes a reset link for a member in Settings → Users, and an admin gets one on the server: `docker compose exec app /monitor admin reset-password --email you@example.com`. Links work once, for an hour.
- After 5 wrong passwords in 15 minutes an account pauses signing in for 15 minutes; a password reset ends the pause.

## Projects

A project is one app, or one app's errors and page views together. Settings → Projects → Add project asks for:

- **Name**, shown in the dashboard and in notifications.
- **Keep data for**: 30, 90 (the default), 180 days, 1 year, or indefinitely. Once a day, older errors, issues, page views, custom events, their statistics and source maps are deleted.
- **What to track**: errors from Laravel, errors from the browser, page views. This decides which steps Setup and keys shows and whether the sidebar shows Pages; the tool accepts data from any SDK with the project's keys either way.

"Setup and keys" shows the project's **DSN** (for Sentry SDKs) and **PostHog token** (for PostHog SDKs), with the steps below filled in, and its events-per-minute limit. Edit project changes the settings, the [route patterns](#page-views-with-posthog-js) and the **events per minute** (1000 by default, 10 to 100 000; see [Configuration](configuration.md#per-project-settings), and keep it low for a public site); "Keys" makes new ones (the old ones stop at once, so update your apps); Delete project removes it and all its data.

On the command line: `docker compose exec app /monitor project create --name Webshop` makes a project (90 days, Laravel and browser errors) and prints its DSN and PostHog token.

## Laravel

With [`sentry/sentry-laravel`](https://docs.sentry.io/platforms/php/guides/laravel/) (tested: 4.29 on Laravel 13 and PHP 8.4):

```sh
composer require sentry/sentry-laravel
```

In `bootstrap/app.php` (Laravel 11 and newer):

```php
use Sentry\Laravel\Integration;

->withExceptions(function (Exceptions $exceptions) {
    Integration::handles($exceptions);
})
```

In `.env`:

```sh
SENTRY_LARAVEL_DSN=https://<key>@monitor.example.com/1
```

Then `php artisan sentry:test` sends a test error. Exceptions your app reports now show under Issues, with source Laravel. `examples/laravel` in this repository is a fresh Laravel app set up this way.

- The user: to see which user had an error (and, with page views, what they did before it), give Sentry the user's id, only the id. In `AppServiceProvider::boot()`:

  ```php
  use Illuminate\Auth\Events\Authenticated;
  use Illuminate\Support\Facades\Event;

  Event::listen(function (Authenticated $event) {
      \Sentry\configureScope(function (\Sentry\State\Scope $scope) use ($event) {
          $scope->setUser(['id' => (string) $event->user->getAuthIdentifier()]);
      });
  });
  ```

  Keep `SENTRY_SEND_DEFAULT_PII` off (the default): with it on, the SDK also sends the user's email address and more (see [Privacy](privacy.md)).
- Releases: `SENTRY_RELEASE=v2.8.1` gives each error its release; the issue shows the release it was first seen in, and the one it came back in.
- Performance tracing is not stored: leave `SENTRY_TRACES_SAMPLE_RATE` unset, or the SDK sends data the tool throws away.

Plain PHP works the same with [`sentry/sentry`](https://docs.sentry.io/platforms/php/) (tested: 4.34): `\Sentry\init(['dsn' => 'https://<key>@monitor.example.com/1']);`. Its issues have source PHP.

## Browser

With [`@sentry/browser`](https://docs.sentry.io/platforms/javascript/) (tested: 11.8 in Chromium):

```sh
npm install @sentry/browser
```

```js
import * as Sentry from '@sentry/browser'

Sentry.init({
  dsn: 'https://<key>@monitor.example.com/1',
  release: 'shop@2.8.1', // optional
})
```

Uncaught errors and unhandled promise rejections show under Issues with source Browser. The tool accepts the SDK's requests from any site (CORS). Leave tracing and session replay off (`tracesSampleRate`, `replaysSessionSampleRate`): the tool does not store them. Production builds are minified: upload their [source maps](#source-maps) to see your own code.

## Page views with posthog-js

Turn on page views for the project (Settings → Projects → Add page views, or tick it in Edit project), then use [`posthog-js`](https://posthog.com/docs/libraries/js) (tested: 1.438.12 in Chromium) with `api_host` set to the tool's `BASE_URL` and the project's PostHog token, both on Setup and keys:

```sh
npm install posthog-js
```

```js
import posthog from 'posthog-js'

posthog.init('<project token>', {
  api_host: 'https://monitor.example.com',
  persistence: 'memory',
  capture_pageview: 'history_change',
  capture_pageleave: true,
  disable_session_recording: true,
})
```

- `persistence: 'memory'` stores nothing in the browser: no cookies, no local storage. The price: a visitor who is not signed in counts as new on every page load (posthog-js warns in the console that it "will mint a new distinct ID on every page load"; for this tool that is expected). Leave it out and posthog-js keeps an id in a cookie, so returning visitors count once; see [Privacy](privacy.md#cookies) before you do.
- `capture_pageview: 'history_change'` counts pages that single-page apps change with `history.pushState` too. `capture_pageleave` gives the time on a page: from a page view to its page leave, or to the next page view in the same tab. A visit without either counts as a view but not in the average time.
- The tool answers what posthog-js asks for (its configuration, feature flags, `/i/v0/e/`, `/batch/`, ...) with everything it does not collect turned off: no feature flags (always an empty list), no session recording, surveys, autocapture or heatmaps.
- The Pages screen shows each page's views, visitors, average time and errors, over 24 hours, 7 or 30 days, a minute or two after they arrive (hourly summaries are rebuilt every minute). Known bots (by User-Agent) are counted apart.
- Routes: ids, UUIDs, hashes and tokens in paths become one route by themselves (`/orders/1234` counts as `/orders/:id`). For the rest, add route patterns in Edit project, one per line, such as `/products/:slug`; they apply to data that comes in after they are saved, and Edit project previews recent paths with the route each gets.

**Custom events**: every event whose name does not start with `$` is stored with its page, its user and the properties your app sent, and counted on the project's Events screen:

```js
posthog.capture('checkout_completed', { plan: 'pro' })
```

Server SDKs (posthog-node, posthog-php, ...) work too: give them the project token and the tool's address as host. Events sent from a server have no page. PostHog's own events (`$identify`, `$autocapture`, ...) are accepted and not stored, except page views and page leaves.

## The same user in errors and page views

Give both browser SDKs the signed-in user's id, as text:

```js
// After sign-in (and on every page load while signed in):
posthog.identify(String(user.id))
Sentry.setUser({ id: String(user.id) })

// After sign-out:
posthog.reset()
Sentry.setUser(null)
```

With the [Laravel listener](#laravel) the server's errors carry the same id. An issue then shows "Before this error": the pages the user of its latest error saw in the 30 minutes before it, with the time on each. "See all activity of this user" lists that user's page views, errors and custom events. Signed-in visitors also count once in the visitor numbers.

## Source maps

Errors from a minified build point at `assets/index-D8xk2Lp1.js:1`. Upload the build's source maps and they show your own files, functions and lines. The tool speaks the part of Sentry's API that `sentry-cli` and `@sentry/vite-plugin` use (tested: `@sentry/cli` 3.8, `@sentry/vite-plugin` 11.8 with Vite 8.3, which uploads with the `sentry` CLI 0.45):

1. Settings → API tokens → New token: a name and the projects it may upload to. The token is shown once, with this setup filled in. Give it to your build as a secret.
2. With `@sentry/vite-plugin` (`npm install --save-dev @sentry/vite-plugin`), in `vite.config.ts`:

   ```js
   import { sentryVitePlugin } from '@sentry/vite-plugin'
   import { defineConfig } from 'vite'

   export default defineConfig({
     build: { sourcemap: 'hidden' },
     plugins: [
       sentryVitePlugin({
         url: 'https://monitor.example.com', // your BASE_URL, exactly
         org: 'monitor',                     // any word: the tool has no organizations
         project: 'webshop',                 // the project's slug
         authToken: process.env.SENTRY_AUTH_TOKEN,
         sourcemaps: { filesToDeleteAfterUpload: ['./dist/**/*.map'] },
         telemetry: false,
       }),
     ],
   })
   ```

   Build with `SENTRY_AUTH_TOKEN=<token>` and `SENTRY_CLI_NO_TELEMETRY=1` in the environment: without the latter, the plugin's Sentry CLI sends its own error reports and traces to sentry.io.
3. Or with `sentry-cli`, after the build: `npx @sentry/cli sourcemaps inject ./dist`, then `npx @sentry/cli sourcemaps upload --project webshop ./dist`, with the token in `SENTRY_AUTH_TOKEN`. The token carries the tool's address, so no `--url` or `--org` is needed.

How maps are found: by the **debug id** that `sourcemaps inject` and the vite plugin put in each file (the browser SDK reports it with each error), or else by the error's **release** and the file's URL. Errors that arrive after the upload are stored with the original frames, so they group by your own code and stay one issue across builds. Errors stored before the upload show the original code on their issue page, looked up when the page opens. That lookup by release and URL cannot work when the file's URL has a segment the tool masks before it stores a URL (a directory or name that looks like a hash, UUID or token, such as `/static/3f9a1c0b7d2e4f6a/app.js`): the stored URL no longer names the file. Lookup by debug id is not affected, so inject debug ids (both ways above do).

More: a token uploads only to its projects (403 for others) and names `BASE_URL` (after changing `BASE_URL`, make new tokens); revoking it stops it at once. Bundles may be up to 100 MiB. One map is used up to a quarter of `SOURCEMAP_CACHE_MB` (16 MiB by default); a larger one is stored but not used ([Configuration](configuration.md#sourcemap_cache_mb)). Source maps are kept for the project's retention, counted from their upload, and are not in [backups](backup-restore.md).

## Alerts

Each project's Alerts page (admins change it; members who see the project read it) sends, by email (with `MAIL_URL`) and to a Slack incoming webhook:

- **New issue** and **returning issue** (a resolved issue that gets a new event; with the release it came back in). At most 30 a project and channel an hour, then one message says how many more were held back.
- **Spike**: the last hour has at least the threshold (5 by default) times the usual hourly number of errors, and at least 10; at most once in 6 hours. Errors of ignored issues do not count, in the last hour nor in the usual number.
- **Summary**: daily, weekly (Mondays) or monthly (the 1st), at 08:00 in `TZ`.

Mail goes to everyone who may see the project. The webhook must be `https://` ("Send test message" checks it). Admins also hear about the tool itself (a disk running full, events that could not be stored or were turned away), at most once in 12 hours each, by mail and on the admins' webhook in Settings → Status.

## Tested SDK versions

These versions sent real traffic to the tool in its tests (recorded fixtures and the end-to-end tests). Newer versions usually work: the tool ignores what it does not know.

| SDK | Version | Tested with |
| --- | --- | --- |
| `sentry/sentry-laravel` | 4.29.0 (on `sentry/sentry` 4.34.0) | Laravel 13.37, PHP 8.4 |
| `sentry/sentry` (plain PHP) | 4.34.0 | PHP 8.4 |
| `@sentry/browser` | 11.8.0 | Chromium (Playwright 1.64.1) |
| `posthog-js` | 1.438.12 | Chromium (Playwright 1.64.1) |
| `@sentry/cli` (source map upload) | 3.8.0 | Vite 8.3 builds |
| `@sentry/vite-plugin` (source map upload) | 11.8.0 (uploads with the `sentry` CLI 0.45.0) | Vite 8.3 |

After an SDK update, check that its errors and page views still arrive (`LOG_LEVEL=debug` shows every request the tool accepts). The recipes that recorded the fixtures are in the repository (`internal/*/testdata/capture/`), described in `CLAUDE.md`.
