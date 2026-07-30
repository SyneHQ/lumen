# Open Core Model

Lumen is open core. This document states exactly what is open, what is not,
and why — so you never have to guess whether the thing you need is going to
be paywalled.

## The promise

**Everything required to ingest, store, query, and self-host your own
analytics data is open source and always will be.** No event caps, no seat
limits, no phone-home, no license key, no feature flags that quietly
downgrade the open build.

The commercial module exists to run Lumen *as a hosted multi-tenant service
for other people*. If that is not what you are doing, you do not need it.

## What's in this repository (open source)

| Component | Path | License |
|---|---|---|
| Ingest engine, Connect/gRPC handlers | `internal/ingest`, `internal/server` | AGPL-3.0 |
| API key auth, tenant context, key cache | `internal/auth` | AGPL-3.0 |
| Server-side enrichment (UA, GeoIP, UTM) | `internal/enrich` | AGPL-3.0 |
| ClickHouse writer, batching, dedup | `internal/ch` | AGPL-3.0 |
| Postgres control plane | `internal/pg`, `internal/provision` | AGPL-3.0 |
| Full database schema and migrations | `migrations/` | AGPL-3.0 |
| Config and secret loading | `internal/config` | AGPL-3.0 |
| Extension interfaces + no-op defaults | `ee/` | AGPL-3.0 |
| Application bootstrap | `app/` | AGPL-3.0 |
| Wire contract | `proto/`, `gen/` | Apache-2.0 |
| Go, TypeScript, Python SDKs | `sdk/` | Apache-2.0 |
| Self-host recipes | `Dockerfile`, `docker-compose.yml` | AGPL-3.0 |

### Why two licenses

The **server is AGPL-3.0** so that anyone who takes Lumen, improves it, and
offers it as a network service has to share those improvements. It does not
restrict you: running Lumen internally, or modified, imposes no obligation on
your own application code.

The **SDKs and wire contract are Apache-2.0** so you can embed them in
closed-source products with zero copyleft risk and a patent grant. Your app
sending events to a Lumen server does not make your app a derivative work of
the server. The SDKs are permissive so that this is unambiguous rather than a
thing you need a lawyer to confirm.

## What lives in the private `enterprise/` submodule

Strictly commercial-operations concerns:

| Component | Why it's closed |
|---|---|
| License validation and entitlements | Only meaningful for paid tiers |
| Per-tenant quota enforcement | Billing enforcement, not a feature |
| Usage metering and billing export | Wired to our payment provider |
| SSO / SAML / SCIM directory sync | Enterprise contract feature |
| Fine-grained RBAC beyond tenant isolation | Enterprise contract feature |
| Audit log export to external SIEMs | Enterprise contract feature |
| Hosted control plane and dashboard UI | Our product surface |

Note what is *not* on that list: ingestion, enrichment, storage, schema,
querying, retention, SDKs, session tracking, identity resolution. Those are
the product. They stay open.

## How the boundary works technically

The open repository defines interfaces in `ee/` and ships **working no-op
implementations** of all of them. The open build is not crippled — quota
checks pass, metering discards, entitlements report `community` with every
open feature enabled.

```
ee.Hooks {
    Licensor       // entitlements; open default = community, unlimited
    QuotaEnforcer  // open default = always allow
    UsageMeter     // open default = discard
    AuditSink      // open default = discard
    ReplayStore    // open default = nil, feature simply absent
}
```

`app.Run(ctx, hooks)` takes those hooks. `cmd/lumen` passes
`ee.CommunityHooks()`. The commercial binary lives in its own module, imports
`github.com/SyneHQ/lumen/app`, and passes its own implementations.

**There are no build tags and no conditional dependencies.** The public
`go.mod` never references the private module. Consequences:

- `git clone && go build ./...` works for anyone, with no submodule access
- The `enterprise/` directory is an empty gitlink for everyone but us
- We cannot accidentally break the open build with a private change
- The interfaces in `ee/` are public, so you can write your own
  implementations — bring-your-own quota logic is a supported use case

```
lumen (public, AGPL)                 lumen-enterprise (private, commercial)
├── ee/         interfaces  ◄──────── implements
├── app/        Run(hooks)  ◄──────── calls
├── internal/   engine                ├── go.mod (separate module)
└── cmd/lumen   → CommunityHooks()    └── cmd/lumen-enterprise → EEHooks()
```

## Rules we hold ourselves to

1. **Nothing that is open today becomes closed later.** Moving a feature from
   open to commercial is a bait-and-switch. We won't.
2. **No crippled open build.** If the open version cannot do something, it is
   because the feature only exists for our hosted operations, not because we
   removed a line.
3. **The `ee/` interfaces stay public and documented.** You can implement them
   yourself.
4. **New core capability lands open.** Ingest-path and storage work is core by
   definition.
5. **If we ever relicense, the last AGPL commit stays forkable.** That is what
   AGPL guarantees and we are not going to pretend otherwise.

## FAQ

**Does AGPL mean I have to open source my app if I use Lumen?**
No. Your application talks to Lumen over the network using Apache-2.0 SDKs.
AGPL section 13 applies to people who modify *Lumen itself* and offer the
modified Lumen to others over a network. Running unmodified Lumen for your
own product triggers nothing.

**Can I modify Lumen for internal use without publishing?**
Yes. AGPL obligations attach to conveying the software or offering it to
remote users. Internal use is unrestricted.

**Can I offer a competing hosted Lumen service?**
Yes, if you publish your modifications under AGPL-3.0. That is the deal.

**Can I get a non-AGPL license for the server?**
Contact sales@synehq.com. We hold the copyright and can dual-license.

**Do I need the enterprise submodule to contribute?**
No. It is optional and unnecessary for all open-source development.

**Can I self-host without limits?**
Yes. There is no license check in the open build. There is nothing to bypass.
