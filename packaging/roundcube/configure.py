#!/usr/bin/env python3
"""Provision fixed-path, same-PostgreSQL Roundcube credentials as root."""
import configparser
import grp
import hashlib
import json
import os
from pathlib import Path
import re
import secrets
import stat
import subprocess
import sys
import tempfile
from urllib.parse import quote, unquote, urlsplit

from legacy_config import parse_php_config

ROOT = Path('/etc/urmail-roundcube')
ENV = Path('/etc/urmail/urmaild.env')
AGENT_ENV = Path('/etc/urmail/agent.env')
V80_RUNTIME = Path('/usr/local/lib/roundcubemail')
V80_PHP = '/usr/bin/php8.4'
ASSETS = Path(__file__).resolve().parent


def private_file(path):
    if path.is_symlink():
        raise ValueError('Refusing a symlink at a managed secret path')
    if path.exists():
        info = path.stat()
        if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o027:
            raise ValueError('Managed secrets must be root-owned regular files without group write or public access')


def parse_environment(text):
    values = {}
    for line in text.splitlines():
        key, sep, value = line.strip().partition('=')
        if sep and not key.startswith('#'):
            values[key] = value.strip().strip('"').strip("'")
    return values


def validate_target(values):
    parsed = urlsplit(values.get('URMAIL_DATABASE_URL_PLATFORM', ''))
    database = unquote(parsed.path.lstrip('/'))
    if parsed.scheme not in ('postgres', 'postgresql') or parsed.hostname not in ('127.0.0.1', 'localhost', '::1'):
        raise ValueError('Roundcube requires the local same-database platform PostgreSQL target')
    if not re.fullmatch(r'[A-Za-z0-9_][A-Za-z0-9_-]{0,62}', database):
        raise ValueError('Invalid PostgreSQL database name')
    port = parsed.port or 5432
    if not 1 <= port <= 65535:
        raise ValueError('Invalid PostgreSQL port')
    hostname = values.get('URMAIL_MYHOSTNAME', '').lower()
    if len(hostname) > 253 or not re.fullmatch(r'(?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?', hostname):
        raise ValueError('Invalid configured mail hostname')
    return database, port, hostname


def atomic_private(path, content, group, mode):
    private_file(path)
    descriptor, temporary = tempfile.mkstemp(prefix='.configure-', dir=path.parent)
    try:
        os.fchmod(descriptor, mode)
        os.fchown(descriptor, 0, group)
        with os.fdopen(descriptor, 'w') as output:
            output.write(content)
            output.flush()
            os.fsync(output.fileno())
        os.replace(temporary, path)
        directory = os.open(path.parent, os.O_DIRECTORY)
        try:
            os.fsync(directory)
        finally:
            os.close(directory)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


def load_existing():
    state = ROOT / 'provision-state.json'
    private_file(state)
    if state.exists():
        return json.loads(state.read_text())
    # Preserve credentials from an earlier managed/manual installation. PHP is
    # executed only on this already trusted, root-owned fixed configuration file.
    php = ROOT / 'secrets.php'
    service = ROOT / 'pg-service.conf'
    private_file(php)
    private_file(service)
    if php.exists() != service.exists():
        raise ValueError('Incomplete existing Roundcube credentials; restore the matching private pair')
    if not php.exists():
        return {}
    result = subprocess.run(['php', '-r', "echo json_encode(require '/etc/urmail-roundcube/secrets.php', JSON_THROW_ON_ERROR);"], check=True, capture_output=True, text=True, timeout=10)
    existing = json.loads(result.stdout)
    parser = configparser.ConfigParser(interpolation=None)
    parser.read_string(service.read_text())
    old = parser['urmail-roundcube']
    if old.get('user') != 'urmail_roundcube':
        raise ValueError('Unexpected Roundcube PostgreSQL role')
    existing['password'] = old['password']
    return existing


