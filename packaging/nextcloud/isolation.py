#!/usr/bin/env python3
"""Prepare a private, non-executing Nextcloud 34.0.5 conversion rehearsal."""
import argparse
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import sys

VERSION = '34.0.5'
ROLE = 'urmail_nextcloud'
SCHEMA = 'nextcloud'
CODE_MOUNT = '/usr/local/lib/owncloud'
DATA_MOUNT = '/home/user-data/owncloud'
ALLOWED_ABSOLUTE_LINKS = {
    'config/config.php': DATA_MOUNT + '/config.php',
    'data': DATA_MOUNT,
}


class PreparationError(Exception):
    """Only fixed, non-secret reason codes escape to public output."""


def fail(code):
    raise PreparationError(code)


def digest(data):
    return hashlib.sha256(data).hexdigest()


def private_directory(path):
    path = Path(path)
    if path.is_symlink() or not path.is_dir():
        fail('PRIVATE_DIRECTORY_REQUIRED')
    info = path.stat()
    if info.st_uid != os.getuid() or info.st_mode & 0o077:
        fail('PRIVATE_DIRECTORY_PERMISSIONS_REQUIRED')
    return path.resolve()


def read_regular(path):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, 'rb') as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                fail('REGULAR_PRIVATE_FILE_REQUIRED')
            if info.st_size > 1024 * 1024:
                fail('METADATA_SIZE_LIMIT_EXCEEDED')
            return source.read()
    except OSError:
        fail('REGULAR_PRIVATE_FILE_REQUIRED')


