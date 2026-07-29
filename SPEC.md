# Lumen SDK Specification contract

This document defines the normative behavior contract shared across all Lumen SDKs (Go, Python, TypeScript).

## 1. Identity & Event Identifiers

- `event_id`: Must be a client-generated **UUIDv7** string per event. Enables server-side batch deduplication.
- `anon_id`: Must be a client-generated **UUIDv7** string assigned on first client init and persisted locally.
  - Browser: stored in `localStorage` under team origin.
  - Native (Go/Python): per-process UUIDv7 or caller-supplied identity.
- `user_id`: String identifier passed during `identify(user_id)`. Defaults to empty string `""` until identified.

## 2. Session Management

- Sessions are client-owned. The SDK generates a `session_id` (UUIDv7).
- A session automatically rotates (generates a new `session_id`) after **30 minutes of inactivity** (no `track` calls).
- A session has a hard lifetime cap of **24 hours**, after which a new `session_id` is issued regardless of activity.
- Calling `reset()` creates a new `anon_id`, a new `session_id`, and clears `user_id`.

## 3. Method Semantics

- `identify(user_id, traits)`:
  1. Sets `user_id` on all subsequent `track` calls within the SDK context.
  2. Emits an `Identify` RPC containing `{anon_id, user_id, traits_json}`.
- `track(event_name, properties)`:
  1. Appends an `Event` to the client buffer with `event_id` (UUIDv7) and client-side timestamp (`ts_unix_ms`).
  2. Merges super properties with per-event properties (per-event properties override super properties).
- `reset()`:
  1. Clears `user_id` and super properties.
  2. Generates new `anon_id` and `session_id`.

## 4. Batching & Queue Limits

| Language | Batch Size | Flush Interval | Queue Cap | Overflow Policy |
|---|---|---|---|---|
| **TypeScript (Browser)** | 50 events | 5 seconds | 1,000 events | Drop oldest, increment `dropped_count` metric |
| **Go / Python (Server)** | 500 events | 1 second | 10,000 events | Drop oldest, non-blocking |

- **Flush triggers**: Queue reaching batch threshold, flush interval timer firing, or explicit `flush()` / `close()` call.
- **Page Unload (Browser)**: Flush remaining events on `visibilitychange -> hidden` via `navigator.sendBeacon` or `fetch` with `keepalive: true`.

## 5. Transport & Retries

- **Transports**:
  - Browser: Connect protocol over HTTP/1.1 (JSON POST to `/lumen.v1.IngestService/Track`).
  - Go / Python: Connect/gRPC over HTTP/2 with Protobuf encoding.
- **Retry Strategy**:
  - Exponential backoff with full jitter. Max 5 retry attempts.
  - Retry ONLY on transient errors: `UNAVAILABLE`, `RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`, or HTTP 502/503/504.
  - NEVER retry client errors (`INVALID_ARGUMENT`, `UNAUTHENTICATED`, HTTP 400/401/403).

## 6. Host Application Integrity

- **Non-blocking & Non-throwing**: All SDK API calls are fire-and-forget and MUST NOT throw unhandled exceptions or block the application's execution thread.
