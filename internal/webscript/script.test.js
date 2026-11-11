// Behaviour tests for script.js: node --test internal/webscript/
'use strict';
const test = require('node:test');
const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const src = fs.readFileSync(path.join(__dirname, 'script.js'), 'utf8');

function run({ href = 'https://shop.example/a?x=1', attrs = {}, pre } = {}) {
  const calls = [];
  const listeners = {};
  const u = new URL(href);
  const location = { href, hostname: u.hostname, protocol: u.protocol, pathname: u.pathname, search: u.search, hash: u.hash };
  const nav = (url) => { const n = new URL(url, location.href); Object.assign(location, { href: n.href, pathname: n.pathname, search: n.search, hash: n.hash }); };
  const a = Object.assign({ 'data-key': 'KEY', src: 'https://lumen.example/js/script.js' }, attrs);
  const forbid = (n) => ({ get() { throw new Error(n + ' touched'); } });
  const win = {
    location, innerWidth: 800, innerHeight: 600, screen: { width: 1920, height: 1080 },
    addEventListener: (t, f) => { (listeners[t] = listeners[t] || []).push(f); },
    fetch: (url, opts) => { calls.push({ url, opts, body: JSON.parse(opts.body) }); return Promise.resolve({}); },
    history: { pushState: (s, t, url) => nav(url), replaceState: (s, t, url) => nav(url) },
    document: {
      referrer: 'https://google.com/',
      currentScript: { src: a.src, getAttribute: (k) => (k in a ? a[k] : null), hasAttribute: (k) => k in a },
    },
    crypto: globalThis.crypto, btoa, TextEncoder, URL, Uint8Array, Date, Math, JSON, String, Array,
  };
  Object.defineProperty(win, 'localStorage', forbid('localStorage'));
  Object.defineProperty(win, 'sessionStorage', forbid('sessionStorage'));
  Object.defineProperty(win, 'indexedDB', forbid('indexedDB'));
  Object.defineProperty(win.document, 'cookie', { get: forbid('cookie').get, set() { throw new Error('cookie set'); } });
  win.window = win;
  if (pre) pre(win);
  vm.runInNewContext(src, vm.createContext(win));
  return { win, calls, listeners, events: () => calls.flatMap((c) => c.body.events) };
}

test('initial pageview body + headers', () => {
  const { calls } = run();
  assert.strictEqual(calls.length, 1);
  const { url, opts, body } = calls[0];
  assert.strictEqual(url, 'https://lumen.example/lumen.v1.IngestService/Track');
  assert.strictEqual(opts.method, 'POST');
  assert.strictEqual(opts.keepalive, true);
  assert.deepStrictEqual({ ...opts.headers }, { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', Authorization: 'Bearer KEY' });
  const c = body.context;
  assert.strictEqual(c.url, 'https://shop.example/a?x=1');
  assert.strictEqual(c.referrer, 'https://google.com/');
  assert.deepStrictEqual([c.screen_w, c.screen_h, c.viewport_w, c.viewport_h], [1920, 1080, 800, 600]);
  for (const k of ['anon_id', 'user_id', 'session_id']) assert.ok(!(k in c), k);
  const e = body.events[0];
  assert.strictEqual(e.name, '$pageview');
  assert.match(e.event_id, /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
  assert.ok(Math.abs(e.ts_unix_ms - Date.now()) < 5000);
  assert.strictEqual(Buffer.from(e.props_json, 'base64').toString(), '{}');
});

test('pushState sends one pageview, same path not duplicated, referrer only first', () => {
  const { win, calls, events, listeners } = run();
  win.history.pushState({}, '', '/b');
  assert.strictEqual(calls.length, 2);
  assert.strictEqual(calls[1].body.context.url, 'https://shop.example/b');
  assert.ok(!('referrer' in calls[1].body.context));
  win.history.replaceState({}, '', '/b');
  win.history.pushState({}, '', '/b#frag');
  listeners.popstate.forEach((f) => f());
  assert.strictEqual(events().length, 2);
  win.history.pushState({}, '', '/b?y=2');
  assert.strictEqual(events().length, 3);
});

test('localhost skipped; data-allow-localhost tracks', () => {
  for (const href of ['http://localhost:3000/', 'http://127.0.0.1/', 'http://[::1]/', 'http://app.localhost/', 'file:///x.html']) {
    assert.strictEqual(run({ href }).calls.length, 0, href);
  }
  assert.strictEqual(run({ href: 'http://localhost:3000/', attrs: { 'data-allow-localhost': '' } }).calls.length, 1);
});

test('lumenIgnore skips', () => {
  assert.strictEqual(run({ pre: (w) => { w.lumenIgnore = true; } }).calls.length, 0);
});

test('data-api overrides endpoint', () => {
  assert.strictEqual(run({ attrs: { 'data-api': 'https://proxy.example/' } }).calls[0].url, 'https://proxy.example/lumen.v1.IngestService/Track');
});

test('window.lumen custom events and pre-load queue', () => {
  const { win, events } = run({
    pre: (w) => { w.lumen = function () { (w.lumen.q = w.lumen.q || []).push(arguments); }; w.lumen('early', { a: 1 }); },
  });
  win.lumen('signup', { plan: 'pro' });
  const ev = events();
  assert.deepStrictEqual(ev.map((e) => e.name), ['early', '$pageview', 'signup']);
  assert.strictEqual(Buffer.from(ev[2].props_json, 'base64').toString(), '{"plan":"pro"}');
  assert.doesNotThrow(() => win.lumen());
});
