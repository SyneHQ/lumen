-- Persons rollup: materialized user/visitor profiles built from lumen.events.
--
-- A person is keyed by (team_id, person_id) where person_id is the user_id once
-- known and the anon_id before that (forward stitching, §3.3). Two materialized
-- views feed one aggregate table:
--
--   1. lumen.persons_activity_mv rolls up every event (first seen, last seen,
--      event count, last device/os/browser/country). The $identify synthetic
--      event carries no client context, so it is excluded from the "last
--      context" states to avoid blanking them out, but it still counts as
--      activity.
--
--   2. lumen.persons_identity_mv fires only on $identify events and extracts
--      the common trait keys (email, name, first_name, last_name, full_name,
--      phone, avatar) plus the full raw traits payload for the details view.
--      Trait keys merge per-property (latest non-empty value per key wins) so a
--      partial re-identify does not erase earlier traits, while the raw traits
--      dump always reflects the newest $identify payload.
--
-- IMPORTANT for the migrations runner: statements are split on semicolons
-- naively, so no semicolon may appear anywhere inside a statement, including
-- inside comments.

CREATE TABLE IF NOT EXISTS lumen.persons (
  team_id       LowCardinality(String),
  person_id     String,

  -- Activity rollup (all events)
  is_identified AggregateFunction(max, UInt8),
  first_seen    AggregateFunction(min, DateTime64(3, 'UTC')),
  last_seen     AggregateFunction(max, DateTime64(3, 'UTC')),
  event_count   AggregateFunction(count, UInt64),
  -- NOTE: all argMax state columns intentionally use plain String args.
  -- ClickHouse cannot materialize defaults for AggregateFunction(argMax,
  -- LowCardinality(String)) columns (26.x), which would break partial-column
  -- inserts from the identity MV and backfill below.
  device_type   AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  os            AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  browser       AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  country       AggregateFunction(argMax, String, DateTime64(3, 'UTC')),

  -- Latest $identify traits (empty states until the first $identify)
  email         AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  name          AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  first_name    AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  last_name     AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  full_name     AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  phone         AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  avatar        AggregateFunction(argMax, String, DateTime64(3, 'UTC')),
  traits        AggregateFunction(argMax, String, DateTime64(3, 'UTC'))
)
ENGINE = AggregatingMergeTree
ORDER BY (team_id, person_id);


-- One-time backfill of persons from existing events. MVs only see new inserts,
-- so historical events are rolled up here. Backfills run before the MVs are
-- created and are skipped once the corresponding MV exists, which keeps the
-- re-run-on-every-boot migrations idempotent.
INSERT INTO lumen.persons
  (team_id, person_id, is_identified, first_seen, last_seen, event_count, device_type, os, browser, country)
SELECT
  team_id,
  if(user_id != '', user_id, anon_id)   AS person_id,
  maxState(toUInt8(user_id != ''))      AS is_identified,
  minState(ts)                          AS first_seen,
  maxState(ts)                          AS last_seen,
  countState()                          AS event_count,
  argMaxIfState(toString(device_type), ts, name != '$identify') AS device_type,
  argMaxIfState(toString(os), ts, name != '$identify')          AS os,
  argMaxIfState(toString(browser), ts, name != '$identify')     AS browser,
  argMaxIfState(toString(country), ts, name != '$identify')     AS country
FROM lumen.events
WHERE if(user_id != '', user_id, anon_id) != ''
  AND (SELECT count() FROM system.tables WHERE database = 'lumen' AND name = 'persons_activity_mv') = 0
GROUP BY team_id, if(user_id != '', user_id, anon_id);

INSERT INTO lumen.persons
  (team_id, person_id, email, name, first_name, last_name, full_name, phone, avatar, traits)
SELECT
  team_id,
  if(user_id != '', user_id, anon_id)        AS person_id,
  argMaxIfState(e_mail, ts, e_mail != '')       AS email,
  argMaxIfState(e_name, ts, e_name != '')       AS name,
  argMaxIfState(e_first_name, ts, e_first_name != '') AS first_name,
  argMaxIfState(e_last_name, ts, e_last_name != '')     AS last_name,
  argMaxIfState(e_full_name, ts, e_full_name != '')     AS full_name,
  argMaxIfState(e_phone, ts, e_phone != '')     AS phone,
  argMaxIfState(e_avatar, ts, e_avatar != '')   AS avatar,
  argMaxState(toJSONString(props), ts)          AS traits
FROM (
  SELECT team_id, user_id, anon_id, ts, props,
    coalesce(props.email.:String, '')      AS e_mail,
    coalesce(props.name.:String, '')       AS e_name,
    coalesce(props.first_name.:String, props.firstName.:String, '') AS e_first_name,
    coalesce(props.last_name.:String, props.lastName.:String, '')   AS e_last_name,
    coalesce(props.full_name.:String, props.fullName.:String, '')   AS e_full_name,
    coalesce(props.phone.:String, '')      AS e_phone,
    coalesce(props.avatar.:String, props.picture.:String, '')       AS e_avatar
  FROM lumen.events AS e
  WHERE e.name = '$identify'
)
WHERE (SELECT count() FROM system.tables WHERE database = 'lumen' AND name = 'persons_identity_mv') = 0
GROUP BY team_id, if(user_id != '', user_id, anon_id);


