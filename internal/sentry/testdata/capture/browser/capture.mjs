// Serves a small checkout page with @sentry/browser on http://localhost:5173,
// opens it in Chromium, clicks the button that throws a TypeError, and waits
// until the SDK has sent the error. The page is served by a real local server
// (not a Playwright route) so Chrome's local network access rules allow the
// SDK to reach the recorder on 127.0.0.1.
import { readFileSync } from 'node:fs';
import { createServer } from 'node:http';
import { chromium } from 'playwright';

const dsn = process.env.SENTRY_DSN;
const bundleName = 'index-D8xk2Lp1.js'; // named like a Vite build output
const bundle = readFileSync(new URL(`./dist/${bundleName}`, import.meta.url), 'utf8');
const html = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Checkout</title></head>
<body>
<p>Total: <span id="total"></span></p>
<button id="checkout">Pay</button>
<script>window.SENTRY_DSN = ${JSON.stringify(dsn)};</script>
<script src="/assets/${bundleName}"></script>
</body>
</html>`;

const site = createServer((req, res) => {
  const url = new URL(req.url, 'http://localhost');
  if (url.pathname.startsWith('/assets/')) {
    res.writeHead(200, { 'Content-Type': 'text/javascript' }).end(bundle);
  } else if (url.pathname.startsWith('/api/')) {
    res.writeHead(200, { 'Content-Type': 'application/json' }).end('{"items":3}');
  } else {
    res.writeHead(200, { 'Content-Type': 'text/html' }).end(html);
  }
});
await new Promise((resolve) => site.listen(5173, 'localhost', resolve));

const browser = await chromium.launch();
const page = await browser.newPage();
page.on('console', (m) => console.log('page console:', m.type(), m.text()));

await page.goto('http://localhost:5173/checkout?coupon=SPRING&card=4111111111111111');
await page.waitForSelector('body[data-ready="yes"]');
const sent = page.waitForResponse((r) => r.url().includes('/envelope/') && r.request().postData()?.includes('"exception"'));
await page.click('#checkout');
await sent;
console.log('error envelope sent');
await page.close({ runBeforeUnload: true }); // page hide: lets the SDK flush what it still holds
await browser.close();
site.close();
