// Tests for the leak check of sanitize.mjs: node --test (in this directory).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { findLeaks } from './sanitize.mjs';

test('finds the user name as a path segment, in every spelling', () => {
  for (const text of [
    '{"abs_path":"/Users/jan/app/index.php"}',
    '{"abs_path":"\\/home\\/jan\\/app\\/index.php"}', // PHP's json_encode
    '{"abs_path":"C:\\\\Users\\\\jan\\\\app"}',
  ]) {
    assert.deepEqual(findLeaks(text, { user: 'jan' }), ['jan'], text);
  }
});

test('short user names in ordinary words are no leak', () => {
  const text = '{"email":"jan@example.com","message":"January sale","url":"https://www.example.com/web/"}';
  assert.deepEqual(findLeaks(text, { user: 'jan' }), []);
  assert.deepEqual(findLeaks(text, { user: 'www' }), []);
  assert.deepEqual(findLeaks('{"middleware":"web"}', { user: 'web' }), []);
});

test('finds the host name as a whole host token', () => {
  assert.deepEqual(findLeaks('{"server_name":"Jans-MacBook.local"}', { host: 'Jans-MacBook.local' }), ['Jans-MacBook.local']);
  assert.deepEqual(findLeaks('{"server_name":"Jans-MacBook"}', { host: 'Jans-MacBook.local' }), ['Jans-MacBook']);
  assert.deepEqual(findLeaks('{"kernel":"Darwin web 25.0.0"}', { host: 'web' }), ['web']);
});

test('host names inside other names or in another case, and localhost, are no leak', () => {
  const ua = '{"User-Agent":["Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7)"]}';
  assert.deepEqual(findLeaks(ua, { host: 'mac.home' }), []);
  const text = '{"server_name":"web-1","url":"http://localhost:8091/webshop","host":"web.example.com"}';
  assert.deepEqual(findLeaks(text, { host: 'web' }), []);
  assert.deepEqual(findLeaks(text, { host: 'localhost' }), []);
});

test('finds the app directory anywhere', () => {
  assert.deepEqual(findLeaks('{"file":"/private/tmp/php-app/public/index.php"}', { appDir: '/private/tmp/php-app' }), [
    '/private/tmp/php-app',
  ]);
});
