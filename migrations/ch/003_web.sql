-- Web analytics schema (Plausible-style) on top of lumen.events.
--
-- Requires ClickHouse 23.x or newer for ALTER TABLE ... MODIFY QUERY on a TO
-- materialized view and default SQL SECURITY INVOKER on plain views. Verified
-- on 26.3.
--
-- IMPORTANT for the migrations runner: statements are split on semicolons
-- naively, so no semicolon may appear anywhere inside a statement, including
-- inside comments.

-- 1. Sessions gain pageview_count and host. Rows aggregated before this
-- migration keep the defaults 0 and '' (no backfill), so those sessions do
-- not show up in lumen.web_sessions.
ALTER TABLE lumen.sessions
  ADD COLUMN IF NOT EXISTS pageview_count SimpleAggregateFunction(sum, UInt64),
  ADD COLUMN IF NOT EXISTS host SimpleAggregateFunction(anyLast, LowCardinality(String));

-- MODIFY QUERY swaps the MV query in place, so no insert is lost in a gap
-- between DROP and CREATE. Re-running it with the same query is a no-op.
ALTER TABLE lumen.sessions_mv MODIFY QUERY
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
  anyLast(referrer_host) AS referrer_host,
  countIf(name = '$pageview') AS pageview_count,
  anyLast(host)          AS host
FROM lumen.events
WHERE session_id != ''
GROUP BY team_id, session_id, anon_id;

CREATE OR REPLACE VIEW lumen.sessions_v AS
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
  referrer_host,
  pageview_count,
  host
FROM lumen.sessions
GROUP BY team_id, session_id, anon_id, user_id, started_at, ended_at, event_count, device_type, country, utm_source, referrer_host, pageview_count, host;

-- 2. Referrer host to source and channel. Reference data, no team column.
-- Joined at query time rather than through a dictionary: a CLICKHOUSE-sourced
-- dictionary connects back as the default user and fails with code 516 once
-- that user has a password (see lumen.identity_dict in 001).
CREATE TABLE IF NOT EXISTS lumen.referrer_sources (
  host    String,
  source  String,
  channel String
) ENGINE = ReplacingMergeTree
ORDER BY host;