def disable():
    # Disabling webmail never opens PostgreSQL or touches private metadata/keys.
    pending = []
    for path in (ENV, AGENT_ENV):
        private_file(path)
        original = path.read_text()
        lines = [line for line in original.splitlines() if not line.startswith('URMAIL_ROUNDCUBE_ENABLED=')]
        lines.append('URMAIL_ROUNDCUBE_ENABLED=false')
        pending.append((path, '\n'.join(lines) + '\n'))
    for path, text in pending:
        atomic_private(path, text, path.stat().st_gid, stat.S_IMODE(path.stat().st_mode))
    print('Disabled webmail in both managed services; mailbox originals, metadata and private keys preserved.')



def legacy_regular_file(path):
    # Legacy files may be world-readable, but source data is never executed.
    # New secret outputs still use the stricter private_file contract.
    if path.is_symlink():
        raise ValueError('Legacy source symlinks require explicit inventory review')
    info = path.stat()
    if not stat.S_ISREG(info.st_mode) or info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError('Legacy source must be a root-owned, non-writable regular file')
    return path.read_text()


def trusted_v80_directory(path):
    if path.is_symlink() or not path.is_dir():
        raise ValueError('The fixed v80 source runtime is required')
    info = path.stat()
    if info.st_uid != 0 or info.st_mode & 0o022:
        raise ValueError('The v80 source directory is not trusted')


def preflight_v80():
    for path in (V80_RUNTIME, V80_RUNTIME / 'config'):
        trusted_v80_directory(path)
    version = legacy_regular_file(V80_RUNTIME / 'version').strip().split(':')[0]
    if version != '1.7.5':
        raise ValueError('Only the explicitly reviewed v80 Roundcube 1.7.5 is supported')
    core = legacy_regular_file(V80_RUNTIME / 'program/include/iniset.php')
    if not re.search(r"define\(\s*['\"]RCMAIL_VERSION['\"]\s*,\s*['\"]1\.7\.5['\"]\s*\)", core):
        raise ValueError('Core version and runtime manifest disagree')
    for name in ('public_html/index.php', 'public_html/static.php'):
        legacy_regular_file(V80_RUNTIME / name)
    result = subprocess.run([V80_PHP, '-r', 'echo PHP_VERSION;'], check=True, capture_output=True, text=True, timeout=10)
    if not re.fullmatch(r'8\.4\.[0-9]+(?:[-+][A-Za-z0-9.-]+)?', result.stdout.strip()):
        raise ValueError('The retained runtime requires the reviewed PHP 8.4 binary')
    source = legacy_regular_file(V80_RUNTIME / 'config/config.inc.php')
    config = parse_php_config(source)
    plugins = config.get('plugins')
    if not isinstance(plugins, list) or not plugins or any(not isinstance(name, str) or not re.fullmatch(r'[A-Za-z][A-Za-z0-9_]{0,63}', name) for name in plugins):
        raise ValueError('Legacy plugin configuration must be an explicit literal list')
    if not isinstance(config.get('des_key'), str) or len(config['des_key']) != 24:
        raise ValueError('A reviewed literal legacy DES key is required')
    for name in plugins:
        folder = V80_RUNTIME / 'plugins' / name
        entry = folder / (name + '.php')
        if folder.is_symlink() or not folder.is_dir() or entry.is_symlink() or not entry.is_file():
            raise ValueError('Every retained legacy plugin entry must exist at its fixed source path')
    if not isinstance(config.get('db_dsnw'), str) or not config['db_dsnw'].startswith('sqlite:///'):
        raise ValueError('Expected the reviewed v80 SQLite metadata source')
    return {'schema_version': 1, 'runtime_mode': 'v80-preserve', 'runtime_version': version,
            'php_version': result.stdout.strip(), 'source_config_sha256': hashlib.sha256(source.encode()).hexdigest(),
            'plugins': list(dict.fromkeys(plugins)), 'legacy_config': config,
            'activation_status': 'blocked',
            'required_proof_fields': {
                'source_snapshot_fingerprint': 'sha256', 'target_database_identity': 'nonempty-string',
                'target_schema': 'literal:roundcube', 'source_identity': 'nonempty-string',
                'normalized_data_digests': 'map:table-name-to-sha256', 'import_audit_manifest': 'sha256'},
            'blockers': ['verified_metadata_import_required', 'legacy_password_adapter_activation_required',
                         'legacy_managesieve_configuration_activation_required',
                         'zpush_identity_sql_adapter_required', 'activation_contract_not_yet_implemented']}


