#!/usr/bin/env python3
"""Execute only inside the marker-bound, network-less V06 rehearsal container."""
import argparse
import base64
import datetime
import decimal
import hashlib
import json
import os
from pathlib import Path
import pwd
import re
import secrets
import shutil
import sqlite3
import stat
import subprocess
import sys

PHP = '/opt/urmail-rehearsal/php/bin/php'
PG = '/usr/lib/postgresql/14/bin'
SOURCE = Path('/source')
CODE = Path('/usr/local/lib/owncloud')
DATA = Path('/home/user-data/owncloud')
WORK = Path('/work')
CLUSTER = Path('/var/lib/urmail-rehearsal-pg')
SOCKET = Path('/run/urmail-pg')
RUNTIME_HARNESS = Path('/tmp/urmail-v06-harness')
GUARDED = False
ENV = {'PATH': '/usr/sbin:/usr/bin:/sbin:/bin', 'LANG': 'C.UTF-8', 'HOME': '/tmp'}


class Blocked(Exception):
    pass


def need(condition, code):
    if not condition:
        raise Blocked(code)


def checksum(path):
    value = hashlib.sha256()
    with open(path, 'rb') as stream:
        for chunk in iter(lambda: stream.read(1024*1024), b''):
            value.update(chunk)
    return value.hexdigest()


def write_private(path, value):
    path.write_text(json.dumps(value, ensure_ascii=True, indent=2) + '\n')
    path.chmod(0o600)


def guard():
    need(os.getuid() == 0 and Path('/.dockerenv').exists(), 'CONTAINER_ROOT_REQUIRED')
    marker = json.loads(Path('/run/urmail-v06.marker').read_text())
    need(marker.get('purpose') == 'urmail-nextcloud-v06-rehearsal-only', 'MARKER_INVALID')
    need(marker.get('php_provenance') in ['original-v80-build-checkpoint-not-release-bundle','verified-v80-release-plus-same-source-pdo-pgsql'] and re.fullmatch(r'sha256:[a-f0-9]{64}',marker.get('runtime_image_id','')), 'RUNTIME_PROVENANCE_REQUIRED')
    for name in ['mnt', 'net', 'pid']:
        need(os.readlink('/proc/self/ns/'+name) != marker['host_namespaces'][name], 'HOST_NAMESPACE_REJECTED')
    need(set(os.listdir('/sys/class/net')) <= {'lo'}, 'NETWORK_ISOLATION_REQUIRED')
    need(not Path('/var/run/docker.sock').exists(), 'DOCKER_SOCKET_REJECTED')
    mounts = [line.split() for line in Path('/proc/self/mountinfo').read_text().splitlines()]
    for point in ['/source', '/harness', '/run/urmail-v06.marker', '/run/source-manifest.private']:
        need(any(row[4] == point and 'ro' in row[5].split(',') for row in mounts), 'READONLY_BIND_REQUIRED')
    need(checksum('/run/source-manifest.private') == marker['manifest_sha256'], 'SOURCE_MANIFEST_MISMATCH')
    need(marker.get('cross_app_consistent') is False and marker.get('rehearsal_only') is True, 'REHEARSAL_MARKER_REQUIRED')
    return marker


def run(command, label, user=None, data=None):
    if user:
        command = ['/usr/sbin/runuser', '-u', user, '--'] + command
    write_private(WORK/'phase.private.json', {'phase':label,'state':'running'})
    result = subprocess.run(command, input=data, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            env=ENV, cwd='/tmp', timeout=3600)
    log = WORK / (label + '.private.log')
    log.write_bytes(result.stdout + b'\n--- stderr ---\n' + result.stderr)
    log.chmod(0o600)
    need(result.returncode == 0, 'STEP_FAILED_' + label.upper())
    return result.stdout


def tree_manifest(root, allow_config_link=False):
    result = {}
    for directory, names, files in os.walk(root, followlinks=False):
        for name in sorted(names+files):
            path = Path(directory)/name
            relative = path.relative_to(root).as_posix()
            info = path.lstat()
            if stat.S_ISLNK(info.st_mode):
                target = os.readlink(path)
                need(allow_config_link and relative == 'config/config.php' and
                     target == '/home/user-data/owncloud/config.php', 'UNMAPPED_SOURCE_LINK')
                result[relative] = {'link': target}
            elif stat.S_ISREG(info.st_mode):
                need(info.st_nlink == 1, 'SOURCE_HARDLINK_REJECTED')
                result[relative] = {'size': info.st_size, 'sha256': checksum(path)}
            elif not stat.S_ISDIR(info.st_mode):
                raise Blocked('SOURCE_SPECIAL_FILE_REJECTED')
    return result


def stage_worker_helpers():
    need(not RUNTIME_HARNESS.exists(), 'NEW_PRIVATE_HELPER_DIRECTORY_REQUIRED')
    RUNTIME_HARNESS.mkdir(mode=0o755)
    fingerprints = []
    for name in ['container_entry.py','schema_preflight.php','config_identity.php']:
        source = Path('/harness')/name
        need(source.is_file() and not source.is_symlink(), 'REGULAR_HARNESS_HELPER_REQUIRED')
        destination = RUNTIME_HARNESS/name
        shutil.copyfile(source,destination)
        destination.chmod(0o644)
        fingerprints.append(name+':'+checksum(destination))
    return hashlib.sha256('\n'.join(fingerprints).encode()).hexdigest()


