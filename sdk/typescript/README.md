# @synehq/lumen-js

Official TypeScript/JavaScript client SDK for the [Lumen](https://github.com/SyneHQ/lumen) event ingestion service.

Events are buffered client-side and flushed in batches over Connect RPC (JSON), so `track()`/`identify()` calls are fire-and-forget: non-blocking, never throw, and never hold up your app.

## Install

```bash
npm install @synehq/lumen-js
```

## Usage

```ts
import { Lumen } from '@synehq/lumen-js';

const lumen = new Lumen('lum_live_...'); // ingest key from your Lumen admin

lumen.track('Page View', { path: '/dashboard' });

lumen.identify('user_123', { email: 'jane@example.com', plan: 'pro' });

// On logout:
lumen.reset();
```

That's it for the common case — the SDK auto-flushes on an interval, on tab backgrounding, and on tab/window close, and manages anonymous ID, session ID, and session rotation for you.

## API

### `new Lumen(ingestKey, options?)`

| Option | Default | Description |
|---|---|---|
| `endpoint` | `http://localhost:50051` | Base URL of your Lumen ingest server |
| `batchSize` | `50` | Max events buffered before an automatic flush |
| `flushIntervalMs` | `5000` | How often the queue auto-flushes |

Throws if `ingestKey` is falsy.

### `track(name, props?)`

Queues an event. Non-blocking, swallows all errors internally.

```ts
lumen.track('Checkout Completed', { amount: 42.0, currency: 'USD' });
```

### `identify(userId, traits?)`

Links the current anonymous visitor to an authenticated user ID and sends traits immediately (not queued — this is its own RPC, not a batched event).

```ts
lumen.identify('user_123', { email: 'jane@example.com' });
```

### `reset()`

Call on logout. Ends the current session (`session_end`), generates a fresh anonymous ID and session ID, clears the identified user, and starts a new session (`session_start`).

### `flush()`

Forces an immediate flush of any queued events. Rarely needed — the SDK flushes automatically on its interval, on `visibilitychange` (tab hidden), and on `beforeunload` (tab/window close).

## What gets sent

Every event automatically carries:

- **Identity**: `anon_id` (persisted in `localStorage`), `user_id` (once identified), `session_id`
- **Page context**: `url`, `referrer`, `user_agent`
- **Device**: `screen_w`/`screen_h`, `viewport_w`/`viewport_h`, `locale`, `timezone`

Browser/OS/device-type classification and GeoIP (country/region/city) are derived server-side from `user_agent` and the request IP — nothing extra to send for those.

## Session lifecycle

The SDK emits explicit `session_start` / `session_end` events (in addition to the session boundaries Lumen derives from event timestamps), paired around:

- SDK initialization (`session_start`) and tab/window close (`session_end`)
- `reset()` — closes the outgoing session, opens a new one
- Automatic session rotation after 30 minutes of inactivity or 24 hours since session start

## Browser support

Built for browser environments (uses `window`, `document`, `navigator`, `localStorage`, `fetch`). Calls degrade gracefully to no-ops in non-browser contexts (e.g. SSR) rather than throwing.

## License

Apache-2.0