-- Re-seeded on every boot so list edits land. ReplacingMergeTree collapses
-- duplicates and readers use FINAL.
INSERT INTO lumen.referrer_sources (host, source, channel)
SELECT host, source, channel FROM VALUES('host String, source String, channel String',
  ('google.com', 'Google', 'Organic Search'),
  ('google.co.uk', 'Google', 'Organic Search'),
  ('google.de', 'Google', 'Organic Search'),
  ('google.fr', 'Google', 'Organic Search'),
  ('google.es', 'Google', 'Organic Search'),
  ('google.it', 'Google', 'Organic Search'),
  ('google.nl', 'Google', 'Organic Search'),
  ('google.ca', 'Google', 'Organic Search'),
  ('google.com.au', 'Google', 'Organic Search'),
  ('google.co.in', 'Google', 'Organic Search'),
  ('google.co.jp', 'Google', 'Organic Search'),
  ('google.com.br', 'Google', 'Organic Search'),
  ('google.com.mx', 'Google', 'Organic Search'),
  ('google.ru', 'Google', 'Organic Search'),
  ('google.pl', 'Google', 'Organic Search'),
  ('google.se', 'Google', 'Organic Search'),
  ('google.ch', 'Google', 'Organic Search'),
  ('google.at', 'Google', 'Organic Search'),
  ('google.be', 'Google', 'Organic Search'),
  ('google.dk', 'Google', 'Organic Search'),
  ('google.no', 'Google', 'Organic Search'),
  ('google.fi', 'Google', 'Organic Search'),
  ('google.ie', 'Google', 'Organic Search'),
  ('google.pt', 'Google', 'Organic Search'),
  ('google.cz', 'Google', 'Organic Search'),
  ('google.com.tr', 'Google', 'Organic Search'),
  ('google.co.id', 'Google', 'Organic Search'),
  ('google.com.sg', 'Google', 'Organic Search'),
  ('google.co.za', 'Google', 'Organic Search'),
  ('google.com.ar', 'Google', 'Organic Search'),
  ('google.co.kr', 'Google', 'Organic Search'),
  ('google.com.hk', 'Google', 'Organic Search'),
  ('google.com.tw', 'Google', 'Organic Search'),
  ('google.co.nz', 'Google', 'Organic Search'),
  ('google.gr', 'Google', 'Organic Search'),
  ('google.ro', 'Google', 'Organic Search'),
  ('google.hu', 'Google', 'Organic Search'),
  ('google.com.ua', 'Google', 'Organic Search'),
  ('google.co.il', 'Google', 'Organic Search'),
  ('google.com.vn', 'Google', 'Organic Search'),
  ('google.com.ph', 'Google', 'Organic Search'),
  ('google.com.my', 'Google', 'Organic Search'),
  ('google.com.pk', 'Google', 'Organic Search'),
  ('google.com.eg', 'Google', 'Organic Search'),
  ('google.com.sa', 'Google', 'Organic Search'),
  ('google.ae', 'Google', 'Organic Search'),
  ('google.cl', 'Google', 'Organic Search'),
  ('google.com.co', 'Google', 'Organic Search'),
  ('google.com.pe', 'Google', 'Organic Search'),
  ('google.co.th', 'Google', 'Organic Search'),
  ('google.com.ng', 'Google', 'Organic Search'),
  ('google.sk', 'Google', 'Organic Search'),
  ('google.bg', 'Google', 'Organic Search'),
  ('google.lt', 'Google', 'Organic Search'),
  ('bing.com', 'Bing', 'Organic Search'),
  ('cn.bing.com', 'Bing', 'Organic Search'),
  ('duckduckgo.com', 'DuckDuckGo', 'Organic Search'),
  ('html.duckduckgo.com', 'DuckDuckGo', 'Organic Search'),
  ('lite.duckduckgo.com', 'DuckDuckGo', 'Organic Search'),
  ('yahoo.com', 'Yahoo', 'Organic Search'),
  ('search.yahoo.com', 'Yahoo', 'Organic Search'),
  ('yahoo.co.jp', 'Yahoo', 'Organic Search'),
  ('search.yahoo.co.jp', 'Yahoo', 'Organic Search'),
  ('uk.search.yahoo.com', 'Yahoo', 'Organic Search'),
  ('yandex.ru', 'Yandex', 'Organic Search'),
  ('yandex.com', 'Yandex', 'Organic Search'),
  ('ya.ru', 'Yandex', 'Organic Search'),
  ('yandex.com.tr', 'Yandex', 'Organic Search'),
  ('yandex.kz', 'Yandex', 'Organic Search'),
  ('yandex.by', 'Yandex', 'Organic Search'),
  ('baidu.com', 'Baidu', 'Organic Search'),
  ('m.baidu.com', 'Baidu', 'Organic Search'),
  ('ecosia.org', 'Ecosia', 'Organic Search'),
  ('search.brave.com', 'Brave Search', 'Organic Search'),
  ('startpage.com', 'Startpage', 'Organic Search'),
  ('qwant.com', 'Qwant', 'Organic Search'),
  ('lite.qwant.com', 'Qwant', 'Organic Search'),
  ('naver.com', 'Naver', 'Organic Search'),
  ('search.naver.com', 'Naver', 'Organic Search'),
  ('m.search.naver.com', 'Naver', 'Organic Search'),
  ('seznam.cz', 'Seznam', 'Organic Search'),
  ('search.seznam.cz', 'Seznam', 'Organic Search'),
  ('kagi.com', 'Kagi', 'Organic Search'),
  ('chatgpt.com', 'ChatGPT', 'AI Assistants'),
  ('chat.openai.com', 'ChatGPT', 'AI Assistants'),
  ('perplexity.ai', 'Perplexity', 'AI Assistants'),
  ('claude.ai', 'Claude', 'AI Assistants'),
  ('gemini.google.com', 'Gemini', 'AI Assistants'),
  ('bard.google.com', 'Gemini', 'AI Assistants'),
  ('copilot.microsoft.com', 'Copilot', 'AI Assistants'),
  ('you.com', 'You.com', 'AI Assistants'),
  ('phind.com', 'Phind', 'AI Assistants'),
  ('chat.deepseek.com', 'DeepSeek', 'AI Assistants'),
  ('chat.mistral.ai', 'Mistral', 'AI Assistants'),
  ('x.com', 'X', 'Organic Social'),
  ('twitter.com', 'X', 'Organic Social'),
  ('t.co', 'X', 'Organic Social'),
  ('mobile.twitter.com', 'X', 'Organic Social'),
  ('facebook.com', 'Facebook', 'Organic Social'),
  ('m.facebook.com', 'Facebook', 'Organic Social'),
  ('l.facebook.com', 'Facebook', 'Organic Social'),
  ('lm.facebook.com', 'Facebook', 'Organic Social'),
  ('fb.com', 'Facebook', 'Organic Social'),
  ('instagram.com', 'Instagram', 'Organic Social'),
  ('l.instagram.com', 'Instagram', 'Organic Social'),
  ('linkedin.com', 'LinkedIn', 'Organic Social'),
  ('lnkd.in', 'LinkedIn', 'Organic Social'),
  ('reddit.com', 'Reddit', 'Organic Social'),
  ('old.reddit.com', 'Reddit', 'Organic Social'),
  ('out.reddit.com', 'Reddit', 'Organic Social'),
  ('youtube.com', 'YouTube', 'Organic Social'),
  ('m.youtube.com', 'YouTube', 'Organic Social'),
  ('youtu.be', 'YouTube', 'Organic Social'),
  ('tiktok.com', 'TikTok', 'Organic Social'),
  ('pinterest.com', 'Pinterest', 'Organic Social'),
  ('threads.net', 'Threads', 'Organic Social'),
  ('threads.com', 'Threads', 'Organic Social'),
  ('bsky.app', 'Bluesky', 'Organic Social'),
  ('mastodon.social', 'Mastodon', 'Organic Social'),
  ('news.ycombinator.com', 'Hacker News', 'Organic Social'),
  ('lobste.rs', 'Lobsters', 'Organic Social'),
  ('discord.com', 'Discord', 'Organic Social'),
  ('t.me', 'Telegram', 'Organic Social'),
  ('web.telegram.org', 'Telegram', 'Organic Social'),
  ('web.whatsapp.com', 'WhatsApp', 'Organic Social'),
  ('wa.me', 'WhatsApp', 'Organic Social'),
  ('quora.com', 'Quora', 'Organic Social'),
  ('vk.com', 'VK', 'Organic Social'),
  ('github.com', 'GitHub', 'Referral'),
  ('gitlab.com', 'GitLab', 'Referral'),
  ('stackoverflow.com', 'Stack Overflow', 'Referral'),
  ('dev.to', 'DEV', 'Referral'),
  ('medium.com', 'Medium', 'Referral'),
  ('producthunt.com', 'Product Hunt', 'Referral'),
  ('hashnode.com', 'Hashnode', 'Referral'),
  ('substack.com', 'Substack', 'Referral'),
  ('en.wikipedia.org', 'Wikipedia', 'Referral'),
  ('en.m.wikipedia.org', 'Wikipedia', 'Referral'),
  ('mail.google.com', 'Gmail', 'Email'),
  ('outlook.live.com', 'Outlook', 'Email'),
  ('outlook.office.com', 'Outlook', 'Email'),
  ('outlook.office365.com', 'Outlook', 'Email'),
  ('mail.yahoo.com', 'Yahoo Mail', 'Email'),
  ('mail.proton.me', 'Proton Mail', 'Email')
);

