"""Synthetic unit tests and optional real-data rehearsal in an owned PG cluster.

The private customer bundle and target credentials are environment-only inputs.
"""
import contextlib
import datetime as dt
import importlib.util
import io
import json
import os
from pathlib import Path
import secrets
import sqlite3
import shutil
import tempfile
import sys
import subprocess
import unittest
from unittest import mock
from urllib.parse import quote, urlsplit, urlunsplit

import psycopg
from psycopg import sql

spec = importlib.util.spec_from_file_location('metadata_migration', Path(__file__).parents[1] / 'migrate_metadata.py')
migration = importlib.util.module_from_spec(spec)
spec.loader.exec_module(migration)


class MetadataUnitTests(unittest.TestCase):
    def test_no_public_schema_clear_sql_or_production_apply_interface(self):
        for dsn in ['postgres://urmail_roundcube:secret@attacker.example/urmail_fixture_' + 'a' * 24,
                    'postgres://postgres:secret@127.0.0.1/urmail_fixture_' + 'a' * 24,
                    'postgres://urmail_roundcube:secret@127.0.0.1/production',
                    'postgres://urmail_roundcube:secret@127.0.0.1/urmail_fixture_' + 'a' * 24 + '?options=-crole=postgres']:
            with self.subTest(target_category=dsn.split('@')[0].split(':')[0]), self.assertRaises(migration.Refusal):
                migration.parse_target(dsn)
        for name in ['users;drop table public.users', '../users', 'users"', 'public.users']:
            with self.assertRaises(migration.Refusal):
                migration.identifier(name)

    def test_semantic_timestamp_and_exact_text_cipher_bytes(self):
        columns = [('created', 'timestamp with time zone'), ('preferences', 'text'), ('cipher', 'bytea')]
        source = ('2026-01-31 01:02:03.123456', 'a:1:{s:3:"key";s:5:"value";}', b'\x00\xffcipher\x80')
        values = tuple(migration.normalized(value, datatype, 'users', 1) for value, (_, datatype) in zip(source, columns))
        self.assertEqual(values[0].tzinfo, dt.timezone.utc)
        self.assertEqual(values[1:], source[1:])
        self.assertEqual(migration.digest_rows([values], columns), migration.digest_rows([values], columns))
        changed = (values[0], values[1] + ' ', values[2])
        self.assertNotEqual(migration.digest_rows([values], columns), migration.digest_rows([changed], columns))
        self.assertEqual(migration.digest_rows([values], columns)['column_digests']['cipher'], migration.digest_rows([changed], columns)['column_digests']['cipher'])

    def test_only_expired_core_cache_is_pruned(self):
        source = sqlite3.connect(':memory:')
        source.execute('CREATE TABLE cache(user_id integer,cache_key text,expires text,data text)')
        source.executemany('INSERT INTO cache VALUES(?,?,?,?)', [(1, 'expired', '2024-01-01 00:00:00', 'synthetic'), (1, 'live', '2099-01-01 00:00:00', 'synthetic'), (1, 'no-expiry', None, 'synthetic')])
        columns = [('user_id', 'integer'), ('cache_key', 'text'), ('expires', 'timestamp with time zone'), ('data', 'text')]
        rows = migration.read_source_rows(source, 'cache', columns, dt.datetime(2026, 1, 1, tzinfo=dt.timezone.utc))
        self.assertEqual({row[1] for row in rows}, {'live', 'no-expiry'})
        self.assertNotIn('carddav_contacts', migration.CACHE_TABLES)
        source.close()

    def test_plain_integer_primary_key_has_no_invented_highwater(self):
        source = sqlite3.connect(':memory:')
        source.execute('CREATE TABLE users(user_id INTEGER PRIMARY KEY)')
        source.execute('INSERT INTO users VALUES(300)')
        source.execute('DELETE FROM users')
        self.assertEqual(migration.source_sequence_highwater(source, 'users'), {'available': False, 'value': None})
        source.close()

    def test_cli_never_echoes_invalid_secret_arguments_or_overwrites_report(self):
        with mock.patch.object(sys, 'argv', ['migrate_metadata.py', '--dsn=synthetic-secret-value']), contextlib.redirect_stderr(io.StringIO()) as errors:
            with self.assertRaises(SystemExit):
                migration.main()
        self.assertNotIn('synthetic-secret-value', errors.getvalue())
        with tempfile.TemporaryDirectory() as directory:
            report = Path(directory) / 'existing.json'
            report.write_text('original-manifest')
            with mock.patch.object(sys, 'argv', ['migrate_metadata.py', 'apply-fixture', '--private-bundle', directory, '--report', str(report)]), mock.patch.object(migration, 'import_fixture') as apply, contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(migration.main(), 1)
            apply.assert_not_called()
            self.assertEqual(report.read_text(), 'original-manifest')

    def test_controlled_error_has_no_exception_message_or_private_values(self):
        refusal = migration.Refusal('TARGET_DATABASE_REJECTED', 'contacts', 4, '23503')
        self.assertEqual(refusal.detail, {'error': 'TARGET_DATABASE_REJECTED', 'table': 'contacts', 'row_ref': 4, 'sqlstate': '23503'})


