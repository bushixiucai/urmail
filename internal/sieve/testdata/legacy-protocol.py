"""Disposable local protocol fixture; contains no production credentials or paths."""
import base64
import pathlib
import os
import re
import socket
import subprocess
import time

root = pathlib.Path('/tmp/source-root/mail/sieve')
store = root / 'example.test/old'
store.mkdir(parents=True)
active = root / 'example.test/old.sieve'
original = b'require ["variables", "fileinto", "mailbox"];\r\nset "route" "OriginalRaw";\r\nfileinto :create "${route}";\r\n'
(store / 'original.sieve').write_bytes(original)
active.symlink_to('old/original.sieve')
for name in ('global_before', 'global_after'):
    (root / name).mkdir()
(root / 'global_before/10-before.sieve').write_text('require ["fileinto", "mailbox"]; if header :is "X-Legacy-Before" "yes" { fileinto :create "BeforeRaw"; stop; }\n')
(root / 'global_after/10-after.sieve').write_text('require ["fileinto", "mailbox"]; if header :is "X-Legacy-After" "yes" { fileinto :create "AfterRaw"; stop; }\n')
pathlib.Path('/etc/dovecot/sieve-spam.sieve').write_text('require ["fileinto", "mailbox"]; if header :is "X-Spam-Flag" "YES" { fileinto :create "SpamRaw"; stop; }\n')
for path in [root, *root.rglob('*')]:
    if not path.is_symlink():
        path.chmod(0o755 if path.is_dir() else 0o644)
        os.chown(path, 65534, 65534)
# A disposable local PostgreSQL instance exercises the exact generated query.
pgdata = pathlib.Path('/tmp/legacy-pg')
pgsocket = pathlib.Path('/tmp/legacy-pg-socket')
pgdata.mkdir()
pgsocket.mkdir()
import pwd
pguser = pwd.getpwnam('postgres')
for path in (pgdata, pgsocket):
    os.chown(path, pguser.pw_uid, pguser.pw_gid)
pgbin = next(pathlib.Path('/usr/lib/postgresql').glob('*/bin'))
def pgexec(*args):
    subprocess.run(['/usr/sbin/runuser', '-u', 'postgres', '--', *map(str, args)], check=True, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE)
