# Public mirror policy

This repository is published through a one-way, reviewed export. The private
integration repository remains the production source of truth and retains
deployment configuration and rollback history.

## What is exported

- frontend, Go backend, shared packages, database schema, and contracts;
- tests and portable development configuration;
- public documentation and a CI workflow that uses no production secrets.

## What is never exported

- Git history, pull requests, Actions logs, or deployment metadata from the
  private repository;
- production workflows, infrastructure manifests, SSH configuration, runtime
  evidence, screenshots, logs, database snapshots, or filled environment files;
- maintainer-specific endpoints, filesystem paths, account identifiers, or
  credentials.

## Release sequence

1. Select an approved committed private revision. Dirty working-tree content is
   not eligible for export.
2. Generate a fresh candidate in an isolated directory using an allowlist.
3. Apply public configuration overlays and remove internal-only material.
4. Run the public-release audit, gitleaks, trufflehog, and binary-content review.
5. Run TypeScript, Go, and Solidity tests and builds without production secrets.
6. Review the complete candidate diff before committing it to this repository.

The public checkout contains a local-only exporter. It reads a committed Git
revision, writes a separate candidate, applies the maintained public overlays,
and stops without committing or pushing:

```bash
python3 scripts/sync_from_private.py \
  --source /path/to/private-checkout \
  --ref origin/main \
  --output /tmp/wallet-trade-console-public-candidate
```

Run it from a clean public checkout so its public-owned templates are the
reviewed versions on `main`. The command deliberately refuses a non-empty output
directory and never copies dirty private working-tree content.

An unreviewed candidate must never be pushed to a public branch, including a
pull-request branch, because every object in a public repository is already
public.

The trufflehog exclusion file is intentionally limited to credential-parser
tests containing fake database URLs and forge-std's upstream shared RPC fixture.
Those files remain covered by gitleaks and the repository policy scanner; do not
add broad directory exclusions.

## Contribution flow

Public contributions are reviewed here, imported into a private integration
branch, and tested there. The resulting approved private revision is then
exported back through the same release sequence. There is no automatic path
from a public pull request to production.
