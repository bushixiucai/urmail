package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// FinalizeRestore preserves the dump's ACLs and restores runtime ownership.
// The connection must use the migration role (a member of urmail_platform).
// pg_restore must retain ACL entries; defaults alone cannot recover revokes.
func FinalizeRestore(ctx context.Context, databaseURL string, restoreIDs ...string) error {
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
 FOR r IN SELECT p.oid::regprocedure AS signature,p.prokind FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='public' AND p.prokind IN('f','p','w') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e') LOOP
 EXECUTE format('ALTER %s %s OWNER TO urmail_platform',CASE r.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,r.signature);
 END LOOP;
 -- --no-owner also makes restored Roundcube objects owned by the migration
 -- login. Restore their dedicated owner without granting panel access.
 IF EXISTS(SELECT 1 FROM pg_namespace WHERE nspname='roundcube') THEN
  ALTER SCHEMA roundcube OWNER TO urmail_roundcube;
  FOR r IN SELECT c.relname,c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='roundcube' AND c.relkind IN('r','p','S') ORDER BY CASE WHEN c.relkind='S' THEN 1 ELSE 0 END LOOP
   EXECUTE format('ALTER %s roundcube.%I OWNER TO urmail_roundcube',CASE WHEN r.relkind='S' THEN 'SEQUENCE' ELSE 'TABLE' END,r.relname);
  END LOOP;
  FOR r IN SELECT p.oid::regprocedure AS signature,p.prokind FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='roundcube' AND p.prokind IN('f','p','w') AND NOT EXISTS(SELECT 1 FROM pg_depend d WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e') LOOP
   EXECUTE format('ALTER %s %s OWNER TO urmail_roundcube',CASE r.prokind WHEN 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,r.signature);
  END LOOP;
 END IF;
 END $$;`)
	if err != nil {
		return fmt.Errorf("restore ownership: %w", err)
	}
	restoreID := ""
	if len(restoreIDs) > 0 {
		restoreID = restoreIDs[0]
	}
	if err = holdRestoredWork(ctx, tx, restoreID, false); err != nil {
		return err
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
 AND (SELECT bool_and(has_table_privilege('urmail_app','domains',permission)) FROM unnest(ARRAY['SELECT','INSERT','DELETE']) permission)
 AND has_column_privilege('urmail_app','domains','status','UPDATE')
 AND has_column_privilege('urmail_app','domains','mode','UPDATE')
 AND NOT has_column_privilege('urmail_app','domains','provisioning_state','UPDATE')
 AND NOT has_column_privilege('urmail_app','domains','custom_web_enabled','UPDATE')
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

// VerifyRestoreIdentity prevents releasing a different database after a
// metadata restore or after restoring source environment files.
func VerifyRestoreIdentity(ctx context.Context, databaseURL, restoreID string) error {
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(ctx) }()
	var actual string
	if err = conn.QueryRow(ctx, `SELECT value->>'restore_id' FROM settings WHERE scope='platform' AND scope_id=0 AND key='restore_replay_review'`).Scan(&actual); err != nil || actual != restoreID {
		return fmt.Errorf("configured runtime database does not match this completed restore; quarantine retained")
	}
	return nil
}
