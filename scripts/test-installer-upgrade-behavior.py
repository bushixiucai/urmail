#!/usr/bin/env python3
"""Run complete installer Bash with recording MOCKS in a disposable temp root.

No Ubuntu/package/DB integration claim: apt, dpkg, services, fencing, preparation,
root identity and downloads are mocked. Real shell branches, Python heredocs,
file I/O and dump fsync execute. Fixed paths are relocated; PATH has no host
fallback. No sudo, Docker, network or host-system-path mutations are needed.
Requires Python >=3.9 and Bash >=4. URMAIL_TEST_TRACE=1 prints JSON evidence.
Fresh download tests document that curl/python must already be available; they
are not a claim that bootstrap on a minimal fresh Ubuntu installation works.
"""
from pathlib import Path
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "installer/install.sh"
MOCK = r'''
import json, os, pathlib, shutil, subprocess, sys, tempfile
root = pathlib.Path(os.environ["MOCK_ROOT"])
name, a = pathlib.Path(sys.argv[0]).name, sys.argv[1:]
failure = os.environ.get("MOCK_FAIL", "")
def record(event, **extra):
    with open(os.environ["MOCK_TRACE"], "a") as f:
        f.write(json.dumps(dict(event=event, args=a, **extra)) + "\n")
def safe(p):
    p = pathlib.Path(p).resolve()
    if p != root and root not in p.parents:
        raise SystemExit("mock refuses host path: " + str(p))
    return p
record(name)
if name == "id":
    print("0")  # mocked root, not a real privilege acquisition
elif name == "dpkg":
    if a == ["--print-architecture"]:
        print("amd64")
    elif a[:1] != ["-i"]:
        raise SystemExit(93)
elif name == "dpkg-deb":
    if failure == "extract":
        raise SystemExit(71)
    if failure != "missing-cli":
        dest = safe(a[-1]) / "usr/bin/urmailctl"
        dest.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / "bin/urmailctl", dest)
        dest.chmod(0o755)
        helper = safe(a[-1]) / "usr/share/urmail/upgrade-checkpoint.py"
        helper.parent.mkdir(parents=True, exist_ok=True)
        helper.write_text("import json,os,sys\nwith open(os.environ['MOCK_TRACE'],'a') as f: f.write(json.dumps({'event':'checkpoint-complete','args':sys.argv[1:]})+'\\n')\nif os.environ.get('MOCK_FAIL') == 'checkpoint-complete': sys.exit(1)\n")
elif name == "urmailctl":
    if a[:2] == ["upgrade", "check-installer-lease"]:
        record("lease.checked", note="MOCK; real FD validation covered by CLI integration tests")
    elif a[:2] == ["upgrade", "fence"]:
        hold = root / "var/lib/urmail-backup/upgrade.hold"
        hold.parent.mkdir(parents=True, exist_ok=True)
        hold.write_text("MOCK retained fence\n")
        record("fence.hold-created")
        record("fence.stop-writers")
        if failure == "fence":
            raise SystemExit(72)  # partial fence already acquired a hold
        record("fence.verify-stopped")
    elif a[:2] == ["config", "prepare"]:
        record("prepare", mock_uid=0)
        if failure == "prepare":
            raise SystemExit(75)
    elif a[:2] == ["migrate", "up"]:
        record("migrate")
        if failure == "migrate":
            raise SystemExit(76)
    else:
        raise SystemExit("unexpected CLI operation: " + repr(a))
elif name == "runuser":
    cmd = a[a.index("--") + 1:]
    base = pathlib.Path(cmd[0]).name
    if base == "psql":
        if "CHECKPOINT" in cmd:
            record("checkpoint")
            if failure == "checkpoint":
                raise SystemExit(73)
        else:
            sys.stdin.read()
    elif base == "pg_dump":
        record("dump", pg_environment=[k for k in os.environ if k.startswith("PG")])
        if failure == "dump":
            raise SystemExit(74)
        sys.stdout.buffer.write(b"MOCK-PG-DUMP\n")
    elif base == "env":
        env = os.environ.copy()
        start = next(i for i, arg in enumerate(cmd[1:], 1) if "=" not in arg)
        for arg in cmd[1:start]:
            key, value = arg.split("=", 1)
            env[key] = value
        raise SystemExit(subprocess.call(cmd[start:], env=env))
    else:
        raise SystemExit("unexpected runuser: " + repr(cmd))
elif name == "su":
    sys.stdin.read()
    if "SELECT 1" in a[-1]:
        print("1")
elif name == "install":
    paths, mode, i = [], 0o755, 0
    while i < len(a):
        if a[i] in ("-o", "-g", "-m"):
            if a[i] == "-m":
                mode = int(a[i + 1], 8)
            i += 2
        elif a[i] == "-d":
            i += 1
        else:
            paths.append(safe(a[i]))
            i += 1
    if "-d" in a:
        for p in paths:
            p.mkdir(parents=True, exist_ok=True)
            p.chmod(mode)
    else:
        shutil.copyfile(paths[0], paths[1])
        paths[1].chmod(mode)
elif name == "mktemp":
    p = safe(a[-1] if a[-1] != "-d" else str(root / "tmp/download.XXXXXX"))
    p.parent.mkdir(parents=True, exist_ok=True)
    print(tempfile.mkdtemp(prefix=p.name.replace("XXXXXX", ""), dir=p.parent))
elif name == "rm":
    for arg in a:
        if not arg.startswith("-"):
            shutil.rmtree(safe(arg))
elif name == "curl":
    if failure == "missing-curl":
        raise SystemExit(127)
    out = safe(a[a.index("-o") + 1])
    if out.name == "manifest.json":
        out.write_text(json.dumps(dict(architecture="amd64", version="1.0", filename="urmail_1.0_amd64.deb", sha256="0" * 64)))
    else:
        out.write_text("MOCK download")
elif name == "python3":
    if failure == "missing-python":
        raise SystemExit(127)
    os.execv(sys.executable, [sys.executable] + a)
elif name == "openssl":
    if a[:1] == ["rand"]:
        print("0123456789abcdef")
    for flag in ("-out", "-keyout"):
        if flag in a:
            safe(a[a.index(flag) + 1]).write_text("MOCK key/cert")
elif name == "sha256sum":
    sys.stdin.read()  # integrity is MOCKED, no authenticity claim
elif name in ("systemctl", "apt-get", "pg_isready", "chown"):
    pass
else:
    raise SystemExit("unimplemented mock: " + name)
'''
SITE_CUSTOMIZE = r'''
import json, os
original_fsync = os.fsync
def recording_fsync(fd):
    original_fsync(fd)
    with open(os.environ["MOCK_TRACE"], "a") as output:
        output.write(json.dumps({"event": "fsync.completed", "args": []}) + "\n")
os.fsync = recording_fsync
'''


