# Contributing

Contributions are welcome through GitHub pull requests.

## Development flow

1. Fork the public repository and create a focused branch.
2. Keep the change independent of any maintainer-owned endpoint or account.
3. Run the relevant TypeScript, Go, and Solidity checks.
4. Run `python3 scripts/public_release_audit.py` before committing.
5. Open a pull request that explains behavior, risk, and validation.

## Public-source boundary

This repository is a sanitized public mirror. Never submit:

- filled environment files, credentials, signer material, or wallet recovery data;
- private infrastructure addresses, SSH configuration, database snapshots, or logs;
- production deployment workflows or credentials;
- screenshots containing authenticated accounts, balances, tokens, or personal data.

Public pull requests do not deploy to production. Accepted changes are reviewed
and integrated through a separate private release process before a later public
snapshot is published.

Database changes should update `packages/database/prisma/schema.prisma`. The
maintainer generates and reviews production migrations only after the change is
accepted into the private integration repository. Contract changes must keep
Hardhat deployment metadata and the shared generated address registry aligned.

## Validation

```bash
pnpm install --frozen-lockfile
pnpm --filter @wallet-trade/database db:generate
pnpm type-check
pnpm test
pnpm build

cd services/api-go
go vet ./...
go test -race -count=1 ./...

cd ../../contracts-foundry
forge build
forge test
```

Keep changes small, preserve existing security boundaries, and document any
non-obvious compatibility or failure-handling decision next to the code that
owns it.
