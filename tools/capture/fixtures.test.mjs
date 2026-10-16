// Guard for every committed fixture: no file under internal/*/testdata
// (gzip bodies decompressed, multipart uploads and zip bundles unpacked)
// may name this machine's user or host. Run on the machine that recorded
// a fixture, it catches a recording committed without its recipe's
// sanitize step. node --test (part of make test).
import assert from 'node:assert/strict';
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';
import { findLeaks, machine, texts } from './leaks.mjs';

const root = fileURLToPath(new URL('../../internal/', import.meta.url));

// fixtures lists the files under each package's testdata, without the
// recipes that make them (capture/) and their npm or Composer packages.
function fixtures() {
  const files = [];
  const walk = (dir) => {
    for (const name of readdirSync(dir)) {
      if (name === 'capture' || name === 'node_modules' || name === 'vendor') continue;
      const path = join(dir, name);
      if (statSync(path).isDirectory()) walk(path);
      else files.push(path);
    }
  };
  for (const pkg of readdirSync(root)) {
    const testdata = join(root, pkg, 'testdata');
    try {
      if (statSync(testdata).isDirectory()) walk(testdata);
    } catch {
      // no testdata in this package
    }
  }
  return files;
}

test('committed fixtures do not name this machine', () => {
  const files = fixtures();
  assert.ok(files.length > 0, 'no fixtures found');
  for (const file of files) {
    // Gzip bodies decompressed, multipart uploads and zip bundles unpacked.
    for (const text of texts(readFileSync(file))) {
      assert.deepEqual(findLeaks(text, machine()), [], file);
    }
  }
});
