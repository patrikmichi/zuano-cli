// Writes internal/signing/testdata/publisher-signature.json from the gateway's own signing code, so the Go
// side is tested against what the gateway verifies. Run with bun, naming the gateway checkout:
//   ZUANO_GATEWAY_ROOT=<abs>/gateway bun run <abs>/scripts/publisher-signing-vector.ts > internal/signing/testdata/publisher-signature.json
import { createPrivateKey, sign } from 'node:crypto';

const root = process.env.ZUANO_GATEWAY_ROOT;
if (!root) throw new Error('set ZUANO_GATEWAY_ROOT to the gateway directory');
// The gateway's modules import a Next.js marker package that only exists in its bundler.
Bun.plugin({ name: 'server-only', setup: (build) => build.module('server-only', () => ({ exports: {}, loader: 'object' })) });
const { versionPayload, verifyWith } = await import(`${root}/lib/workflow-market/signing.ts`);
const { manifestDigest } = await import(`${root}/lib/workflows/manifest.ts`);

// A fixed seed, so the signature is the same on every run (Ed25519 is deterministic).
const seed = Buffer.from('000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f', 'hex');
const key = createPrivateKey({ key: Buffer.concat([Buffer.from('302e020100300506032b657004220420', 'hex'), seed]), format: 'der', type: 'pkcs8' });
const publicKey = (key.export({ format: 'jwk' }) as { x: string }).x;

// Characters that JSON encoders escape differently, a nested object, a fraction, and the authoring-only keys the digest drops.
const manifest = {
  schema: 'zuano.workflow/v1',
  title: 'Invoice check <é> & more',
  summary: 'Checks "invoices" ✓',
  version: '1.2.0',
  category: 'finance',
  limits: { maxSteps: 12, timeoutSeconds: 30.5 },
  input: { type: 'object', properties: { amount: { type: 'number' }, note: { type: 'string' } }, required: ['amount'] },
  steps: [
    { id: 'fetch', kind: 'tool', description: 'authoring only', tool: 'gmail.search', params: { q: 'invoice', n: 3 } },
    { id: 'notify', kind: 'tool', tool: 'slack.post', params: { text: 'done   line' }, 'x-studio': { x: 1, y: 2 } },
  ],
  'x-studio': { zoom: 1 },
};

const record = { handle: 'acme', slug: 'invoice-check', version: '1.2.0', contentHash: manifestDigest(manifest), manifest };
const payload: Buffer = versionPayload(record);
const signature = sign(null, payload, key).toString('base64');
if (!verifyWith(publicKey, payload, signature)) throw new Error('the gateway does not verify its own vector');

console.log(JSON.stringify({ seed_hex: seed.toString('hex'), public_key: publicKey, handle: record.handle, slug: record.slug, version: record.version, content_hash: record.contentHash, manifest, payload: payload.toString('utf8'), signature }, null, 2));