def copy_source():
    original_code = SOURCE/'usr/local/lib/owncloud'
    original_data = SOURCE/'home/user-data/owncloud'
    need(original_code.is_dir() and not original_code.is_symlink() and original_data.is_dir() and not original_data.is_symlink(), 'SOURCE_LAYOUT_INVALID')
    before_code, before_data = tree_manifest(original_code,True), tree_manifest(original_data)
    need(re.search(rb"\$OC_VersionString\s*=\s*['\"]34\.0\.5['\"]", (original_code/'version.php').read_bytes()) is not None, 'SOURCE_VERSION_INVALID')
    for destination in [CODE, DATA, CLUSTER, WORK]:
        need(destination.is_dir() and not list(destination.iterdir()), 'NEW_EMPTY_WORK_BIND_REQUIRED')
    shutil.copytree(original_code, CODE, symlinks=True, dirs_exist_ok=True)
    shutil.copytree(original_data, DATA, symlinks=True, dirs_exist_ok=True)
    need(tree_manifest(original_code,True) == before_code and tree_manifest(original_data) == before_data, 'SOURCE_CHANGED_DURING_COPY')
    need(tree_manifest(CODE,True)==before_code and tree_manifest(DATA)==before_data, 'PRIVATE_CLONE_CONTENT_MISMATCH')
    write_private(WORK/'source-files.private.json', {'code': before_code, 'data': before_data})
    config = CODE/'config'
    need(not config.is_symlink() and config.is_dir(), 'CONFIG_DIRECTORY_INVALID')
    need(not list(config.glob('*.config.php')), 'EXTRA_CONFIG_FRAGMENTS_REQUIRE_REVIEW')
    # Private clone only. Keep original instanceid/passwordsalt/secret in base config.
    # Private clone only: allow converter to rewrite db* keys in the copied config.
    # Source mirror remains read-only; do not inherit production config_is_read_only.
    overlay = "<?php\n$CONFIG = " + "[\n" + "\n".join([
        "'trusted_domains'=>['localhost'],", "'trusted_proxies'=>[],", "'overwrite.cli.url'=>'http://localhost',",
        "'overwritehost'=>'localhost',", "'overwriteprotocol'=>'http',", "'maintenance'=>true,",
        "'config_is_read_only'=>false,",
        "'mail_smtpmode'=>'smtp',", "'mail_smtphost'=>'127.0.0.1',", "'mail_smtpport'=>9,",
        "'redis'=>[],", "'dbreplica'=>[],", "'dbsharding'=>[],", "'dbpersistent'=>false,",
        "'objectstore'=>[],", "'appstoreenabled'=>false,", "'updatechecker'=>false,",
        "'has_internet_connection'=>false,", "'memcache.local'=>'',", "'memcache.distributed'=>'',", "'memcache.locking'=>'',",
        "'tempdirectory'=>'/tmp',", "'log_type'=>'file',", "'logfile'=>'/home/user-data/owncloud/isolated.private.log',",
        "'user_backends'=>[],", "'debug'=>false,"
    ]) + "\n];\n"
    destination = config/'zz-urmail-isolated.config.php'
    destination.write_text(overlay)
    destination.chmod(0o600)
    web = pwd.getpwnam('www-data')
    for root in [CODE, DATA]:
        os.chmod(root, 0o700)
        for directory, names, files in os.walk(root, followlinks=False, topdown=False):
            for name in names+files:
                os.chown(Path(directory)/name, web.pw_uid, web.pw_gid, follow_symlinks=False)
            os.chown(directory, web.pw_uid, web.pw_gid)
    return before_code, before_data


def sqlite_source():
    path = SOURCE/'home/user-data/owncloud/owncloud.db'
    need(not any(Path(str(path)+suffix).exists() for suffix in ['-wal','-journal']), 'CONSISTENT_SQLITE_BACKUP_REQUIRED')
    connection = sqlite3.connect('file:'+str(path)+'?mode=ro&immutable=1', uri=True)
    tables = [row[0] for row in connection.execute("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")]
    need(len(tables) == 160 and all(re.fullmatch(r'[A-Za-z0-9_]+', name) for name in tables), 'SOURCE_160_TABLES_REQUIRED')
    return connection, tables


def bootstrap_internal():
    need(os.getuid() == pwd.getpwnam('postgres').pw_uid and Path('/run/urmail-v06.marker').exists(), 'POSTGRES_WORKER_REQUIRED')
    import psycopg2
    secret = json.load(sys.stdin)['password']
    connection = psycopg2.connect(dbname='postgres', user='postgres', host=str(SOCKET))
    connection.autocommit = True
    with connection.cursor() as cursor:
        cursor.execute("CREATE DATABASE urmail")
    connection.close()
    connection = psycopg2.connect(dbname='urmail', user='postgres', host=str(SOCKET))
    with connection, connection.cursor() as cursor:
        cursor.execute("CREATE ROLE urmail_nextcloud LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %s", (secret,))
        cursor.execute("ALTER ROLE urmail_nextcloud SET search_path=nextcloud")
        cursor.execute("CREATE SCHEMA nextcloud AUTHORIZATION urmail_nextcloud")
        cursor.execute("REVOKE ALL ON SCHEMA nextcloud FROM PUBLIC")
        cursor.execute("REVOKE CREATE ON SCHEMA public FROM PUBLIC")
        cursor.execute("CREATE SCHEMA roundcube")
        cursor.execute("REVOKE ALL ON SCHEMA roundcube FROM PUBLIC")
        cursor.execute("CREATE TABLE public.platform_boundary(id integer PRIMARY KEY, proof text); INSERT INTO public.platform_boundary VALUES(1,'preserved-platform-fixture')")
        cursor.execute("CREATE TABLE roundcube.metadata_boundary(id integer PRIMARY KEY, proof text); INSERT INTO roundcube.metadata_boundary VALUES(1,'preserved-roundcube-fixture')")
        cursor.execute("CREATE FUNCTION public.privileged_boundary() RETURNS text LANGUAGE SQL SECURITY DEFINER SET search_path=pg_catalog,public AS $$ SELECT proof FROM platform_boundary $$")
        cursor.execute("REVOKE ALL ON FUNCTION public.privileged_boundary() FROM PUBLIC")
        cursor.execute("ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON TABLES FROM PUBLIC")
        cursor.execute("ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE ALL ON SEQUENCES FROM PUBLIC")
        cursor.execute("ALTER DEFAULT PRIVILEGES FOR ROLE urmail_nextcloud IN SCHEMA nextcloud REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC")
    connection.close()


