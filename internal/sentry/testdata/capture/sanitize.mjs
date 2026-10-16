// Makes a request recorded by tools/capture safe to commit as a fixture. It
// replaces the local app directory with /var/www/webshop and the operating
// system strings sentry-php sends (kernel build, host name) with neutral
// ones, then refuses to write anything that still names this machine's user
// or host. A gzip body stays gzip (recompressed); the headers file gets the
// new Content-Length and bodySize. The SDK's structure is kept as sent.
//
//   node sanitize.mjs <NNNN-....body> <NNNN-....json> <out> [app-dir]
//
// writes <out>.envelope (or <out>.envelope.gz) and <out>.headers.json.
// `node --test` in this directory runs sanitize.test.mjs.
import { readFileSync, realpathSync, writeFileSync } from 'node:fs';
import { hostname, userInfo } from 'node:os';
import { fileURLToPath } from 'node:url';
import { gunzipSync, gzipSync } from 'node:zlib';

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

/**
 * findLeaks returns what in text still names this machine: the user name as
 * a whole path segment (/Users/jan/, C:\\Users\\jan\\, also JSON's \/jan\/),
 * the host name or its first label as a whole host token (not "web" inside
 * "web-1", "webshop" or "web.example.com"), and the app directory anywhere.
 * User and host are matched in the exact case the system reports them, as
 * paths and SDKs write them: a host named "mac" is no match for "Mac OS X"
 * in a User-Agent. Whole segments and tokens keep short names such as
 * "www", "web" or "jan" from matching ordinary words; "localhost" is no leak.
 */
export function findLeaks(text, { user, host, appDir }) {
  const leaks = [];
  if (user && new RegExp(`(^|[\\\\/])${escapeRegExp(user)}([\\\\/]|$)`).test(text)) leaks.push(user);
  for (const name of new Set(host ? [host, host.split('.')[0]] : [])) {
    if (name === '' || name === 'localhost') continue;
    if (new RegExp(`(^|[^A-Za-z0-9.-])${escapeRegExp(name)}($|[^A-Za-z0-9.-])`).test(text)) leaks.push(name);
  }
  if (appDir && text.includes(appDir)) leaks.push(appDir);
  return leaks;
}

function main() {
const [bodyPath, metaPath, out, appDir] = process.argv.slice(2);
if (!bodyPath || !metaPath || !out) {
  console.error('usage: node sanitize.mjs <captured.body> <captured.json> <out> [app-dir]');
  process.exit(2);
}

const raw = readFileSync(bodyPath);
const gzipped = raw[0] === 0x1f && raw[1] === 0x8b;
let text = (gzipped ? gunzipSync(raw) : raw).toString('utf8');

if (appDir) {
  const neutral = '/var/www/webshop';
  // PHP's json_encode writes "/" as "\/", so replace both spellings.
  const escaped = (s) => s.replaceAll('/', '\\/');
  text = text.replaceAll(appDir, neutral).replaceAll(escaped(appDir), escaped(neutral));
}
// contexts.os as sentry-php sends it (name, version, build, kernel_version).
text = text.replace(
  /"os":\{[^{}]*\}/g,
  '"os":{"name":"Linux","version":"6.8.0","build":"#45-Ubuntu SMP PREEMPT_DYNAMIC",' +
    '"kernel_version":"Linux web-1 6.8.0-45-generic #45-Ubuntu SMP PREEMPT_DYNAMIC x86_64"}',
);

for (const leak of findLeaks(text, { user: userInfo().username, host: hostname(), appDir })) {
  console.error(`sanitize: the body still contains ${JSON.stringify(leak)}; extend the replacements`);
  process.exit(1);
}

const body = gzipped ? gzipSync(Buffer.from(text, 'utf8')) : Buffer.from(text, 'utf8');
const bodyOut = `${out}.envelope${gzipped ? '.gz' : ''}`;
writeFileSync(bodyOut, body);

const meta = JSON.parse(readFileSync(metaPath, 'utf8'));
meta.headers['Content-Length'] = [String(body.length)];
meta.bodySize = body.length;
writeFileSync(`${out}.headers.json`, `${JSON.stringify(meta, null, 2)}\n`);
console.log(`sanitize: wrote ${bodyOut} (${body.length} bytes) and ${out}.headers.json`);
}

// Run as a script (not imported by the test). Both sides are resolved, so a
// symlinked path (macOS /tmp is /private/tmp) still counts as this file.
if (process.argv[1] && realpathSync(process.argv[1]) === realpathSync(fileURLToPath(import.meta.url))) main();
