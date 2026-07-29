# Lumen — architecture plan

Event ingestion service. Go + gRPC/Connect. ClickHouse for events, per-team row policies.
Teams query their own events through the existing Syne dashboard — no new query path.
SDKs for Go, Python, TypeScript.

**Stack decision (settled):** ClickHouse for events, Postgres for the control plane,
no ORM. `CLICKHOUSE` is already a `DatabaseType` and `clickhouse-go/v2` is already a
dependency in `db.api.go`, so the dashboard query story works unchanged. Postgres for
`tenants`/`api_keys` because those want transactions and unique constraints and the
cluster is already running. GORM buys nothing across two control tables and costs
throughput on the ingest path.

---

# Part I — Service

## 1. What Lumen is (and is not)

**Is:** a gRPC ingest endpoint (`Track`, `Identify`), plus a small internal control plane
(`Provision`, `RotateKey`, `Deprovision`).

**Is not:** a query API. The dashboard already executes ClickHouse through
`src/lib/db/executor.ts` and `db.api.go`. Provisioning writes a normal `Connection` row
(`type: CLICKHOUSE`) with the team's own ClickHouse user. Dashboard needs zero changes.
This is the single biggest scope cut in the design — do not build a query RPC until
someone actually asks for one.

**Boundary:** Lumen never touches the Prisma metadata DB. It owns the `lumen` ClickHouse
database and its own Postgres schema. The Next.js app writes the `Connection` row, so
KMS/tenant encryption stays in TypeScript where it already works
(`src/lib/encryption/*`). No KMS in Go.

## 2. Three principles that shape everything below

1. **Events are immutable.** ClickHouse is bad at mutation and good at scans. Identity
   stitching, sessionization, and enrichment therefore happen at *write* time (cheap,
   in Go) or at *read* time (via views), never by rewriting history.
2. **Parse once, in Go.** User-agent parsing, geo lookup, URL/UTM decomposition, bot
   detection — server side. Three SDKs must not each carry a UA parser that drifts. The
   SDK sends raw signals; the server derives meaning.
3. **Typed columns for what dashboards filter on, JSON for the long tail.** A
   `LowCardinality(String)` column is one to two orders of magnitude faster to
   group/filter than a JSON extraction, and it compresses to near-nothing when repeated.

## 3. Data model

### 3.1 Events — ClickHouse

```sql
CREATE TABLE lumen.events (
  -- identity
  team_id       LowCardinality(String),
  ts            DateTime64(3, 'UTC'),     -- client event time
  name          LowCardinality(String),
  event_id      UUID,                     -- client-generated (UUIDv7), idempotency key
  anon_id       String,                   -- device/browser identity, always present
  user_id       String,                   -- '' until identify()
  session_id    String,

  -- device / app context (SDK-reported)
  sdk           LowCardinality(String),   -- go | python | js
  sdk_version   LowCardinality(String),
  app_version   LowCardinality(String),
  os            LowCardinality(String),
  os_version    LowCardinality(String),
  device_type   LowCardinality(String),   -- desktop | mobile | tablet | server | bot
  device_model  LowCardinality(String),
  manufacturer  LowCardinality(String),
  browser       LowCardinality(String),
  browser_version LowCardinality(String),
  screen_w      UInt16,
  screen_h      UInt16,
  viewport_w    UInt16,
  viewport_h    UInt16,
  locale        LowCardinality(String),
  timezone      LowCardinality(String),

  -- web context (SDK-reported, server-decomposed)
  url           String,
  path          String,
  host          LowCardinality(String),
  referrer      String,
  referrer_host LowCardinality(String),
  utm_source    LowCardinality(String),
  utm_medium    LowCardinality(String),
  utm_campaign  LowCardinality(String),
  utm_term      LowCardinality(String),
  utm_content   LowCardinality(String),

  -- server-derived
  country       LowCardinality(FixedString(2)),
  region        LowCardinality(String),
  city          LowCardinality(String),
  ip            IPv6           CODEC(ZSTD(3)),  -- see §6.4, empty unless team opts in
  ingested_at   DateTime       DEFAULT now(),

  -- long tail
  props         JSON(max_dynamic_paths = 512, SKIP REGEXP '^\\$')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (team_id, name, ts, event_id)
TTL toDateTime(ts) + INTERVAL 12 MONTH;
```

