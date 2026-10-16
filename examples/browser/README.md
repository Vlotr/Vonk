# Browser example

A small page with the official [`@sentry/browser`](https://docs.sentry.io/platforms/javascript/) SDK (11.8.0), served by Vite. It reads the DSN from the address, so one page works with any project:

```
http://localhost:5174/?dsn=http://<key>@localhost:8000/1
```

Two buttons fail on purpose:

| Button | Error | In the dashboard |
| --- | --- | --- |
| Throw a TypeError | `TypeError: Cannot read properties of undefined (reading 'price')` in a click handler | an issue with source Browser |
| Reject a promise | `Error: Failed to fetch /api/stock`, a rejection nobody handles | another issue with source Browser |

## Run it

Start the monitoring tool and create a project first; `monitor project create` prints the DSN.

```sh
cd examples/browser
npm install
npx vite --port 5174
```

Open the page with `?dsn=...`, click the buttons, and look at Issues in the dashboard. The SDK sends to the tool's address straight from the browser, which the tool allows (CORS).

The stack trace points at `src/main.js` because the Vite dev server sends the source as it is. A production build sends minified files; readable traces for those need source maps, which arrive in a later version.
