-- Least-privilege grants for the execution registry.
-- Template: __API_ROLE__, __PUBLISHER_ROLE__ and __OWNER_ROLE__ are replaced by
-- the database operator after validating each name as a plain lowercase
-- identifier. Apply as one transaction after creating the API and publisher roles.
--
-- The API reads the registry and writes only the attempt columns it already
-- owns; it can neither stage, seal, activate nor move the pointer. The
-- publisher stages and seals through the CLI and activates only through
-- execution_registry_activate (SECURITY DEFINER). The invariants themselves
-- are trigger-enforced and hold whatever is granted.

-- SECURITY DEFINER must not retain the migration superuser's privileges.
-- The owner cannot log in and receives only the reads and pointer/history
-- writes used by this function; publisher/API roles are never its members.
DO $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '__OWNER_ROLE__') THEN
    CREATE ROLE __OWNER_ROLE__ NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
  END IF;
  IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = '__OWNER_ROLE__'
      AND (rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls))
      OR pg_has_role('__API_ROLE__', '__OWNER_ROLE__', 'MEMBER')
      OR pg_has_role('__PUBLISHER_ROLE__', '__OWNER_ROLE__', 'MEMBER')
      OR EXISTS (SELECT 1 FROM pg_auth_members WHERE member = (SELECT oid FROM pg_roles WHERE rolname = '__OWNER_ROLE__')) THEN
    RAISE EXCEPTION 'execution registry owner must be an isolated non-login role';
  END IF;
  IF pg_has_role('__API_ROLE__', '__PUBLISHER_ROLE__', 'MEMBER')
      OR EXISTS (SELECT 1 FROM pg_roles WHERE rolname IN ('__API_ROLE__','__PUBLISHER_ROLE__')
        AND (rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls)) THEN
    RAISE EXCEPTION 'API and publisher must be separate least-privilege roles without inherited publication access';
  END IF;
END $$;
REVOKE ALL ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings,
  execution_registry_activations, execution_registry_pointer FROM __OWNER_ROLE__;
GRANT USAGE ON SCHEMA public TO __OWNER_ROLE__;
GRANT SELECT ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings,
  execution_registry_activations, execution_registry_pointer TO __OWNER_ROLE__;
GRANT INSERT ON execution_registry_activations, execution_registry_pointer TO __OWNER_ROLE__;
GRANT UPDATE (active_revision, activation_version) ON execution_registry_pointer TO __OWNER_ROLE__;
ALTER FUNCTION execution_registry_activate(BIGINT, BIGINT, TEXT, TEXT, JSONB) OWNER TO __OWNER_ROLE__;

REVOKE ALL ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings,
  execution_registry_activations, execution_registry_pointer FROM __API_ROLE__, __PUBLISHER_ROLE__;
REVOKE ALL ON FUNCTION execution_registry_activate(BIGINT, BIGINT, TEXT, TEXT, JSONB) FROM PUBLIC, __API_ROLE__;

GRANT SELECT ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings,
  execution_registry_activations, execution_registry_pointer TO __API_ROLE__;

GRANT SELECT ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings,
  execution_registry_activations, execution_registry_pointer TO __PUBLISHER_ROLE__;
GRANT INSERT ON execution_registry_snapshots, execution_registry_entries, execution_registry_markets, execution_registry_bindings TO __PUBLISHER_ROLE__;
GRANT UPDATE (sealed, content_digest) ON execution_registry_snapshots TO __PUBLISHER_ROLE__;
GRANT USAGE ON SEQUENCE execution_registry_revision_seq TO __PUBLISHER_ROLE__;
GRANT EXECUTE ON FUNCTION execution_registry_activate(BIGINT, BIGINT, TEXT, TEXT, JSONB) TO __PUBLISHER_ROLE__;

-- Check effective privileges, including grants inherited through other roles.
-- Direct REVOKE cannot remove those; the operator must separate the roles.
DO $$
DECLARE relation TEXT; column_name TEXT;
BEGIN
  FOREACH relation IN ARRAY ARRAY['execution_registry_snapshots','execution_registry_entries','execution_registry_markets',
      'execution_registry_bindings','execution_registry_activations','execution_registry_pointer'] LOOP
    IF has_table_privilege('__API_ROLE__', relation, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
        OR has_any_column_privilege('__API_ROLE__', relation, 'INSERT,UPDATE,REFERENCES') THEN
      RAISE EXCEPTION 'API retains inherited registry write access';
    END IF;
  END LOOP;
  FOREACH relation IN ARRAY ARRAY['execution_registry_activations','execution_registry_pointer'] LOOP
    IF has_table_privilege('__PUBLISHER_ROLE__', relation, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
        OR has_any_column_privilege('__PUBLISHER_ROLE__', relation, 'INSERT,UPDATE,REFERENCES') THEN
      RAISE EXCEPTION 'publisher retains direct activation write access';
    END IF;
  END LOOP;
  FOREACH relation IN ARRAY ARRAY['execution_registry_snapshots','execution_registry_entries','execution_registry_markets','execution_registry_bindings'] LOOP
    IF has_table_privilege('__PUBLISHER_ROLE__', relation, 'DELETE,TRUNCATE,REFERENCES,TRIGGER')
        OR has_any_column_privilege('__PUBLISHER_ROLE__', relation, 'REFERENCES')
        OR has_table_privilege('__OWNER_ROLE__', relation, 'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
        OR has_any_column_privilege('__OWNER_ROLE__', relation, 'INSERT,UPDATE,REFERENCES') THEN
      RAISE EXCEPTION 'publisher or owner retains unexpected snapshot write access';
    END IF;
    FOR column_name IN SELECT attname FROM pg_attribute WHERE attrelid=relation::regclass AND attnum>0 AND NOT attisdropped LOOP
      IF NOT (relation='execution_registry_snapshots' AND column_name IN ('sealed','content_digest'))
          AND has_column_privilege('__PUBLISHER_ROLE__', relation, column_name, 'UPDATE') THEN
        RAISE EXCEPTION 'publisher retains unexpected snapshot column update access';
      END IF;
    END LOOP;
  END LOOP;
  IF has_table_privilege('__OWNER_ROLE__','execution_registry_activations','UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
      OR has_any_column_privilege('__OWNER_ROLE__','execution_registry_activations','UPDATE,REFERENCES')
      OR has_table_privilege('__OWNER_ROLE__','execution_registry_pointer','DELETE,TRUNCATE,REFERENCES,TRIGGER')
      OR has_any_column_privilege('__OWNER_ROLE__','execution_registry_pointer','REFERENCES')
      OR has_column_privilege('__OWNER_ROLE__','execution_registry_pointer','id','UPDATE') THEN
    RAISE EXCEPTION 'owner retains unexpected activation write access';
  END IF;
  IF has_function_privilege('__API_ROLE__', 'execution_registry_activate(bigint,bigint,text,text,jsonb)', 'EXECUTE') THEN
    RAISE EXCEPTION 'API retains inherited registry activation access';
  END IF;
END $$;
