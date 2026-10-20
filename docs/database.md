# Public database setup

The public edition includes one fresh-install schema in
`scripts/public-db/schema.sql`. It contains tables, indexes, foreign keys,
checks, functions, triggers and default privilege restrictions. The only
initialized state is the legacy fence clock at zero. It contains no user data,
deployment roles or private migration history.

## Initialize an empty database

Install PostgreSQL 16, its `psql` client, Python 3, Node and pnpm. Create an empty
UTF8 database using an account that can create objects in its `public` schema,
then:

```bash
export DATABASE_URL=postgresql://app_user:password@127.0.0.1:5432/wallet_trade_console
pnpm install --frozen-lockfile
pnpm --filter @wallet-trade/database db:generate
pnpm --filter @wallet-trade/database db:init
```

`db:init` runs in one transaction and rejects a nonempty `public` schema or a
database whose server encoding is not UTF8. Registry digest canonicalization
requires Unicode support.
Repeating it is an error; it never drops or recreates existing tables.
Do not use it to upgrade an existing database. Plan and review an explicit
upgrade against your own schema and backup first.

Prisma `db:push` only models the Prisma-supported structure. It cannot replace
this initializer because registry activation, Campaign commands and outbox
repair depend on additional SQL guards. To compare the initialized table
structure with Prisma without changing it:

```bash
pnpm --filter @wallet-trade/database exec prisma migrate diff \
  --from-url "$DATABASE_URL" \
  --to-schema-datamodel ./prisma/schema.prisma --exit-code
```

The API requires `NODE_ENV` and every `WEB3_*` credential shown in
`services/api-go/.env.example`. Generate your own signing key. Enabling
`CAMPAIGN_V2_ENABLED` also enables its authorized push namespace when realtime
is enabled.

## Roles

The initializer creates no login roles or credentials. The database owner is
suitable for disposable development databases. For separate execution-registry
API and publisher roles, review `scripts/ops/execution-registry-grants.sql`.
Create the two nonprivileged login roles yourself, replace its three role
placeholders with distinct plain lowercase identifiers, and apply the SQL as
one transaction using a database administrator. The function owner is an
isolated non-login role. Do not grant API callers direct registry publication
or activation rights.

The template grants only registry-specific rights. An application's remaining
table permissions require their own review.

## Verification and maintenance

Public CI initializes fresh PostgreSQL 16 databases from this exact SQL. It
checks Prisma structure parity, nonempty-schema rejection, registry least
privilege, Campaign persistence, outbox repair, persistent sessions, mandatory
push recovery and cancellation through the actual API and Socket.IO.
The cancellation fixture uses synthetic server-issued sessions. It does not
prove a real wallet, SIWE or browser flow.

Changes to `schema.sql` must update this public-owned baseline and rerun the
same checks. Include changed guards and any required singleton initialization;
matching Prisma tables alone does not establish behavioral parity. The public
exporter preserves the entire `scripts` directory across future source syncs.
