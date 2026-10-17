// Serves a small shop page with posthog-js on http://localhost:5173, opens
// it in Chromium and does what the dashboard counts, waiting between steps
// so each one is its own request: the page load, a page change inside the
// app (history.pushState), identify, a custom event, and leaving the page.
// The page is served by a real local server (not a Playwright route) so
// Chrome's local network access rules let the SDK reach the recorder.
//
// The recorder must answer the configuration endpoints the way the monitor
// does (responses.json), because posthog-js picks its compression from them:
//   go run ./tools/capture -addr 127.0.0.1:19999 -out <dir> -responses internal/posthog/testdata/capture/responses.json
//   POSTHOG_HOST=http://127.0.0.1:19999 npm run capture
import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { chromium } from 'playwright';

const host = process.env.POSTHOG_HOST;
const token = 'phc_fixture0capture0token0for0tests0only0000000'; // matches responses.json
const bundle = readFileSync(new URL('./dist/app.js', import.meta.url), 'utf8');
const html = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Blue shoes</title></head>
<body>
<h1>Blue shoes</h1>
<script>window.POSTHOG_HOST = ${JSON.stringify(host)}; window.POSTHOG_TOKEN = ${JSON.stringify(token)};</script>
<script src="/assets/app.js"></script>
</body>
</html>`;

const site = createServer((req, res) => {
  if (req.url.startsWith('/assets/')) {
    res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(bundle);
  } else {
    res.writeHead(200, { 'Content-Type': 'text/html' }).end(html);
  }
});
await new Promise((resolve) => site.listen(5173, 'localhost', resolve));

// Look like a visitor's Chrome: posthog-js sends nothing from a browser
// that looks automated (navigator.webdriver, or HeadlessChrome in the
// User-Agent or in navigator.userAgentData's brands).
const userAgent =
  'Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36';
const browser = await chromium.launch();
const context = await browser.newContext({ userAgent });
await context.addInitScript(() => {
  Object.defineProperty(Navigator.prototype, 'webdriver', { get: () => false });
  Object.defineProperty(Navigator.prototype, 'userAgentData', { get: () => undefined });
});
const page = await context.newPage();
page.on('console', (m) => console.log('page console:', m.type(), m.text()));
page.on('request', (r) => console.log('request:', r.method(), r.url()));
const pause = (ms) => page.waitForTimeout(ms);

await page.goto('http://localhost:5173/products/blue-shoes?utm_source=newsletter&token=tok_abc123');
await page.waitForSelector('body[data-ready="yes"]');
await pause(4000); // the request queue sends every few seconds

await page.evaluate(() => history.pushState({}, '', '/checkout?step=2'));
await pause(4000);

await page.evaluate(() => window.posthog.identify('42', { email: 'jan@example.com' }));
await pause(4000);

await page.evaluate(() => window.posthog.capture('custom', { plan: 'pro', password: 'hunter2' }));
await pause(4000);

await page.close({ runBeforeUnload: true }); // page hide: $pageleave
await pause(1000).catch(() => {});
await context.close();
await browser.close();
site.close();
console.log('done');