pgexec(pgbin / 'initdb', '-D', pgdata, '-A', 'trust', '--no-locale')
pgexec(pgbin / 'pg_ctl', '-D', pgdata, '-l', pgdata / 'server.log', '-o', '-k /tmp/legacy-pg-socket -h ""', '-w', 'start')
sql = """CREATE DATABASE legacy_sieve;"""
subprocess.run(['/usr/bin/psql', '-h', str(pgsocket), '-U', 'postgres', '-d', 'postgres', '-c', sql], check=True, stdout=subprocess.DEVNULL)
sql = """
CREATE TABLE tenants(id bigint,status text);
CREATE TABLE domains(id bigint,tenant_id bigint,status text);
CREATE TABLE mailboxes(id bigint,tenant_id bigint,domain_id bigint,address text,password_hash text,status text,quota_bytes bigint,sieve_mode text);
INSERT INTO tenants VALUES(1,'active'); INSERT INTO domains VALUES(1,1,'active');
INSERT INTO mailboxes VALUES(1,1,1,'old@example.test','{PLAIN}fixture-only','active',-1,'legacy_v80'),(2,1,1,'managed@example.test','{PLAIN}fixture-only','active',-1,'managed');
CREATE FUNCTION urmail_domain_mail_ready(bigint,bigint) RETURNS boolean LANGUAGE SQL AS 'SELECT true';
CREATE FUNCTION urmail_mail_allowed(bigint) RETURNS boolean LANGUAGE SQL AS 'SELECT true';
"""
subprocess.run(['/usr/bin/psql', '-v', 'ON_ERROR_STOP=1', '-h', str(pgsocket), '-U', 'postgres', '-d', 'legacy_sieve'], input=sql.encode(), check=True, stdout=subprocess.DEVNULL)
pathlib.Path('/tmp/test-db.conf').write_bytes(pathlib.Path('/test-db.conf').read_bytes())
config = '''protocols = lmtp sieve
listen = 127.0.0.1
ssl = no
disable_plaintext_auth = no
auth_mechanisms = plain
mail_uid = 65534
mail_gid = 65534
first_valid_uid = 1
mail_location = maildir:/tmp/mail/%u
log_path = /tmp/dovecot.log
passdb {
 driver = sql
 args = /tmp/test-db.conf
}
userdb {
 driver = sql
 args = /tmp/test-db.conf
}
service managesieve-login {
 inet_listener sieve {
  address = 127.0.0.1
  port = 4190
 }
}
service lmtp {
 unix_listener /tmp/legacy-lmtp {
  mode = 0600
 }
}
protocol lmtp {
 mail_plugins = sieve
}
plugin {
 sieve = file:/tmp/managed/%u.sieve
}
'''
pathlib.Path('/tmp/dovecot.conf').write_text(config)
subprocess.run(['/usr/bin/sievec', '-c', '/tmp/dovecot.conf', '/etc/dovecot/sieve-spam.sieve'], check=True)
server = subprocess.Popen(['/usr/sbin/dovecot', '-c', '/tmp/dovecot.conf', '-F'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
def deliver(name, header, subject='ordinary', recipient='old@example.test'):
    conn = socket.socket(socket.AF_UNIX)
    conn.settimeout(5)
    conn.connect('/tmp/legacy-lmtp')
    io = conn.makefile('rwb', buffering=0)
    def response():
        lines = []
        while True:
            line = io.readline()
            assert line, 'LMTP EOF'
            lines.append(line)
            if len(line) < 4 or line[3:4] != b'-':
                return b''.join(lines)
    def send(data, code):
        io.write(data + b'\r\n')
        result = response()
        assert result.startswith(code), result
    assert response().startswith(b'220')
    send(b'LHLO isolated', b'250')
    send(b'MAIL FROM:<sender@example.test>', b'250')
    send(('RCPT TO:<' + recipient + '>').encode(), b'250')
    send(b'DATA', b'354')
    body = ('From: sender@example.test\r\nTo: ' + recipient + '\r\nSubject: ' + subject + '\r\nMessage-ID: <' + name + '@fixture>\r\n' + header + '\r\nFixture only.\r\n.\r\n').encode()
    io.write(body)
    assert response().startswith(b'250')
    send(b'QUIT', b'221')
    conn.close()
try:
    for attempt in range(100):
        try:
            client = socket.create_connection(('127.0.0.1', 4190), timeout=3)
            break
        except OSError:
            if server.poll() is not None:
                raise RuntimeError('isolated Dovecot exited')
            time.sleep(.05)
    else:
        raise RuntimeError('isolated ManageSieve did not start')
    stream = client.makefile('rwb', buffering=0)
    def reply():
        lines, literals = [], []
        while True:
            line = stream.readline()
            if not line:
                raise RuntimeError('unexpected ManageSieve EOF')
            lines.append(line)
            literal = re.search(rb'\{([0-9]+)\}\r\n$', line)
            if literal:
                literals.append(stream.read(int(literal.group(1))))
            if re.match(rb'^(OK|NO|BYE)(?: |\r)', line):
                return b''.join(lines), literals
    def command(data, ok=True):
        stream.write(data + b'\r\n')
        lines, literals = reply()
        assert (lines.splitlines()[-1].startswith(b'OK')) == ok, lines
        return lines, literals
    assert reply()[0].splitlines()[-1].startswith(b'OK')
    token = base64.b64encode(b'\0old@example.test\0fixture-only')
    command(b'AUTHENTICATE "PLAIN" "' + token + b'"')
    listed, _ = command(b'LISTSCRIPTS')
    assert b'"original" ACTIVE' in listed, listed
    _, literals = command(b'GETSCRIPT "original"')
    assert literals == [original], literals
    print('PASS original raw LIST/GET bytes and active symlink')
    command(b'GETSCRIPT "../other/original"', ok=False)
    deliver('raw-original', '')
    assert len(list(pathlib.Path('/tmp/mail/old@example.test/.OriginalRaw/new').glob('*'))) == 1
    edited = b'require ["variables", "fileinto", "mailbox"];\r\nset "route" "EditedRaw";\r\nif header :is "Subject" "route" { fileinto :create "${route}"; stop; }\r\n'
    command(b'PUTSCRIPT "edited" {' + str(len(edited)).encode() + b'+}\r\n' + edited)
    assert active.read_bytes() == original
    command(b'SETACTIVE "edited"')
    assert active.read_bytes() == edited and active.is_symlink()
    assert (store / 'original.sieve').read_bytes() == original
    assert active.resolve() == store / 'edited.sieve'
    bad = b'not_valid_script;\r\n'
    command(b'PUTSCRIPT "broken" {' + str(len(bad)).encode() + b'+}\r\n' + bad, ok=False)
    assert active.read_bytes() == edited
    listed, _ = command(b'LISTSCRIPTS')
    assert b'"edited" ACTIVE' in listed and b'"original"' in listed
    command(b'LOGOUT')
    print('PASS raw PUT/SETACTIVE and compile-error preserves active/original')
    deliver('raw-edited', '', 'route')
    deliver('raw-before', 'X-Legacy-Before: yes\r\n')
    deliver('raw-after', 'X-Legacy-After: yes\r\n')
    deliver('raw-spam', 'X-Spam-Flag: YES\r\n')
    deliver('managed', 'X-Legacy-Before: yes\r\n', recipient='managed@example.test')
    for folder in ('EditedRaw', 'BeforeRaw', 'AfterRaw', 'SpamRaw'):
        assert len(list((pathlib.Path('/tmp/mail/old@example.test') / ('.' + folder) / 'new').glob('*'))) == 1, folder
    assert len(list(pathlib.Path('/tmp/mail/managed@example.test/new').glob('*'))) == 1
    assert not pathlib.Path('/tmp/mail/managed@example.test/.BeforeRaw').exists()
    print('PASS real LMTP executes raw rules and per-user before/after/spam; managed mailbox isolated')
finally:
    if pathlib.Path('/tmp/dovecot.log').exists():
        print(pathlib.Path('/tmp/dovecot.log').read_text())
    server.terminate()
    try:
        server.wait(timeout=5)
    except subprocess.TimeoutExpired:
        server.kill()
        server.wait(timeout=5)
    pgexec(pgbin / 'pg_ctl', '-D', pgdata, '-m', 'fast', '-w', 'stop')
