# Browser example

A small TypeScript page with the official [`@sentry/browser`](https://docs.sentry.io/platforms/javascript/) SDK (11.8.0), built with Vite. It reads the DSN from the address, so one page works with any project:

```
http://localhost:5174/?dsn=http://<key>@localhost:8000/1
```

Two buttons fail on purpose:

| Button | Error | In the dashboard |
| --- | --- | --- |
| Throw a TypeError | `TypeError: Cannot read properties of undefined (reading 'price')` in `showPrice` | an issue with source Browser |
| Reject a promise | `Error: Failed to fetch /api/stock`, a rejection nobody handles | another issue with source Browser |

## Run it

Start the monitoring tool and create a project first; `monitor project create` prints the DSN.

```sh
cd examples/browser
npm install
npx vite --port 5174
```

Open the page with `?dsn=...`, click the buttons, and look at Issues in the dashboard. The SDK sends to the tool's address straight from the browser, which the tool allows (CORS). The development server sends the code as Vite compiled it from `src/main.ts`, not minified.

## A production build with source maps

A production build is minified: without source maps its errors point at `assets/index-<hash>.js:1`. `vite.config.ts` uploads the build's source maps with [`@sentry/vite-plugin`](https://www.npmjs.com/package/@sentry/vite-plugin) (11.8.0) when a token is set. Make one in Settings → API tokens for the project, then:

```sh
SENTRY_AUTH_TOKEN=<token> SENTRY_URL=http://localhost:8000 SENTRY_PROJECT=<project slug> \
  SENTRY_CLI_NO_TELEMETRY=1 npm run build
npx vite preview --port 5175
```

`SENTRY_URL` is the tool's `BASE_URL` (the token names it); `VITE_SENTRY_RELEASE` optionally gives the errors a release. The plugin deletes the maps from `dist/` after the upload, so they are not served. Open http://localhost:5175/?dsn=... and click "Throw a TypeError": the issue shows `src/main.ts`, `showPrice` and the TypeScript line, `price.textContent = (product as Product).price.toFixed(2)`.
