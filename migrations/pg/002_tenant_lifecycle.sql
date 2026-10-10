-- Replay-safe: only rows without lifecycle metadata become legacy candidates.
ALTER TABLE lumen_tenants ADD COLUMN IF NOT EXISTS lifecycle_state text;
ALTER TABLE lumen_tenants ADD COLUMN IF NOT EXISTS lifecycle_reason text;
ALTER TABLE lumen_tenants ADD COLUMN IF NOT EXISTS legacy_recovery_reason text;
ALTER TABLE lumen_tenants ADD COLUMN IF NOT EXISTS legacy_recovery_started_at timestamptz;
ALTER TABLE lumen_tenants ADD COLUMN IF NOT EXISTS lifecycle_changed_at timestamptz NOT NULL DEFAULT now();
UPDATE lumen_tenants SET lifecycle_state = 'legacy' WHERE lifecycle_state IS NULL;
ALTER TABLE lumen_tenants ALTER COLUMN lifecycle_state SET DEFAULT 'legacy';
ALTER TABLE lumen_tenants ALTER COLUMN lifecycle_state SET NOT NULL;
DO $$ BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid = 'lumen_tenants'::regclass AND conname = 'lumen_tenant_lifecycle_state') THEN
    ALTER TABLE lumen_tenants ADD CONSTRAINT lumen_tenant_lifecycle_state
      CHECK (lifecycle_state IN ('legacy', 'active', 'creating', 'provisioning', 'deleting', 'inactive'));
  END IF;
END $$;