def configure_postgres_internal():
    need(os.getuid() == pwd.getpwnam('postgres').pw_uid, 'POSTGRES_WORKER_REQUIRED')
    hba = CLUSTER/'pg_hba.conf'
    hba.write_text('local all postgres peer\nlocal urmail urmail_nextcloud scram-sha-256\nlocal all all reject\n')
    configuration = CLUSTER/'postgresql.conf'
    with configuration.open('a') as stream:
        stream.write("\nlisten_addresses=''\nunix_socket_directories='/run/urmail-pg'\nunix_socket_permissions=0777\npassword_encryption='scram-sha-256'\nshared_buffers='64MB'\nmax_connections=12\n")

def start_postgres(password):
    postgres = pwd.getpwnam('postgres')
    os.chmod(CLUSTER, 0o700)
    os.chown(CLUSTER, postgres.pw_uid, postgres.pw_gid)
    SOCKET.mkdir(mode=0o755, exist_ok=True)
    os.chmod(SOCKET,0o755)
    os.chown(SOCKET, postgres.pw_uid, postgres.pw_gid)
    run([PG+'/initdb', '-D', str(CLUSTER), '--auth-local=peer', '--auth-host=reject', '--no-locale', '-E', 'UTF8'], 'pg_init', 'postgres')
    run(['/usr/bin/python3',str(RUNTIME_HARNESS/'container_entry.py'),'--internal-pg-config'], 'pg_config','postgres')
    run([PG+'/pg_ctl', '-D', str(CLUSTER), '-l', str(CLUSTER/'server.private.log'), '-w', 'start'], 'pg_start', 'postgres')
    run(['/usr/bin/python3', str(RUNTIME_HARNESS/'container_entry.py'), '--internal-bootstrap'], 'pg_bootstrap', 'postgres', json.dumps({'password': password}).encode())


def nc_connection(password):
    import psycopg2
    return psycopg2.connect(dbname='urmail', user='urmail_nextcloud', password=password, host=str(SOCKET))


def role_preflight(password):
    import psycopg2
    try:
        nc_connection('deliberately-invalid-password').close()
        raise Blocked('PASSWORD_AUTHENTICATION_NOT_ENFORCED')
    except psycopg2.OperationalError:
        pass
    connection = nc_connection(password)
    connection.autocommit = True
    with connection.cursor() as cursor:
        cursor.execute("SELECT current_database(),current_user,session_user,current_schema(),current_setting('role')")
        need(cursor.fetchone() == ('urmail','urmail_nextcloud','urmail_nextcloud','nextcloud','none'), 'REAL_LOGIN_BINDING_INVALID')
        cursor.execute("SELECT rolsuper OR rolinherit OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR EXISTS(SELECT 1 FROM pg_auth_members WHERE member=r.oid) FROM pg_roles r WHERE rolname=current_user")
        need(cursor.fetchone() == (False,), 'ROLE_PRIVILEGES_INVALID')
        cursor.execute("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='nextcloud'")
        need(cursor.fetchone() == (0,), 'EMPTY_NEXTCLOUD_SCHEMA_REQUIRED')
        for statement in ['SELECT * FROM public.platform_boundary','SELECT * FROM roundcube.metadata_boundary', 'SELECT public.privileged_boundary()', 'SET ROLE postgres']:
            try:
                cursor.execute(statement)
            except psycopg2.errors.InsufficientPrivilege:
                continue
            raise Blocked('ROLE_BOUNDARY_NOT_ENFORCED')
        cursor.execute('RESET ROLE')
        cursor.execute('SELECT current_user')
        need(cursor.fetchone() == ('urmail_nextcloud',), 'RESET_ROLE_ESCALATION')
    return connection


def canonical(value, kind):
    if value is None:
        return None
    if kind in ['int2','int4','int8','oid']:
        return ['integer', str(int(value))]
    if kind == 'bool':
        need(value in [True,False,0,1], 'BOOLEAN_REPRESENTATION_INVALID')
        return ['boolean', bool(value)]
    if kind in ['float4','float8','numeric']:
        return ['number', str(decimal.Decimal(str(value)).normalize())]
    if kind == 'bytea':
        if isinstance(value,str):
            value = value.encode('utf-8')
        need(isinstance(value,(bytes,bytearray,memoryview)), 'BINARY_REPRESENTATION_INVALID')
        return ['binary', base64.b64encode(bytes(value)).decode()]
    if kind in ['timestamp','timestamptz']:
        if isinstance(value,str):
            value = datetime.datetime.fromisoformat(value.replace('Z','+00:00'))
        need(isinstance(value,datetime.datetime), 'TIMESTAMP_REPRESENTATION_INVALID')
        if kind=='timestamptz':
            if value.tzinfo is None:
                value = value.replace(tzinfo=datetime.timezone.utc)
            value = value.astimezone(datetime.timezone.utc)
        return ['timestamp',value.isoformat(sep=' ',timespec='microseconds')]
    if kind in ['json', 'jsonb']:
        if isinstance(value, (bytes, bytearray, memoryview)):
            value = bytes(value).decode()
        if isinstance(value, str):
            value = json.loads(value)
        need(isinstance(value, (dict, list, str, int, float, bool, type(None))), 'JSON_REPRESENTATION_INVALID')
        return ['json', json.dumps(value, sort_keys=True, separators=(',', ':'), ensure_ascii=True)]
    if kind in ['bpchar', 'char']:
        # PG CHAR(n)/bpchar pads with spaces; SQLite CHAR does not.
        need(isinstance(value, str), 'CHAR_REPRESENTATION_INVALID')
        return ['text', value.rstrip(' ')]
    if isinstance(value, (datetime.datetime,datetime.date,datetime.time)):
        value = value.isoformat(sep=' ') if isinstance(value,datetime.datetime) else value.isoformat()
    need(isinstance(value, str), 'UNSUPPORTED_COLUMN_REPRESENTATION')
    return ['text', value]


