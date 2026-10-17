# PostHog example

A small shop of three pages with the official [`posthog-js`](https://posthog.com/docs/libraries/js) (1.438.12) for page views and [`@sentry/browser`](https://docs.sentry.io/platforms/javascript/) (11.8.0) for errors, both sending to the monitoring tool, served by Vite. It uses the setup the tool's "Setup and keys" page gives:

```js
posthog.init(token, {
  api_host: host,               // the tool's BASE_URL
  persistence: 'memory',        // no cookies, nothing stored in the browser
  capture_pageview: 'history_change',
  capture_pageleave: true,      // the time on a page
  disable_session_recording: true,
})
```

| Page | What it shows in the dashboard |
| --- | --- |
| `/` | a page view of `/` |
| `/products/` | a page view of `/products`; its links change the address to `/products/blue-shoes` with `history.pushState`, a page view each (`/products/:slug` with that route pattern), and send the custom event `product_viewed` |
| `/checkout/` | a page view of `/checkout`; Pay sends the custom event `checkout_started` and then throws a `TypeError`, an issue on that page |

"Sign in as user 42" gives both SDKs the same user id (`posthog.identify('42')` and `Sentry.setUser({ id: '42' })`, see `src/analytics.js`), so the error's user and the page views of that visitor are one person: the issue page lists the pages they saw before the error ("Before this error") and links to all their activity. Open the shop with `?user=anna@example.com` to sign in as another id. The shop keeps the id in `sessionStorage`, as a real app keeps it in its session, and identifies again on every page load. The custom events show under Events.

## Run it

Start the monitoring tool, create a project (`monitor project create --name Webshop` prints the DSN and the PostHog token) and turn on page views for it in Settings (Add page views). Then:

```sh
cd examples/posthog
npm install
VITE_POSTHOG_TOKEN=phc_... VITE_POSTHOG_HOST=http://localhost:8000 \
VITE_SENTRY_DSN=http://<key>@localhost:8000/1 npx vite --port 5175
```

`VITE_SENTRY_RELEASE=shop@2.8.1` (optional) gives the errors a release, which the issue page shows.

Open http://localhost:5175, sign in, click through the pages and wait a little on each, pay, then close the tab. The Pages and Events screens show the pages with their views and average time, and the custom events, a minute or two later (the hourly summaries are rebuilt every minute); the TypeError's issue page shows the pages before it at once.

posthog-js warns in the browser console that it mints a new distinct ID on every page load: that is what `persistence: 'memory'` means, and the tool needs nothing more. posthog-js sends nothing from a browser that looks automated (`navigator.webdriver`, or a bot's name in `navigator.userAgent`); the end-to-end test hides those, as `e2e/phase3-pages.spec.ts` shows.
