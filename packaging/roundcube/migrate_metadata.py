#!/usr/bin/env python3
"""Pinned v80 metadata rehearsal. Production activation is deliberately unavailable."""
import argparse
import base64
import datetime as dt
import hashlib
import json
import math
import os
from pathlib import Path
import re
import sqlite3
import stat
import sys
import tempfile
from urllib.parse import quote, unquote, urlsplit

try:
    import psycopg
    from psycopg import sql
except ImportError:
    psycopg = sql = None

PROFILE = 'roundcube-1.7.5-carddav-5.1.4-v1'
SCHEMA = 'roundcube'
PINS = {
    'SQL/postgres.initial.sql': '001c8b21c81014b93c2aa411bfb33b232c498d22209f5591698dfde7bae1affe',
    'SQL/sqlite.initial.sql': 'fbd26b092961792d81edcc834b6ec9b8b8577b2addddd2d2cd31d15ba9fdd9c5',
    'plugins/carddav/dbmigrations/INIT-currentschema/postgres.sql': '83cd1eff12cee87e77f7119a85e77ee64d160aea1f5bec6903473cfc9172e9e1',
    'plugins/carddav/dbmigrations/INIT-currentschema/sqlite3.sql': '67ec4c0f01eb48ee059166412300632b98abbe946d8fee574fa1e9b9330903b2',
    'plugins/carddav/src/Db/Database.php': '75c84b820d8a22cd1777f10f761fd0bdfa4b0f862e737c4deb987d1b15eb790e',
    'plugins/persistent_login/sql/postgres.sql': '451deee0162deaf122b15bac4dfae9fad65d6d49645ba6074eca90faa611fed4',
    'plugins/persistent_login/sql/sqlite.sql': '58b6dded30d913fba6915a37460bc8ec0fae20f4c802cdb8565510ed4d18cafd',
    'version': '16ce04a902d2e5721a1bb1280da38045fa7ba9217f85b288a74cb62a2afe746e',
}
MIGRATIONS = (
    '0000-dbinit', '0001-categories', '0002-increasetextfieldlengths', '0003-fixtimestampdefaultvalue',
    '0004-fixtimestampdefaultvalue', '0005-changemysqlut8toutf8mb4', '0006-rmgroupsnotnull',
    '0007-replaceurlplaceholders', '0008-unifyindexes', '0009-dropauthschemefield',
    '0010-increasetextfieldlengths', '0011-unifymigrationsidcolumn', '0012-fixmysqlconstraints',
    '0013-changemysqlcollationscasesensitive', '0014-unifytimestampdefaultvalue',
    '0015-fixmysqlconstraints', '0016-increasetextfieldlengths', '0017-accountentities',
    '0018-accountentities2', '0019-accountentities3', '0020-distinctcolumnnames',
    '0021-addressbookflags', '0022-accountflags',
)
AUTH_TABLES = {'session', 'auth_tokens', 'uploads'}
CACHE_TABLES = {'cache', 'cache_shared', 'cache_index', 'cache_thread', 'cache_messages'}
SEQUENCES = {table: (column, sequence) for table, column, sequence in [
    ('users', 'user_id', 'users_seq'), ('identities', 'identity_id', 'identities_seq'),
    ('responses', 'response_id', 'responses_seq'), ('collected_addresses', 'address_id', 'collected_addresses_seq'),
    ('contacts', 'contact_id', 'contacts_seq'), ('contactgroups', 'contactgroup_id', 'contactgroups_seq'),
    ('searches', 'search_id', 'searches_seq'), ('filestore', 'file_id', 'filestore_seq'),
    ('carddav_accounts', 'id', 'carddav_accounts_seq'), ('carddav_addressbooks', 'id', 'carddav_addressbooks_seq'),
    ('carddav_contacts', 'id', 'carddav_contacts_seq'), ('carddav_xsubtypes', 'id', 'carddav_xsubtypes_seq'),
    ('carddav_groups', 'id', 'carddav_groups_seq'), ('carddav_migrations', 'id', 'carddav_migrations_seq')]}


class Refusal(Exception):
    """Only controlled metadata can be included in diagnostics."""
    def __init__(self, code, table=None, row=None, state=None):
        self.detail = {'error': code}
        if table and re.fullmatch(r'[a-z_]+', table):
            self.detail['table'] = table
        if row is not None:
            self.detail['row_ref'] = int(row)
        if state and re.fullmatch(r'[0-9A-Z]{5}', state):
            self.detail['sqlstate'] = state
        super().__init__(code)