def rows_digest(rows, kinds):
    encoded = []
    for row in rows:
        need(len(row) == len(kinds), 'COLUMN_COUNT_CHANGED')
        encoded.append(json.dumps([canonical(value,kind) for value,kind in zip(row,kinds)],ensure_ascii=True,separators=(',',':')))
    encoded.sort()
    digest = hashlib.sha256()
    for row in encoded:
        digest.update(row.encode()+b'\n')
    return len(encoded), digest.hexdigest()


def sqlite_column_map(source, table):
    """SQLite may preserve historical column casing (remote_Url) while PG folds to remote_url."""
    mapping = {}
    for _cid, name, *_rest in source.execute('PRAGMA table_info("'+table+'")'):
        key = name.lower()
        need(key not in mapping, 'SQLITE_COLUMN_CASE_COLLISION')
        mapping[key] = name
    return mapping


def compare_all(source, tables, connection, prefix, revoked_tables=(), stage="initial"):
    from psycopg2 import sql
    results = []
    with connection.cursor() as cursor:
        for table in tables:
            cursor.execute("SELECT column_name,udt_name FROM information_schema.columns WHERE table_schema='nextcloud' AND table_name=%s ORDER BY ordinal_position", (table,))
            columns = cursor.fetchall()
            write_private(WORK/'continuity-current.private.json',{'stage':stage,'table':table,'columns_and_types':columns})
            need(columns, 'SOURCE_TABLE_NOT_CONVERTED')
            source_map = sqlite_column_map(source, table)
            need(set(source_map) == {row[0].lower() for row in columns}, 'SOURCE_COLUMN_NOT_CONVERTED')
            names = [row[0] for row in columns]
            kinds = [row[1] for row in columns]
            sqlite_names = [source_map[name.lower()] for name in names]
            before_rows = source.execute('SELECT '+','.join('"'+name.replace('"','""')+'"' for name in sqlite_names)+' FROM "'+table+'"').fetchall()
            cursor.execute(sql.SQL('SELECT {} FROM nextcloud.{}').format(sql.SQL(',').join(map(sql.Identifier,names)),sql.Identifier(table)))
            after_rows = cursor.fetchall()
            if table in revoked_tables:
                need(not after_rows, 'COPIED_AUTHENTICATION_STATE_NOT_REVOKED')
                results.append({'table_hash':hashlib.sha256(table.encode()).hexdigest(),'source_rows':len(before_rows),'target_rows':0,'explicit_auth_revocation':True})
            elif table == prefix+'migrations':
                # Official converter rebuilds this table; all source migrations must remain.
                before = {json.dumps(row, default=str) for row in before_rows}
                after = {json.dumps(row, default=str) for row in after_rows}
                need(before <= after, 'MIGRATION_HISTORY_LOST')
                results.append({'table_hash':hashlib.sha256(table.encode()).hexdigest(),'source_rows':len(before),'target_rows':len(after),'framework_rebuilt':True})
            else:
                before, after = rows_digest(before_rows,kinds), rows_digest(after_rows,kinds)
                if before != after:
                    write_private(WORK/'continuity-failure.private.json',{'table':table,'columns':names,'types':kinds,'source_count_sha256':before,'target_count_sha256':after})
                    raise Blocked('SEMANTIC_TABLE_CONTENT_CHANGED')
                results.append({'table_hash':hashlib.sha256(table.encode()).hexdigest(),'rows':before[0],'sha256':before[1]})
    write_private(WORK/('continuity-'+stage+'.private.json'), results)
    return len(results)


def sequence_floor(maximum, start, increment, last_value, is_called, source_highwater):
    need(increment > 0, 'UNSUPPORTED_SEQUENCE_DIRECTION')
    candidates = [start-increment, last_value if is_called else start-increment]
    candidates += [value for value in [maximum,source_highwater] if value is not None]
    floor = max(candidates)
    original_next = last_value+increment if is_called else last_value
    source_floor = max([start-increment]+[value for value in [maximum,source_highwater] if value is not None])
    return floor,original_next,original_next<=source_floor


