#!/usr/bin/env python3
"""Exercise deb package upgrade preservation and Alertmanager pre-check guard for observability."""
import os
import shutil
import sys
from pathlib import Path
import re
import stat
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]
BUILD_SH = ROOT / "packaging/deb/build.sh"
ALERTMANAGER_SERVICE = ROOT / "packaging/systemd/urmail-alertmanager.service"


def extract_embedded_script(source: str, marker: str) -> str:
    _, rest = source.split(f'cat >"$STAGE/DEBIAN/{marker}" <<\'EOF\'\n', 1)
    script, _ = rest.split("\nEOF", 1)
    return script


def extract_exec_start_pre(service_text: str) -> str:
    for line in service_text.splitlines():
        if line.startswith("ExecStartPre="):
            return line.split("ExecStartPre=", 1)[1].strip()
    raise ValueError("ExecStartPre not found in service definition")


GNU_STAT_LINE = 'stat -c \'%a %U %G\' "$OBS_DIR"'


class ObservabilityPackagingTests(unittest.TestCase):
    def setUp(self):
        self.build_source = BUILD_SH.read_text()
        self.service_source = ALERTMANAGER_SERVICE.read_text()

    def test_build_sh_observability_destination(self):
        """Templates must install to /usr/share/urmail/observability, not directly into /etc."""
        self.assertIn('install -d "$STAGE/usr/share/urmail/observability"', self.build_source)
        self.assertIn('install -m 0644 "$ROOT/packaging/observability/"*.yml "$STAGE/usr/share/urmail/observability/"', self.build_source)
        # Ensure there is no data installation of observability directly into $STAGE/etc
        self.assertNotIn('"$STAGE/etc/urmail/observability"', self.build_source)

    def test_preinst_fresh_install_no_snapshot(self):
        """Fresh install must not create an empty or stale migration snapshot."""
        preinst = extract_embedded_script(self.build_source, "preinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            migration_dir = base / "var/lib/urmail-config-migration/observability"
            script = self._adapt_script(preinst, base)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse(migration_dir.exists())

    def test_preinst_snapshots_existing_observability_configs(self):
        """Upgrades must safely snapshot existing configurations and enabled markers before unpack."""
        preinst = extract_embedded_script(self.build_source, "preinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            obs_dir = base / "etc/urmail/observability"
            obs_dir.mkdir(parents=True)
            # Mirror the production layout: alertmanager runs with 0750
            # root:urmail-monitor on /etc/urmail/observability.
            os.chmod(obs_dir, 0o750)
            custom_cfg = "route:\n  receiver: external-webhook\nreceivers:\n  - name: external-webhook\n"
            (obs_dir / "alertmanager.yml").write_text(custom_cfg)
            (obs_dir / "receiver.enabled").write_text("")
            (obs_dir / "monitor.enabled").write_text("")
            token_file = obs_dir / "metrics.token"
            token_file.write_text("token-secret-12345")
            token_file.chmod(0o640)

            script = self._adapt_script(preinst, base)
            if sys.platform == "darwin":
                # macOS stat lacks -c; CI and production run GNU stat.
                script = script.replace(GNU_STAT_LINE, "printf '750 root wheel\\n'")
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            migration_dir = base / "var/lib/urmail-config-migration/observability"
            self.assertTrue(migration_dir.is_dir())
            migration_base = base / "var/lib/urmail-config-migration"
            self.assertEqual(stat.S_IMODE(migration_base.stat().st_mode), 0o700)
            # The snapshot mirrors the live directory mode instead of a
            # hardcoded 0700, so restoring it cannot downgrade the target.
            self.assertEqual(
                stat.S_IMODE(migration_dir.stat().st_mode),
                stat.S_IMODE(obs_dir.stat().st_mode),
            )

            self.assertEqual((migration_dir / "alertmanager.yml").read_text(), custom_cfg)
            self.assertTrue((migration_dir / "receiver.enabled").exists())
            self.assertTrue((migration_dir / "monitor.enabled").exists())
            self.assertEqual((migration_dir / "metrics.token").read_text(), "token-secret-12345")
            self.assertEqual(stat.S_IMODE((migration_dir / "metrics.token").stat().st_mode), 0o640)

    def test_postinst_fresh_install_seeds_templates(self):
        """Fresh install must seed templates from /usr/share/urmail/observability when target does not exist."""
        postinst = extract_embedded_script(self.build_source, "postinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            usr_share = base / "usr/share/urmail/observability"
            usr_share.mkdir(parents=True)
            (usr_share / "alertmanager.yml").write_text("template-alertmanager")
            (usr_share / "prometheus.yml").write_text("template-prometheus")

            script = self._adapt_script(postinst, base, stub_systemctl=True)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            obs_dir = base / "etc/urmail/observability"
            self.assertTrue(obs_dir.is_dir())
            self.assertEqual((obs_dir / "alertmanager.yml").read_text(), "template-alertmanager")
            self.assertEqual((obs_dir / "prometheus.yml").read_text(), "template-prometheus")
            self.assertFalse((obs_dir / "receiver.enabled").exists())

    def test_postinst_restores_from_snapshot_without_overwriting(self):
        """Postinst must prioritize restoring existing configuration from snapshot and never overwrite it."""
        postinst = extract_embedded_script(self.build_source, "postinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            migration_dir = base / "var/lib/urmail-config-migration/observability"
            migration_dir.mkdir(parents=True)
            custom_cfg = "custom-external-receiver-configuration"
            (migration_dir / "alertmanager.yml").write_text(custom_cfg)
            (migration_dir / "receiver.enabled").write_text("")
            (migration_dir / "metrics.token").write_text("saved-token")
            (migration_dir / "metrics.token").chmod(0o640)

            # Template exists in /usr/share
            usr_share = base / "usr/share/urmail/observability"
            usr_share.mkdir(parents=True)
            (usr_share / "alertmanager.yml").write_text("stock-template-should-not-overwrite")
            (usr_share / "alerts.yml").write_text("stock-alerts-new")

            script = self._adapt_script(postinst, base, stub_systemctl=True)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            obs_dir = base / "etc/urmail/observability"
            # Existing alertmanager.yml must be the restored custom config, NOT the stock template
            self.assertEqual((obs_dir / "alertmanager.yml").read_text(), custom_cfg)
            self.assertTrue((obs_dir / "receiver.enabled").exists())
            self.assertEqual((obs_dir / "metrics.token").read_text(), "saved-token")
            self.assertEqual(stat.S_IMODE((obs_dir / "metrics.token").stat().st_mode), 0o640)
            # New file not in snapshot was seeded
            self.assertEqual((obs_dir / "alerts.yml").read_text(), "stock-alerts-new")

    def test_postinst_preserves_existing_directory_permissions(self):
        """Upgrades must not reset permissions of existing directories (alertmanager needs 0750 root:urmail-monitor)."""
        postinst = extract_embedded_script(self.build_source, "postinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            obs_dir = base / "etc/urmail/observability"
            obs_dir.mkdir(parents=True)
            (obs_dir / "alertmanager.yml").write_text("operator-managed")
            os.chmod(obs_dir, 0o750)
            varlib = base / "var/lib/urmail"
            varlib.mkdir(parents=True)
            os.chmod(varlib, 0o700)

            script = self._adapt_script(postinst, base, stub_systemctl=True)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            # Existing directories keep operator permissions untouched.
            self.assertEqual(stat.S_IMODE(obs_dir.stat().st_mode), 0o750)
            self.assertEqual(stat.S_IMODE(varlib.stat().st_mode), 0o700)

    def test_postinst_creates_missing_directories(self):
        """Idempotent ensure_dir must still create directories that do not exist."""
        postinst = extract_embedded_script(self.build_source, "postinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            script = self._adapt_script(postinst, base, stub_systemctl=True)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            for expected in [
                base / "var/lib/urmail/backups",
                base / "var/lib/urmail/archive",
                base / "etc/urmail/observability",
                base / "etc/postfix/urmail",
            ]:
                self.assertTrue(expected.is_dir(), f"missing {expected}")

    def test_build_sh_has_no_unconditional_etc_install_dash_d(self):
        """postinst must never unconditionally reset permissions under /etc."""
        postinst = extract_embedded_script(self.build_source, "postinst")
        for line in postinst.splitlines():
            stripped = line.strip()
            if stripped.startswith("install -d") and "/etc/" in stripped:
                self.fail(f"unconditional install -d on system path: {stripped}")

    def test_upgrade_preserves_operator_directory_permissions_end_to_end(self):
        """preinst snapshot + postinst restore must round-trip 0750 root-group mode.

        This is the v0.6.6 acceptance case: alertmanager requires 0750
        root:urmail-monitor on /etc/urmail/observability and the upgrade used
        to reset it to 0700 via the migration snapshot's chmod stamp.
        """
        preinst = extract_embedded_script(self.build_source, "preinst")
        postinst = extract_embedded_script(self.build_source, "postinst")
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            obs_dir = base / "etc/urmail/observability"
            obs_dir.mkdir(parents=True)
            (obs_dir / "alertmanager.yml").write_text("operator-managed-config")
            os.chmod(obs_dir, 0o750)
            live_mode = stat.S_IMODE(obs_dir.stat().st_mode)

            # Upgrade step 1: preinst snapshots the live directory.
            script = self._adapt_script(preinst, base)
            if sys.platform == "darwin":
                script = script.replace(GNU_STAT_LINE, "printf '750 root wheel\\n'")
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            # The unpack in between could wipe the directory; emulate it.
            shutil.rmtree(obs_dir)
            obs_dir.mkdir(parents=True)
            os.chmod(obs_dir, 0o700)  # worst case: unpack leaves 0700 behind

            # Upgrade step 2: postinst restores snapshot and directory mode.
            script = self._adapt_script(postinst, base, stub_systemctl=True)
            result = subprocess.run(["/bin/sh", "-c", script], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

            # Config survived and the operator's mode was restored, not reset.
            self.assertEqual((obs_dir / "alertmanager.yml").read_text(), "operator-managed-config")
            self.assertEqual(stat.S_IMODE(obs_dir.stat().st_mode), live_mode)

    def test_alertmanager_exec_start_pre_blocks_placeholder_receiver(self):
        """Alertmanager ExecStartPre must abort when receiver.enabled points to 127.0.0.1 placeholder."""
        exec_start_pre = extract_exec_start_pre(self.service_source)
        with tempfile.TemporaryDirectory() as temp_dir:
            base = Path(temp_dir)
            obs_dir = base / "etc/urmail/observability"
            obs_dir.mkdir(parents=True)
            cfg_path = obs_dir / "alertmanager.yml"

            cmd = exec_start_pre.replace("/etc/urmail/observability", str(obs_dir))

            # Case 1: Stock alertmanager.yml with 127.0.0.1 placeholder
            cfg_path.write_text((ROOT / "packaging/observability/alertmanager.yml").read_text())
            res = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            self.assertNotEqual(res.returncode, 0)
            self.assertIn("127.0.0.1", res.stderr)

            # Case 2: Config with localhost placeholder
            cfg_path.write_text("route:\n  receiver: test\nreceivers:\n  - name: test\n    webhook_configs:\n      - url: http://localhost:9099/alerts\n")
            res = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            self.assertNotEqual(res.returncode, 0)

            # Case 3: Missing alertmanager.yml
            cfg_path.unlink()
            res = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            self.assertNotEqual(res.returncode, 0)
            self.assertIn("missing", res.stderr)

            # Case 4: Properly configured external receiver
            cfg_path.write_text("""# External alertmanager receiver
route:
  receiver: ops-webhook
receivers:
  - name: ops-webhook
    webhook_configs:
      - url: https://alerts.ops.example.com/webhook
        send_resolved: true
""")
            res = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            self.assertEqual(res.returncode, 0, f"Expected success but got: {res.stderr}")

            # Case 5: External receiver with 127.0.0.1 in a comment
            cfg_path.write_text("""# Replaced 127.0.0.1 test hook with production endpoint
route:
  receiver: ops-webhook
receivers:
  - name: ops-webhook
    webhook_configs:
      - url: https://alerts.ops.example.com/webhook
        send_resolved: true
""")
            res = subprocess.run(cmd, shell=True, capture_output=True, text=True)
            self.assertEqual(res.returncode, 0, f"Expected success with comment but got: {res.stderr}")

    def _adapt_script(self, script: str, base: Path, stub_systemctl: bool = False) -> str:
        """Isolate script execution to a temporary root directory."""
        prelude = [
            "set -e",
            """install() {
  args=""
  skip=0
  for a in "$@"; do
    if [ "$skip" -gt 0 ]; then
      skip=$((skip - 1))
      continue
    fi
    if [ "$a" = "-o" ] || [ "$a" = "-g" ]; then
      skip=1
      continue
    fi
    args="$args \"$a\""
  done
  eval "command install $args"
}""",
        ]
        if stub_systemctl:
            prelude.extend([
                "getent() { return 0; }",
                "addgroup() { return 0; }",
                "adduser() { return 0; }",
                "systemctl() { return 0; }",
                "chown() { return 0; }",
            ])
        body = script
        paths = sorted([
            "/var/lib/urmail-config-migration",
            "/usr/share/urmail",
            "/etc/urmail",
            "/etc/opendkim",
            "/etc/nginx",
            "/etc/dovecot",
            "/etc/nsd",
            "/etc/postfix",
            "/etc/systemd",
            "/var/lib/urmail-acme",
            "/var/lib/urmail-agent",
            "/var/lib/urmail",
            "/var/lib/nsd",
            "/etc/letsencrypt",
            "/var/log/letsencrypt",
        ], key=len, reverse=True)

        for p in paths:
            target = str(base / p.lstrip("/"))
            body = re.sub(r'(?<![/\\w])' + re.escape(p) + r'(?![-w])', target, body)

        return "\n".join(prelude) + "\n" + body


if __name__ == "__main__":
    unittest.main(verbosity=2)