def modern_bash():
    for candidate in (os.environ.get("BASH"), shutil.which("bash"),
                      "/opt/homebrew/bin/bash", "/usr/local/bin/bash", "/bin/bash"):
        if candidate and Path(candidate).is_file():
            result = subprocess.run([candidate, "-c", 'test "' + '$' + '{BASH_VERSINFO[0]}" -ge 4'])
            if result.returncode == 0:
                return candidate
    raise RuntimeError("Bash >=4 required for installer mapfile statements")


class InstallerBehavior(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        cls.bash = modern_bash()

    def run_installer(self, failure="", fresh=False, download=False):
        with tempfile.TemporaryDirectory(prefix="urmail-installer-behavior-") as directory:
            root = Path(directory)
            bindir = root / "bin"
            bindir.mkdir()
            trace = root / "trace.jsonl"
            trace.touch()
            commands = ("id dpkg dpkg-deb urmailctl runuser su install mktemp rm curl python3 "
                        "openssl sha256sum systemctl apt-get pg_isready chown").split()
            for name in commands:
                if failure == "missing-" + ("python" if name == "python3" else name):
                    continue  # genuinely absent, including command -v checks
                path = bindir / name
                path.write_text("#!" + sys.executable + "\n" + MOCK)
                path.chmod(0o755)
            # No host PATH fallback: only safe utilities, with local-file targets.
            for name in ("basename", "seq", "sleep", "awk", "sed", "grep", "cat", "chmod"):
                tool = shutil.which(name)
                self.assertIsNotNone(tool, name)
                (bindir / name).symlink_to(tool)
            for folder in ("etc/urmail", "tmp", "root", "var/run/postgresql", "usr/share/urmail"):
                (root / folder).mkdir(parents=True, exist_ok=True)
            (root / "etc/os-release").write_text('ID=ubuntu\nVERSION_ID="24.04"\n')
            if not fresh:
                (root / "etc/urmail/urmaild.env").write_text(
                    "URMAIL_DATABASE_URL=postgres://urmail_app:kept@127.0.0.1:5432/urmail?sslmode=disable\n"
                    "URMAIL_DATABASE_URL_PLATFORM=postgres://urmail_platform:kept@127.0.0.1:5432/urmail?sslmode=disable\n"
                    "URMAIL_DATABASE_URL_DOVECOT=postgres://urmail_dovecot:kept@127.0.0.1:5432/urmail?sslmode=disable\n"
                    "URMAIL_DATABASE_URL_MIGRATE=postgres://urmail_migrate:kept@127.0.0.1:5432/urmail?sslmode=disable\n"
                    "URMAIL_STORAGE_ROOT=/data/operator-mail\n"
                    f"URMAIL_MASTER_KEY_PATH={root}/etc/urmail/operator.key\n"
                    "URMAIL_AI_DB_MAX_CONNS=2\n"
                    "URMAIL_OPERATOR_SENTINEL=preserved\n")
                (root / "etc/urmail/operator.key").write_text("fixture existing custom key")
                (root / "etc/urmail/operator.key").chmod(0o640)
                if failure == "pending":
                    (root / "var/lib/urmail-agent").mkdir(parents=True)
                    (root / "var/lib/urmail-agent/pending.json").write_text("{}")
                if failure == "wrong-db":
                    path = root / "etc/urmail/urmaild.env"
                    path.write_text(path.read_text().replace("/urmail?", "/tenantprod?"))
                if failure == "secondary-mode":
                    path = root / "etc/urmail/urmaild.env"
                    path.write_text(path.read_text() + "URMAIL_DNS_SECONDARY_ONLY=true\n")
                if failure == "missing-key":
                    (root / "etc/urmail/operator.key").unlink()
                (root / "etc/urmail/agent.env").write_text(
                    "URMAIL_BACKUP_S3_SECRET_KEY=retained\n"
                    "URMAIL_DNS_PROVIDER=cloudflare\n"
                    "URMAIL_CLOUDFLARE_TOKEN=root-only-secret\n"
                    "URMAIL_METRICS_TOKEN=root-only-metrics\n")
            # Exercise the production merge, not a stub that silently truncates operator settings.
            for helper in ("configure-env.py", "configure-agent-env.py"):
                shutil.copyfile(SOURCE.parent / helper, root / "usr/share/urmail" / helper)
            (root / "sitecustomize.py").write_text(SITE_CUSTOMIZE)
            # Execute full source. Single-pass rewrite never rewrites new root.
            # Exclude suffixes such as $CLI_EXTRACT_DIR/usr/bin (not host paths).
            prefixes = ("/etc/", "/var/", "/root/", "/tmp/", "/usr/bin/", "/usr/sbin/", "/usr/share/", "/run/", "/home/")
            script = re.sub(r"(?<![A-Za-z0-9_}])(?:" + "|".join(re.escape(p) for p in prefixes) + ")",
                            lambda m: str(root) + m.group(), SOURCE.read_text())
            for command in ("runuser", "pg_dump", "urmailctl"):
                for subdir in ("usr/bin", "usr/sbin"):
                    script = script.replace(str(root / subdir / command), str(bindir / command))
            installer = root / "installer.sh"
            installer.write_text(script)
            env = dict(PATH=str(bindir), HOME=str(root / "root"), LANG="C",
                       MOCK_ROOT=str(root), MOCK_TRACE=str(trace), MOCK_FAIL=failure,
                       PYTHONPATH=str(root), URMAIL_MYHOSTNAME="box.example.test",
                       URMAIL_PRIMARY_DOMAIN="example.test", URMAIL_PUBLIC_IPV4="192.0.2.1",
                       URMAIL_ADMIN_PASSWORD="mock-admin", PGPASSWORD="must-not-leak")
            if not download:
                package = root / "urmail_mock_amd64.deb"
                package.touch()
                env["URMAIL_PACKAGE"] = str(package)
            result = subprocess.run([self.bash, str(installer)], env=env,
                                    text=True, capture_output=True, timeout=30)
            if not fresh and result.returncode == 2:
                merged = (root / "etc/urmail/urmaild.env").read_text()
                for retained in ("URMAIL_STORAGE_ROOT=/data/operator-mail",
                                 f"URMAIL_MASTER_KEY_PATH={root}/etc/urmail/operator.key",
                                 "URMAIL_AI_DB_MAX_CONNS=2",
                                 "URMAIL_OPERATOR_SENTINEL=preserved"):
                    self.assertIn(retained + "\n", merged)
                agent = (root / "etc/urmail/agent.env").read_text()
                for secret in ("URMAIL_BACKUP_S3_SECRET_KEY=retained",
                               "URMAIL_DNS_PROVIDER=cloudflare",
                               "URMAIL_CLOUDFLARE_TOKEN=root-only-secret",
                               "URMAIL_METRICS_TOKEN=root-only-metrics"):
                    self.assertIn(secret + "\n", agent)
                self.assertNotIn("root-only-secret", merged)
                self.assertNotIn("root-only-metrics", merged)
                self.assertFalse((root / "etc/urmail/master.key").exists())
            events = [json.loads(line) for line in trace.read_text().splitlines()]
            hold = (root / "var/lib/urmail-backup/upgrade.hold").exists()
            dumps = [p.read_bytes() for p in (root / "root").glob("urmail-upgrade-backups/*/database.dump")]
            if os.environ.get("URMAIL_TEST_TRACE"):
                print(json.dumps(dict(scenario=failure or "success", fresh=fresh,
                                      exit=result.returncode, hold=hold, events=events)))
            return result, events, hold, dumps

    def assert_held_without_start(self, result, events, hold):
        self.assertNotEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertTrue(hold, result.stderr)
        self.assertIn("UPGRADE FENCE RETAINED", result.stderr)
        for event in events:
            args = event["args"]
            self.assertFalse(event["event"] == "urmailctl" and args[:2] == ["upgrade", "release"])
            if event["event"] == "systemctl" and any(x in args for x in ("start", "restart", "--now")):
                self.assertTrue(any(x.startswith("postgresql") for x in args), event)

    def test_preflight_refuses_pending_wrong_db_and_missing_active_key(self):
        for failure in ("pending", "wrong-db", "missing-key", "secondary-mode"):
            with self.subTest(failure=failure):
                result, events, hold, _ = self.run_installer(failure)
                self.assert_held_without_start(result, events, hold)
                names = [event["event"] for event in events]
                for forbidden in ("apt-get", "checkpoint", "dump", "migrate", "prepare"):
                    self.assertNotIn(forbidden, names)

    def test_upgrade_success_prepared_but_held(self):
        result, events, hold, dumps = self.run_installer()
        self.assert_held_without_start(result, events, hold)
        self.assertEqual(result.returncode, 2, result.stderr)
        names = [e["event"] for e in events]
        milestones = ["dpkg-deb", "fence.hold-created", "fence.stop-writers", "fence.verify-stopped",
                      "checkpoint", "dump", "fsync.completed", "apt-get", "migrate", "prepare"]
        positions = [names.index(name) for name in milestones]
        self.assertEqual(positions, sorted(positions))
        dpkg_install = next(i for i, e in enumerate(events) if e["event"] == "dpkg" and e["args"][:1] == ["-i"])
        self.assertLess(names.index("apt-get"), dpkg_install)
        self.assertLess(dpkg_install, names.index("migrate"))
        self.assertEqual(dumps, [b"MOCK-PG-DUMP\n"])
        self.assertEqual(next(e["pg_environment"] for e in events if e["event"] == "dump"), [])
        self.assertEqual(next(e["mock_uid"] for e in events if e["event"] == "prepare"), 0)

    def test_failures_retain_hold_and_abort_at_boundary(self):
        for failure, forbidden in (("fence", ("checkpoint", "dump", "apt-get", "migrate", "prepare")),
                                   ("checkpoint", ("dump", "apt-get", "migrate", "prepare")),
                                   ("dump", ("fsync.completed", "apt-get", "migrate", "prepare")),
                                   ("checkpoint-complete", ("apt-get", "migrate", "prepare")),
                                   ("migrate", ("prepare",)), ("prepare", ())):
            with self.subTest(failure=failure):
                result, events, hold, _ = self.run_installer(failure)
                self.assert_held_without_start(result, events, hold)
                self.assertNotEqual(result.returncode, 2, result.stderr)
                names = [e["event"] for e in events]
                reached = "fence.stop-writers" if failure == "fence" else failure
                self.assertIn(reached, names, result.stderr)
                for name in forbidden:
                    self.assertNotIn(name, names)
                if failure in ("fence", "checkpoint", "dump"):
                    self.assertFalse(any(e["event"] == "dpkg" and e["args"][:1] == ["-i"] for e in events))

    def test_upgrade_missing_cli_never_falls_back_or_mutates_dependencies(self):
        for failure in ("missing-cli", "extract"):
            with self.subTest(failure=failure):
                result, events, hold, _ = self.run_installer(failure)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("new fencing CLI", result.stderr)
                self.assertFalse(hold)  # no fence acquired, do not invent one
                self.assertFalse(any(e["event"] in ("apt-get", "urmailctl", "checkpoint") for e in events))
                self.assertFalse(any(e["event"] == "dpkg" and e["args"][:1] == ["-i"] for e in events))

    def test_fresh_download_requires_curl_and_python_before_apt(self):
        for failure in ("missing-curl", "missing-python"):
            with self.subTest(failure=failure):
                result, events, hold, _ = self.run_installer(failure, fresh=True, download=True)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(hold)
                names = [e["event"] for e in events]
                missing = "python3" if failure == "missing-python" else "curl"
                self.assertNotIn(missing, names)
                self.assertIn(missing, result.stderr)
                self.assertNotIn("apt-get", names)
                self.assertFalse(any(e["event"] == "dpkg" and e["args"][:1] == ["-i"] for e in events))


if __name__ == "__main__":
    unittest.main(verbosity=2)