def sha(data):
    return hashlib.sha256(data).hexdigest()


def private_regular(path):
    if path.is_symlink():
        raise Refusal('PRIVATE_INPUT_SYMLINK')
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid not in (0, os.getuid()) or info.st_mode & 0o077:
        raise Refusal('PRIVATE_INPUT_PERMISSIONS')


def identifier(name):
    if not re.fullmatch(r'[a-z][a-z0-9_-]*', name):
        raise Refusal('UNREVIEWED_IDENTIFIER')
    return '"' + name + '"'


def load_bundle(directory):
    directory = Path(directory)
    if directory.is_symlink() or not directory.is_dir() or directory.stat().st_uid not in (0, os.getuid()) or directory.stat().st_mode & 0o077:
        raise Refusal('PRIVATE_BUNDLE_REQUIRED')
    private_regular(directory / 'input-manifest.json')
    manifest = json.loads((directory / 'input-manifest.json').read_text())
    if not isinstance(manifest, dict):
        raise Refusal('INVALID_INPUT_MANIFEST')
    if manifest.get('roundcube_version') != '1.7.5' or manifest.get('carddav_version') != '5.1.4':
        raise Refusal('UNREVIEWED_VENDOR_VERSION')
    files = manifest.get('files', {})
    if not isinstance(files, dict) or len(files) > 2000:
        raise Refusal('INVALID_INPUT_MANIFEST')
    for name, record in files.items():
        relative = Path(name)
        if relative.is_absolute() or '..' in relative.parts or not isinstance(record, dict):
            raise Refusal('INVALID_INPUT_MANIFEST')
        path = directory / relative
        if not path.resolve().is_relative_to(directory.resolve()) or (name != 'snapshot.sqlite' and not name.startswith('vendor/')):
            raise Refusal('INVALID_INPUT_MANIFEST')
        private_regular(path)
        if path.stat().st_size != record.get('bytes') or sha(path.read_bytes()) != record.get('sha256'):
            raise Refusal('INPUT_FINGERPRINT_MISMATCH')
    snapshot = directory / 'snapshot.sqlite'
    if any(Path(str(snapshot) + suffix).exists() or Path(str(snapshot) + suffix).is_symlink() for suffix in ('-wal', '-shm', '-journal')):
        raise Refusal('STANDALONE_SNAPSHOT_REQUIRED')
    if snapshot.stat().st_size > 128 * 1024 * 1024 or 'snapshot.sqlite' not in files:
        raise Refusal('SOURCE_SNAPSHOT_LIMIT')
    vendor = {}
    for name, digest in PINS.items():
        if 'vendor/' + name not in files:
            raise Refusal('INCOMPLETE_VENDOR_MANIFEST')
        path = directory / 'vendor' / name
        private_regular(path)
        if sha(path.read_bytes()) != digest:
            raise Refusal('VENDOR_FINGERPRINT_MISMATCH')
        vendor[name] = path.read_text()
    return snapshot, vendor, manifest


def expected_source(vendor):
    connection = sqlite3.connect(':memory:')
    connection.executescript(vendor['SQL/sqlite.initial.sql'])
    connection.executescript(vendor['plugins/carddav/dbmigrations/INIT-currentschema/sqlite3.sql'].replace('TABLE_PREFIX', ''))
    connection.executescript(vendor['plugins/persistent_login/sql/sqlite.sql'])
    return connection


