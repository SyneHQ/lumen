// Run after `npm run build`: node test/options.check.mjs
import assert from 'node:assert/strict';

const store = new Map();
globalThis.localStorage = { getItem: (k) => store.get(k) ?? null, setItem: (k, v) => store.set(k, v) };
globalThis.window = { location: { href: 'https://example.com/' }, innerWidth: 390, innerHeight: 800, addEventListener() {} };
globalThis.document = { referrer: '', visibilityState: 'visible', addEventListener() {} };
globalThis.screen = { width: 390, height: 844 };
const sent = [];
globalThis.fetch = (url, init) => { sent.push(JSON.parse(init.body)); return Promise.resolve(); };
const { Lumen } = await import('../dist/index.js');

const strict = new Lumen('k', { endpoint: 'x', persist: false, deviceInfo: false });
strict.track('page_view');
strict.flush();
assert.equal(store.size, 0, 'persist:false stores nothing');
const o = sent.at(-1).events.at(-1).overrides;
assert.equal(o.user_agent, ''); assert.equal(o.screen_w, 0); assert.equal(o.viewport_w, 0);
strict.reset();
assert.equal(store.size, 0, 'reset with persist:false stores nothing');

const dflt = new Lumen('k', { endpoint: 'x' });
dflt.track('page_view'); dflt.flush();
assert.ok(store.get('lumen_anon_id'), 'default persists the anon id');
assert.equal(sent.at(-1).events.at(-1).overrides.screen_w, 390, 'default sends device info');
console.log('ok');
process.exit(0);
