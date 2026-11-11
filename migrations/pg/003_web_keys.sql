-- Website ("web") API keys and the daily salt for cookieless visitor ids. Replay-safe.
ALTER TABLE lumen_api_keys ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'server';
ALTER TABLE lumen_api_keys ADD COLUMN IF NOT EXISTS allowed_origins text[] NOT NULL DEFAULT '{}';
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'lumen_api_keys'::regclass AND conname = 'lumen_api_keys_kind') THEN
    ALTER TABLE lumen_api_keys ADD CONSTRAINT lumen_api_keys_kind CHECK (kind IN ('server', 'web'));
  END IF;
END $$;

CREATE TABLE IF NOT EXISTS lumen_web_salts (
  day  date PRIMARY KEY,
  salt bytea NOT NULL
);
