# Security Policy

Lumen handles product analytics data — event streams, user identifiers, IP
addresses, and session records belonging to other people's users. We treat
security reports as high priority.

## Reporting a vulnerability

**Do not open a public issue, discussion, or pull request for a security
vulnerability.**

Report privately through either channel:

- **GitHub Private Vulnerability Reporting** (preferred) —
  [open a draft advisory](https://github.com/SyneHQ/lumen/security/advisories/new)
- **Email** — security@synehq.com

If you want to encrypt, say so in a first plaintext message and we will send
a key.

### What to include

- Affected version, commit SHA, or hosted endpoint
- Component (ingest, admin RPC, auth, SDK, migrations, deployment default)
- Steps to reproduce, ideally a minimal request or script
- Impact you believe is achievable
- Whether you have disclosed this anywhere else

### What to expect

| Stage | Target |
|---|---|
| Acknowledgement | 48 hours |
| Initial assessment and severity | 5 business days |
| Fix or documented mitigation | 90 days, faster for critical |
| Public advisory | after a fix ships, coordinated with you |

We will keep you updated, credit you in the advisory unless you prefer
otherwise, and tell you plainly if we decide something is not a
vulnerability and why.

We do not currently run a paid bug bounty.

## Scope

### In scope

- Cross-tenant data access or leakage of any kind
- Authentication or authorization bypass on ingest or admin RPCs
- API key forgery, prediction, or privilege escalation via keys
- SQL or ClickHouse injection via event names, properties, or context fields
- Remote code execution, SSRF, path traversal
- Denial of service reachable by an unauthenticated caller
- Secret leakage — logs, error responses, metrics, container images
- Insecure defaults that a reasonable operator would not notice
- Vulnerabilities in the published client SDKs

### Out of scope

- Missing hardening that has no exploit path (report as a normal issue)
- Vulnerabilities requiring an already-compromised host or database
- DoS achieved only by an authenticated tenant exceeding their own quota
- Reports from automated scanners with no demonstrated impact
- Social engineering, physical access, or attacks on our staff
- Outdated dependencies with no reachable code path
- Anything you found by attacking a third party's Lumen deployment without
  their written permission

The `enterprise/` submodule is a separate private repository. Report issues
there to the same address.

## Supported versions

| Version | Status |
|---|---|
| `main` | supported |
| latest tagged minor | supported |
| previous minor | critical fixes only |
| older | unsupported |

Lumen is pre-1.0 in practice. Run a recent version.

## Operator hardening checklist

Self-hosting Lumen means you own these:

- **`ADMIN_TOKEN` has no default.** The server refuses to start without it.
  Generate with `openssl rand -hex 32`. Rotate it if it was ever in a shell
  history, CI log, or committed compose file.
- **Never expose the admin port (`50052`) to the internet.** It provisions
  tenants and mints API keys. Bind it to a private network or localhost.
- **Terminate TLS in front of the ingest port.** API keys are bearer
  credentials; plaintext HTTP/2 leaks them.
- **Treat API keys as secrets at rest.** They are stored hashed; do not add
  logging that prints the raw key.
- **IP addresses and GeoIP data are personal data** under GDPR and similar
  regimes. Decide deliberately whether to store IPs, and configure retention
  TTLs on the ClickHouse tables.
- **Set retention.** Append-only event tables grow without bound by default.
- **Do not run the container as root.** The published image already uses
  distroless nonroot; keep it that way.
- **Scope Postgres and ClickHouse credentials** to the `lumen` database only.

## Safe harbor

If you make a good-faith effort to comply with this policy, we will not
pursue or support legal action against you. Stay within these bounds:

- Test only against your own deployment or one you have permission to test
- Do not access, modify, or exfiltrate data belonging to others
- Do not degrade service for other users
- Give us reasonable time to fix before disclosing publicly

Contact security@synehq.com if you are unsure whether something is in scope.
Asking first is always fine.
