package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// FinalizeRestore preserves the dump's ACLs and restores runtime ownership.
// The connection must use the migration role (a member of urmail_platform).
// pg_restore must retain ACL entries; defaults alone cannot recover revokes.
func FinalizeRestore(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `DO $$ DECLARE r record; BEGIN
 FOR r IN SELECT c.relname,c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p','S') ORDER BY CASE WHEN c.relkind='S' THEN 1 ELSE 0 END LOOP
 EXECUTE format('ALTER %s public.%I OWNER TO urmail_platform',CASE WHEN r.relkind='S' THEN 'SEQUENCE' ELSE 'TABLE' END,r.relname);
 END LOOP;
 FOR r IN SELECT p.oid::regprocedure AS signature FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind='f' LOOP
 EXECUTE format('ALTER FUNCTION %s OWNER TO urmail_platform',r.signature);
 END LOOP;
 END $$;`)
	if err != nil {
		return fmt.Errorf("restore ownership: %w", err)
	}
	return tx.Commit(ctx)
}

// VerifyRuntimePrivileges checks the restored role boundary, not just row counts.
func VerifyRuntimePrivileges(ctx context.Context, databaseURL string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var ok bool
	err = conn.QueryRow(ctx, `SELECT
 (SELECT bool_and(has_table_privilege('urmail_app','mailboxes',permission)) FROM unnest(ARRAY['SELECT','INSERT','DELETE']) permission)
 AND has_column_privilege('urmail_app','mailboxes','quota_bytes','UPDATE')
 AND NOT has_column_privilege('urmail_app','mailboxes','bytes_used','UPDATE')
 AND (SELECT bool_and(has_table_privilege('urmail_app','domains',permission)) FROM unnest(ARRAY['SELECT','INSERT','UPDATE','DELETE']) permission)
 AND has_table_privilege('urmail_dovecot','mailboxes','SELECT')
 AND has_table_privilege('urmail_dovecot','tenants','SELECT')
 AND NOT has_table_privilege('urmail_dovecot','mailboxes','INSERT,UPDATE,DELETE')
 AND NOT has_table_privilege('urmail_app','sessions','SELECT,INSERT,UPDATE,DELETE')
 AND NOT has_table_privilege('urmail_app','audit_logs','INSERT,UPDATE,DELETE')
 AND has_sequence_privilege('urmail_app','mailboxes_id_seq','USAGE')
 AND NOT has_schema_privilege('urmail_app','public','CREATE')
 AND NOT has_schema_privilege('urmail_dovecot','public','CREATE')
 AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN('r','p') AND c.relname<>'schema_migrations' AND c.relowner<>'urmail_platform'::regrole)
 AND EXISTS(SELECT 1 FROM pg_policies WHERE schemaname='public' AND tablename='settings' AND policyname='settings_delete')
 AND NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname IN('urmail_app','urmail_dovecot','urmail_migrate') AND rolsuper)
 AND EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_app' AND NOT rolbypassrls)
 AND EXISTS(SELECT 1 FROM pg_roles WHERE rolname='urmail_dovecot' AND rolbypassrls)`).Scan(&ok)
	if err != nil {
		return fmt.Errorf("verify restore privileges: %w", err)
	}
	if !ok {
		return fmt.Errorf("restored runtime role boundary is incomplete")
	}
	return nil
}
