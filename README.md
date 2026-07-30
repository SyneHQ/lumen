# Lumen

[![License: AGPL v3](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE)
[![SDKs: Apache 2.0](https://img.shields.io/badge/SDKs-Apache--2.0-green.svg)](sdk/LICENSE)

Lumen is a multi-tenant event ingestion and session analytics service built in Go. It ingests high-throughput telemetry streams over Connect RPC / HTTP/2, enriches events server-side, and stores them in ClickHouse for real-time analytical queries.

Postgres is used for transactional control plane metadata (tenant accounts and API keys), while ClickHouse stores append-only event logs, materialized sessions, and identity maps.

**Open source, self-hostable, no limits.** No event caps, no license key, no phone-home. The server is AGPL-3.0; the client SDKs are Apache-2.0 so you can embed them in closed-source apps. A small set of commercial-operations features (licensing, quotas, billing, SSO) live in a separate private module and are not required to run anything. See [OPEN_CORE.md](OPEN_CORE.md).

---

## Architecture

```
                    ┌─────────────────────────┐
                    │ Client SDKs (Go/TS/Py)  │
                    └────────────┬────────────┘
                                 │ Connect RPC (HTTP/2)
                                 ▼
                    ┌─────────────────────────┐
                    │  Lumen Ingest Engine    │
                    │  - Auth Interceptor     │
                    │  - UA & UTM Enricher    │
                    └────┬───────────────┬────┘
                         │               │
  Async Batches          │               │ Key & Tenant Lookup
  (async_insert=1)       ▼               ▼
            ┌────────────────┐   ┌────────────────┐
            │   ClickHouse   │   │    Postgres    │
            │   (OLAP Logs)  │   │(Control Plane) │
            └────────────────┘   └────────────────┘
```

---

## Quickstart

### 1. Run local dependencies
```bash
docker compose up -d clickhouse postgres
```

### 2. Set the admin token

`ADMIN_TOKEN` has **no default** — the admin port provisions tenants and mints
API keys, so the server refuses to start without one.

```bash
export ADMIN_TOKEN="$(openssl rand -hex 32)"
```

For throwaway local work, `LUMEN_DEV=1` generates an ephemeral token and prints
it at startup instead.

### 3. Run the server
```bash
go run ./cmd/lumen
```

Or run via Docker:
```bash
docker run -p 50051:50051 -p 50052:50052 \
  -e ADMIN_TOKEN="$(openssl rand -hex 32)" \
  ghcr.io/synehq/lumen:latest
```

> Never expose the admin port (`50052`) to the internet, and terminate TLS in
> front of the ingest port. See [SECURITY.md](SECURITY.md) for the full
> operator hardening checklist.

---

## Client SDK Usage

### Go

```go
package main

import (
    "context"
    "time"
    "github.com/SyneHQ/lumen/sdk/go"
)

func main() {
    client, _ := lumen.New("lum_live_your_api_key",
        lumen.WithEndpoint("http://localhost:50051"),
        lumen.WithBatchSize(100),
    )
    defer client.Close(context.Background())

    // Track an event
    client.Track(context.Background(), "order_completed", lumen.P{
        "amount": 99.50,
        "currency": "USD",
    })

    // Identify user
    client.Identify(context.Background(), "usr_12345", lumen.P{
        "plan": "pro",
    })
}
```

### TypeScript (`@syne/lumen-js`)

```typescript
import { Lumen } from "@syne/lumen-js";

const lumen = new Lumen({
  apiKey: "lum_live_your_api_key",
  endpoint: "http://localhost:50051",
});

// Track pageview
lumen.track("page_view", { path: "/pricing" });

// Identify user
lumen.identify("usr_12345", { email: "alex@example.com" });
```

### Python

```python
from lumen import Client

lumen = Client("lum_live_your_api_key", endpoint="http://localhost:50051")

lumen.track("signup_completed", {"plan": "growth"})
lumen.identify("usr_12345", {"company": "Acme Corp"})
lumen.close()
```

---

## Configuration

Settings can be set via environment variables or loaded directly from Infisical at startup.

| Environment Variable | Default | Description |
|---|---|---|
| `INGEST_PORT` | `50051` | Port for public Connect / gRPC telemetry ingestion |
| `ADMIN_PORT` | `50052` | Port for internal control plane RPCs |
| `METRICS_PORT` | `9090` | Prometheus metrics and health check port |
| `ADMIN_TOKEN` | **required, no default** | Token for admin provisioning RPCs. Min 32 chars. Server refuses to start without it. |
| `LUMEN_DEV` | `false` | Development only. Generates an ephemeral `ADMIN_TOKEN` instead of failing. |
| `CLICKHOUSE_DSN` | `clickhouse://127.0.0.1:9001/lumen` | Native ClickHouse connection string |
| `POSTGRES_DSN` | `postgres://postgres:postgres@localhost:5433/lumen?sslmode=disable` | Postgres connection string |

### Infisical Secret Loading

If `INFISICAL_CLIENT_ID` and `INFISICAL_CLIENT_SECRET` (or `INFISICAL_TOKEN`) are set, Lumen automatically fetches configuration secrets directly into memory at startup without writing them to process environment variables.

```bash
export INFISICAL_CLIENT_ID="your-client-id"
export INFISICAL_CLIENT_SECRET="your-client-secret"
export INFISICAL_PROJECT_ID="your-project-id"
export INFISICAL_ENV="prod"
```

---

## Testing

Run unit tests, E2E flow tests, and data race detection:

```bash
# Run all tests with Go data race detector
go test -race -v ./...
```

To run tests against live local ClickHouse and Postgres instances:
```bash
docker compose up -d clickhouse postgres
go test -v ./tests/live_db_test.go
```

---

## Project layout

```
ee/            Extension interfaces + open no-op defaults (the commercial seam)
app/           Bootstrap: app.Run(ctx, hooks)
cmd/lumen/     Community binary
internal/      Engine: auth, enrich, ingest, ch, pg, provision, server
migrations/    ClickHouse + Postgres schema
proto/, gen/   Wire contract (Apache-2.0)
sdk/           Go, TypeScript, Python clients (Apache-2.0)
enterprise/    Private submodule, optional, not needed to build or run
```

---

## Contributing

Bug reports and PRs welcome. See [CONTRIBUTING.md](CONTRIBUTING.md) for setup,
DCO sign-off, and commit conventions. Security issues go to
[SECURITY.md](SECURITY.md) — **never** a public issue.

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

---

## License

| Path | License |
|---|---|
| Server (everything not listed below) | [AGPL-3.0-or-later](LICENSE) |
| `sdk/`, `proto/`, `gen/` | [Apache-2.0](sdk/LICENSE) |
| `enterprise/` (private submodule) | Commercial, not part of this distribution |

Using Lumen in your product does **not** put your code under AGPL — your app
talks to the server over the network using Apache-2.0 SDKs. See
[OPEN_CORE.md](OPEN_CORE.md) for the full explanation, the open-core boundary,
and the commitments we hold ourselves to.

Need a non-AGPL server license? sales@synehq.com.
"Lumen" and "Syne" are trademarks — see [TRADEMARK.md](TRADEMARK.md).
