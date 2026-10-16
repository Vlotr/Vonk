// The leak check for fixtures recorded with tools/capture: what in a
// recorded request still names this machine. Every recipe's sanitize step
// refuses to write a fixture with a leak, and fixtures.test.mjs checks all
// committed fixtures with it (node --test, part of make test).
import { hostname, userInfo } from 'node:os';

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

/** machine returns this machine's user and host name, for findLeaks. */
export function machine() {
  return { user: userInfo().username, host: hostname() };
}