-- 3. Pageviews with source and channel. Default SQL SECURITY (INVOKER) keeps the
-- querying tenant's row policy on lumen.events in effect.
-- A referrer equal to the site's own host counts as no referrer.
CREATE OR REPLACE VIEW lumen.web_pageviews AS
SELECT
  e.team_id AS team_id,
  e.ts AS ts,
  e.anon_id AS anon_id,
  e.session_id AS session_id,
  e.host AS host,
  e.path AS path,
  e.url AS url,
  e.ref_norm AS referrer_host,
  multiIf(
    e.utm_source != '', toString(e.utm_source),
    e.ref = '', 'Direct / None',
    r.source != '', r.source,
    e.ref) AS source,
  multiIf(
    lower(e.utm_medium) IN ('cpc', 'ppc', 'paid', 'paidsearch'), 'Paid Search',
    lower(e.utm_medium) = 'email', 'Email',
    e.ref = '' AND e.utm_source = '', 'Direct',
    r.channel != '', r.channel,
    'Referral') AS channel,
  e.utm_source AS utm_source,
  e.utm_medium AS utm_medium,
  e.utm_campaign AS utm_campaign,
  e.country AS country,
  e.region AS region,
  e.city AS city,
  e.device_type AS device_type,
  e.browser AS browser,
  e.os AS os
FROM (
  SELECT *,
    replaceRegexpOne(lower(referrer_host), '^www\\.', '') AS ref_norm,
    if(ref_norm = replaceRegexpOne(lower(host), '^www\\.', ''), '', ref_norm) AS ref
  FROM lumen.events
  WHERE name = '$pageview' AND device_type != 'bot'
) AS e
LEFT JOIN (SELECT host, source, channel FROM lumen.referrer_sources FINAL) AS r ON r.host = e.ref;

-- 4. Sessions that saw at least one pageview.
CREATE OR REPLACE VIEW lumen.web_sessions AS
SELECT
  team_id,
  session_id,
  anon_id,
  host,
  started_at,
  ended_at,
  dateDiff('second', started_at, ended_at) AS duration_s,
  pageview_count,
  entry_path,
  exit_path,
  country,
  device_type,
  utm_source,
  referrer_host
FROM lumen.sessions_v
WHERE pageview_count > 0;