`ORDER BY` leads with `team_id` so the row policy prunes at the index granule, not per
row — that is what keeps tenancy free rather than making it a full scan.

That column list looks heavy; on disk it is not. Every `LowCardinality` column on a
single-app tenant collapses to a dictionary of one or two values and costs a few bits per
row. Sending them per-event on the wire *would* be wasteful, which is why the wire format
sends context once per batch (§5.2), not once per event.

`props JSON` splits each distinct property path into its own sub-column, so filtering on
`props.plan` costs about what a real column costs rather than parsing every row. Two
parameters worth setting deliberately rather than defaulting:

- `max_dynamic_paths = 512` — paths beyond the limit fall into a shared blob that reads
  much slower. The default is 1024; capping lower and alerting on it is better than
  silently degrading. A tenant instrumenting a loop with `props[user_id]` as a *key* will
  otherwise blow the budget and quietly halve their query speed. Monitor distinct path
  count per team and surface it.
- `SKIP REGEXP '^\$'` — `$`-prefixed keys are reserved for the SDK context that already
  has typed columns (§3.1). Skipping them stops the same data being stored twice.

Add type hints (`props.revenue Float64`) later for paths that turn out hot; that is a
pure-win schema change requiring no rewrite.

TTL replaces the partition-maintenance loop entirely: no `pg_partman`, no daily tick, no
`DROP TABLE` cron. Make it a per-team setting later by moving to
`TTL ts + INTERVAL dictGet('retention', ...) DAY` — not now.

### 3.2 Sessions — incrementally materialized, not batch-computed

The SDK owns the session (§5.3): it generates `session_id` and rotates it after 30
minutes of inactivity. The server never holds session state — that would make ingest
stateful and kill horizontal scaling for no benefit.

Session *analytics* are then free via an `AggregatingMergeTree` fed by a materialized
view. No cron, no batch job, updated as events land:

```sql
CREATE TABLE lumen.sessions (
  team_id      LowCardinality(String),
  session_id   String,
  anon_id      String,
  user_id      SimpleAggregateFunction(anyLast, String),
  started_at   SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
  ended_at     SimpleAggregateFunction(max, DateTime64(3, 'UTC')),
  event_count  SimpleAggregateFunction(sum, UInt64),
  entry_path   AggregateFunction(argMin, String, DateTime64(3, 'UTC')),
  exit_path    AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  device_type  SimpleAggregateFunction(anyLast, LowCardinality(String)),
  country      SimpleAggregateFunction(anyLast, LowCardinality(FixedString(2))),
  utm_source   SimpleAggregateFunction(anyLast, LowCardinality(String)),
  referrer_host SimpleAggregateFunction(anyLast, LowCardinality(String))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(started_at)
ORDER BY (team_id, session_id);

CREATE MATERIALIZED VIEW lumen.sessions_mv TO lumen.sessions AS
SELECT
  team_id, session_id, anon_id,
  anyLast(user_id)      AS user_id,
  min(ts)               AS started_at,
  max(ts)               AS ended_at,
  count()               AS event_count,
  argMinState(path, ts) AS entry_path,
  argMaxState(path, ts) AS exit_path,
  anyLast(device_type)  AS device_type,
  anyLast(country)      AS country,
  anyLast(utm_source)   AS utm_source,
  anyLast(referrer_host) AS referrer_host
FROM lumen.events
WHERE session_id != ''
GROUP BY team_id, session_id, anon_id;
```

`SimpleAggregateFunction` for min/max/sum/anyLast means reads need no `-Merge` combinator
— `SELECT * FROM sessions` just works, which matters a lot when the consumer is a team
writing ad-hoc SQL in the dashboard rather than an engineer who knows ClickHouse. Only
`entry_path`/`exit_path` need `argMinMerge(...)`. Ship a `sessions_v` view that wraps the
merges so the team-facing surface is plain columns.

Session duration, bounce rate, entry/exit paths, sessions-per-user all fall out of this
table with no additional machinery.

