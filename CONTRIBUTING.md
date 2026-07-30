# Contributing to Lumen

Thanks for considering a contribution. Lumen is an open-core project: the
ingestion engine, storage schema, and client SDKs are open source, and a
small set of commercial features live in a separate private repository. See
[OPEN_CORE.md](OPEN_CORE.md) for exactly where that line sits.

## Ground rules

- Be respectful. See [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md).
- **Never open a public issue for a security vulnerability.** Follow
  [SECURITY.md](SECURITY.md) instead.
- Open an issue before starting anything large. A rejected 2,000-line PR is
  a bad day for both of us.

## Licensing and provenance

By submitting a pull request you agree that your contribution is licensed
under the license already covering the files you touched:

| Path | License |
|---|---|
| everything except the below | AGPL-3.0-or-later |
| `sdk/`, `proto/`, `gen/` | Apache-2.0 |

We use the [Developer Certificate of Origin](https://developercertificate.org/)
(DCO), not a CLA. Sign off every commit:

```bash
git commit -s -m "fix(enrich): handle empty user-agent"
```

That appends a `Signed-off-by:` trailer asserting you have the right to
submit the code. CI rejects unsigned commits. Amend with
`git commit --amend -s --no-edit` if you forget.

> Do not paste code from another project unless its license is compatible
> and you say so in the PR description.

## Development setup

Requirements: Go 1.24+, Docker, `buf` (only if changing `.proto` files).

```bash
git clone https://github.com/SyneHQ/lumen.git
cd lumen

# 1. Start ClickHouse + Postgres
docker compose up -d clickhouse postgres

# 2. Required: no insecure default exists for the admin token
export ADMIN_TOKEN="$(openssl rand -hex 32)"

# 3. Run
go run ./cmd/lumen
```

Ports default to `50051` (ingest), `50052` (admin), `9090` (metrics).

You do **not** need the `enterprise/` submodule. It is optional, private,
and unnecessary for any open-source contribution.

### Tests

```bash
go test -race ./...                    # unit + wire E2E, no external deps
go test -race -tags=livedb ./tests/... # requires docker compose services up
make lint                              # gofmt, go vet, staticcheck
```

Every PR must pass `go test -race ./...`. The race detector is not optional
here: ingest is a concurrent hot path and we have been bitten before.

### Regenerating protobuf

```bash
buf generate    # writes gen/
```

Commit the generated `gen/` output alongside the `.proto` change. Never
hand-edit files in `gen/`.

## Commit style

[Conventional Commits](https://www.conventionalcommits.org/). The scope is
usually the package name.

```
feat(ingest): support gzip-compressed batch payloads
fix(auth): treat expired keys as unauthenticated, not internal error
perf(ch): reuse column buffers across batch inserts
docs(readme): document GEOIP_DB_PATH
refactor(config): collapse duplicate secret resolution
test(enrich): cover malformed referrer URLs
chore(deps): bump clickhouse-go to v2.30.0
```

Breaking changes get a `!` and a `BREAKING CHANGE:` footer.

## Pull requests

1. Branch from `main`.
2. Keep it focused — one logical change.
3. Add tests. Bug fixes need a regression test that fails before the fix.
4. Update docs if you changed behavior, config, or the wire format.
5. Fill in the PR template: what, why, how tested.

Reviews aim for a first response within a few business days. Maintainers may
push small fixups directly to your branch to avoid a round-trip.

## What we're likely to accept

- Bug fixes, with tests
- Performance work on the ingest path, with before/after numbers
- New enrichment fields, SDK ergonomics, additional SDK languages
- Docs, examples, self-hosting guides, deployment recipes
- ClickHouse schema and query improvements

## What we're likely to decline

- Vendor-specific integrations that belong in a plugin, not core
- Large dependencies added for small convenience
- Reformatting or renaming sweeps unrelated to a fix
- Features that only make sense for our hosted product — those belong in
  the private enterprise module
- Anything that weakens tenant isolation

## Wire compatibility

`proto/` is a public contract. SDKs in the wild will be older than your
server. Adding fields is fine; renaming, renumbering, or removing them is
not. Never reuse a field number.

## Architecture notes for new contributors

- `internal/auth` — API key verification, Ristretto cache, tenant context
- `internal/enrich` — user-agent, GeoIP, and URL/UTM parsing (pure, easy to test)
- `internal/ingest` — Connect handlers, validation, batching
- `internal/ch` — ClickHouse writes (`async_insert`, dedup tokens)
- `internal/pg` — Postgres control plane (teams, API keys)
- `ee/` — extension interfaces with open no-op defaults; the seam the
  commercial module plugs into. Interface changes here need a maintainer
  review because they affect a repo you cannot see.

## Questions

Open a [discussion](https://github.com/SyneHQ/lumen/discussions) or an issue.