def regular_facts(path, limit):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
        with os.fdopen(descriptor, 'rb') as source:
            info = os.fstat(source.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                fail('REGULAR_PRIVATE_FILE_REQUIRED')
            if info.st_size > limit:
                fail('SOURCE_SIZE_LIMIT_EXCEEDED')
            checksum = hashlib.sha256()
            total = 0
            header = b''
            while chunk := source.read(1024 * 1024):
                if not header:
                    header = chunk[:16]
                total += len(chunk)
                if total > limit:
                    fail('SOURCE_SIZE_LIMIT_EXCEEDED')
                checksum.update(chunk)
            return {'bytes': total, 'sha256': checksum.hexdigest(), 'header': header}
    except OSError:
        fail('REGULAR_PRIVATE_FILE_REQUIRED')


def inspect_tree(root, absolute_links=None, max_bytes=10 * 1024**3):
    """Inspect link text before any traversal; never follow an absolute link."""
    root = private_directory(root)
    links = absolute_links or {}
    records = []
    size = 0
    for directory, names, files in os.walk(root, followlinks=False):
        for name in sorted(names + files):
            path = Path(directory) / name
            relative = path.relative_to(root).as_posix()
            info = path.lstat()
            if not stat.S_ISLNK(info.st_mode) and info.st_mode & 0o022:
                fail('WRITABLE_SOURCE_ENTRY_REJECTED')
            if stat.S_ISLNK(info.st_mode):
                target = os.readlink(path)
                if target.startswith('/'):
                    if links.get(relative) != target:
                        fail('UNMAPPED_ABSOLUTE_SYMLINK')
                else:
                    parts = PurePosixPath(relative).parent.joinpath(target)
                    depth = 0
                    for part in parts.parts:
                        if part == '..':
                            depth -= 1
                        elif part != '.':
                            depth += 1
                        if depth < 0:
                            fail('ESCAPING_RELATIVE_SYMLINK')
                    # Resolve only lexical components. Never follow a source link
                    # through the host's production namespace, even for metadata.
                    normalized = []
                    for part in parts.parts:
                        if part == '..':
                            normalized.pop()
                        elif part != '.':
                            normalized.append(part)
                    prefix = root
                    for part in normalized:
                        prefix = prefix / part
                        try:
                            if stat.S_ISLNK(prefix.lstat().st_mode):
                                fail('CHAINED_SOURCE_SYMLINK_REJECTED')
                        except FileNotFoundError:
                            fail('DANGLING_SOURCE_SYMLINK_REJECTED')
                records.append([relative, 'link', target])
            elif stat.S_ISDIR(info.st_mode):
                records.append([relative, 'directory'])
            elif stat.S_ISREG(info.st_mode):
                facts = regular_facts(path, max_bytes-size)
                size += facts['bytes']
                if size > max_bytes:
                    fail('SOURCE_SIZE_LIMIT_EXCEEDED')
                records.append([relative, 'file', facts['bytes'], facts['sha256']])
            else:
                fail('SPECIAL_SOURCE_ENTRY_REJECTED')
    encoded = json.dumps(sorted(records), ensure_ascii=True, separators=(',', ':')).encode()
    return {'sha256': digest(encoded), 'entries': len(records), 'bytes': size}


def validate_coverage(proof, code_digest, sqlite_digest):
    if not proof:
        return 'BLOCKED_TABLE_COVERAGE_NOT_PROVEN'
    if proof.get('code_sha256') != code_digest or proof.get('sqlite_sha256') != sqlite_digest:
        fail('TABLE_COVERAGE_PROOF_BINDING_MISMATCH')
    source = proof.get('source_tables')
    covered = proof.get('covered_tables')
    if not isinstance(source, list) or not isinstance(covered, list) or not source:
        fail('TABLE_COVERAGE_PROOF_INVALID')
    if any(not isinstance(table, str) or not re.fullmatch(r'[A-Za-z0-9_]+', table) for table in source + covered):
        fail('TABLE_COVERAGE_PROOF_INVALID')
    if set(source) - set(covered):
        fail('REMOVED_APP_TABLES_NOT_COVERED')
    return 'COVERAGE_DECLARED_REQUIRES_RUNTIME_SCHEMA_VERIFICATION'


def runtime_command(bundle, image, database, socket_directory, port=5432):
    """Return fixed argv only. This module intentionally never executes it."""
    if os.getuid() == 0:
        fail('NONROOT_REHEARSAL_OPERATOR_REQUIRED')
    if not re.fullmatch(r'nextcloud@sha256:[a-f0-9]{64}', image):
        fail('PINNED_NEXTCLOUD_IMAGE_REQUIRED')
    if not re.fullmatch(r'[a-z][a-z0-9_]{0,62}', database) or not 1 <= port <= 65535:
        fail('DATABASE_BINDING_INVALID')
    bundle = private_directory(bundle)
    socket_directory = private_directory(socket_directory)
    for entry in socket_directory.iterdir():
        if not re.fullmatch(r'\.s\.PGSQL\.[0-9]+(?:\.lock)?', entry.name) or entry.is_symlink():
            fail('DEDICATED_POSTGRES_SOCKET_DIRECTORY_REQUIRED')
        mode = entry.lstat().st_mode
        if not (stat.S_ISSOCK(mode) or entry.name.endswith('.lock') and stat.S_ISREG(mode)):
            fail('DEDICATED_POSTGRES_SOCKET_DIRECTORY_REQUIRED')
    for path in [str(bundle), str(socket_directory)]:
        if ',' in path or '\n' in path or '\r' in path:
            fail('CONTAINER_MOUNT_PATH_INVALID')
    return ['docker', 'run', '--rm', '--interactive', '--network', 'none', '--read-only',
            '--cap-drop', 'ALL', '--security-opt', 'no-new-privileges', '--pids-limit', '128',
            '--user', f'{os.getuid()}:{os.getgid()}', '--memory', '2g',
            '--tmpfs', '/tmp:rw,nosuid,nodev,noexec,size=256m',
            '--mount', f'type=bind,src={bundle / "code"},dst={CODE_MOUNT}',
            '--mount', f'type=bind,src={bundle / "data"},dst={DATA_MOUNT}',
            '--mount', f'type=bind,src={socket_directory},dst=/run/urmail-pg,readonly',
            '--workdir', CODE_MOUNT, '--entrypoint', '/usr/local/bin/php', image,
            'occ', 'db:convert-type', '--no-interaction', '--all-apps', f'--port={port}',
            'pgsql', ROLE, '/run/urmail-pg', database]


def prepare(code, data, output, image, database, socket_directory, coverage=None):
    code = private_directory(code)
    data = private_directory(data)
    if code == data or code.is_relative_to(data) or data.is_relative_to(code):
        fail('SEPARATE_PRIVATE_SOURCE_ROOTS_REQUIRED')
    output = Path(output)
    parent = private_directory(output.parent)
    if output.exists() or output.is_symlink() or parent.is_relative_to(code) or parent.is_relative_to(data):
        fail('NEW_SEPARATE_OUTPUT_REQUIRED')
    before_code = inspect_tree(code, ALLOWED_ABSOLUTE_LINKS)
    before_data = inspect_tree(data)
    version = read_regular(code / 'version.php')
    if not re.search(rb"\$OC_VersionString\s*=\s*['\"]34\.0\.5['\"]", version):
        fail('EXACT_NEXTCLOUD_VERSION_REQUIRED')
    if not (data / 'config.php').is_file() or (data / 'config.php').is_symlink():
        fail('PRIVATE_CONFIG_COPY_REQUIRED')
    sqlite = regular_facts(data / 'owncloud.db', 10 * 1024**3)
    if sqlite['header'] != b'SQLite format 3\0':
        fail('SQLITE_COPY_REQUIRED')
    if any((data / name).exists() for name in ['owncloud.db-wal', 'owncloud.db-journal']):
        fail('CONSISTENT_SQLITE_BACKUP_REQUIRED')
    coverage_state = validate_coverage(coverage, before_code['sha256'], sqlite['sha256'])
    output.mkdir(mode=0o700)
    try:
        shutil.copytree(code, output / 'code', symlinks=True)
        shutil.copytree(data, output / 'data', symlinks=True)
        if inspect_tree(code, ALLOWED_ABSOLUTE_LINKS) != before_code or inspect_tree(data) != before_data:
            fail('PRIVATE_COPY_CHANGED_DURING_PREPARATION')
        # This overlay is interpreted only in the future isolated runtime. Secrets,
        # instanceid and old UIDs remain in the private base configuration.
        overlay = "<?php\n$CONFIG = [\n'maintenance'=>true,\n'backgroundjobs_mode'=>'ajax',\n" \
            "'trusted_domains'=>['localhost'],\n'trusted_proxies'=>[],\n" \
            "'overwrite.cli.url'=>'http://localhost',\n'overwritehost'=>'localhost',\n" \
            "'overwriteprotocol'=>'http',\n'mail_smtpmode'=>'smtp',\n" \
            "'mail_smtphost'=>'127.0.0.1',\n'mail_smtpport'=>9,\n" \
            "'redis'=>[],\n'memcache.distributed'=>'',\n'memcache.locking'=>'',\n" \
            "'dbreplica'=>[],\n'dbsharding'=>[],\n'dbpersistent'=>false,\n'objectstore'=>[],\n" \
            "'updatechecker'=>false,\n'appstoreenabled'=>false,\n'has_internet_connection'=>false,\n" \
            "'tempdirectory'=>'/tmp',\n'log_type'=>'file',\n'logfile'=>'/home/user-data/owncloud/isolated.log',\n" \
            "'user_backends'=>[],\n];\n"

        destination = output / 'code' / 'config' / 'zz-urmail-isolated.config.php'
        if destination.exists() or destination.is_symlink() or not destination.parent.is_dir():
            fail('ISOLATION_OVERLAY_COLLISION')
        destination.write_text(overlay)
        destination.chmod(0o600)
        plan = {'version': VERSION, 'state': 'PREPARED_NOT_EXECUTABLE',
                'conversion': 'NOT_RUN', 'rehearsal_only': True, 'cutover_allowed': False,
                'cross_application_consistency': 'NOT_ESTABLISHED', 'role': ROLE, 'schema': SCHEMA,
                'same_platform_database': database, 'source_code': before_code,
                'source_data': before_data, 'sqlite_sha256': sqlite['sha256'],
                'table_coverage': coverage_state,
                'required_gates': ['runtime_namespace_and_network_none', 'role_and_empty_schema',
                                   'runtime_schema_coverage', 'session_revocation',
                                   'semantic_identity_dav_files_comparison', 'recovery_checkpoint'],
                'stdin_secret_required': True,
                'trusted_argv': runtime_command(output, image, database, socket_directory)}
        encoded = json.dumps(plan, ensure_ascii=True, indent=2).encode() + b'\n'
        (output / 'plan.json').write_bytes(encoded)
        (output / 'plan.json').chmod(0o600)
        return {'state': plan['state'], 'conversion': 'NOT_RUN', 'plan_sha256': digest(encoded)}
    except Exception:
        # Only the new directory created by this invocation can be removed.
        shutil.rmtree(output)
        raise


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ['code', 'data', 'output', 'image', 'database', 'socket-directory']:
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args(argv)
    try:
        print(json.dumps(prepare(args.code, args.data, args.output, args.image,
                                 args.database, args.socket_directory)))
        return 0
    except PreparationError as error:
        print(json.dumps({'state': 'BLOCKED', 'reason': str(error)}))
        return 1
    except Exception:
        print(json.dumps({'state': 'BLOCKED', 'reason': 'PREPARATION_FAILED'}))
        return 1


if __name__ == '__main__':
    sys.exit(main())