def public_v80_preflight(preflight):
    return {key: value for key, value in preflight.items() if key != 'legacy_config'}

def main():
    if os.geteuid() != 0 or sys.argv[1:] not in ([], ['disable'], ['preflight-v80'], ['configure-v80']):
        raise ValueError('Run a fixed configure mode as root; arbitrary arguments are refused')
    if sys.argv[1:] == ['disable']:
        disable()
        return
    v80 = preflight_v80() if sys.argv[1:] in (['preflight-v80'], ['configure-v80']) else None
    if sys.argv[1:] == ['preflight-v80']:
        print(json.dumps(public_v80_preflight(v80), indent=2))
        return
    if not v80 and (V80_RUNTIME.exists() or V80_RUNTIME.is_symlink()):
        raise ValueError('A source Roundcube runtime requires explicit v80 review; distro configuration is forbidden')
    private_file(ENV)
    private_file(AGENT_ENV)
    if v80:
        disable()
    agent_original = AGENT_ENV.read_text()
    original = ENV.read_text()
    values = parse_environment(original)
    database, port, hostname = validate_target(values)
    if ROOT.is_symlink():
        raise ValueError('Refusing a symlink at the managed configuration directory')
    group = grp.getgrnam('www-data').gr_gid
    ROOT.mkdir(mode=0o750, exist_ok=True)
    if ROOT.stat().st_uid != 0:
        raise ValueError('Managed configuration directory must be root-owned')
    os.chown(ROOT, 0, group)
    os.chmod(ROOT, 0o750)
    existing = load_existing()
    if not v80 and existing.get('runtime_mode') == 'v80-preserve':
        raise ValueError('A retained v80 installation cannot implicitly switch to the distro runtime')
    if v80 and existing.get('des_key') and existing['des_key'] != v80['legacy_config']['des_key']:
        raise ValueError('Existing and source DES keys disagree; implicit rotation is forbidden')
    bridge = existing.get('bridge_key') or values.get('URMAIL_ROUNDCUBE_BRIDGE_KEY') or secrets.token_hex(32)
    if values.get('URMAIL_ROUNDCUBE_BRIDGE_KEY') and values['URMAIL_ROUNDCUBE_BRIDGE_KEY'] != bridge:
        raise ValueError('Roundcube and daemon bridge keys disagree; refusing implicit rotation')
    if not re.fullmatch(r'[A-Za-z0-9_-]{32,128}', bridge):
        raise ValueError('Invalid bridge key format')
    password = existing.get('password') or secrets.token_hex(32)
    des = v80['legacy_config']['des_key'] if v80 else existing.get('des_key') or secrets.token_hex(12)
    if not isinstance(password, str) or not password or len(password) > 256 or any(ch in password for ch in '\r\n\x00') or not isinstance(des, str) or len(des) != 24:
        raise ValueError('Invalid existing credential format')
    # Reject all unsafe paths before changing the database or writing any secret.
    for name in ('secrets.php', 'pg-service.conf', 'provision-state.json', 'password-driver.php', 'password-config.inc.php', 'managesieve-config.inc.php'):
        private_file(ROOT / name)
    # Peer-authenticated local superuser, fixed role and no shell. SQL values are
    # escaped literals; no connection password or secret appears in argv/output.
    literal = password.replace("'", "''")
    sql = "BEGIN;\nSET LOCAL standard_conforming_strings=on;\nDO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname='roundcube' AND r.rolname='urmail_roundcube') THEN RAISE EXCEPTION 'Roundcube schema is not initialized in the target database'; END IF; END $$;\nALTER ROLE urmail_roundcube LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + literal + "';\nALTER ROLE urmail_roundcube SET search_path=roundcube;\nCOMMIT;\n"
    subprocess.run(['runuser', '-u', 'postgres', '--', 'psql', '--no-psqlrc', '-p', str(port), '-d', database, '-v', 'ON_ERROR_STOP=1'], input=sql, text=True, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=30)
    current = {'db_dsnw': 'pgsql://urmail_roundcube:' + quote(password, safe='') + '@127.0.0.1:' + str(port) + '/' + quote(database, safe=''), 'des_key': des, 'bridge_key': bridge, 'trusted_hosts': [hostname], 'mail_hostname': hostname, 'panel_url': 'https://' + hostname, 'product_name': existing.get('product_name', 'urMail.cn'), 'language': existing.get('language', 'zh_CN')}
    if v80:
        current['runtime_mode'] = 'v80-preserve'
        current['legacy_config'] = v80['legacy_config']
        current['v80_preflight'] = public_v80_preflight(v80)
        current['product_name'] = v80['legacy_config'].get('product_name', current['product_name'])
        current['language'] = v80['legacy_config'].get('language', current['language'])
    else:
        current['runtime_mode'] = 'distro'
    driver = (ASSETS / 'plugins/urmail_bridge/password_driver.php').read_text()
    password_config = (ASSETS / 'password-config.inc.php').read_text()
    managesieve_config = (ASSETS / 'managesieve-config.inc.php').read_text()
    current['password_driver_sha256'] = hashlib.sha256(driver.encode()).hexdigest()
    current['password_config_sha256'] = hashlib.sha256(password_config.encode()).hexdigest()
    current['managesieve_config_sha256'] = hashlib.sha256(managesieve_config.encode()).hexdigest()
    # Write durable recovery state first. A retry reuses it after a later write
    # failure rather than changing DES/session or bridge secrets.
    atomic_private(ROOT / 'provision-state.json', json.dumps(dict(current, password=password)) + '\n', 0, 0o600)
    encoded = json.dumps(current, ensure_ascii=True).replace('\\', '\\\\').replace("'", "\\'")
    atomic_private(ROOT / 'secrets.php', "<?php return json_decode('" + encoded + "', true, 32, JSON_THROW_ON_ERROR);\n", group, 0o640)
    service = '[urmail-roundcube]\nhost=127.0.0.1\nport=' + str(port) + '\ndbname=' + database + '\nuser=urmail_roundcube\npassword=' + password + '\n'
    atomic_private(ROOT / 'pg-service.conf', service, group, 0o640)
    atomic_private(ROOT / 'password-driver.php', driver, group, 0o640)
    atomic_private(ROOT / 'password-config.inc.php', password_config, group, 0o640)
    atomic_private(ROOT / 'managesieve-config.inc.php', managesieve_config, group, 0o640)
    filtered = [line for line in original.splitlines() if not line.startswith(('URMAIL_ROUNDCUBE_BRIDGE_KEY=', 'URMAIL_ROUNDCUBE_ENABLED='))]
    filtered += ['URMAIL_ROUNDCUBE_BRIDGE_KEY=' + bridge, 'URMAIL_ROUNDCUBE_ENABLED=' + ('false' if v80 else 'true')]
    atomic_private(ENV, '\n'.join(filtered) + '\n', ENV.stat().st_gid, stat.S_IMODE(ENV.stat().st_mode))
    agent_lines = [line for line in agent_original.splitlines() if not line.startswith('URMAIL_ROUNDCUBE_ENABLED=')]
    agent_lines.append('URMAIL_ROUNDCUBE_ENABLED=' + ('false' if v80 else 'true'))
    atomic_private(AGENT_ENV, '\n'.join(agent_lines) + '\n', AGENT_ENV.stat().st_gid, stat.S_IMODE(AGENT_ENV.stat().st_mode))
    if v80:
        print('Prepared v80-preserve credentials and literal legacy configuration. Activation is blocked pending verified import and credential/identity adapters; both enable flags remain false.')
        return
    print('Configured isolated Roundcube metadata in the existing local PostgreSQL database. Secrets preserved; no model or payment operation performed. Restart urmaild-agent and urmaild after installation.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError, json.JSONDecodeError):
        # Do not echo subprocess diagnostics, DSNs, malformed file contents or
        # credential-bearing parser exceptions into logs.
        print('Roundcube configuration refused. Check local target, root-owned private files, matching keys and initialized database roles.', file=sys.stderr)
        sys.exit(1)
