# Go backend

`services/api-go` contains the HTTP API and the background workers used by the
open-source application. The binaries share domain packages but keep transport,
indexing, realtime, and scheduled work in separate entry points.

## Binaries

| Path | Responsibility |
|---|---|
| `cmd/api` | HTTP API and optional realtime server |
| `cmd/indexer` | On-chain event indexer |
| `cmd/market-stream` | Market snapshot worker |
| `cmd/pricefeed` | Reference price worker |
| `cmd/bridgerelayer` | Optional bridge delivery worker |
| `cmd/routes` | Route inventory used by tests and contract checks |

## Configuration

Copy [`.env.example`](.env.example) to `.env` and provide a local
`DATABASE_URL`, `NODE_ENV`, `WEB3_JWT_ISSUER`, `WEB3_JWT_AUDIENCE`,
`WEB3_JWT_KEY_ID`, `WEB3_JWT_SECRET` and `WEB3_AUTH_EPOCH`. These credentials
are required at startup; the example describes how to generate a local secret.
Initialize the database with the root `pnpm --filter @wallet-trade/database db:init`
command; see [Database setup](../../docs/database.md). Optional integrations
fail closed or report that they are not configured. Filled environment files, signer keys, and production endpoints
must never be committed.

The binaries discover contract metadata from `packages/shared/deployments` when
run inside this monorepo. Set `CONTRACTS_DEPLOYMENTS_DIR` only when packaging the
service independently.

## Run and test

```bash
set -a
. ./.env
set +a
go run ./cmd/api
go vet ./...
go test -race -count=1 ./...
go build ./...
```

The Go API reads process environment variables, so load the filled file before
starting it.

The default API listen address is `:8080`; the root `pnpm dev:api` command uses
port `8090` for local development.
