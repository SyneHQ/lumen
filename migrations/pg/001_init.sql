-- Postgres schema migration for Lumen control plane tables

CREATE TABLE IF NOT EXISTS lumen_tenants (
  team_id    text PRIMARY KEY,
  ch_user    text UNIQUE NOT NULL,
  created_at timestamptz DEFAULT now(),
  store_ip   boolean NOT NULL DEFAULT false
);

CREATE TABLE IF NOT EXISTS lumen_api_keys (
  key_hash   bytea PRIMARY KEY,
  key_prefix text NOT NULL,
  name       text NOT NULL DEFAULT 'Default Ingestion Key',
  team_id    text NOT NULL REFERENCES lumen_tenants(team_id) ON DELETE CASCADE,
  created_at timestamptz DEFAULT now(),
  revoked_at timestamptz
);

CREATE INDEX IF NOT EXISTS idx_lumen_api_keys_team_id ON lumen_api_keys (team_id);
