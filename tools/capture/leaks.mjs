// The leak check for fixtures recorded with tools/capture: what in a
// recorded request still names this machine. Every recipe's sanitize step
// refuses to write a fixture with a leak, and fixtures.test.mjs checks all
// committed fixtures with it (node --test, part of make test).
import { hostname, userInfo } from 'node:os';
import { gunzipSync, inflateRawSync } from 'node:zlib';

const escapeRegExp = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// NEUTRAL names are ordinary words of the recordings: path segments such
// as /var/www/app, Laravel's "web" middleware, Chrome and Mac in a
// User-Agent, npm and Desktop in a recipe's paths. A user or host with
// such a name cannot be told from them, so it is not looked for; neither
// is a name under 3 characters.
const NEUTRAL = new Set(['app', 'www', 'var', 'web', 'mac', 'chrome', 'desktop', 'npm']);
const lookedFor = (name) => Boolean(name) && name.length >= 3 && !NEUTRAL.has(name.toLowerCase());

/**
 * findLeaks returns what in text still names this machine: the user name as
 * a whole path segment (/Users/jan/, C:\\Users\\jan\\, also JSON's \/jan\/),
 * the host name or its first label as a whole host token (not "web" inside
 * "web-1", "webshop" or "web.example.com"), and the app directory anywhere.
 * User and host are matched in the exact case the system reports them, as
 * paths and SDKs write them: a host named "mac" is no match for "Mac OS X"
 * in a User-Agent. Whole segments and tokens keep short names such as
 * "jan" from matching ordinary words; names that are themselves ordinary
 * words of the recordings (NEUTRAL, such as "web" or "app"), names under 3
 * characters and "localhost" are not looked for.
 */
export function findLeaks(text, { user, host, appDir }) {
  const leaks = [];
  if (lookedFor(user) && new RegExp(`(^|[\\\\/])${escapeRegExp(user)}([\\\\/]|$)`).test(text)) leaks.push(user);
  for (const name of new Set(host ? [host, host.split('.')[0]] : [])) {
    if (!lookedFor(name) || name === 'localhost') continue;
    if (new RegExp(`(^|[^A-Za-z0-9.-])${escapeRegExp(name)}($|[^A-Za-z0-9.-])`).test(text)) leaks.push(name);
  }
  if (appDir && text.includes(appDir)) leaks.push(appDir);
  return leaks;
}

/** machine returns this machine's user and host name, for findLeaks. */
export function machine() {
  return { user: userInfo().username, host: hostname() };
}

/**
 * texts returns what a recorded body holds as text, unpacked the way the
 * clients packed it, so findLeaks sees everything: gzip is decompressed, a
 * multipart body (sentry-cli's chunk uploads) is split into its parts, and
 * a zip (an artifact bundle) into its files, at any depth.
 */
export function texts(data) {
  if (data.length >= 2 && data[0] === 0x1f && data[1] === 0x8b) return texts(gunzipSync(data));
  if (isZip(data)) return zipEntries(data).flatMap(texts);
  const parts = multipartParts(data);
  if (parts) return parts.flatMap((p) => [p.headers, ...texts(p.body)]);
  return [data.toString('utf8')];
}

const isZip = (b) => b.length >= 4 && b[0] === 0x50 && b[1] === 0x4b && (b[2] === 3 || b[2] === 5);

/** zipEntries returns the files of a zip, uncompressed (stored or deflate). */
function zipEntries(zip) {
  let end = -1;
  for (let i = zip.length - 22; i >= 0; i--) {
    if (zip.readUInt32LE(i) === 0x06054b50) {
      end = i;
      break;
    }
  }
  if (end < 0) throw new Error('zip without an end of central directory');
  const entries = [];
  let at = zip.readUInt32LE(end + 16);
  for (let n = zip.readUInt16LE(end + 10); n > 0; n--) {
    if (zip.readUInt32LE(at) !== 0x02014b50) throw new Error('zip central directory is damaged');
    const method = zip.readUInt16LE(at + 10);
    const size = zip.readUInt32LE(at + 20);
    const nameLength = zip.readUInt16LE(at + 28);
    const local = zip.readUInt32LE(at + 42);
    const name = zip.subarray(at + 46, at + 46 + nameLength).toString('utf8');
    const start = local + 30 + zip.readUInt16LE(local + 26) + zip.readUInt16LE(local + 28);
    const raw = zip.subarray(start, start + size);
    entries.push(Buffer.from(name), method === 8 ? inflateRawSync(raw) : raw);
    at += 46 + nameLength + zip.readUInt16LE(at + 30) + zip.readUInt16LE(at + 32);
  }
  return entries;
}

/** multipartParts splits a multipart/form-data body, or returns null for anything else. */
function multipartParts(data) {
  const firstLine = data.indexOf('\r\n');
  if (firstLine < 3 || data[0] !== 0x2d || data[1] !== 0x2d) return null;
  const boundary = data.subarray(0, firstLine);
  const parts = [];
  let at = firstLine + 2;
  for (;;) {
    const next = data.indexOf(boundary, at);
    if (next < 0) break;
    const part = data.subarray(at, next - 2); // without the CRLF before the boundary
    const split = part.indexOf('\r\n\r\n');
    if (split >= 0) parts.push({ headers: part.subarray(0, split).toString('utf8'), body: part.subarray(split + 4) });
    at = next + boundary.length + 2;
  }
  return parts;
}