> Not doing: server-side sessionization for clients that don't send `session_id`. The
> query-time recipe (`neighbor()`/window function over ordered events with a 30-min gap
> rule) covers the rare case. Add a materialized path only if a real integration can't
> keep a session id.

### 3.3 Identity — forward stitching by default, retroactive by view

This is where most analytics products rot. The design splits it deliberately:

**Forward stitching (default, free).** `identify(user_id)` makes the SDK write `user_id`
on every subsequent event and emit one `$identify` event carrying `{anon_id, user_id}`.
No server work, no history rewrite. Covers the overwhelmingly common question: "what did
this logged-in user do?"

**Retroactive stitching (opt-in, read-time).** Pre-login events carry `user_id = ''`.
To attribute them, resolve through a dictionary at query time:

```sql
CREATE TABLE lumen.identities (
  team_id    LowCardinality(String),
  anon_id    String,
  user_id    String,
  updated_at DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (team_id, anon_id);

CREATE DICTIONARY lumen.identity_dict (
  team_id String, anon_id String, user_id String
)
PRIMARY KEY team_id, anon_id
SOURCE(CLICKHOUSE(TABLE 'identities' DB 'lumen'))
LAYOUT(COMPLEX_KEY_HASHED())
LIFETIME(MIN 60 MAX 120);

CREATE VIEW lumen.events_resolved
  DEFINER = lumen_admin SQL SECURITY DEFINER AS
SELECT *, if(user_id != '', user_id,
             dictGetOrDefault('lumen.identity_dict', 'user_id',
                              (team_id, anon_id), anon_id)) AS person_id
FROM lumen.events;
```

**The security catch, stated plainly:** row policies do not apply inside `dictGet`. A team
user granted `dictGet` on `identity_dict` could probe another team's `(team_id, anon_id)`
pairs and read back their `user_id`s. So team users are **never** granted `dictGet` or
`SELECT` on `identities`. They get `SELECT` on the `SQL SECURITY DEFINER` view only, which
runs the lookup as `lumen_admin` while the underlying `events` row policy still filters
the rows — so the `team_id` fed into the dictionary can only ever be their own. Verify
this in the phase-2 isolation test; it is the single subtlest correctness property in the
design.

(`SQL SECURITY DEFINER` on views needs ≥ 24.4, comfortably below the pinned version.)

Anti-goal: Mixpanel-style `alias()` chains and ID-merge graphs. They are a well-known
source of unresolvable support tickets. One anon → one user, last write wins, done.

### 3.4 Control plane — Postgres

```sql
CREATE TABLE lumen_tenants (
  team_id    text PRIMARY KEY,
  ch_user    text UNIQUE NOT NULL,
  created_at timestamptz DEFAULT now(),
  store_ip   boolean NOT NULL DEFAULT false
);
CREATE TABLE lumen_api_keys (
  key_hash   bytea PRIMARY KEY,        -- sha256 of the secret
  key_prefix text NOT NULL,            -- for display: lum_live_a1b2…
  team_id    text NOT NULL REFERENCES lumen_tenants(team_id),
  revoked_at timestamptz
);
CREATE INDEX ON lumen_api_keys (team_id);
```

Own database/schema, own credentials, no foreign keys into Prisma's tables. `pgx/v5`
directly — six queries total.

## 4. Tenancy: ClickHouse users + row policies

```sql
CREATE USER lumen_t_<slug> IDENTIFIED WITH sha256_password BY '<32 random bytes>'
  SETTINGS max_execution_time = 60, max_memory_usage = 4000000000 READONLY;
GRANT SELECT ON lumen.events           TO lumen_t_<slug>;
GRANT SELECT ON lumen.sessions_v       TO lumen_t_<slug>;
GRANT SELECT ON lumen.events_resolved  TO lumen_t_<slug>;
-- deliberately NOT granted: lumen.identities, dictGet on lumen.identity_dict

CREATE ROW POLICY pol_ev_<slug>   ON lumen.events   USING team_id = '<id>' TO lumen_t_<slug>;
CREATE ROW POLICY pol_sess_<slug> ON lumen.sessions USING team_id = '<id>' TO lumen_t_<slug>;

CREATE QUOTA q_<slug> FOR INTERVAL 1 hour MAX queries = 1000, result_rows = 100000000
  TO lumen_t_<slug>;
```