def sequences_and_write_proof(connection, prefix, source):
    from psycopg2 import sql
    source_highwaters = {}
    if source.execute("SELECT count(*) FROM sqlite_master WHERE type='table' AND name='sqlite_sequence'").fetchone()[0]:
        source_highwaters = {name:int(value) for name,value in source.execute('SELECT name,seq FROM sqlite_sequence') if value is not None}
    with connection.cursor() as cursor:
        cursor.execute("SELECT s.relname,t.relname,a.attname,q.seqstart,q.seqincrement FROM pg_class s JOIN pg_namespace n ON n.oid=s.relnamespace JOIN pg_sequence q ON q.seqrelid=s.oid JOIN pg_depend d ON d.objid=s.oid AND d.deptype IN('a','i') JOIN pg_class t ON t.oid=d.refobjid JOIN pg_attribute a ON a.attrelid=t.oid AND a.attnum=d.refobjsubid WHERE n.nspname='nextcloud' AND s.relkind='S'")
        sequences = cursor.fetchall()
        need(sequences, 'OWNED_SEQUENCES_REQUIRED')
        proof = []
        for sequence, table, column, start, increment in sequences:
            cursor.execute(sql.SQL('SELECT max({}) FROM nextcloud.{}').format(sql.Identifier(column),sql.Identifier(table)))
            maximum = cursor.fetchone()[0]
            cursor.execute(sql.SQL('SELECT last_value,is_called FROM nextcloud.{}').format(sql.Identifier(sequence)))
            last_value,is_called = cursor.fetchone()
            source_highwater = source_highwaters.get(table)
            floor,original_next,original_collision = sequence_floor(maximum,start,increment,last_value,is_called,source_highwater)
            cursor.execute('SELECT setval(%s::regclass,%s,%s)', (sql.Identifier('nextcloud',sequence).as_string(connection),floor if floor >= start else start,floor >= start))
            cursor.execute('SELECT nextval(%s::regclass)', (sql.Identifier('nextcloud',sequence).as_string(connection),))
            value = cursor.fetchone()[0]
            need(value > floor, 'SEQUENCE_RESYNC_PROOF_FAILED')
            proof.append({'sequence':sequence,'table':table,'column':column,'maximum':None if maximum is None else str(maximum),'source_sqlite_highwater':None if source_highwater is None else str(source_highwater),'original_last_value':str(last_value),'original_is_called':is_called,'original_nextval':str(original_next),'original_would_reuse_id':original_collision,'resync_applied':True,'resync_floor':str(floor),'nextval':str(value)})
    connection.autocommit = False
    try:
        with connection.cursor() as cursor:
            table = prefix+'mimetypes'
            cursor.execute(sql.SQL('SELECT max(id) FROM nextcloud.{}').format(sql.Identifier(table)))
            maximum = cursor.fetchone()[0] or 0
            cursor.execute(sql.SQL('INSERT INTO nextcloud.{}(mimetype) VALUES(%s) RETURNING id').format(sql.Identifier(table)), ('application/x-urmail-rehearsal-'+secrets.token_hex(8),))
            inserted_id = cursor.fetchone()[0]
            need(inserted_id > max(maximum,source_highwaters.get(table,0)), 'REAL_INSERT_SEQUENCE_PROOF_FAILED')
            insert_proof = {'table':table,'inserted_id':str(inserted_id),'source_highwater':str(source_highwaters.get(table,0)),'rolled_back':True}
    finally:
        connection.rollback()
        connection.autocommit = True
    write_private(WORK/'sequence-proof.private.json',{'sequences':proof,'real_insert_proof':insert_proof,'real_insert_rolled_back':True})
    return {'owned':len(sequences),'resync_applied':len(sequences),'original_sequences_requiring_resync':sum(item['original_would_reuse_id'] for item in proof)}


def revoke_copied_authentication(connection, tables, prefix):
    from psycopg2 import sql
    expected = prefix+'authtoken'
    need(expected in tables, 'AUTH_TOKEN_TABLE_REQUIRED')
    revoked = {}
    connection.autocommit = False
    try:
        with connection.cursor() as cursor:
            for table in [prefix+'authtoken',prefix+'login_flow_v2']:
                if table in tables:
                    cursor.execute(sql.SQL('DELETE FROM nextcloud.{}').format(sql.Identifier(table)))
                    revoked[table] = cursor.rowcount
        connection.commit()
    except Exception:
        connection.rollback()
        raise
    finally:
        connection.autocommit = True
    write_private(WORK/'authentication-revocation.private.json',{'reason':'Invalidate authentication state copied into an isolated rehearsal; business identity and data remain preserved','tables_and_counts':revoked})
    return set(revoked)


def final_admin_check():
    result = run([PG+'/psql','-h',str(SOCKET),'-U','postgres','-d','urmail','-Atc',"SELECT (SELECT proof FROM public.platform_boundary WHERE id=1)||'|'||(SELECT proof FROM roundcube.metadata_boundary WHERE id=1)"], 'platform_sentinels','postgres')
    need(result.strip() == b'preserved-platform-fixture|preserved-roundcube-fixture', 'OTHER_SCHEMAS_CHANGED')


# Official createSchema only migrates apps still on disk / still defined.
# These residual source tables are reviewed and must still be copied, not skipped by -n.
REVIEWED_RESIDUAL_TABLES = {
    'oc_file_metadata': 'obsolete_core_file_metadata_replaced_by_files_metadata_but_source_rows_present',
    'oc_ex_event_handlers': 'obsolete_app_api_provider_table_empty',
    'oc_ex_speech_to_text': 'obsolete_app_api_provider_table_empty',
    'oc_ex_speech_to_text_q': 'obsolete_app_api_provider_table_empty',
    'oc_ex_text_processing': 'obsolete_app_api_provider_table_empty',
    'oc_ex_text_processing_q': 'obsolete_app_api_provider_table_empty',
    'oc_ex_translation': 'obsolete_app_api_provider_table_empty',
    'oc_ex_translation_q': 'obsolete_app_api_provider_table_empty',
    'oc_group_folders': 'removed_app_groupfolders_empty_tables',
    'oc_group_folders_acl': 'removed_app_groupfolders_empty_tables',
    'oc_group_folders_groups': 'removed_app_groupfolders_empty_tables',
    'oc_group_folders_manage': 'removed_app_groupfolders_empty_tables',
    'oc_group_folders_trash': 'removed_app_groupfolders_empty_tables',
    'oc_group_folders_versions': 'removed_app_groupfolders_empty_tables',
}