def tables(connection):
    return {row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'")}



class PrivateSnapshotConnection(sqlite3.Connection):
    def close(self):
        try:
            super().close()
        finally:
            temporary = getattr(self, '_private_snapshot', None)
            if temporary is not None:
                temporary.cleanup()
                self._private_snapshot = None

def source_inventory(snapshot, vendor, fingerprint):
    contents = snapshot.read_bytes()
    if sha(contents) != fingerprint:
        raise Refusal('SOURCE_SNAPSHOT_CHANGED')
    # Bind the database engine to exactly the bytes fingerprinted above. A
    # private immutable copy also supports SQLite backup files with WAL header
    # mode, which deserialize() cannot open; the source/header is never patched.
    temporary = tempfile.TemporaryDirectory(prefix='urmail-v05-snapshot-')
    path = Path(temporary.name) / 'snapshot.sqlite'
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, 'wb') as output:
        output.write(contents)
    source = sqlite3.connect('file:' + quote(str(path)) + '?mode=ro&immutable=1', uri=True, factory=PrivateSnapshotConnection)
    source._private_snapshot = temporary
    try:
        source.execute('PRAGMA query_only=ON')
        source.execute('PRAGMA trusted_schema=OFF')
        if source.execute('PRAGMA quick_check').fetchone()[0] != 'ok':
            raise Refusal('SOURCE_INTEGRITY_FAILED')
        expected = expected_source(vendor)
        names = tables(source)
        if names - tables(expected) or tables(expected) - names - {'auth_tokens'}:
            raise Refusal('UNREVIEWED_SOURCE_TABLES')
        for name in sorted(names):
            actual_columns = {row[1] for row in source.execute('PRAGMA table_info(' + identifier(name) + ')')}
            expected_columns = {row[1] for row in expected.execute('PRAGMA table_info(' + identifier(name) + ')')}
            if actual_columns != expected_columns:
                raise Refusal('UNREVIEWED_SOURCE_COLUMNS', name)
            for column in actual_columns:
                identifier(column)
        source_version = source.execute('SELECT value FROM system WHERE name=?', ('roundcube-version',)).fetchone()
        expected_version = expected.execute('SELECT value FROM system WHERE name=?', ('roundcube-version',)).fetchone()
        if source_version != expected_version or source.execute('SELECT count(*) FROM system').fetchone()[0] != 1:
            raise Refusal('UNREVIEWED_SOURCE_SCHEMA_VERSION', 'system')
        history = tuple(row[0] for row in source.execute('SELECT filename FROM carddav_migrations ORDER BY filename'))
        if history != MIGRATIONS:
            raise Refusal('INCOMPLETE_CARDDAV_MIGRATION_HISTORY', 'carddav_migrations')
        counts = {name: source.execute('SELECT count(*) FROM ' + identifier(name)).fetchone()[0] for name in sorted(names)}
        if any(count > 500000 for count in counts.values()):
            raise Refusal('SOURCE_ROW_LIMIT')
        expected.close()
        return source, counts, source_version[0]
    except BaseException:
        source.close()
        raise


def parse_target(dsn):
    parsed = urlsplit(dsn)
    database = unquote(parsed.path.removeprefix('/'))
    if parsed.scheme not in ('postgres', 'postgresql') or parsed.hostname not in ('127.0.0.1', 'localhost', '::1') or parsed.query or parsed.fragment:
        raise Refusal('LOCAL_FIXED_TARGET_REQUIRED')
    if parsed.username != 'urmail_roundcube' or not parsed.password or not re.fullmatch(r'urmail_fixture_[a-f0-9]{24}', database):
        raise Refusal('OWNED_FIXTURE_LOGIN_REQUIRED')
    return {'host': parsed.hostname, 'port': parsed.port or 5432, 'dbname': database,
            'user': 'urmail_roundcube', 'password': unquote(parsed.password), 'connect_timeout': 5,
            'hostaddr': '::1' if parsed.hostname == '::1' else '127.0.0.1',
            'application_name': 'urmail-v05-fixture-only', 'autocommit': False}


