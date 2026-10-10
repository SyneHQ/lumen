# Tenant lifecycle recovery

Deletion records its intent and revokes every API key in PostgreSQL before removing the ClickHouse user, policies and quota. The retained tenant becomes `inactive`; startup skips it. Reprovisioning creates new credentials and never restores old keys.

Startup retries `deleting` and confirmed `provisioning` cleanup. A `creating` record means ClickHouse ownership is unconfirmed. An operator must review the original creation outcome before changing that record or user. Startup does not guess ownership or delete an unconfirmed user.

API authentication can retain a previously valid key for up to 60 seconds. Do not assume immediate revocation across replicas.

## Recover an old deletion

1. Stop Lumen writers. Review prior deletion evidence for every exact tenant ID. Retained revoked keys alone do not prove deletion.
2. Create a regular, operator-owned mode-0600 file with the reviewed IDs:

```json
{"team_ids":["exact-team-id"],"reason":"operator_confirmed_legacy_deletion"}
```

3. Use the same database configuration as Lumen and run:

```sh
lumen-maintenance recover-legacy-deletions --allowlist /absolute/path.json
```

The command accepts retained tenants with nonempty, fully revoked key history. It rejects active and unconfirmed-creation records. It preserves a recovery marker, removes access, and reports the allowlist SHA-256 and recovered count without exposing tenant IDs.

4. Preserve the receipt, restart Lumen, and verify startup plus an unrelated active tenant. If cleanup fails, preserve the state and retry the same reviewed allowlist; completed tenants remain inactive.

The command does not detect deleted users or infer permission to remove them. Operators must provide that evidence. Database recovery operations use a 90-second context deadline. Use an external process timeout to bound configuration loading and shutdown. Build the utility with `go build -o lumen-maintenance ./cmd/lumen-maintenance`. Enterprise image packaging is tracked separately.