@unittest.skipUnless(os.environ.get('URMAIL_V05_PRIVATE_BUNDLE') and os.environ.get('URMAIL_V05_CLUSTER_ADMIN_DSN'), 'requires the explicit private bundle and a dedicated disposable cluster')
class MetadataOwnedClusterTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bundle = Path(os.environ['URMAIL_V05_PRIVATE_BUNDLE'])
        cls.admin_dsn = os.environ['URMAIL_V05_CLUSTER_ADMIN_DSN']
        snapshot, _, _ = migration.load_bundle(cls.bundle)
        cls.snapshot_sha = migration.sha(snapshot.read_bytes())
        cls.password = secrets.token_urlsafe(40)
        with psycopg.connect(cls.admin_dsn, autocommit=True) as connection:
            if connection.execute("SHOW cluster_name").fetchone()[0] != 'urmail-v05-private-rehearsal':
                raise RuntimeError('The explicit dedicated rehearsal cluster marker is required')
            # This must be a new, dedicated cluster. Never alter a shared role.
            if connection.execute("SELECT 1 FROM pg_roles WHERE rolname='urmail_roundcube'").fetchone():
                raise RuntimeError('Dedicated rehearsal cluster already contains the runtime role')
            connection.execute('CREATE ROLE v05_other_schema_owner NOLOGIN')
            connection.execute(sql.SQL('CREATE ROLE urmail_roundcube LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD {}').format(sql.Literal(cls.password)))
        cls.root = urlsplit(cls.admin_dsn)

    @classmethod
    def tearDownClass(cls):
        with psycopg.connect(cls.admin_dsn, autocommit=True) as connection:
            connection.execute('DROP ROLE urmail_roundcube')
            connection.execute('DROP ROLE v05_other_schema_owner')

    def setUp(self):
        self.database = 'urmail_fixture_' + secrets.token_hex(12)
        with psycopg.connect(self.admin_dsn, autocommit=True) as connection:
            connection.execute(sql.SQL('CREATE DATABASE {}').format(sql.Identifier(self.database)))
            oid = connection.execute('SELECT oid FROM pg_database WHERE datname=%s', (self.database,)).fetchone()[0]
            proof = {'kind': 'urmail-v05-owned-fixture-v1', 'database': self.database, 'database_oid': oid,
                     'snapshot_sha256': self.snapshot_sha, 'nonce': secrets.token_hex(32)}
            connection.execute(sql.SQL('COMMENT ON DATABASE {} IS {}').format(sql.Identifier(self.database), sql.Literal(json.dumps(proof))))
        self.target_admin = urlunsplit(self.root._replace(path='/' + self.database))
        with psycopg.connect(self.target_admin, autocommit=True) as connection:
            connection.execute('REVOKE CREATE ON SCHEMA public FROM PUBLIC')
            connection.execute('CREATE SCHEMA roundcube AUTHORIZATION urmail_roundcube')
            connection.execute('CREATE TABLE public.mailboxes(private_value text)')
            connection.execute("INSERT INTO public.mailboxes VALUES('synthetic-denial-sentinel')")
            connection.execute('CREATE SCHEMA other_role AUTHORIZATION v05_other_schema_owner')
            connection.execute('CREATE TABLE other_role.private_data(value text)')
            connection.execute('ALTER TABLE other_role.private_data OWNER TO v05_other_schema_owner')
        self.target = urlunsplit(self.root._replace(netloc='urmail_roundcube:' + quote(self.password, safe='') + '@' + self.root.hostname + ':' + str(self.root.port), path='/' + self.database))

    def tearDown(self):
        with psycopg.connect(self.admin_dsn, autocommit=True) as connection:
            connection.execute(sql.SQL('DROP DATABASE {} WITH (FORCE)').format(sql.Identifier(self.database)))

    def empty(self):
        with psycopg.connect(self.target) as connection:
            return connection.execute("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='roundcube'").fetchone()[0] == 0

    def test_actual_v80_all_business_columns_and_sequences_under_login(self):
        report = migration.import_fixture(self.bundle, self.target)
        self.assertTrue(report['all_business_columns_verified'])
        self.assertTrue(report['fixture_only'])
        self.assertFalse(report['activation_authority'])
        expected = {'users': 36, 'identities': 37, 'contacts': 5, 'collected_addresses': 42,
                    'carddav_accounts': 35, 'carddav_addressbooks': 42, 'carddav_contacts': 72, 'carddav_migrations': 23}
        for name, count in expected.items():
            self.assertEqual(report['tables'][name]['rows'], count, name)
            self.assertTrue(report['tables'][name]['column_digests'])
        self.assertEqual(report['discarded_auth_rows']['session'], 26)
        with psycopg.connect(self.target) as connection:
            self.assertEqual(connection.execute('SELECT session_user,current_user').fetchone(), ('urmail_roundcube', 'urmail_roundcube'))
            for table, (column, sequence) in migration.SEQUENCES.items():
                maximum = connection.execute(sql.SQL('SELECT max({}) FROM roundcube.{}').format(sql.Identifier(column), sql.Identifier(table))).fetchone()[0]
                nextvalue = connection.execute('SELECT nextval(%s::regclass)', ('roundcube.' + sequence,)).fetchone()[0]
                proof = report['sequences'][table]
                self.assertEqual(nextvalue, proof['next_value_floor'], table)
                self.assertGreater(nextvalue, maximum or 0, table)
                if proof['source_sqlite_sequence']['available']:
                    self.assertGreater(nextvalue, proof['source_sqlite_sequence']['value'], table)
                self.assertGreater(nextvalue, proof['target_before']['allocated_floor'], table)
            for table in migration.AUTH_TABLES:
                self.assertEqual(connection.execute(sql.SQL('SELECT count(*) FROM roundcube.{}').format(sql.Identifier(table))).fetchone()[0], 0)
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'TARGET_SCHEMA_NOT_EMPTY')
        output = os.environ.get('URMAIL_V05_REHEARSAL_REPORT')
        if output:
            migration.write_report(output, report)

    def test_sequence_highwater_never_reuses_deleted_or_target_allocated_ids(self):
        # Fully synthetic sequence source: no private snapshot bytes are changed.
        source = sqlite3.connect(':memory:')
        source.execute('CREATE TABLE users(user_id INTEGER PRIMARY KEY AUTOINCREMENT)')
        source.execute('INSERT INTO users(user_id) VALUES(700)')
        source.execute('DELETE FROM users WHERE user_id=700')
        source.execute('INSERT INTO users(user_id) VALUES(3)')
        source.commit()
        with psycopg.connect(self.target) as connection:
            connection.execute('SET LOCAL search_path=roundcube,pg_catalog')
            _, vendor, _ = migration.load_bundle(self.bundle)
            migration.create_schema(connection, vendor)
            connection.execute("SELECT setval('roundcube.contacts_seq',900,true)")
            connection.execute("SELECT setval('roundcube.identities_seq',1200,false)")
            proof = migration.resynchronize_sequences(connection, source)
            self.assertEqual(proof['users']['source_sqlite_sequence'], {'available': True, 'value': 700})
            self.assertFalse(proof['contacts']['source_sqlite_sequence']['available'])
            self.assertTrue(proof['users']['resync_performed'])
            self.assertFalse(proof['responses']['resync_performed'])
            self.assertEqual(proof['contacts']['target_before']['last_value'], 900)
            self.assertEqual(proof['identities']['target_before']['last_value'], 1200)
            self.assertEqual(proof['identities']['target_after'], {'last_value': 1200, 'is_called': False})
            self.assertFalse(proof['identities']['resync_performed'])
            for name, minimum in [('users', 700), ('contacts', 900), ('identities', 1199)]:
                sequence = migration.SEQUENCES[name][1]
                value = connection.execute('SELECT nextval(%s::regclass)', ('roundcube.' + sequence,)).fetchone()[0]
                self.assertGreater(value, minimum)
                self.assertEqual(value, proof[name]['next_value_floor'])
        source.close()

    def test_cli_apply_under_real_login_has_only_controlled_output(self):
        with tempfile.TemporaryDirectory(prefix='urmail-v05-cli-private-') as directory:
            report = Path(directory) / 'verified.json'
            result = subprocess.run([sys.executable, str(Path(__file__).parents[1] / 'migrate_metadata.py'), 'apply-fixture', '--private-bundle', str(self.bundle), '--report', str(report)], env=dict(os.environ, URMAIL_ROUNDCUBE_MIGRATION_DSN=self.target), capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(json.loads(result.stdout), {'status': 'verified_fixture', 'production_activation': False})
            self.assertEqual(result.stderr, '')
            self.assertEqual(report.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(report.read_text())['tables']['users']['rows'], 36)

    def test_same_count_wrong_content_aborts_and_rolls_back_every_object(self):
        def tamper(connection):
            before = connection.execute('SELECT count(*) FROM users').fetchone()[0]
            connection.execute("UPDATE users SET preferences=COALESCE(preferences,'')||%s WHERE user_id=(SELECT min(user_id) FROM users)", ('synthetic-tamper',))
            self.assertEqual(connection.execute('SELECT count(*) FROM users').fetchone()[0], before)
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target, before_verify=tamper)
        self.assertEqual(refused.exception.detail, {'error': 'NORMALIZED_DATA_DIGEST_MISMATCH', 'table': 'users'})
        self.assertTrue(self.empty())
        # A positively verified empty rollback may be retried; a committed schema may not.
        report = migration.import_fixture(self.bundle, self.target)
        self.assertTrue(report['all_business_columns_verified'])

    def test_sql_failure_is_redacted_and_rolls_back_ddl_and_rows(self):
        def fail(connection):
            # PostgreSQL's private error DETAIL would contain the complete row.
            # The dedicated server suppresses these logs; the importer returns
            # only controlled SQLSTATE, never the exception/row/statement text.
            connection.execute('UPDATE users SET username=NULL WHERE user_id=(SELECT min(user_id) FROM users)')
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target, before_verify=fail)
        self.assertEqual(refused.exception.detail, {'error': 'TARGET_DATABASE_REJECTED', 'sqlstate': '23502'})
        self.assertTrue(self.empty())

    def test_lost_commit_acknowledgment_is_not_reported_as_a_safe_retry(self):
        original = psycopg.connect
        class LostAcknowledgment:
            def __init__(self, connection):
                self.connection = connection
            def __enter__(self):
                self.connection.__enter__()
                return self
            def __getattr__(self, name):
                return getattr(self.connection, name)
            def __exit__(self, kind, error, traceback):
                result = self.connection.__exit__(kind, error, traceback)
                if kind is None:
                    raise psycopg.OperationalError('synthetic acknowledgment loss')
                return result
        with mock.patch.object(migration.psycopg, 'connect', side_effect=lambda **kwargs: LostAcknowledgment(original(**kwargs))):
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail, {'error': 'COMMIT_OUTCOME_UNKNOWN_NO_REPLAY'})
        self.assertFalse(self.empty())
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'TARGET_SCHEMA_NOT_EMPTY')

    def test_missing_or_wrong_fingerprint_proof_and_root_setrole_rejected(self):
        with psycopg.connect(self.target_admin, autocommit=True) as connection:
            connection.execute(sql.SQL('COMMENT ON DATABASE {} IS NULL').format(sql.Identifier(self.database)))
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'OWNED_FIXTURE_PROOF_REQUIRED')
        self.assertTrue(self.empty())
        with psycopg.connect(self.target_admin, autocommit=True) as root:
            oid = root.execute('SELECT oid FROM pg_database WHERE datname=current_database()').fetchone()[0]
            wrong = {'kind': 'urmail-v05-owned-fixture-v1', 'database': self.database, 'database_oid': oid,
                     'snapshot_sha256': '0' * 64, 'nonce': secrets.token_hex(32)}
            root.execute(sql.SQL('COMMENT ON DATABASE {} IS {}').format(sql.Identifier(self.database), sql.Literal(json.dumps(wrong))))
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'OWNED_FIXTURE_PROOF_MISMATCH')
        self.assertTrue(self.empty())
        with psycopg.connect(self.target_admin) as root:
            root.execute('SET LOCAL ROLE urmail_roundcube')
            with self.assertRaises(migration.Refusal) as refused:
                migration.check_target(root, self.snapshot_sha)
            self.assertEqual(refused.exception.detail['error'], 'ACTUAL_RESTRICTED_LOGIN_REQUIRED')
        with psycopg.connect(self.target) as runtime:
            with self.assertRaises(psycopg.errors.InsufficientPrivilege):
                runtime.execute('SELECT * FROM public.mailboxes')

    def test_wrong_sqlite_backend_version_is_not_copied_as_postgres_version(self):
        with tempfile.TemporaryDirectory(prefix='urmail-v05-private-bad-version-') as directory:
            bundle = Path(directory) / 'input'
            shutil.copytree(self.bundle, bundle)
            sidecar = bundle / 'snapshot.sqlite-wal'
            sidecar.write_bytes(b'')
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'STANDALONE_SNAPSHOT_REQUIRED')
            sidecar.unlink()
            with contextlib.closing(sqlite3.connect(bundle / 'snapshot.sqlite')) as source:
                source.execute("UPDATE system SET value='unreviewed-version' WHERE name='roundcube-version'")
                source.commit()
            manifestpath = bundle / 'input-manifest.json'
            manifest = json.loads(manifestpath.read_text())
            snapshot = bundle / 'snapshot.sqlite'
            manifest['files']['snapshot.sqlite'] = {'bytes': snapshot.stat().st_size, 'sha256': migration.sha(snapshot.read_bytes())}
            manifestpath.write_text(json.dumps(manifest))
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(bundle, self.target)
            self.assertEqual(refused.exception.detail, {'error': 'UNREVIEWED_SOURCE_SCHEMA_VERSION', 'table': 'system'})
            self.assertTrue(self.empty())

    def test_no_inherit_or_setrole_memberships_and_no_external_grants(self):
        with psycopg.connect(self.admin_dsn, autocommit=True) as root:
            root.execute('ALTER ROLE urmail_roundcube INHERIT')
        try:
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'UNSAFE_TARGET_ROLE')
        finally:
            with psycopg.connect(self.admin_dsn, autocommit=True) as root:
                root.execute('ALTER ROLE urmail_roundcube NOINHERIT')
        with psycopg.connect(self.admin_dsn, autocommit=True) as root:
            root.execute('GRANT pg_write_all_data TO urmail_roundcube')
        try:
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'TARGET_ROLE_MEMBERSHIP_FORBIDDEN')
        finally:
            with psycopg.connect(self.admin_dsn, autocommit=True) as root:
                root.execute('REVOKE pg_write_all_data FROM urmail_roundcube')
        with psycopg.connect(self.target_admin, autocommit=True) as root:
            root.execute('GRANT SELECT ON public.mailboxes TO urmail_roundcube')
        try:
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'TARGET_ROLE_EXTERNAL_PRIVILEGES_FORBIDDEN')
        finally:
            with psycopg.connect(self.target_admin, autocommit=True) as root:
                root.execute('REVOKE SELECT ON public.mailboxes FROM urmail_roundcube')
        with psycopg.connect(self.target_admin, autocommit=True) as root:
            root.execute('CREATE SCHEMA pgevil_role AUTHORIZATION v05_other_schema_owner')
            root.execute('CREATE TABLE pgevil_role.private_data(value text)')
            root.execute('GRANT USAGE ON SCHEMA pgevil_role TO urmail_roundcube')
            root.execute('GRANT SELECT ON pgevil_role.private_data TO urmail_roundcube')
        try:
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'TARGET_ROLE_EXTERNAL_PRIVILEGES_FORBIDDEN')
        finally:
            with psycopg.connect(self.target_admin, autocommit=True) as root:
                root.execute('REVOKE SELECT ON pgevil_role.private_data FROM urmail_roundcube')
        with psycopg.connect(self.target_admin, autocommit=True) as root:
            root.execute("CREATE FUNCTION public.external_sentinel() RETURNS text LANGUAGE sql SECURITY DEFINER AS 'SELECT private_value FROM public.mailboxes'")
        try:
            with self.assertRaises(migration.Refusal) as refused:
                migration.import_fixture(self.bundle, self.target)
            self.assertEqual(refused.exception.detail['error'], 'TARGET_ROLE_EXTERNAL_PRIVILEGES_FORBIDDEN')
        finally:
            with psycopg.connect(self.target_admin, autocommit=True) as root:
                root.execute('REVOKE ALL ON FUNCTION public.external_sentinel() FROM PUBLIC')
        self.assertTrue(self.empty())
        # An operator granted the metadata role is the inverse relationship,
        # not an elevation membership granted to the restricted runtime itself.
        with psycopg.connect(self.admin_dsn, autocommit=True) as root:
            root.execute('GRANT urmail_roundcube TO postgres')
        try:
            self.assertTrue(migration.import_fixture(self.bundle, self.target)['all_business_columns_verified'])
        finally:
            with psycopg.connect(self.admin_dsn, autocommit=True) as root:
                root.execute('REVOKE urmail_roundcube FROM postgres')

    def test_runtime_cannot_read_write_or_create_in_public_or_other_role(self):
        for statement in ['SELECT * FROM public.mailboxes', "INSERT INTO public.mailboxes VALUES('synthetic')", 'UPDATE public.mailboxes SET private_value=NULL', 'CREATE TABLE public.forbidden(value int)', 'SELECT * FROM other_role.private_data', "INSERT INTO other_role.private_data VALUES('synthetic')", 'CREATE TABLE other_role.forbidden(value int)']:
            with self.assertRaises(psycopg.errors.InsufficientPrivilege):
                with psycopg.connect(self.target) as runtime:
                    self.assertEqual(runtime.execute('SELECT session_user,current_user').fetchone(), ('urmail_roundcube', 'urmail_roundcube'))
                    runtime.execute(statement)
        self.assertTrue(self.empty())

    def test_private_shadow_function_cannot_execute_before_empty_schema_gate(self):
        with psycopg.connect(self.target_admin) as root:
            root.execute("CREATE FUNCTION roundcube.current_database() RETURNS name LANGUAGE plpgsql SECURITY DEFINER AS $$ BEGIN UPDATE public.mailboxes SET private_value='shadow-executed'; RETURN current_catalog; END $$")
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'TARGET_SCHEMA_NOT_EMPTY')
        with psycopg.connect(self.target_admin) as root:
            self.assertEqual(root.execute('SELECT private_value FROM public.mailboxes').fetchone()[0], 'synthetic-denial-sentinel')

    def test_nonempty_private_function_is_not_an_empty_schema(self):
        with psycopg.connect(self.target) as connection:
            connection.execute("CREATE FUNCTION roundcube.fixture_function() RETURNS integer LANGUAGE sql AS 'SELECT 1'")
        with self.assertRaises(migration.Refusal) as refused:
            migration.import_fixture(self.bundle, self.target)
        self.assertEqual(refused.exception.detail['error'], 'TARGET_SCHEMA_NOT_EMPTY')


if __name__ == '__main__':
    unittest.main(verbosity=2)
