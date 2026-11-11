// Lumen web analytics. Cookieless: no ids, no cookies, no storage.
//   <script defer data-key="KEY" src="https://<lumen-host>/js/script.js"></script>
// Options: data-api (endpoint), data-hash (count hash changes),
// data-allow-localhost. Custom events: lumen('signup', {plan: 'pro'});
// to call before load, add first:
//   <script>window.lumen = window.lumen || function () { (lumen.q = lumen.q || []).push(arguments) }</script>
(function () {
  'use strict';
  var w = window, d = document, l = w.location, s = d.currentScript;
  if (!s) return;
  var key = s.getAttribute('data-key');
  var api = (s.getAttribute('data-api') || new URL(s.src).origin).replace(/\/$/, '');
  var useHash = s.hasAttribute('data-hash');
  var h = l.hostname;
  var local = l.protocol === 'file:' || h === '127.0.0.1' || h === '[::1]' || /(^|\.)localhost$/.test(h);
  var skip = !key || w.lumenIgnore === true || (local && !s.hasAttribute('data-allow-localhost'));
  var first = true, last = null;

  function uuid() {
    var b = new Uint8Array(16);
    crypto.getRandomValues(b);
    var t = Date.now();
    for (var i = 5; i >= 0; i--) { b[i] = t % 256; t = Math.floor(t / 256); }
    b[6] = (b[6] & 0x0f) | 0x70;
    b[8] = (b[8] & 0x3f) | 0x80;
    var x = Array.from(b, function (v) { return (v + 256).toString(16).slice(1); }).join('');
    return x.replace(/^(.{8})(.{4})(.{4})(.{4})/, '$1-$2-$3-$4-');
  }

  function b64(str) {
    return btoa(String.fromCharCode.apply(null, new TextEncoder().encode(str)));
  }

  function send(events) {
    var sc = w.screen || {};
    var ctx = {
      sdk: 'web', url: l.href,
      screen_w: sc.width || 0, screen_h: sc.height || 0,
      viewport_w: w.innerWidth || 0, viewport_h: w.innerHeight || 0
    };
    if (first && d.referrer) ctx.referrer = d.referrer;
    first = false;
    try {
      fetch(api + '/lumen.v1.IngestService/Track', {
        method: 'POST', keepalive: true,
        headers: { 'Content-Type': 'application/json', 'Connect-Protocol-Version': '1', Authorization: 'Bearer ' + key },
        body: JSON.stringify({ context: ctx, events: events })
      }).catch(function () {});
    } catch (e) {}
  }

  function track(name, props) {
    try {
      if (skip || !name) return;
      // Sent at once with keepalive: nothing to flush on pagehide.
      send([{ event_id: uuid(), ts_unix_ms: Date.now(), name: String(name), props_json: b64(JSON.stringify(props || {})) }]);
    } catch (e) {}
  }

  function pageview() {
    var p = l.pathname + l.search + (useHash ? l.hash : '');
    if (p === last) return;
    last = p;
    track('$pageview');
  }

  var q = (w.lumen && w.lumen.q) || [];
  w.lumen = track;
  for (var i = 0; i < q.length; i++) track(q[i][0], q[i][1]);
  if (skip) return;

  ['pushState', 'replaceState'].forEach(function (m) {
    var orig = history[m];
    history[m] = function () { var r = orig.apply(this, arguments); pageview(); return r; };
  });
  w.addEventListener('popstate', pageview);
  if (useHash) w.addEventListener('hashchange', pageview);
  pageview();
})();