Every table a team can reach needs its own policy — forgetting one on `sessions` would
expose every team's session table. Provisioning must create policies from a single list
in code, not from hand-written DDL, so adding a table can't silently skip one.

Three things Postgres RLS didn't give for free, each one line here: per-user resource
limits (a team cannot take the cluster down with one query), quotas (rate limiting with
no app code), and `READONLY` enforced at the user level.

The ingest user (`lumen_writer`) has `INSERT` only and no row policy: the server resolves
`team_id` from the API key and stamps it, so a policy on write would be redundant cost on
the hot path.

**Note:** ClickHouse row policies are permissive and additive — once any policy exists on
a table, a user with no policy sees nothing. That is the safe direction, but assert it in
the phase-2 test rather than trusting this paragraph.

**"Each user gets its own creds":** the design gives each *team* a user. Per-user is the
same code path with a different key in `lumen_tenants` — and ClickHouse users are far
cheaper than Postgres roles, so per-user is genuinely viable here in a way it wasn't
before. Still defaulting to per-team; say the word.

## 5. Ingest

### 5.1 Transport: Connect, serving gRPC on the same port

Use [connectrpc](https://connectrpc.com/) (`connect-go`) rather than raw `grpc-go`.

The reason is the TypeScript SDK. Browsers cannot speak native gRPC — a browser client
needs gRPC-Web plus an Envoy sidecar to translate. `connect-go` serves the Connect
protocol, gRPC, *and* gRPC-Web from one handler on one port. So:

- Go and Python SDKs use standard gRPC. `grpcurl`, reflection, and any prebuilt gRPC
  client keep working exactly as asked.
- The browser TS SDK talks Connect over HTTP/1.1 with JSON — no proxy, no sidecar, and
  `navigator.sendBeacon` works on page unload because it's a plain HTTP POST.

One proto, three transports, zero infrastructure. Choosing raw grpc-go instead buys an
Envoy deployment and a worse browser story.

### 5.2 Wire format — context once per batch

```proto
service Ingest {
  rpc Track(TrackRequest) returns (TrackResponse);
  rpc TrackStream(stream TrackRequest) returns (stream TrackAck);
  rpc Identify(IdentifyRequest) returns (IdentifyResponse);
}

message Context {                     // sent ONCE per batch, applies to all events
  string anon_id = 1;
  string user_id = 2;
  string session_id = 3;
  string sdk = 4;  string sdk_version = 5;  string app_version = 6;
  string user_agent = 7;              // raw; server parses (principle 2)
  string os = 8;  string os_version = 9;    // native SDKs report directly
  string device_model = 10; string manufacturer = 11;
  uint32 screen_w = 12; uint32 screen_h = 13;
  uint32 viewport_w = 14; uint32 viewport_h = 15;
  string locale = 16; string timezone = 17;
  string url = 18; string referrer = 19;
  bytes  super_props_json = 20;       // merged under per-event props
}

message Event {
  string event_id = 1;                // UUIDv7, client-generated
  int64  ts_unix_ms = 2;
  string name = 3;
  bytes  props_json = 4;
  Context overrides = 5;              // optional, for events that differ mid-batch
}

message TrackRequest { Context context = 1; repeated Event events = 2; }

message IdentifyRequest {
  string anon_id = 1; string user_id = 2; bytes traits_json = 3;
}
```

Context-per-batch is the difference between ~40 bytes and ~400 bytes on the wire per
event for a typical browser payload. The columns are still denormalized onto every row
server-side, where `LowCardinality` makes the repetition free.

### 5.3 Server-side enrichment

Between decode and insert, in Go, per batch:

| derived | from | how |
|---|---|---|
| `browser`, `browser_version`, `os`, `device_type`, `manufacturer` | `user_agent` | `uap-go` (ua-parser), regexes compiled once at boot |
| `country`, `region`, `city` | connection IP | MaxMind GeoLite2 mmdb, memory-mapped |
| `path`, `host` | `url` | `net/url` |
| `referrer_host` | `referrer` | `net/url` |
| `utm_*` | `url` query | `net/url` |
| bot flag → `device_type = 'bot'` | UA | parser's bot list |

UA parsing is the one genuinely CPU-hot step (regex-heavy). Cache it: UA string → parsed
struct in a `ristretto` cache. A single tenant's traffic has a handful of distinct UAs, so
hit rate is ~99% and the cost disappears. Same for IP → geo, keyed on /24.

Native SDKs (Go, Python) skip UA entirely and report `os`/`device_type` directly; those
fields win over anything parsed.

### 5.4 Write path

```
Connect/gRPC handler → validate + enrich → clickhouse-go native batch (Append/Send)
                     → async_insert = 1, wait_for_async_insert = 1
                     → server-side buffer, ClickHouse forms the part
```

Gone versus a Postgres design: sharded channels, per-shard flusher goroutines, unlogged
staging tables, an `INSERT … SELECT … ON CONFLICT` hop, and the partition-maintenance
loop. ClickHouse's async insert *is* the buffering subsystem, written by people who do
this full time.

- `wait_for_async_insert = 1` acks after the part is durable. ~200ms–1s ack latency, and
  it removes the data-loss window a client-side buffer would have. For SDK telemetry that
  trade is obviously right; the SDK is fire-and-forget on its own side.
- Idempotency: set `insert_deduplication_token` to a hash of the batch. An SDK retry of
  the same batch is dropped server-side. This covers the real failure mode exactly; it
  does *not* dedup the same `event_id` arriving in two differently-shaped batches. If
  that matters later, `ReplacingMergeTree` + `FINAL` is the upgrade — not free, so don't
  reach for it preemptively.
- Backpressure: on queue-full, return `RESOURCE_EXHAUSTED`. Never silent drop.
- `Identify` writes one row to `lumen.identities` and one `$identify` event. Low volume,
  synchronous insert, no batching.
- Validation at the trust boundary: event name length, prop count, JSON depth, batch
  size, `ts` sanity window (reject > 24h future, clamp > 1y past). A malformed SDK must
  not be able to write junk that breaks the MV.

Auth: `x-lumen-key` metadata → interceptor → sha256 → `ristretto` cache (60s positive,
10s negative TTL) → `team_id` in context. A revoked key dies within a minute and a
key-guessing flood never reaches Postgres.

Ballpark: expect protobuf decode and UA parsing to top the profile, not the insert.
Phase 1 measures it.

## 6. Ports, ops, privacy

| port | what | exposure |
|---|---|---|
| 50051 | `Ingest` — Connect + gRPC + gRPC-Web on one handler | public, TLS |
| 50052 | `Admin` | cluster-internal only, shared bearer token |
| 9090 | `/metrics`, `/healthz`, grpc health + reflection | internal |

Reflection stays on so `grpcurl` and generated clients work without shipping descriptors.

Stateless — buffering is server-side and sessions are client-side — so scale horizontally
behind any HTTP/2 LB, with no drain logic beyond finishing in-flight RPCs.
OTel + Prometheus, matching what apollo already pulls in.

**Privacy, decided rather than deferred:** IP is used for geo and then dropped. `ip` is
written only when `lumen_tenants.store_ip` is true. Browser SDK sends no cookies; `anon_id`
lives in `localStorage` under the team's own origin. A `$delete_user` admin RPC issues
`ALTER TABLE … DELETE WHERE team_id = ? AND (user_id = ? OR anon_id = ?)` — a lightweight
delete, async, fine for GDPR SLAs.

### 6.1 Self-hosted ClickHouse — version and shape

**Pinned: `clickhouse/clickhouse-server:26.3-lts`** (currently `26.3.17.56-lts`), the same
tag in dev and prod.

Checked against upstream releases: latest stable is `26.5.6.64`, newest tag is
`26.7.1.1315` (a `.1` — first cut of that line, not yet patched). The two current LTS
lines are `26.3` and the older `25.8`. Take the LTS:

- LTS gets ~1 year of patches, so self-hosting doesn't become a monthly upgrade treadmill.
  A `.1` stable release is where other people find the regressions.
- 26.3 clears every feature gate with room to spare — JSON went production-ready in 25.3,
  `SQL SECURITY DEFINER` in 24.4. No fallback branches, no conditional DDL.
- 26.3.17 is seventeen patches deep. That is the whole point of picking it over 26.5.

Deployment shape for phase 1: **single node**, no ZooKeeper/Keeper, `MergeTree` not
`ReplicatedMergeTree`. One node handles this workload far past the point where it stops
being the interesting problem. But write the DDL through a migration layer that can swap
the engine family, because going replicated later means `ReplicatedMergeTree` everywhere
plus a 3-node Keeper ensemble — cheap to plan for, annoying to retrofit blind.

Storage: ZSTD compression (default for this data shape), one fast local NVMe. Set
`max_server_memory_usage_to_ram_ratio` and a real `max_concurrent_queries` before any
team gets creds — the per-user quotas in §4 bound one tenant, not the box.

New infra beyond the server itself: a GeoLite2 mmdb file (needs a free MaxMind account
key, refreshed weekly). Nothing in `docker-compose.yml` runs ClickHouse today; phase 1
adds a single-node service there for local dev on the identical tag.

---

# Part II — SDKs

## 7. Shared behaviour contract

Three SDKs that disagree produce data that can't be joined. Fix the semantics once, in a
`SPEC.md` in this repo, and hold all three to it with the same conformance test:

| concern | rule |
|---|---|
| `event_id` | UUIDv7, generated client-side. Makes retries idempotent. |
| `anon_id` | UUIDv7, generated on first use, persisted. Browser: `localStorage`. Python/Go server: per-process, or caller-supplied. |
| session | client-owned. New `session_id` after **30 min** of inactivity, and a hard cap of 24h. Rotate on `reset()`. |
| `identify(user_id)` | sets `user_id` on all subsequent events **and** sends one `Identify` RPC. |
| `reset()` | new `anon_id`, new `session_id`, clear `user_id`. Call on logout. |
| super properties | set once, merged into every event's props, per-event keys win. |
| batching | flush at 50 events or 5s (browser) / 500 events or 1s (server). |
| queue | bounded. On overflow drop **oldest** and increment a dropped counter that ships with the next batch — never block the caller's thread. |
| retry | exponential backoff with full jitter, max 5 attempts, only on `UNAVAILABLE`, `RESOURCE_EXHAUSTED`, `DEADLINE_EXCEEDED`. Never retry `INVALID_ARGUMENT` / `UNAUTHENTICATED`. |
| clocks | client stamps `ts`; server clamps absurd values (§5.4) rather than trusting or discarding. |
| failure mode | analytics must never break the host app. Every public method is non-throwing and non-blocking. |

Codegen the transport (buf + `protoc-gen-go`/`grpclib`/`connect-es`); hand-write the
ergonomic layer. Generated stubs make a bad public API — nobody wants
`client.Track(ctx, &connect.Request[lumenv1.TrackRequest]{...})` in their app code.

## 8. Per-language notes

**Go** — `lumen.New(key, opts...)`, `l.Track(ctx, "signup", lumen.P{"plan": "pro"})`.
Background goroutine + bounded channel, `Close(ctx)` flushes. Reports `device_type =
server`, no UA. This one is nearly free: it's the same batching code the server already
has, inverted.

**Python** — sync API over a background thread (`queue.Queue` + daemon worker), because
the majority of Python users are in Django/Flask/Celery, not asyncio. Offer
`AsyncLumen` for asyncio users; both wrap one shared codec layer. `atexit` flush.
`grpclib` or `grpcio` — pick `grpcio` for wheel availability, it's what everyone already
has transitively.

**TypeScript** — one package, two entry points via `exports` map: `node` (gRPC, long-lived
stream) and `browser` (Connect + JSON + `fetch`). Browser specifics that matter:
- flush on `visibilitychange → hidden` using `navigator.sendBeacon`, not on `unload`
  (which mobile Safari never fires)
- persist the pending queue to `localStorage` so a closed tab doesn't lose the batch
- autocapture (pageviews, clicks, SPA route changes) behind an explicit opt-in flag —
  default off; autocapture-by-default is how these libraries get a reputation for
  bloating bundles and capturing PII by accident
- keep the browser bundle under ~10KB gzipped. That is a design constraint, not an
  aspiration: it rules out pulling in the full generated Connect client, so the browser
  transport is hand-written JSON POST against the Connect protocol.

## 9. Layout

```
lumen/
  buf.yaml  buf.gen.yaml
  proto/lumen/v1/{ingest.proto,admin.proto}
  SPEC.md                    # the §7 contract, normative for all SDKs
  cmd/lumen/main.go
  internal/
    config/      env + flags
    auth/        key hashing, ristretto cache, interceptors
    enrich/      ua parsing, geo, url decomposition (+ caches)
    ingest/      validation, batch assembly, async insert
    ch/          clickhouse-go pool, DDL, migrations
    pg/          control-plane queries (pgx)
    provision/   user + policy + key lifecycle
    server/      connect wiring, health, reflection
  migrations/    ch/*.sql, pg/*.sql — embedded via embed.FS, applied on boot
  sdk/
    go/  python/  typescript/
    conformance/  # one test suite, run against all three
  Makefile  Dockerfile  docker-compose.yml   # clickhouse for local dev
```

Module `github.com/SyneHQ/lumen`, Go 1.24, matching apollo.

## 10. Provisioning flow

Initiated from the app, not from Lumen. New route `POST /api/lumen` in the Next.js app:

1. Authz: caller is `OWNER`/`ADMIN` of `teamId` (mirror the existing check in
   [connections/route.ts:202](../app/src/app/api/connections/route.ts:202)).
2. gRPC → `Admin.Provision{team_id}` on the internal port. Lumen:
   - ClickHouse: `CREATE USER` + grants + row policy **per exposed table** + quota
   - Postgres: insert `lumen_tenants` + `lumen_api_keys` (one transaction)
   - generate `lum_live_<8 char prefix>_<32 byte secret>`, store sha256
   - return `{host, port, database, username, password, ingest_key}` — plaintext, once.

   Not atomic across the two stores; ClickHouse has no transactions. Order it
   ClickHouse-first, Postgres-second, and make the RPC idempotent on `team_id` so a retry
   after partial failure converges. A ClickHouse user with no Postgres row is inert; the
   reverse would be a live key pointing at nothing.
3. App encrypts via `ConnectionEncryptionService` and creates the `Connection` row
   (`type: CLICKHOUSE`, `name: "Lumen"`, `port: 9440`, `ssl: true`, team + tenant
   connected), exactly like the existing POST handler.
4. App stores the ingest key and shows it once.

`RotateKey` is separate. `Deprovision` revokes keys and drops the user/policies; it does
**not** drop data.

## 11. Phases

1. **Skeleton** — buf codegen, `Track` over Connect, clickhouse-go, `events` DDL, async
   insert, enrichment. Load test to a real number before anything else is built.
2. **Tenancy** — users, policies, quotas, `Provision`/`RotateKey`/`Deprovision`, key auth.
   Isolation test: team A cannot see team B's events *or* sessions, and cannot reach
   `identities` through the definer view.
3. **Sessions + identity** — `sessions` table, MV, `sessions_v`, `Identify`,
   `events_resolved`.
4. **App integration** — `/api/lumen` route, `Connection` row via the existing encryption
   service, key reveal in UI.
5. **SDKs** — `SPEC.md` + conformance suite first, then Go (cheapest, validates the spec),
   then TS, then Python.

Phase 1 is standalone-testable and is where the speed claim gets proven or dies.
Phases 2 and 5 can run in parallel once the proto is frozen.

## Open calls

- Per-team users vs per-user creds (§4) — assumed per-team, though ClickHouse makes
  per-user cheap enough to reconsider.
- ~~ClickHouse version~~ — settled: self-hosted `26.3-lts`, single node (§6.1).
- Autocapture in the browser SDK — assumed opt-in, off by default.
- Where the ingest key is stored/displayed app-side. `APIKey` is scoped to Syne's own API;
  reusing it would blur two auth surfaces. Assumed a new column/table.
