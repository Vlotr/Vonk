// Makes a request recorded by tools/capture a fixture: it refuses to write
// anything that still names this machine's user or host (the shared leak
// check in tools/capture/leaks.mjs), then copies the body exactly as
// posthog-js sent it (still gzip) and the request's method, path, query and
// headers, without the time it was received.
//
//   node sanitize.mjs <NNNN-....body> <NNNN-....json> <out>
//
// writes <out>.body (when the request had one) and <out>.request.json.
import { readFileSync, writeFileSync } from 'node:fs';
import { gunzipSync } from 'node:zlib';
import { findLeaks, machine } from '../../../../tools/capture/leaks.mjs';

const [bodyPath, metaPath, out] = process.argv.slice(2);
if (!bodyPath || !metaPath || !out) {
  console.error('usage: node sanitize.mjs <captured.body> <captured.json> <out>');
  process.exit(2);
}

const body = readFileSync(bodyPath);
const meta = JSON.parse(readFileSync(metaPath, 'utf8'));
delete meta.received;
const text = (body[0] === 0x1f && body[1] === 0x8b ? gunzipSync(body) : body).toString('utf8');
for (const leak of findLeaks(text + JSON.stringify(meta), machine())) {
  console.error(`sanitize: the request still contains ${JSON.stringify(leak)}; do not commit it`);
  process.exit(1);
}

if (body.length > 0) writeFileSync(`${out}.body`, body);
writeFileSync(`${out}.request.json`, `${JSON.stringify(meta, null, 2)}\n`);
console.log(`sanitize: wrote ${out}.request.json${body.length > 0 ? ` and ${out}.body (${body.length} bytes)` : ''}`);
