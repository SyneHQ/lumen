// Lumen web analytics (cookieless, no ids, no storage). Usage: README.md.
(function () {
  'use strict';
  var w = window, d = document, l = w.location, s = d.currentScript;
  if (!s) return;
  var key = s.getAttribute('data-key');
  var api = (s.getAttribute('data-api') || new URL(s.src).origin).replace(/\/$/, '');
  var useHash = s.hasAttribute('data-hash');
  var h = l.hostname;
  var local = l.protocol === 'file:' || h === '127.0.0.1' || h === '[::1]' || /(^|\.)localhost$/.test(h);
  var bot = (w.navigator || {}).webdriver === true || w._phantom || w.__nightmare || w.Cypress;
  var skip = !key || bot || w.lumenIgnore === true || (local && !s.hasAttribute('data-allow-localhost'));
  var first = true, last = null;

  function uuid() {
    var b = new Uint8Array(16);
    crypto.getRandomValues(b);
    var t = Date.now();
    for (var i = 5; i >= 0; i--) { b[i] = t % 256; t = Math.floor(t / 256); }
    b[6] = b[6] & 15 | 112; // version 7
    b[8] = b[8] & 63 | 128; // variant
    var x = Array.from(b, function (v) { return (v + 256).toString(16).slice(1); }).join('');
    return x.replace(/^(.{8})(.{4})(.{4})(.{4})/, '$1-$2-$3-$4-');
  }

  function b64(str) {
    return btoa(String.fromCharCode.apply(null, new TextEncoder().encode(str)));
  }

  function send(events) {
    var sc = w.screen;
    var ctx = { sdk: 'web', url: l.href, screen_w: sc.width, screen_h: sc.height, viewport_w: w.innerWidth, viewport_h: w.innerHeight };
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
  w.addEventListener('pageshow', function (e) { if (e.persisted) { last = null; pageview(); } });
  // Prerender/hidden: wait until shown.
  if (/^(prerender|hidden)$/.test(d.visibilityState)) d.addEventListener('visibilitychange', function f() {
    if (d.visibilityState === 'visible') { d.removeEventListener('visibilitychange', f); pageview(); }
  });
  else pageview();
})();