def map_sqlite_column_type(typename):
    text = (typename or 'TEXT').strip().upper()
    match = re.fullmatch(r'VARCHAR\((\d+)\)', text)
    if match:
        width = int(match.group(1))
        need(1 <= width <= 8000, 'RESIDUAL_VARCHAR_WIDTH_INVALID')
        return f'varchar({width})'
    if text in ['CLOB', 'TEXT'] or text.startswith('TEXT'):
        return 'text'
    if 'BLOB' in text:
        return 'bytea'
    if text in ['SMALLINT', 'BOOLEAN', 'BOOL']:
        return 'smallint'
    if 'BIGINT' in text or 'UNSIGNED' in text:
        return 'bigint'
    if text in ['INTEGER', 'INT'] or text.startswith('INT'):
        return 'bigint'
    if any(token in text for token in ['REAL', 'DOUBLE', 'FLOAT']):
        return 'double precision'
    raise Blocked('RESIDUAL_COLUMN_TYPE_UNSUPPORTED')


def ensure_residual_tables(connection, source, missing_tables):
    need(isinstance(missing_tables, list), 'RESIDUAL_MISSING_LIST_INVALID')
    unknown = sorted(set(missing_tables) - set(REVIEWED_RESIDUAL_TABLES))
    need(not unknown, 'UNREVIEWED_MISSING_TABLES')
    created = []
    with connection.cursor() as cursor:
        for table in sorted(set(missing_tables)):
            need(re.fullmatch(r'oc_[a-z0-9_]+', table), 'RESIDUAL_TABLE_NAME_INVALID')
            columns = source.execute('PRAGMA table_info("'+table+'")').fetchall()
            need(columns, 'RESIDUAL_SOURCE_TABLE_MISSING')
            definitions = []
            primary = []
            for _cid, name, typename, notnull, _default, pk in columns:
                need(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', name), 'RESIDUAL_COLUMN_NAME_INVALID')
                sql_type = map_sqlite_column_type(typename)
                null_sql = ' NOT NULL' if notnull else ''
                definitions.append(f'"{name}" {sql_type}{null_sql}')
                if pk:
                    primary.append((pk, name))
            need(primary, 'RESIDUAL_PRIMARY_KEY_REQUIRED')
            primary_names = [name for _order, name in sorted(primary)]
            definitions.append('PRIMARY KEY (' + ','.join(f'"{name}"' for name in primary_names) + ')')
            cursor.execute('CREATE TABLE nextcloud.'+table+' ('+', '.join(definitions)+')')
            created.append({
                'table': table,
                'reason': REVIEWED_RESIDUAL_TABLES[table],
                'columns': len(columns),
                'primary_key': primary_names,
            })
    write_private(WORK/'residual-tables.private.json', {
        'created': created,
        'policy': 'reviewed_residual_schema_for_official_convert_copy',
        'clear_schema_used': False,
    })
    return created


def list_nextcloud_tables(connection):
    with connection.cursor() as cursor:
        cursor.execute("SELECT tablename FROM pg_tables WHERE schemaname='nextcloud' ORDER BY tablename")
        return [row[0] for row in cursor.fetchall()]


# createSchema/migrate may insert these into an empty PG appconfig before copyTable runs.
REVIEWED_MIGRATION_INJECTED_APPCONFIG = {
    ('core', 'default_encryption_module'),
    ('user_ldap', 'configuration_prefixes'),
}


def pg_quote_ident(name):
    need(re.fullmatch(r'[A-Za-z_][A-Za-z0-9_]*', name), 'IDENTIFIER_INVALID')
    return '"' + name + '"'


def repair_incomplete_table_copies(connection, source, tables):
    """Re-copy any table whose row count diverged after official copyTable."""
    repaired = []
    with connection.cursor() as cursor:
        for table in tables:
            if table.endswith('migrations'):
                continue
            source_map = sqlite_column_map(source, table)
            cursor.execute(
                "SELECT column_name,udt_name FROM information_schema.columns "
                "WHERE table_schema='nextcloud' AND table_name=%s ORDER BY ordinal_position",
                (table,),
            )
            columns = cursor.fetchall()
            need(columns and set(source_map) == {name.lower() for name, _kind in columns}, 'SOURCE_COLUMN_NOT_CONVERTED')
            names = [name for name, _kind in columns]
            kinds = [kind for _name, kind in columns]
            sqlite_names = [source_map[name.lower()] for name in names]
            before = source.execute(
                'SELECT ' + ','.join('"' + name.replace('"', '""') + '"' for name in sqlite_names) + ' FROM "' + table + '"'
            ).fetchall()
            cursor.execute('SELECT count(*) FROM nextcloud.' + table)
            target_count = cursor.fetchone()[0]
            if target_count == len(before):
                continue
            cursor.execute('DELETE FROM nextcloud.' + table)
            if before:
                placeholders = ','.join(['%s'] * len(names))
                insert = (
                    'INSERT INTO nextcloud.' + table + '(' + ','.join(pg_quote_ident(name) for name in names) + ') '
                    'VALUES (' + placeholders + ')'
                )
                for row in before:
                    values = []
                    for value, kind in zip(row, kinds):
                        if kind in ['json', 'jsonb'] and isinstance(value, str):
                            values.append(value)
                        else:
                            values.append(value)
                    cursor.execute(insert, values)
            cursor.execute('SELECT count(*) FROM nextcloud.' + table)
            need(cursor.fetchone()[0] == len(before), 'TABLE_REPAIR_COUNT_MISMATCH')
            repaired.append({'table': table, 'rows': len(before), 'previous_target_rows': target_count})
    write_private(WORK/'table-copy-repair.private.json', {
        'repaired': repaired,
        'policy': 'recount_and_recopy_when_official_copyTable_rowcount_diverges',
    })
    return repaired


def ensure_source_migration_history(connection, source, prefix):
    """copyTable skips oc_migrations; createSchema only records apps still on disk."""
    table = prefix + 'migrations'
    need(table in {row[0] for row in source.execute("SELECT name FROM sqlite_master WHERE type='table'")}, 'MIGRATIONS_TABLE_REQUIRED')
    source_rows = {(app, version) for app, version in source.execute('SELECT app, version FROM "'+table+'"')}
    inserted = []
    with connection.cursor() as cursor:
        cursor.execute('SELECT app, version FROM nextcloud.'+table)
        target_rows = {(app, version) for app, version in cursor.fetchall()}
        missing = sorted(source_rows - target_rows)
        for app, version in missing:
            need(isinstance(app, str) and isinstance(version, str) and app and version, 'MIGRATION_ROW_INVALID')
            need(re.fullmatch(r'[A-Za-z0-9_.-]+', app) and re.fullmatch(r'[A-Za-z0-9_.-]+', version), 'MIGRATION_ROW_INVALID')
            cursor.execute('INSERT INTO nextcloud.'+table+'(app, version) VALUES (%s, %s)', (app, version))
            inserted.append({'app': app, 'version': version})
        cursor.execute('SELECT app, version FROM nextcloud.'+table)
        after = {(app, version) for app, version in cursor.fetchall()}
        need(source_rows <= after, 'MIGRATION_HISTORY_LOST')
    write_private(WORK/'migrations-history.private.json', {
        'source_rows': len(source_rows),
        'inserted_missing_from_createSchema': inserted,
        'policy': 'preserve_source_migration_rows_skipped_by_official_copyTable',
    })
    return inserted


def reconcile_migration_injected_appconfig(connection, source):
    source_keys = {(appid, key) for appid, key in source.execute('SELECT appid, configkey FROM oc_appconfig')}
    removed = []
    with connection.cursor() as cursor:
        cursor.execute('SELECT appid, configkey FROM nextcloud.oc_appconfig')
        target_keys = {(appid, key) for appid, key in cursor.fetchall()}
        extra = sorted(target_keys - source_keys)
        missing = sorted(source_keys - target_keys)
        need(not missing, 'APPCONFIG_SOURCE_KEYS_MISSING')
        unknown = [item for item in extra if item not in REVIEWED_MIGRATION_INJECTED_APPCONFIG]
        need(not unknown, 'UNREVIEWED_APPCONFIG_INJECTION')
        for appid, key in extra:
            cursor.execute('DELETE FROM nextcloud.oc_appconfig WHERE appid=%s AND configkey=%s', (appid, key))
            need(cursor.rowcount == 1, 'APPCONFIG_INJECTION_DELETE_FAILED')
            removed.append({'appid': appid, 'configkey': key})
    write_private(WORK/'appconfig-reconcile.private.json', {
        'removed_migration_injected_keys': removed,
        'policy': 'delete_reviewed_createSchema_injections_before_continuity',
    })
    return removed


def main(mode):
    global GUARDED
    marker = guard()
    GUARDED = True
    harness_hash = stage_worker_helpers()
    before_code, before_data = copy_source()
    source, tables = sqlite_source()
    php = run([PHP,'-v'], 'php_version').decode(errors='replace')
    need('PHP 8.4.27' in php, 'PHP_RUNTIME_VERSION_INVALID')
    modules = run([PHP,'-m'], 'php_modules').decode().lower().split()
    declared = json.loads((SOURCE/'usr/local/lib/owncloud/composer.json').read_text()).get('require',{})
    required = {name[4:].lower() for name in declared if name.startswith('ext-')} | {'pdo_pgsql','pdo_sqlite','sqlite3','intl'}
    missing = sorted(required-set(modules))
    write_private(WORK/'runtime-modules.private.json',{'required_from_copied_source':sorted(required),'loaded':sorted(set(modules)),'missing':missing})
    need(not missing, 'PHP_REQUIRED_EXTENSION_MISSING')
    before_config = json.loads(run([PHP,str(RUNTIME_HARNESS/'config_identity.php')], 'config_before','www-data'))
    need(before_config['dbtype'] in ['sqlite3','sqlite'], 'SOURCE_SQLITE_CONFIG_REQUIRED')
    need(before_config['datadirectory']==str(DATA), 'PRIVATE_DATA_DIRECTORY_BINDING_REQUIRED')
    report = {'rehearsal_only':True,'cutover_allowed':False,'cross_app_consistent':False,
              'mode':mode,'harness_sha256':harness_hash,'source_manifest_sha256':marker['manifest_sha256'],'source_tables':len(tables),
              'source_version':'34.0.5','runtime_php':'8.4.27','runtime_php_sha256':checksum(PHP),'runtime_php_source':marker['php_provenance'],'runtime_image_id':marker['runtime_image_id'],
              'conversion':'NOT_RUN'}
    password = secrets.token_hex(32)
    started = False
    try:
        if mode != 'inspect':
            start_postgres(password)
            started = True
            connection = role_preflight(password)
            report['real_login_role_preflight'] = 'PASS'
            coverage = run([PHP,str(RUNTIME_HARNESS/'schema_preflight.php')], 'schema_preflight','www-data',password.encode())
            coverage = json.loads(coverage)
            need(set(tables) == set(coverage['source_tables']), 'RUNTIME_SOURCE_TABLES_CHANGED')
            prefix = coverage['prefix']
            need(isinstance(prefix,str) and re.fullmatch(r'[A-Za-z][A-Za-z0-9_]{0,40}',prefix), 'TABLE_PREFIX_INVALID')
            missing = coverage.get('missing_tables') or []
            residual = ensure_residual_tables(connection, source, missing) if missing else []
            target_tables = list_nextcloud_tables(connection)
            coverage['target_tables_after_residual'] = target_tables
            coverage['residual_tables_created'] = [item['table'] for item in residual]
            write_private(WORK/'coverage.private.json', coverage)
            need(not (set(tables) - set(target_tables)), 'ALL_160_TABLE_COVERAGE_REQUIRED')
            report['schema_coverage'] = 'PASS_ALL_160'
            report['residual_tables_covered'] = len(residual)
            if mode == 'convert':
                # Stock occ db:convert-type ends in PgSqlTools sequence SQL that breaks on
                # private schema (MAX() FROM). Copy + saveDBInfo via guarded PHP; resync here.
                convert_report = json.loads(run([PHP,str(RUNTIME_HARNESS/'schema_preflight.php'),'convert'], 'convert','www-data',password.encode()))
                need(convert_report.get('tables_copied') == len(tables), 'CONVERT_TABLE_COUNT_MISMATCH')
                need(convert_report.get('official_sequence_resync') == 'SKIPPED_PRIVATE_SCHEMA_BUG', 'CONVERT_SEQUENCE_POLICY_INVALID')
                report['official_convert_tables_copied'] = convert_report['tables_copied']
                report['official_sequence_resync'] = convert_report['official_sequence_resync']
                report['appconfig_migration_injections_removed'] = len(reconcile_migration_injected_appconfig(connection, source))
                report['source_migration_rows_restored'] = len(ensure_source_migration_history(connection, source, prefix))
                report['tables_rowcount_repaired'] = len(repair_incomplete_table_copies(connection, source, tables))
                report['tables_compared'] = compare_all(source,tables,connection,prefix)
                sequence_report = sequences_and_write_proof(connection,prefix,source)
                report['owned_sequences_proven'] = sequence_report['owned']
                report['sequence_resync'] = sequence_report
                report['tables_compared_after_write_rollback'] = compare_all(source,tables,connection,prefix,stage='after-write')
                final_admin_check()
                after_config = json.loads(run([PHP,str(RUNTIME_HARNESS/'config_identity.php')], 'config_after','www-data'))
                need(after_config['dbtype']=='pgsql' and after_config['dbname']=='urmail' and after_config['dbuser']=='urmail_nextcloud', 'CONVERTED_CONFIG_BINDING_INVALID')
                for key in ['instanceid_sha256','passwordsalt_sha256','secret_sha256']:
                    need(before_config[key]==after_config[key], 'INSTANCE_IDENTITY_OR_ENCRYPTION_SECRET_CHANGED')
                after_files = json.loads(run(['/usr/bin/python3',str(RUNTIME_HARNESS/'container_entry.py'),'--internal-files'], 'files_after','www-data'))
                checked = 0
                for label,before in [('code',before_code),('data',before_data)]:
                    for path,facts in before.items():
                        if label=='data' and path in ['config.php','owncloud.db']:
                            continue
                        need(after_files[label].get(path)==facts, 'PHYSICAL_SOURCE_FILE_CONTENT_CHANGED')
                        checked += 1
                report['physical_copied_files_checked'] = checked
                report['instance_identity_and_secrets_preserved'] = True
                report['conversion'] = 'PASS_WITH_REAL_LOGIN_AND_FULL_TABLE_COMPARISON'
                revoked = revoke_copied_authentication(connection,tables,prefix)
                report['tables_compared_after_auth_revocation'] = compare_all(source,tables,connection,prefix,revoked,stage='after-auth')
                report['sessions'] = 'COPIED_AUTH_TOKEN_AND_PENDING_FLOW_ROWS_REVOKED'
                report['session_browser_consumer'] = 'NOT_RUN'
                report['consumer_dav_imap_files'] = 'NOT_RUN'
            connection.close()
        need(tree_manifest(SOURCE/'usr/local/lib/owncloud',True) == before_code and tree_manifest(SOURCE/'home/user-data/owncloud') == before_data, 'IMMUTABLE_SOURCE_CHANGED')
        report['immutable_source'] = 'PASS'
        report['status'] = 'PASS_REHEARSAL_STAGE'
        write_private(WORK/'result.json', report)
        print(json.dumps(report,ensure_ascii=True))
    finally:
        source.close()
        if started:
            run([PG+'/pg_ctl','-D',str(CLUSTER),'-m','fast','-w','stop'], 'pg_stop','postgres')


if __name__ == '__main__':
    try:
        if sys.argv[1:] == ['--internal-bootstrap']:
            bootstrap_internal()
        elif sys.argv[1:] == ['--internal-pg-config']:
            configure_postgres_internal()
        elif sys.argv[1:] == ['--internal-files']:
            need(os.getuid()==pwd.getpwnam('www-data').pw_uid and Path('/run/urmail-v06.marker').exists(),'WEB_WORKER_REQUIRED')
            print(json.dumps({'code':tree_manifest(CODE,True),'data':tree_manifest(DATA)}))
        else:
            parser = argparse.ArgumentParser()
            parser.add_argument('--mode', choices=['inspect','preflight','convert'],required=True)
            main(parser.parse_args().mode)
    except Exception as error:
        code = str(error) if isinstance(error,Blocked) else 'CONTROLLED_RUNTIME_FAILURE'
        if GUARDED and WORK.is_dir():
            write_private(WORK/'failure.json', {'status':'BLOCKED','reason':code,'rehearsal_only':True,'conversion':'NOT_ACCEPTED'})
        print(json.dumps({'status':'BLOCKED','reason':code,'rehearsal_only':True}))
        sys.exit(1)
