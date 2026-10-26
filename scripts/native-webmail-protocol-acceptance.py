#!/usr/bin/env python3
"""Run native webmail against disposable, network-isolated Dovecot and Postfix.

No product DB, existing container, public listener or production host is used.
The self-signed fixture certificate proves private test trust, not public PKI.
"""

import argparse
import hashlib
import json
import os
import shutil
from pathlib import Path
import subprocess
import time
import uuid


def run(args, **kwargs):
    return subprocess.run(args, check=True, text=True, capture_output=True, **kwargs)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--image", default="urmail-v3-runtime:22.04")
    parser.add_argument("--evidence", default="tmp/native-webmail-remediation-2026-10-26")
    args = parser.parse_args()
    root = Path(__file__).resolve().parent.parent
    evidence = (root / args.evidence).resolve()
    evidence.mkdir(parents=True, exist_ok=True)
    previous = [path for path in evidence.glob("runtime-*") if path.is_file() and path.suffix in (".log", ".json")]
    if previous:
        archive = evidence / "attempts" / (str(time.time_ns()) + "-previous")
        archive.mkdir(parents=True)
        for path in previous:
            shutil.copy2(path, archive / path.name)
        for filename in ("dovecot.log", "postfix.log"):
            if (evidence / filename).exists():
                shutil.copy2(evidence / filename, archive / filename)
    image = json.loads(run(["docker", "image", "inspect", args.image]).stdout)[0]
    arch = image["Architecture"]
    if arch not in ("arm64", "amd64"):
        raise RuntimeError("unsupported fixture image architecture")
    binary = evidence / "webmail-runtime.test"
    env = dict(os.environ, GOOS="linux", GOARCH=arch, CGO_ENABLED="0")
    run(["go", "test", "-c", "-o", str(binary), "./internal/webmail"], cwd=root, env=env)
    api_binary = evidence / "webmail-api-runtime.test"
    run(["go", "test", "-c", "-o", str(api_binary), "./internal/api/handlers"], cwd=root, env=env)
    milter_binary = evidence / "frommilter-runtime.test"
    run(["go", "test", "-c", "-o", str(milter_binary), "./internal/mail/frommilter"], cwd=root, env=env)
    archive_binary = evidence / "archive-runtime.test"
    run(["go", "test", "-c", "-o", str(archive_binary), "./internal/archive"], cwd=root, env=env)
    name = "urmail-native-protocol-" + uuid.uuid4().hex[:12]
    created = False
    result = {"image_id": image["Id"], "architecture": arch, "container": name,
              "test_binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
              "api_test_binary_sha256": hashlib.sha256(api_binary.read_bytes()).hexdigest(),
              "milter_test_binary_sha256": hashlib.sha256(milter_binary.read_bytes()).hexdigest(),
              "archive_test_binary_sha256": hashlib.sha256(archive_binary.read_bytes()).hexdigest(),
              "scope": "isolated real PostgreSQL/HTTP/Dovecot/Postfix acceptance; not new-install or production proof",
              "tls_scope": "self-signed fixture CA explicitly trusted by IMAP test; not public PKI",
              "source_files_sha256": {str(path.relative_to(root)): hashlib.sha256(path.read_bytes()).hexdigest()
                  for path in sorted([*root.glob("internal/webmail/*.go"), *root.glob("internal/api/handlers/mailbox_native*.go"),
                                      *root.glob("internal/mailboxschedule/*.go"), *root.glob("migrations/*.sql"),
                                      *root.glob("internal/archive/*.go"), *root.glob("internal/mail/frombind/*.go"),
                                      *root.glob("internal/mail/frommilter/*.go"), *root.glob("internal/api/middleware/*.go"),
                                      *root.glob("internal/domain/auth/*.go"), *root.glob("internal/domain/mail/password*.go"),
                                      *root.glob("internal/api/handlers/archive_content*.go"),
                                      root / "scripts/native-webmail-protocol-acceptance.py"])},
              "network_mode": "none", "host_ports": False, "status": "FAILED"}
    try:
        run(["docker", "run", "--detach", "--name", name, "--network", "none", "--entrypoint", "sleep", image["Id"], "infinity"])
        created = True
        isolation = json.loads(run(["docker", "inspect", name]).stdout)[0]["HostConfig"]
        if isolation["NetworkMode"] != "none" or isolation.get("PortBindings"):
            raise RuntimeError("fixture isolation verification failed")
        result["network_mode"] = isolation["NetworkMode"]
        result["host_ports"] = bool(isolation.get("PortBindings"))
        run(["docker", "cp", str(binary), name + ":/tmp/webmail-runtime.test"])
        run(["docker", "cp", str(api_binary), name + ":/tmp/webmail-api-runtime.test"])
        run(["docker", "cp", str(milter_binary), name + ":/tmp/frommilter-runtime.test"])
        run(["docker", "cp", str(archive_binary), name + ":/tmp/archive-runtime.test"])
        setup = r'''
set -eu
root=/tmp/native-mail-runtime
mkdir -p "$root/mail" /run/dovecot
chmod 0755 "$root"
for user in alice bob hidden; do
 mkdir -p "$root/mail/$user/Maildir/cur" "$root/mail/$user/Maildir/new" "$root/mail/$user/Maildir/tmp"
done
chown -R 2000:2000 "$root/mail"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj /CN=runtime.example.test -addext subjectAltName=DNS:runtime.example.test -keyout "$root/key.pem" -out "$root/cert.pem" >/dev/null 2>&1
chmod 0600 "$root/key.pem"
printf '%s\n' 'alice@example.test:{PLAIN}isolated-fixture-password' 'bob@example.test:{PLAIN}isolated-fixture-password' 'hidden@example.test:{PLAIN}isolated-fixture-password' > "$root/users"
chmod 0644 "$root/users"
cat > "$root/dovecot.conf" <<'CONF'
protocols = imap lmtp
listen = 127.0.0.1
base_dir = /run/dovecot
log_path = /tmp/native-mail-runtime/dovecot.log
info_log_path = /tmp/native-mail-runtime/dovecot.log
ssl = required
ssl_cert = </tmp/native-mail-runtime/cert.pem
ssl_key = </tmp/native-mail-runtime/key.pem
auth_mechanisms = plain login
disable_plaintext_auth = yes
mail_home = /tmp/native-mail-runtime/mail/%n
mail_location = maildir:~/Maildir
mail_uid = 2000
mail_gid = 2000
first_valid_uid = 2000
passdb {
 driver = passwd-file
 args = username_format=%u /tmp/native-mail-runtime/users
}
userdb {
 driver = static
 args = uid=2000 gid=2000 home=/tmp/native-mail-runtime/mail/%n
}
namespace inbox {
 inbox = yes
 mailbox Drafts {
  special_use = \Drafts
 }
}
service imap-login {
 inet_listener imap {
  port = 0
 }
 inet_listener imaps {
  port = 993
  ssl = yes
 }
}
service auth {
 unix_listener auth-postfix {
  mode = 0666
  user = postfix
  group = postfix
 }
}
service lmtp {
 unix_listener lmtp {
  mode = 0666
  user = postfix
  group = postfix
 }
}
CONF
cat > /etc/postfix/main.cf <<'CONF'
compatibility_level = 3.6
myhostname = runtime.example.test
mydestination = localhost
inet_interfaces = loopback-only
inet_protocols = ipv4
mynetworks = 127.0.0.1/32
smtpd_peername_lookup = no
smtpd_sasl_type = dovecot
smtpd_sasl_path = /run/dovecot/auth-postfix
smtpd_sasl_auth_enable = yes
smtpd_relay_restrictions = permit_sasl_authenticated,reject
smtpd_recipient_restrictions = permit_sasl_authenticated,reject
smtpd_tls_security_level = may
smtpd_tls_cert_file = /tmp/native-mail-runtime/cert.pem
smtpd_tls_key_file = /tmp/native-mail-runtime/key.pem
message_size_limit = 33554432
virtual_mailbox_domains = example.test
virtual_mailbox_maps = static:1
virtual_transport = lmtp:unix:/run/dovecot/lmtp
maillog_file = /var/log/native-webmail.log
CONF
cat > /etc/postfix/master.cf <<'CONF'
127.0.0.1:587 inet n - n - - smtpd
pickup unix n - n 60 1 pickup
cleanup unix n - n - 0 cleanup
qmgr unix n - n 300 1 qmgr
rewrite unix - - n - - trivial-rewrite
proxymap unix - - n - - proxymap
proxywrite unix - - n - 1 proxymap
tlsmgr unix - - n 1000 1 tlsmgr
bounce unix - - n - 0 bounce
defer unix - - n - 0 bounce
trace unix - - n - 0 bounce
verify unix - - n - 1 verify
lmtp unix - - n - - lmtp
anvil unix - - n - 1 anvil
scache unix - - n - 1 scache
postlog unix-dgram n - n - 1 postlogd
CONF
dovecot -c "$root/dovecot.conf"
postfix start
mkdir -p "$root/pg" "$root/pgsocket"
chown postgres:postgres "$root/pg" "$root/pgsocket"
runuser -u postgres -- /usr/lib/postgresql/14/bin/initdb -D "$root/pg" -A trust --no-locale --encoding=UTF8 > "$root/pg-init.log"
runuser -u postgres -- /usr/lib/postgresql/14/bin/pg_ctl -D "$root/pg" -l "$root/pg/postgresql.log" -o "-h 127.0.0.1 -p 5432 -k $root/pgsocket" start
runuser -u postgres -- createdb -h 127.0.0.1 urmail_runtime
'''
        setup_result = subprocess.run(["docker", "exec", "-i", name, "sh", "-s"], input=setup, text=True, capture_output=True)
        (evidence / "runtime-setup.log").write_text(setup_result.stdout + setup_result.stderr)
        setup_result.check_returncode()
        for _ in range(50):
            probe = subprocess.run(["docker", "exec", name, "python3", "-c", "import socket; socket.create_connection(('127.0.0.1',993),1).close(); socket.create_connection(('127.0.0.1',587),1).close()"], text=True, capture_output=True)
            if probe.returncode == 0:
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("fixture protocol listeners did not become available")
        versions = run(["docker", "exec", name, "sh", "-c", "dovecot --version; postconf mail_version; openssl version; /usr/lib/postgresql/14/bin/postgres --version"])
        (evidence / "runtime-versions.log").write_text(versions.stdout + versions.stderr)
        result["versions"] = versions.stdout.splitlines()
        tests = subprocess.run(["docker", "exec", "-e", "URMAIL_WEBMAIL_RUNTIME=1", name, "/tmp/webmail-runtime.test", "-test.run", "^TestNativeWebmailRuntime$", "-test.v", "-test.count=1", "-test.timeout=60s"], text=True, capture_output=True)
        (evidence / "runtime-protocol.log").write_text(tests.stdout + tests.stderr)
        print(tests.stdout, end="")
        print(tests.stderr, end="")
        result["exit_code"] = tests.returncode
        if tests.returncode:
            raise RuntimeError("native protocol acceptance failed")
        api_tests = subprocess.run(["docker", "exec", "-e", "URMAIL_WEBMAIL_RUNTIME=1", "-e", "URMAIL_TEST_DATABASE_URL=postgres://postgres@127.0.0.1:5432/urmail_runtime?sslmode=disable", name, "/tmp/webmail-api-runtime.test", "-test.run", "^TestMailboxNativeRuntime$", "-test.v", "-test.count=1", "-test.timeout=2m"], text=True, capture_output=True)
        (evidence / "runtime-api.log").write_text(api_tests.stdout + api_tests.stderr)
        print(api_tests.stdout, end="")
        print(api_tests.stderr, end="")
        result["api_exit_code"] = api_tests.returncode
        if api_tests.returncode or "--- PASS: TestMailboxNativeRuntime" not in api_tests.stdout:
            raise RuntimeError("native API/runtime acceptance failed or was skipped")
        for binary_path, switch, test_name, filename, timeout in (
                ("/tmp/archive-runtime.test", "URMAIL_ARCHIVE_RUNTIME", "TestArchivePostfixSizeRuntime", "runtime-archive.log", "3m"),
                ("/tmp/frommilter-runtime.test", "URMAIL_FROMBIND_RUNTIME", "TestPostfixFromAuthorizationRuntime", "runtime-frommilter.log", "60s")):
            stage = subprocess.run(["docker", "exec", "-e", switch + "=1", name, binary_path,
                                    "-test.run", "^" + test_name + "$", "-test.v", "-test.count=1", "-test.timeout=" + timeout],
                                   text=True, capture_output=True)
            (evidence / filename).write_text(stage.stdout + stage.stderr)
            print(stage.stdout, end="")
            print(stage.stderr, end="")
            result[test_name + "_exit_code"] = stage.returncode
            if stage.returncode or "--- PASS: " + test_name not in stage.stdout:
                raise RuntimeError(test_name + " failed or was skipped")
        result["status"] = "PASS"
    finally:
        if created:
            for filename in ("dovecot.log", "postfix.log"):
                log = subprocess.run(["docker", "exec", name, "cat", ("/var/log/native-webmail.log" if filename == "postfix.log" else "/tmp/native-mail-runtime/" + filename)], text=True, capture_output=True)
                (evidence / filename).write_text(log.stdout + log.stderr)
            removed = subprocess.run(["docker", "rm", "--force", name], text=True, capture_output=True)
            result["owned_container_removed"] = removed.returncode == 0
        (evidence / "runtime-result.json").write_text(json.dumps(result, indent=2) + "\n")
    print("Evidence:", evidence)


if __name__ == "__main__":
    main()
