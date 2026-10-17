// Tests of texts(), which unpacks recorded bodies for the leak check.
// node --test (part of make test).
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { deflateRawSync, gzipSync } from 'node:zlib';
import { texts } from './leaks.mjs';

// zip builds a zip file with deflated entries, as sentry-cli writes bundles.
function zip(files) {
  const locals = [];
  const central = [];
  let offset = 0;
  for (const [name, content] of Object.entries(files)) {
    const data = deflateRawSync(Buffer.from(content));
    const nameBytes = Buffer.from(name);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(8, 8);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(content.length, 22);
    local.writeUInt16LE(nameBytes.length, 26);
    const dir = Buffer.alloc(46);
    dir.writeUInt32LE(0x02014b50, 0);
    dir.writeUInt16LE(8, 10);
    dir.writeUInt32LE(data.length, 20);
    dir.writeUInt32LE(content.length, 24);
    dir.writeUInt16LE(nameBytes.length, 28);
    dir.writeUInt32LE(offset, 42);
    locals.push(local, nameBytes, data);
    central.push(dir, nameBytes);
    offset += 30 + nameBytes.length + data.length;
  }
  const centralBytes = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(files).length, 10);
  end.writeUInt32LE(centralBytes.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, centralBytes, end]);
}

test('texts unpacks a multipart upload of a gzip chunk of a zip bundle', () => {
  const bundle = zip({ 'manifest.json': '{"files":{}}', 'files/_/_/main.js.map': '{"sources":["/Users/jan/app/src/main.ts"]}' });
  const body = Buffer.concat([
    Buffer.from('--b0undary\r\nContent-Disposition: form-data; name="file_gzip"; filename="abc"\r\n\r\n'),
    gzipSync(bundle),
    Buffer.from('\r\n--b0undary--\r\n'),
  ]);
  const all = texts(body).join('\n');
  assert.match(all, /\/Users\/jan\/app\/src\/main\.ts/);
  assert.match(all, /manifest\.json/);
  assert.match(all, /filename="abc"/);
});

test('texts leaves plain text and gzip text readable', () => {
  assert.deepEqual(texts(Buffer.from('{"a":1}')), ['{"a":1}']);
  assert.deepEqual(texts(gzipSync('{"b":2}')), ['{"b":2}']);
});
