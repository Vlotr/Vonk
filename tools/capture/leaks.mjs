// The leak check for fixtures recorded with tools/capture: what in a
// recorded request still names this machine. Every recipe's sanitize step
// refuses to write a fixture with a leak, and fixtures.test.mjs checks all
// committed fixtures with it (node --test, part of make test).
import { hostname, userInfo } from 'node:os';

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