def check_target(connection, fingerprint):
    row = connection.execute("SELECT current_database(),(SELECT oid FROM pg_database WHERE datname=current_database()),session_user,current_user,current_setting('role'),(SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database())").fetchone()
    database, oid, session_user, current_user, role, comment = row
    if session_user != 'urmail_roundcube' or current_user != session_user or role != 'none':
        raise Refusal('ACTUAL_RESTRICTED_LOGIN_REQUIRED')
    logging = connection.execute("SELECT current_setting('log_error_verbosity'),current_setting('log_min_error_statement'),current_setting('log_min_messages')").fetchone()
    if logging[0] != 'terse' or logging[1] not in ('fatal', 'panic') or logging[2] not in ('fatal', 'panic'):
        raise Refusal('PRIVATE_ERROR_LOGGING_REQUIRED')
    attributes = connection.execute('SELECT rolcanlogin,rolinherit,rolsuper,rolcreatedb,rolcreaterole,rolreplication,rolbypassrls FROM pg_roles WHERE rolname=current_user').fetchone()
    if attributes != (True, False, False, False, False, False, False):
        raise Refusal('UNSAFE_TARGET_ROLE')
    if connection.execute('SELECT EXISTS(SELECT 1 FROM pg_auth_members WHERE member=(SELECT oid FROM pg_roles WHERE rolname=current_user))').fetchone()[0]:
        raise Refusal('TARGET_ROLE_MEMBERSHIP_FORBIDDEN')
    external = connection.execute("""
        SELECT EXISTS(SELECT 1 FROM pg_namespace n
         WHERE n.nspname<>%s AND n.nspname<>'information_schema' AND n.nspname !~ '^pg_'
         AND has_schema_privilege(current_user,n.oid,'CREATE'))
        OR EXISTS(SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
         WHERE n.nspname<>%s AND n.nspname<>'information_schema' AND n.nspname !~ '^pg_'
         AND ((c.relkind IN('r','p','v','m','f') AND
          (has_table_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
           OR has_any_column_privilege(current_user,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
          OR (c.relkind='S' AND has_sequence_privilege(current_user,c.oid,'SELECT,USAGE,UPDATE'))))
        OR EXISTS(SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
         WHERE p.prosecdef AND n.nspname<>%s AND n.nspname<>'information_schema' AND n.nspname !~ '^pg_'
         AND has_function_privilege(current_user,p.oid,'EXECUTE'))
        """, (SCHEMA, SCHEMA, SCHEMA)).fetchone()[0]
    if external:
        raise Refusal('TARGET_ROLE_EXTERNAL_PRIVILEGES_FORBIDDEN')
    try:
        proof = json.loads(comment)
    except (TypeError, ValueError):
        raise Refusal('OWNED_FIXTURE_PROOF_REQUIRED') from None
    if proof.get('kind') != 'urmail-v05-owned-fixture-v1' or proof.get('database_oid') != oid or proof.get('database') != database or proof.get('snapshot_sha256') != fingerprint or not re.fullmatch(r'[a-f0-9]{64}', proof.get('nonce', '')):
        raise Refusal('OWNED_FIXTURE_PROOF_MISMATCH')
    owner = connection.execute('SELECT r.rolname FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname=%s', (SCHEMA,)).fetchone()
    if owner != ('urmail_roundcube',) or connection.execute("SELECT has_schema_privilege(current_user,'public','CREATE')").fetchone()[0]:
        raise Refusal('PRIVATE_SCHEMA_BOUNDARY_REQUIRED')
    if connection.execute("SELECT count(*) FROM pg_depend WHERE refclassid='pg_namespace'::regclass AND refobjid=(SELECT oid FROM pg_namespace WHERE nspname=%s)", (SCHEMA,)).fetchone()[0]:
        raise Refusal('TARGET_SCHEMA_NOT_EMPTY')
    return {'database': database, 'database_oid': oid, 'role': current_user, 'schema': SCHEMA,
            'fixture_nonce_sha256': sha(proof['nonce'].encode())}


def pg_columns(connection, table):
    return connection.execute('SELECT column_name,data_type FROM information_schema.columns WHERE table_schema=%s AND table_name=%s ORDER BY ordinal_position', (SCHEMA, table)).fetchall()


def normalized(value, datatype, table, row):
    if value is None:
        return None
    try:
        if datatype in ('integer', 'bigint', 'smallint'):
            if isinstance(value, float) and (not math.isfinite(value) or not value.is_integer()):
                raise ValueError()
            return int(value)
        if datatype in ('timestamp with time zone', 'timestamp without time zone'):
            if isinstance(value, str):
                value = dt.datetime.fromisoformat(value.replace('Z', '+00:00'))
            if not isinstance(value, dt.datetime):
                raise ValueError()
            if datatype == 'timestamp with time zone':
                # Roundcube SQLite datetime('now') and stored timestamps use UTC.
                if value.tzinfo is None:
                    value = value.replace(tzinfo=dt.timezone.utc)
                return value.astimezone(dt.timezone.utc)
            if value.tzinfo is not None:
                raise ValueError()
            return value
        if datatype in ('text', 'character varying', 'character'):
            if isinstance(value, bytes):
                value = value.decode('utf-8', errors='strict')
            if not isinstance(value, str):
                raise ValueError()
            return value
        if datatype == 'bytea':
            return value.encode() if isinstance(value, str) else bytes(value)
    except (ValueError, TypeError, UnicodeError, OverflowError):
        raise Refusal('SOURCE_VALUE_NOT_REPRESENTABLE', table, row) from None
    raise Refusal('UNREVIEWED_TARGET_TYPE', table, row)


