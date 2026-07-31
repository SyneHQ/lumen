-- ClickHouse schema initialization for Lumen event ingestion service

CREATE DATABASE IF NOT EXISTS lumen;

-- 1. Events Table (Immutable raw event stream)
CREATE TABLE IF NOT EXISTS lumen.events (
  -- Multi-tenant & core event identifiers
  team_id         LowCardinality(String),
  ts              DateTime64(3, 'UTC'),     -- Client timestamp
  name            LowCardinality(String),   -- Event name
  event_id        UUID,                     -- Client UUIDv7 (idempotency key)
  anon_id         String,                   -- Anonymous identity
  user_id         String,                   -- Identified user ID (empty if unauthenticated)
  session_id      String,                   -- Client session ID

  -- Client SDK & OS metadata
  sdk             LowCardinality(String),
  sdk_version     LowCardinality(String),
  app_version     LowCardinality(String),
  os              LowCardinality(String),
  os_version      LowCardinality(String),
  device_type     LowCardinality(String),   -- desktop | mobile | tablet | server | bot
  device_model    LowCardinality(String),
  manufacturer    LowCardinality(String),
  browser         LowCardinality(String),
  browser_version LowCardinality(String),
  screen_w        UInt16,
  screen_h        UInt16,
  viewport_w      UInt16,
  viewport_h      UInt16,
  locale          LowCardinality(String),
  timezone        LowCardinality(String),

  -- Decomposed web & UTM parameters
  url             String,
  path            String,
  host            LowCardinality(String),
  referrer        String,
  referrer_host   LowCardinality(String),
  utm_source      LowCardinality(String),
  utm_medium      LowCardinality(String),
  utm_campaign    LowCardinality(String),
  utm_term        LowCardinality(String),
  utm_content     LowCardinality(String),

  -- GeoIP & network context (derived server-side)
  country         LowCardinality(FixedString(2)),
  region          LowCardinality(String),
  city            LowCardinality(String),
  ip              IPv6 CODEC(ZSTD(3)),      -- Retained only if store_ip is enabled for tenant
  ingested_at     DateTime DEFAULT now(),

  -- Custom event JSON payload
  props           JSON(max_dynamic_paths = 512, SKIP REGEXP '^\\$')
)
ENGINE = MergeTree
PARTITION BY toYYYYMM(ts)
ORDER BY (team_id, name, ts, event_id)
TTL toDateTime(ts) + INTERVAL 12 MONTH;


-- 2. Materialized Sessions Aggregation Table
CREATE TABLE IF NOT EXISTS lumen.sessions (
  team_id         LowCardinality(String),
  session_id      String,
  anon_id         String,
  user_id         SimpleAggregateFunction(anyLast, String),
  started_at      SimpleAggregateFunction(min, DateTime64(3, 'UTC')),
  ended_at        SimpleAggregateFunction(max, DateTime64(3, 'UTC')),
  event_count     SimpleAggregateFunction(sum, UInt64),
  entry_path      AggregateFunction(argMin, String, DateTime64(3, 'UTC')),
  exit_path       AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  device_type     SimpleAggregateFunction(anyLast, LowCardinality(String)),
  country         SimpleAggregateFunction(anyLast, LowCardinality(FixedString(2))),
  utm_source      SimpleAggregateFunction(anyLast, LowCardinality(String)),
  referrer_host   SimpleAggregateFunction(anyLast, LowCardinality(String))
)
ENGINE = AggregatingMergeTree
PARTITION BY toYYYYMM(started_at)
ORDER BY (team_id, session_id, anon_id);


-- Materialized View populating sessions automatically on event insert
CREATE MATERIALIZED VIEW IF NOT EXISTS lumen.sessions_mv TO lumen.sessions AS
SELECT
  team_id, session_id, anon_id,
  anyLast(user_id)       AS user_id,
  min(ts)                AS started_at,
  max(ts)                AS ended_at,
  count()                AS event_count,
  argMinState(path, ts)  AS entry_path,
  argMaxState(path, ts)  AS exit_path,
  anyLast(device_type)   AS device_type,
  anyLast(country)       AS country,
  anyLast(utm_source)    AS utm_source,
  anyLast(referrer_host) AS referrer_host
FROM lumen.events
WHERE session_id != ''
GROUP BY team_id, session_id, anon_id;


-- Helper View unwrapping aggregate state functions for plain SQL dashboard access
CREATE VIEW IF NOT EXISTS lumen.sessions_v AS
SELECT
  team_id,
  session_id,
  anon_id,
  user_id,
  started_at,
  ended_at,
  event_count,
  argMinMerge(entry_path) AS entry_path,
  argMaxMerge(exit_path)  AS exit_path,
  device_type,
  country,
  utm_source,
  referrer_host
FROM lumen.sessions
GROUP BY team_id, session_id, anon_id, user_id, started_at, ended_at, event_count, device_type, country, utm_source, referrer_host;


-- 3. Identity Resolution Tables & Security Definer Views
CREATE TABLE IF NOT EXISTS lumen.identities (
  team_id    LowCardinality(String),
  anon_id    String,
  user_id    String,
  updated_at DateTime DEFAULT now()
) ENGINE = ReplacingMergeTree(updated_at)
ORDER BY (team_id, anon_id);

-- Helper View unwrapping latest identity mappings for plain SQL dashboard access
CREATE VIEW IF NOT EXISTS lumen.identities_v AS
SELECT
  team_id,
  anon_id,
  user_id,
  max(updated_at) AS updated_at
FROM lumen.identities
GROUP BY team_id, anon_id, user_id;


-- Dictionary for fast identity lookups in queries
CREATE DICTIONARY IF NOT EXISTS lumen.identity_dict (
  team_id String,
  anon_id String,
  user_id String
)
PRIMARY KEY team_id, anon_id
SOURCE(CLICKHOUSE(TABLE 'identities' DB 'lumen'))
LAYOUT(COMPLEX_KEY_HASHED())
LIFETIME(MIN 60 MAX 120);

-- SQL SECURITY DEFINER view safely resolving retroactive identities while preserving row security policies
CREATE VIEW IF NOT EXISTS lumen.events_resolved
  DEFINER = current_user SQL SECURITY DEFINER AS
SELECT
  *,
  if(user_id != '', user_id, dictGetOrDefault('lumen.identity_dict', 'user_id', (team_id, anon_id), anon_id)) AS person_id
FROM lumen.events;
