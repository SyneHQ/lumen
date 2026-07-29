# Lumen

Lumen is a multi-tenant event ingestion and session analytics service built in Go. It ingests high-throughput telemetry streams over Connect RPC / HTTP/2, enriches events server-side, and stores them in ClickHouse for real-time analytical queries.

Postgres is used for transactional control plane metadata (tenant accounts and API keys), while ClickHouse stores append-only event logs, materialized sessions, and identity maps.

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

### 2. Run the server
```bash
go run ./cmd/lumen
```

Or run via Docker:
```bash
docker run -p 50051:50051 -p 50052:50052 ghcr.io/synehq/lumen:latest
```

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
| `ADMIN_TOKEN` | `lumen-internal-secret-token` | Token required for admin provisioning RPCs |
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
