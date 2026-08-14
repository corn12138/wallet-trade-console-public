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
`DATABASE_URL`. Optional integrations fail closed or report that they are not
configured. Filled environment files, signer keys, and production endpoints
must never be committed.

The binaries discover contract metadata from `packages/shared/deployments` when
run inside this monorepo. Set `CONTRACTS_DEPLOYMENTS_DIR` only when packaging the
service independently.

## Run and test

```bash
go run ./cmd/api
go vet ./...
go test -race -count=1 ./...
go build ./cmd/api ./cmd/bridgerelayer ./cmd/indexer ./cmd/market-stream ./cmd/pricefeed ./cmd/routes
```

The default API listen address is `:8080`; the root `pnpm dev:api` command uses
port `8090` for local development.