def atom(value):
    if isinstance(value, dt.datetime):
        return {'timestamp': value.isoformat(timespec='microseconds')}
    if isinstance(value, (bytes, bytearray, memoryview)):
        return {'bytes': base64.b64encode(value).decode()}
    return value


def digest_rows(rows, columns):
    packed = sorted(((json.dumps([atom(value) for value in row], ensure_ascii=True, separators=(',', ':')).encode(), row) for row in rows), key=lambda item: item[0])
    table_digest = hashlib.sha256()
    per_column = {name: hashlib.sha256() for name, _ in columns}
    for encoded, row in packed:
        table_digest.update(len(encoded).to_bytes(8, 'big') + encoded)
        for (name, _), value in zip(columns, row):
            content = json.dumps(atom(value), ensure_ascii=True, separators=(',', ':')).encode()
            per_column[name].update(len(content).to_bytes(8, 'big') + content)
    return {'rows': len(rows), 'row_digest': table_digest.hexdigest(),
            'column_digests': {name: digest.hexdigest() for name, digest in per_column.items()}}


def read_source_rows(source, table, columns, as_of):
    selected = ','.join(identifier(name) for name, _ in columns)
    records = source.execute('SELECT ' + selected + ' FROM ' + identifier(table)).fetchall()
    result = []
    for index, record in enumerate(records, 1):
        row = tuple(normalized(value, datatype, table, index) for value, (_, datatype) in zip(record, columns))
        if table in CACHE_TABLES:
            expiry = row[next(index for index, (name, _) in enumerate(columns) if name == 'expires')]
            if expiry is not None and expiry <= as_of:
                continue
        result.append(row)
    return result


def create_schema(connection, vendor):
    # Only exact reviewed vendor bytes are accepted; no PHP migration execution,
    # supplied SQL, public schema prefix or arbitrary identifier is available.
    for name in ['SQL/postgres.initial.sql', 'plugins/carddav/dbmigrations/INIT-currentschema/postgres.sql', 'plugins/persistent_login/sql/postgres.sql']:
        connection.execute(vendor[name].replace('TABLE_PREFIX', ''), prepare=False)


def table_order(connection, names):
    edges = connection.execute("SELECT child.relname,parent.relname FROM pg_constraint fk JOIN pg_class child ON child.oid=fk.conrelid JOIN pg_class parent ON parent.oid=fk.confrelid JOIN pg_namespace n ON n.oid=child.relnamespace WHERE fk.contype='f' AND n.nspname=%s", (SCHEMA,)).fetchall()
    remaining = set(names)
    ordered = []
    while remaining:
        ready = sorted(name for name in remaining if not any(child == name and parent in remaining for child, parent in edges))
        if not ready:
            raise Refusal('UNREVIEWED_FOREIGN_KEY_CYCLE')
        ordered.extend(ready)
        remaining.difference_update(ready)
    return ordered


def verify_tables(connection, expected):
    for table, (columns, rows) in expected.items():
        actual = connection.execute(sql.SQL('SELECT {} FROM {}.{}').format(sql.SQL(',').join(sql.Identifier(name) for name, _ in columns), sql.Identifier(SCHEMA), sql.Identifier(table))).fetchall()
        if digest_rows(actual, columns) != digest_rows(rows, columns):
            raise Refusal('NORMALIZED_DATA_DIGEST_MISMATCH', table)


def source_sequence_highwater(source, table):
    """Use SQLite's recorded AUTOINCREMENT history only when it exists."""
    present = source.execute("SELECT 1 FROM sqlite_master WHERE type='table' AND name='sqlite_sequence'").fetchone()
    row = source.execute('SELECT seq FROM sqlite_sequence WHERE name=?', (table,)).fetchone() if present else None
    if row is None:
        return {'available': False, 'value': None}
    value = row[0]
    if not isinstance(value, int) or value < 0 or value >= 9223372036854775807:
        raise Refusal('SOURCE_SEQUENCE_HIGHWATER_REJECTED', table)
    return {'available': True, 'value': value}


