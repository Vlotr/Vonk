// Makes the requests tools/capture recorded from one upload (sentry-cli or
// the vite plugin) fixtures: in order, as NN-METHOD-path.request.json
// (method, path, query and headers, without the receive time) and
// NN-METHOD-path.body (the body exactly as sent). The API token becomes
// "sntrys_fixture", which the replay tests sign in with. It refuses to
// write anything that still names this machine, in the headers or inside
// the uploaded bundles (tools/capture/leaks.mjs unpacks them).
//
//   node sanitize.mjs <captured dir> <out dir>
import { mkdirSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { findLeaks, machine, texts } from '../../../../tools/capture/leaks.mjs';

const fixtureToken = 'sntrys_fixture';

const [from, out] = process.argv.slice(2);
if (!from || !out) {
  console.error('usage: node sanitize.mjs <captured dir> <out dir>');
  process.exit(2);
}

const recorded = readdirSync(from).filter((name) => name.endsWith('.json')).sort();
const fixtures = [];
for (const [i, name] of recorded.entries()) {
  const meta = JSON.parse(readFileSync(join(from, name), 'utf8'));
  const body = readFileSync(join(from, name.replace(/\.json$/, '.body')));
  delete meta.received;
  if (meta.headers.Authorization) meta.headers.Authorization = [`Bearer ${fixtureToken}`];
  for (const text of [JSON.stringify(meta), ...texts(body)]) {
    for (const leak of findLeaks(text, machine())) {
      console.error(`sanitize: ${name} still contains ${JSON.stringify(leak)}; do not commit it`);
      process.exit(1);
    }
  }
  const base = `${String(i + 1).padStart(2, '0')}-${name.replace(/^\d+-/, '').replace(/\.json$/, '')}`;
  fixtures.push({ base, meta, body });
}

mkdirSync(out, { recursive: true });
for (const { base, meta, body } of fixtures) {
  writeFileSync(join(out, `${base}.request.json`), `${JSON.stringify(meta, null, 2)}\n`);
  if (body.length > 0) writeFileSync(join(out, `${base}.body`), body);
  console.log(`sanitize: wrote ${join(out, base)}`);
}