-- Materialized views keep persons up to date for every newly ingested event.
CREATE MATERIALIZED VIEW IF NOT EXISTS lumen.persons_activity_mv
TO lumen.persons
  (team_id, person_id, is_identified, first_seen, last_seen, event_count, device_type, os, browser, country)
AS
SELECT
  team_id,
  if(user_id != '', user_id, anon_id)   AS person_id,
  maxState(toUInt8(user_id != ''))      AS is_identified,
  minState(ts)                          AS first_seen,
  maxState(ts)                          AS last_seen,
  countState()                          AS event_count,
  argMaxIfState(toString(device_type), ts, name != '$identify') AS device_type,
  argMaxIfState(toString(os), ts, name != '$identify')          AS os,
  argMaxIfState(toString(browser), ts, name != '$identify')     AS browser,
  argMaxIfState(toString(country), ts, name != '$identify')     AS country
FROM lumen.events
WHERE if(user_id != '', user_id, anon_id) != ''
GROUP BY team_id, if(user_id != '', user_id, anon_id);

CREATE MATERIALIZED VIEW IF NOT EXISTS lumen.persons_identity_mv
TO lumen.persons
  (team_id, person_id, email, name, first_name, last_name, full_name, phone, avatar, traits)
AS
SELECT
  team_id,
  if(user_id != '', user_id, anon_id)   AS person_id,
  argMaxIfState(e_mail, ts, e_mail != '')       AS email,
  argMaxIfState(e_name, ts, e_name != '')       AS name,
  argMaxIfState(e_first_name, ts, e_first_name != '') AS first_name,
  argMaxIfState(e_last_name, ts, e_last_name != '')     AS last_name,
  argMaxIfState(e_full_name, ts, e_full_name != '')     AS full_name,
  argMaxIfState(e_phone, ts, e_phone != '')     AS phone,
  argMaxIfState(e_avatar, ts, e_avatar != '')   AS avatar,
  argMaxState(toJSONString(props), ts)          AS traits
FROM (
  SELECT team_id, user_id, anon_id, ts, props,
    coalesce(props.email.:String, '')      AS e_mail,
    coalesce(props.name.:String, '')       AS e_name,
    coalesce(props.first_name.:String, props.firstName.:String, '') AS e_first_name,
    coalesce(props.last_name.:String, props.lastName.:String, '')   AS e_last_name,
    coalesce(props.full_name.:String, props.fullName.:String, '')   AS e_full_name,
    coalesce(props.phone.:String, '')      AS e_phone,
    coalesce(props.avatar.:String, props.picture.:String, '')       AS e_avatar
  FROM lumen.events AS e
  WHERE e.name = '$identify'
)
GROUP BY team_id, if(user_id != '', user_id, anon_id);


-- Helper view unwrapping aggregate states into plain columns for dashboard
-- consumers. SQL SECURITY INVOKER keeps each team's row policies in effect.
-- display_name falls back through the usual trait spellings, then email.
CREATE VIEW IF NOT EXISTS lumen.persons_v
  SQL SECURITY INVOKER AS
SELECT
  team_id,
  person_id,
  maxMerge(is_identified)  AS is_identified,
  minMerge(first_seen)     AS first_seen,
  maxMerge(last_seen)      AS last_seen,
  countMerge(event_count)  AS events,
  nullIf(argMaxMerge(device_type), '') AS device_type,
  nullIf(argMaxMerge(os), '')          AS os,
  nullIf(argMaxMerge(browser), '')     AS browser,
  nullIf(argMaxMerge(country), '')     AS country,
  nullIf(argMaxMerge(email), '')       AS email,
  nullIf(argMaxMerge(name), '')        AS name,
  nullIf(argMaxMerge(first_name), '')  AS first_name,
  nullIf(argMaxMerge(last_name), '')   AS last_name,
  nullIf(argMaxMerge(full_name), '')   AS full_name,
  nullIf(argMaxMerge(phone), '')       AS phone,
  nullIf(argMaxMerge(avatar), '')      AS avatar,
  nullIf(argMaxMerge(traits), '')      AS traits,
  -- display_name must re-read the state columns through the p alias, since
  -- the plain names above are already shadowed by SELECT output aliases
  coalesce(
    nullIf(argMaxMerge(p.name), ''),
    nullIf(argMaxMerge(p.full_name), ''),
    nullIf(trimBoth(concatWithSeparator(' ', nullIf(argMaxMerge(p.first_name), ''), nullIf(argMaxMerge(p.last_name), ''))), ''),
    nullIf(argMaxMerge(p.email), '')
  ) AS display_name
FROM lumen.persons AS p
GROUP BY p.team_id, p.person_id;
