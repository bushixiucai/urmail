#!/usr/bin/env python3
"""Provision fixed-path, same-PostgreSQL Roundcube credentials as root."""
import configparser
import grp
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

ROOT = Path('/etc/urmail-roundcube')
ENV = Path('/etc/urmail/urmaild.env')
AGENT_ENV = Path('/etc/urmail/agent.env')


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


def main():
    if os.geteuid() != 0 or (len(sys.argv) != 1 and sys.argv[1:] != ['disable']):
        raise ValueError('Run the fixed configure command as root; only disable is accepted')
    if sys.argv[1:] == ['disable']:
        disable()
        return
    private_file(ENV)
    private_file(AGENT_ENV)
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
    bridge = existing.get('bridge_key') or values.get('URMAIL_ROUNDCUBE_BRIDGE_KEY') or secrets.token_hex(32)
    if values.get('URMAIL_ROUNDCUBE_BRIDGE_KEY') and values['URMAIL_ROUNDCUBE_BRIDGE_KEY'] != bridge:
        raise ValueError('Roundcube and daemon bridge keys disagree; refusing implicit rotation')
    if not re.fullmatch(r'[A-Za-z0-9_-]{32,128}', bridge):
        raise ValueError('Invalid bridge key format')
    password = existing.get('password') or secrets.token_hex(32)
    des = existing.get('des_key') or secrets.token_hex(12)
    if not isinstance(password, str) or not password or len(password) > 256 or any(ch in password for ch in '\r\n\x00') or not isinstance(des, str) or len(des) != 24:
        raise ValueError('Invalid existing credential format')
    # Reject all unsafe paths before changing the database or writing any secret.
    for name in ('secrets.php', 'pg-service.conf', 'provision-state.json'):
        private_file(ROOT / name)
    # Peer-authenticated local superuser, fixed role and no shell. SQL values are
    # escaped literals; no connection password or secret appears in argv/output.
    literal = password.replace("'", "''")
    sql = "BEGIN;\nSET LOCAL standard_conforming_strings=on;\nDO $$ BEGIN IF NOT EXISTS (SELECT 1 FROM pg_namespace n JOIN pg_roles r ON r.oid=n.nspowner WHERE n.nspname='roundcube' AND r.rolname='urmail_roundcube') THEN RAISE EXCEPTION 'Roundcube schema is not initialized in the target database'; END IF; END $$;\nALTER ROLE urmail_roundcube LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD '" + literal + "';\nALTER ROLE urmail_roundcube SET search_path=roundcube;\nCOMMIT;\n"
    subprocess.run(['runuser', '-u', 'postgres', '--', 'psql', '--no-psqlrc', '-p', str(port), '-d', database, '-v', 'ON_ERROR_STOP=1'], input=sql, text=True, check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, timeout=30)
    current = {'db_dsnw': 'pgsql://urmail_roundcube:' + quote(password, safe='') + '@127.0.0.1:' + str(port) + '/' + quote(database, safe=''), 'des_key': des, 'bridge_key': bridge, 'trusted_hosts': [hostname], 'mail_hostname': hostname, 'panel_url': 'https://' + hostname, 'product_name': existing.get('product_name', 'urMail.cn'), 'language': existing.get('language', 'zh_CN')}
    # Write durable recovery state first. A retry reuses it after a later write
    # failure rather than changing DES/session or bridge secrets.
    atomic_private(ROOT / 'provision-state.json', json.dumps(dict(current, password=password)) + '\n', 0, 0o600)
    encoded = json.dumps(current, ensure_ascii=True).replace('\\', '\\\\').replace("'", "\\'")
    atomic_private(ROOT / 'secrets.php', "<?php return json_decode('" + encoded + "', true, 32, JSON_THROW_ON_ERROR);\n", group, 0o640)
    service = '[urmail-roundcube]\nhost=127.0.0.1\nport=' + str(port) + '\ndbname=' + database + '\nuser=urmail_roundcube\npassword=' + password + '\n'
    atomic_private(ROOT / 'pg-service.conf', service, group, 0o640)
    filtered = [line for line in original.splitlines() if not line.startswith(('URMAIL_ROUNDCUBE_BRIDGE_KEY=', 'URMAIL_ROUNDCUBE_ENABLED='))]
    filtered += ['URMAIL_ROUNDCUBE_BRIDGE_KEY=' + bridge, 'URMAIL_ROUNDCUBE_ENABLED=true']
    atomic_private(ENV, '\n'.join(filtered) + '\n', ENV.stat().st_gid, stat.S_IMODE(ENV.stat().st_mode))
    agent_lines = [line for line in agent_original.splitlines() if not line.startswith('URMAIL_ROUNDCUBE_ENABLED=')]
    agent_lines.append('URMAIL_ROUNDCUBE_ENABLED=true')
    atomic_private(AGENT_ENV, '\n'.join(agent_lines) + '\n', AGENT_ENV.stat().st_gid, stat.S_IMODE(AGENT_ENV.stat().st_mode))
    print('Configured isolated Roundcube metadata in the existing local PostgreSQL database. Secrets preserved; no model or payment operation performed. Restart urmaild-agent and urmaild after installation.')


if __name__ == '__main__':
    try:
        main()
    except (ValueError, OSError, KeyError, subprocess.SubprocessError, json.JSONDecodeError):
        # Do not echo subprocess diagnostics, DSNs, malformed file contents or
        # credential-bearing parser exceptions into logs.
        print('Roundcube configuration refused. Check local target, root-owned private files, matching keys and initialized database roles.', file=sys.stderr)
        sys.exit(1)
