#!/usr/bin/env python3
"""Initialize an empty PostgreSQL database with the complete public schema."""

import os
from pathlib import Path
import subprocess
import sys
from urllib.parse import parse_qsl, unquote, urlsplit


def main() -> int:
    parts = urlsplit(os.environ.get("DATABASE_URL", ""))
    if parts.scheme not in ("postgres", "postgresql") or not parts.hostname or not parts.path.strip("/"):
        print("DATABASE_URL must name an existing, empty PostgreSQL database.", file=sys.stderr)
        return 2
    query = dict(parse_qsl(parts.query))
    if query.pop("schema", "public") != "public":
        print("The public baseline supports only the public schema.", file=sys.stderr)
        return 2
    supported = {
        "sslmode": "PGSSLMODE",
        "sslrootcert": "PGSSLROOTCERT",
        "sslcert": "PGSSLCERT",
        "sslkey": "PGSSLKEY",
        "channel_binding": "PGCHANNELBINDING",
        "connect_timeout": "PGCONNECT_TIMEOUT",
        "application_name": "PGAPPNAME",
        "target_session_attrs": "PGTARGETSESSIONATTRS",
        "options": "PGOPTIONS",
    }
    if query.keys() - supported.keys():
        print("Unsupported DATABASE_URL options: " + ", ".join(sorted(query.keys() - supported.keys())), file=sys.stderr)
        return 2
    env = os.environ.copy()
    for name in ("PGSERVICE", "PGSERVICEFILE", "PGHOSTADDR", "PGPASSWORD"):
        env.pop(name, None)
    env.update(PGHOST=parts.hostname, PGPORT=str(parts.port or 5432), PGDATABASE=unquote(parts.path[1:]))
    if parts.username is not None:
        env["PGUSER"] = unquote(parts.username)
    if parts.password is not None:
        env["PGPASSWORD"] = unquote(parts.password)
    env.update({supported[key]: value for key, value in query.items()})
    guard = """
BEGIN;
SELECT pg_advisory_xact_lock(78124793810632);
DO $bootstrap$
BEGIN
  IF current_setting('server_encoding') <> 'UTF8' THEN
    RAISE EXCEPTION 'public schema requires a UTF8 database';
  END IF;
  IF EXISTS (
    SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
    WHERE n.nspname='public' AND c.relkind IN ('r','p','v','m','S','f')
  ) OR EXISTS (
    SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
    WHERE n.nspname='public'
  ) OR EXISTS (
    SELECT 1 FROM pg_type t JOIN pg_namespace n ON n.oid=t.typnamespace
    WHERE n.nspname='public' AND t.typtype IN ('e','d')
  ) THEN
    RAISE EXCEPTION 'public schema is not empty; bootstrap is fresh-install only';
  END IF;
END
$bootstrap$;
"""
    schema = Path(__file__).with_name("schema.sql").read_text()
    # A single transaction preserves an existing database on a failed preflight
    # or DDL error. The URL stays out of argv and successful command output.
    result = subprocess.run(
        [env.get("PSQL", "psql"), "-X", "-q", "-v", "ON_ERROR_STOP=1"],
        input=guard + schema + "\nCOMMIT;\n",
        text=True,
        env=env,
        stdout=subprocess.DEVNULL,
    )
    if result.returncode == 0:
        print("Public database initialized (tables, constraints, functions and triggers).")
    return result.returncode


if __name__ == "__main__":
    sys.exit(main())