def resynchronize_sequences(connection, source):
    """Preserve observed row IDs, source history and target allocated floors."""
    proof = {}
    for table, (column, sequence) in SEQUENCES.items():
        maximum = connection.execute(sql.SQL('SELECT max({}) FROM {}.{}').format(sql.Identifier(column), sql.Identifier(SCHEMA), sql.Identifier(table))).fetchone()[0]
        source_history = source_sequence_highwater(source, table)
        query = sql.SQL('SELECT last_value,is_called FROM {}.{}').format(sql.Identifier(SCHEMA), sql.Identifier(sequence))
        before_value, before_called = connection.execute(query).fetchone()
        # An uncalled sequence's next value is reserved, not yet allocated.
        # Preserve that next-value floor too; never move it backwards.
        target_floor = max(0, before_value if before_called else before_value - 1)
        floor = max(maximum or 0, source_history['value'] or 0, target_floor)
        before_next = before_value + 1 if before_called else before_value
        # Preserve an already sufficient counter verbatim, including is_called;
        # do not rewrite an uncalled reservation as a smaller called value.
        desired_value, desired_called = (before_value, before_called) if before_next > floor else (floor, True)
        performed = (before_value, before_called) != (desired_value, desired_called)
        # The pinned vendor uses independent nextval(text) sequences rather
        # than OWNED BY; pg_get_serial_sequence would miss these counters.
        if performed:
            connection.execute('SELECT setval(%s::regclass,%s,%s)', (SCHEMA + '.' + sequence, desired_value, desired_called))
        after_value, after_called = connection.execute(query).fetchone()
        if (after_value, after_called) != (desired_value, desired_called):
            raise Refusal('TARGET_SEQUENCE_PROOF_MISMATCH', table)
        proof[table] = {'sequence': sequence, 'source_sqlite_sequence': source_history,
                        'current_max_id': maximum,
                        'target_before': {'last_value': before_value, 'is_called': before_called,
                                          'allocated_floor': target_floor},
                        'resync_performed': performed,
                        'target_after': {'last_value': after_value, 'is_called': after_called},
                        'next_value_floor': after_value + 1 if after_called else after_value}
    return proof


def import_fixture(directory, dsn, before_verify=None):
    if psycopg is None:
        raise Refusal('PSYCOPG3_REQUIRED')
    snapshot, vendor, manifest = load_bundle(directory)
    fingerprint = manifest['files']['snapshot.sqlite']['sha256']
    source, counts, source_version = source_inventory(snapshot, vendor, fingerprint)
    as_of = dt.datetime.now(dt.timezone.utc)
    table, row_ref = None, None
    phase = 'precommit'
    report = {'profile': PROFILE, 'fixture_only': True, 'activation_authority': False,
              'snapshot_sha256': fingerprint, 'input_manifest_sha256': sha((Path(directory) / 'input-manifest.json').read_bytes()),
              'provided_source_manifest_sha256': manifest.get('source_manifest_sha256'),
              'vendor_sha256': PINS, 'source_schema_version': source_version, 'cache_cutoff_utc': as_of.isoformat(),
              'tables': {}, 'discarded_auth_rows': {}, 'auth_source_presence': {table: table in counts for table in sorted(AUTH_TABLES)}}
    try:
        with psycopg.connect(**parse_target(dsn)) as connection:
            # Inspect the empty target using catalog-only resolution; a stale
            # private SECURITY DEFINER function must never shadow a gate.
            connection.execute('SET LOCAL search_path=pg_catalog')
            connection.execute("SET LOCAL timezone='UTC'")
            connection.execute("SET LOCAL lock_timeout='5s'")
            connection.execute('SELECT pg_advisory_xact_lock(7750360500)')
            report['target'] = check_target(connection, fingerprint)
            connection.execute('SET LOCAL search_path=roundcube,pg_catalog')
            create_schema(connection, vendor)
            target_version = connection.execute('SELECT value FROM system WHERE name=%s', ('roundcube-version',)).fetchone()[0]
            report['target_schema_version'] = target_version
            if target_version != source_version:
                raise Refusal('BACKEND_SCHEMA_VERSION_REVIEW_REQUIRED', 'system')
            names = tables(source) - {'system'} - AUTH_TABLES
            expected = {}
            for table in table_order(connection, names):
                columns = pg_columns(connection, table)
                if not columns:
                    raise Refusal('TARGET_TABLE_MISSING', table)
                source_columns = {row[1] for row in source.execute('PRAGMA table_info(' + identifier(table) + ')')}
                if {name for name, _ in columns} != source_columns:
                    raise Refusal('BACKEND_COLUMN_MAPPING_REVIEW_REQUIRED', table)
                rows = read_source_rows(source, table, columns, as_of)
                statement = sql.SQL('INSERT INTO {}.{} ({}) VALUES ({})').format(sql.Identifier(SCHEMA), sql.Identifier(table), sql.SQL(',').join(sql.Identifier(name) for name, _ in columns), sql.SQL(',').join(sql.Placeholder() for _ in columns))
                for row_ref, row in enumerate(rows, 1):
                    connection.execute(statement, row)
                expected[table] = (columns, rows)
                report['tables'][table] = digest_rows(rows, columns)
                if table in CACHE_TABLES:
                    report['tables'][table]['expired_rows_discarded'] = counts[table] - len(rows)
            for table in sorted(AUTH_TABLES):
                report['discarded_auth_rows'][table] = counts.get(table, 0)
                if connection.execute(sql.SQL('SELECT count(*) FROM {}.{}').format(sql.Identifier(SCHEMA), sql.Identifier(table))).fetchone()[0]:
                    raise Refusal('AUTH_STATE_NOT_EMPTY', table)
            report['sequences'] = resynchronize_sequences(connection, source)
            report['sequence_resync_performed'] = any(item['resync_performed'] for item in report['sequences'].values())
            table, row_ref = None, None
            if before_verify:
                before_verify(connection)
            verify_tables(connection, expected)
            report['all_business_columns_verified'] = True
            phase = 'commit_pending'
        return report
    except Refusal:
        raise
    except psycopg.Error as error:
        code = 'COMMIT_OUTCOME_UNKNOWN_NO_REPLAY' if phase == 'commit_pending' else 'TARGET_DATABASE_REJECTED'
        raise Refusal(code, table, row_ref, error.sqlstate) from None
    except (sqlite3.Error, UnicodeError, ValueError, TypeError, OverflowError):
        raise Refusal('SOURCE_READ_OR_ENCODING_REJECTED', table, row_ref) from None
    finally:
        source.close()


