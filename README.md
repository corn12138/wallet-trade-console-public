# Wallet Trade Console

Wallet Trade Console is an open-source Web3 trading workspace that combines a
Next.js frontend, a Go API and indexer, PostgreSQL-backed projections, realtime
market data, and Solidity contracts tested with Hardhat and Foundry.

This repository is the sanitized public source edition. Production credentials,
infrastructure manifests, private deployment workflows, operational evidence,
and environment-specific endpoints are intentionally maintained outside this
repository. Nothing in this repository deploys to the project's production
environment.

## Highlights

- Sign-In With Ethereum with nonce consumption, JWT refresh rotation, and CSRF protection.
- Transaction preflight and deterministic security checks before wallet signing.
- Go API, chain indexer, price feed, bridge relayer, and Socket.IO-compatible realtime service.
- Perpetuals, launchpad, swap, staking, bridge, NFT, and portfolio product surfaces.
- Hardhat deployment tooling plus Foundry parity, fuzz, and invariant-oriented tests.
- Prisma schema as the portable database contract.

## Architecture

```text
Browser + wallet
      |
      | HTTP / Socket.IO
      v
Next.js web  -------------------+
      |                         |
      v                         v
Go API + workers          Solidity contracts
      |                         |
      +---- PostgreSQL ---------+
      +---- Redis (optional)
```

The public repository contains only portable application code. You provide your
own database, RPC endpoint, authentication secrets, storage, and deployment
platform. See [Architecture](docs/architecture.md) for the component boundaries.

## Repository layout

| Path | Purpose |
|---|---|
| `apps/web` | Next.js 16 App Router frontend |
| `services/api-go` | Go API and background workers |
| `packages/shared` | Shared TypeScript contracts, ABIs, and deployment registry |
| `packages/database` | Portable Prisma schema and database helpers |
| `contracts` | Hardhat contracts, tests, and deployment tooling |
| `contracts-foundry` | Foundry parity, fuzz, and Solidity tests |

## Prerequisites

- Node.js 22.12 or newer
- pnpm 9.15.4
- Go 1.25.13 or newer
- PostgreSQL 16 for data-backed local flows
- Foundry for Solidity tests

The contracts are testnet-oriented reference implementations and have not been
represented as a completed third-party security audit. Nothing here is
financial advice.

## Quick start

```bash
pnpm install --frozen-lockfile
cp apps/web/.env.example apps/web/.env.local
cp services/api-go/.env.example services/api-go/.env
export DATABASE_URL=postgresql://app_user:password@127.0.0.1:5432/wallet_trade_console
pnpm --filter @wallet-trade/database db:generate
pnpm --filter @wallet-trade/database db:push
```

Set a local `DATABASE_URL` in `services/api-go/.env`, then run the API and web
app in separate terminals:

```bash
pnpm dev:api
pnpm dev:web
```

The default development URLs are:

- Web: `http://127.0.0.1:3002`
- API: `http://127.0.0.1:8090/api`

Testnet and AI features remain disabled until you provide their optional
environment variables. Never commit a filled `.env` file or a funded private
key.

## Validation

```bash
pnpm type-check
pnpm test
pnpm build

cd services/api-go
go vet ./...
go test -race -count=1 ./...
go build ./cmd/api ./cmd/bridgerelayer ./cmd/indexer ./cmd/market-stream ./cmd/pricefeed ./cmd/routes

cd ../../contracts-foundry
forge build
forge test
```

The public CI runs the same frontend, Go, and Foundry gates without production
secrets. `python3 scripts/public_release_audit.py` performs the repository's
additional public-release policy check.

## Public mirror model

The production repository remains private and is the source of truth. Public
updates are exported from an approved committed revision, sanitized and tested
in an isolated candidate, and only then committed here. Public contributions
are reviewed and imported through the private integration pipeline; this
repository never deploys directly to production.

See [Public mirror policy](docs/public-mirror.md) and
[Contributing](CONTRIBUTING.md) before opening a pull request.

## Security

Please do not report vulnerabilities or suspected credential exposure in a
public issue. Follow [SECURITY.md](SECURITY.md) instead.

## License

MIT, see [LICENSE](LICENSE).
