# Architecture

Wallet Trade Console separates user-facing rendering, API orchestration,
on-chain projection, realtime delivery, and contract execution.

## Components

- `apps/web`: Next.js App Router UI, wallet connection, SIWE session flow, and
  REST/Socket.IO clients.
- `services/api-go/cmd/api`: HTTP boundary, authentication, validation, and
  read/write use-case composition.
- `services/api-go/cmd/indexer`: confirmed-chain event ingestion and monotonic
  checkpoints.
- `services/api-go/cmd/market-stream`: market snapshot production and optional
  Redis fanout.
- `services/api-go/cmd/pricefeed`: reference-price ingestion.
- `services/api-go/cmd/bridgerelayer`: optional bridge delivery worker; it runs
  observe-only when signer material is absent.
- `packages/database`: Prisma schema and migrations.
- `contracts` and `contracts-foundry`: protocol implementation, deployment
  metadata, and independent TypeScript/Solidity test surfaces.

## Trust boundaries

1. Wallet signatures are verified server-side; client-provided identity is not
   trusted on guarded routes.
2. Chain-derived state is committed with checkpoints so retries cannot move the
   indexer backwards.
3. Transaction review is deterministic. Optional AI text can explain a verdict
   but cannot change it.
4. Missing RPC, signer, storage, Redis, or AI configuration must degrade to an
   explicit non-executable or unavailable state.
5. Environment-specific endpoints and deployment credentials are runtime input,
   never source defaults.

## Local topology

The smallest useful local setup is the web app, Go API, and PostgreSQL. Redis,
RPC access, indexer workers, media storage, bridge signing, and AI explanations
are optional and can be enabled independently.
