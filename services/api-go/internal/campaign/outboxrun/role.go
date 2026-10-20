package outboxrun

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// CheckRole checks effective column grants as well as table grants: a table
// REVOKE alone does not prove the absence of inherited column privileges.
// The dedicated login uses direct grants, never SET ROLE or role memberships:
// NOINHERIT can conceal a settable writer from effective-privilege checks.
// This runtime check complements the deployment review of functions and grants;
// it never creates roles or changes permissions on the caller's database.
func CheckRole(ctx context.Context, pool *pgxpool.Pool) error {
	var allowed bool
	err := pool.QueryRow(ctx, `SELECT session_user=current_user
 AND NOT pg_is_in_recovery() AND current_setting('transaction_read_only')='off'
 AND NOT EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname<>current_user AND pg_has_role(current_user,r.oid,'MEMBER'))
 AND NOT EXISTS(SELECT 1 FROM pg_roles r WHERE pg_has_role(current_user,r.oid,'MEMBER')
   AND (r.rolsuper OR r.rolcreaterole OR r.rolcreatedb OR r.rolreplication OR r.rolbypassrls))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind IN ('r','p') AND pg_has_role(current_user,c.relowner,'MEMBER'))
 AND has_table_privilege(current_user,'public.durable_outbox','SELECT')
 AND has_table_privilege(current_user,'public.outbox_streams','SELECT')
 AND NOT has_table_privilege(current_user,'public.durable_outbox','INSERT,DELETE,TRUNCATE,REFERENCES,TRIGGER')
 AND NOT EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid='public.durable_outbox'::regclass AND a.attnum>0 AND NOT a.attisdropped
   AND (has_column_privilege(current_user,a.attrelid,a.attnum,'INSERT')
     OR has_column_privilege(current_user,a.attrelid,a.attnum,'REFERENCES')
     OR CASE WHEN a.attname IN ('available_at','published_at','lease_owner','lease_until','attempts','error_code','dead_lettered_at')
     THEN NOT has_column_privilege(current_user,a.attrelid,a.attnum,'UPDATE')
     ELSE has_column_privilege(current_user,a.attrelid,a.attnum,'UPDATE') END))
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
   WHERE n.nspname='public' AND c.relkind IN ('r','p') AND c.oid<>'public.durable_outbox'::regclass
   AND (has_table_privilege(current_user,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
     OR EXISTS(SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
       AND (has_column_privilege(current_user,c.oid,a.attnum,'INSERT') OR has_column_privilege(current_user,c.oid,a.attnum,'UPDATE')
         OR has_column_privilege(current_user,c.oid,a.attnum,'REFERENCES')))))`).Scan(&allowed)
	if err != nil {
		return ErrStore
	}
	if !allowed {
		return ErrRole
	}
	return nil
}
