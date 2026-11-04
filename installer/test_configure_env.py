#!/usr/bin/env python3
"""Exercise upgrade preservation and failed-write safety for installer settings."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("configure_env", Path(__file__).with_name("configure-env.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)

DEFAULTS = """URMAIL_DATABASE_URL=postgres://new-managed
URMAIL_POLICY_ENABLED=true
URMAIL_WEBMAIL_NATIVE=true
URMAIL_CARDDAV_ENABLED=true
URMAIL_CALDAV_ENABLED=true
URMAIL_EAS_ENABLED=true
URMAIL_LOG_LEVEL=info
"""


class InstallerEnvironmentTests(unittest.TestCase):
    def test_new_install_has_enabled_interfaces_and_private_file(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "urmaild.env"
            module.write(target, DEFAULTS, {})
            values = module.parse(target.read_text())
            for key in module.BOOLEAN_KEYS - {"URMAIL_ARCHIVE_ENABLED"}:
                self.assertEqual(values[key], "true")
            self.assertEqual(target.stat().st_mode & 0o777, 0o600)

    def test_upgrade_and_repeated_install_preserve_secrets_and_flags(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "urmaild.env"
            target.write_text("URMAIL_DATABASE_URL=postgres://old\nURMAIL_MASTER_KEY_PATH=/secure/master.key\n"
                              "URMAIL_AI_API_KEY=$(touch NEVER_EXECUTE)\nURMAIL_WEBMAIL_NATIVE=false\n"
                              "URMAIL_CARDDAV_ENABLED=false\nURMAIL_LOG_LEVEL=debug\n"
                              "URMAIL_STORAGE_ROOT=/srv/mail\nURMAIL_DB_AI_MAX_CONNS=1\n"
                              "URMAIL_DNS_PROVIDER=cloudflare\nURMAIL_CLOUDFLARE_TOKEN=provider-secret\n"
                              "URMAIL_METRICS_TOKEN=telemetry-secret\n")
            module.write(target, DEFAULTS, {})
            first = target.read_bytes()
            module.write(target, DEFAULTS, {})
            self.assertEqual(first, target.read_bytes())
            values = module.parse(first.decode())
            self.assertEqual(values["URMAIL_DATABASE_URL"], "postgres://new-managed")
            self.assertEqual(values["URMAIL_MASTER_KEY_PATH"], "/secure/master.key")
            self.assertEqual(values["URMAIL_AI_API_KEY"], "$(touch NEVER_EXECUTE)")
            self.assertEqual(values["URMAIL_WEBMAIL_NATIVE"], "false")
            self.assertEqual(values["URMAIL_CARDDAV_ENABLED"], "false")
            self.assertEqual(values["URMAIL_LOG_LEVEL"], "debug")
            for key, expected in {"URMAIL_STORAGE_ROOT": "/srv/mail", "URMAIL_DB_AI_MAX_CONNS": "1",
                                  "URMAIL_DNS_PROVIDER": "cloudflare", "URMAIL_CLOUDFLARE_TOKEN": "provider-secret",
                                  "URMAIL_METRICS_TOKEN": "telemetry-secret"}.items():
                self.assertEqual(values[key], expected)
            self.assertEqual(list(Path(directory).iterdir()), [target])

    def test_legacy_absent_webmail_does_not_silently_enable(self):
        with tempfile.TemporaryDirectory() as directory:
            target = Path(directory) / "urmaild.env"
            target.write_text("URMAIL_LOG_LEVEL=debug\n")
            module.write(target, DEFAULTS, {})
            self.assertEqual(module.parse(target.read_text())["URMAIL_WEBMAIL_NATIVE"], "false")

    def test_explicit_operator_change_wins(self):
        values = module.merge({"URMAIL_EAS_ENABLED": "true"}, module.parse(DEFAULTS),
                              {"URMAIL_EAS_ENABLED": "false"})
        self.assertEqual(values["URMAIL_EAS_ENABLED"], "false")

    def test_invalid_values_leave_existing_file_unchanged(self):
        for overrides in ({"URMAIL_EAS_ENABLED": "invalid"},
                          {"URMAIL_LOG_LEVEL": "info\nURMAIL_EAS_ENABLED=true"},
                          {"URMAIL_POLICY_ENABLED": "false"}):
            with self.subTest(overrides=list(overrides)), tempfile.TemporaryDirectory() as directory:
                target = Path(directory) / "urmaild.env"
                old = "URMAIL_LOG_LEVEL=debug\n"
                target.write_text(old)
                with self.assertRaises(ValueError):
                    module.write(target, DEFAULTS, overrides)
                self.assertEqual(target.read_text(), old)


if __name__ == "__main__":
    unittest.main()