def write_report(path, report):
    path = Path(path)
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), 0o600)
    with os.fdopen(descriptor, 'w') as output:
        json.dump(report, output, indent=2)
        output.write('\n')
        output.flush()
        os.fsync(output.fileno())


class SafeParser(argparse.ArgumentParser):
    def error(self, message):
        self.exit(2, 'Metadata migration arguments refused; use the documented fixed modes.\n')


def main():
    parser = SafeParser(description=__doc__)
    parser.add_argument('mode', choices=['inspect', 'apply-fixture'])
    parser.add_argument('--private-bundle', type=Path, required=True)
    parser.add_argument('--report', type=Path, required=True)
    args = parser.parse_args()
    descriptor = None
    committed = False
    try:
        descriptor = os.open(args.report, os.O_WRONLY | os.O_CREAT | os.O_EXCL | getattr(os, 'O_NOFOLLOW', 0), 0o600)
        if args.mode == 'inspect':
            snapshot, vendor, manifest = load_bundle(args.private_bundle)
            fingerprint = manifest['files']['snapshot.sqlite']['sha256']
            source, counts, version = source_inventory(snapshot, vendor, fingerprint)
            source.close()
            report = {'profile': PROFILE, 'snapshot_sha256': fingerprint, 'source_schema_version': version,
                      'table_counts': counts, 'fixture_only': True, 'activation_authority': False}
        else:
            report = import_fixture(args.private_bundle, os.environ.get('URMAIL_ROUNDCUBE_MIGRATION_DSN', ''))
            committed = True
        with os.fdopen(descriptor, 'w') as output:
            descriptor = None
            json.dump(report, output, indent=2)
            output.write('\n')
            output.flush()
            os.fsync(output.fileno())
        print(json.dumps({'status': 'verified_fixture' if args.mode == 'apply-fixture' else 'inspected', 'production_activation': False}))
        return 0
    except Refusal as error:
        print(json.dumps(error.detail), file=sys.stderr)
    except (OSError, ValueError, KeyError, sqlite3.Error):
        print(json.dumps({'error': 'COMMITTED_REPORT_WRITE_FAILED_NO_REPLAY' if committed else 'PRIVATE_INPUT_OR_REPORT_REFUSED'}), file=sys.stderr)
    finally:
        if descriptor is not None:
            os.close(descriptor)
    return 1


if __name__ == '__main__':
    sys.exit(main())
