#!/usr/bin/env python3
"""Real root-env merge regression tests; no service or root filesystem writes."""
import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest import mock

spec = importlib.util.spec_from_file_location("configure_agent_env", Path(__file__).with_name("configure-agent-env.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


def daemon_values():
    return {**{key: "postgres://managed/urmail" for key in module.DATABASE_KEYS},
            "URMAIL_POLICY_ENABLED": "true", "URMAIL_WEBMAIL_NATIVE": "false",
            "URMAIL_CLOUDFLARE_TOKEN": "", "URMAIL_STORAGE_ROOT": "/srv/mail"}


def document(values):
    return "".join(key + "=" + value + "\n" for key, value in values.items())


class AgentEnvironmentTests(unittest.TestCase):
    def test_preserves_arbitrary_root_secrets_and_operator_values(self):
        old = {"URMAIL_DNS_PROVIDER": "cloudflare", "URMAIL_CLOUDFLARE_TOKEN": "root-secret",
               "URMAIL_STORAGE_ROOT": "/srv/root-mail", "URMAIL_MASTER_KEY_PATH": "/secure/root.key",
               "URMAIL_METRICS_TOKEN": "telemetry-secret", "URMAIL_DB_AI_MAX_CONNS": "1",
               "URMAIL_BACKUP_S3_SECRET_KEY": "backup-secret", "URMAIL_FUTURE_ROOT_SECRET": "$(never-execute)"}
        merged = module.merge(daemon_values(), old, {})
        for key, value in old.items():
            self.assertEqual(merged[key], value)

    def test_shared_database_and_all_deployment_flags_follow_daemon(self):
        daemon = daemon_values()
        daemon.update({key: "false" for key in module.DEPLOYMENT_KEYS})
        daemon["URMAIL_POLICY_ENABLED"] = "true"
        previous = {key: "stale" for key in module.SHARED_KEYS}
        overrides = dict(previous)
        merged = module.merge(daemon, previous, overrides)
        for key in module.SHARED_KEYS:
            self.assertEqual(merged[key], daemon[key])
        daemon.pop("URMAIL_WORKERS_HELD")
        self.assertNotIn("URMAIL_WORKERS_HELD", module.merge(daemon, previous, overrides))

    def test_explicit_root_override_and_new_key(self):
        merged = module.merge(daemon_values(), {"URMAIL_ACME_SERVER": "old"},
                              {"URMAIL_ACME_SERVER": "new", "URMAIL_NEW_ROOT_SECRET": "new-secret",
                               "URMAIL_CLOUDFLARE_TOKEN": "", "PATH": "ignored"})
        self.assertEqual(merged["URMAIL_ACME_SERVER"], "new")
        self.assertEqual(merged["URMAIL_NEW_ROOT_SECRET"], "new-secret")
        self.assertEqual(merged["URMAIL_CLOUDFLARE_TOKEN"], "")
        self.assertNotIn("PATH", merged)

    def test_cli_private_atomic_repeatable_and_daemon_unchanged(self):
        with tempfile.TemporaryDirectory() as directory:
            daemon, agent = Path(directory) / "daemon.env", Path(directory) / "agent.env"
            daemon.write_text(document(daemon_values()))
            before = daemon.read_bytes()
            agent.write_text("URMAIL_FUTURE_ROOT_SECRET=root-only\n")
            command = [sys.executable, str(Path(module.__file__)), str(daemon), str(agent)]
            subprocess.run(command, env={}, check=True, capture_output=True)
            first = agent.read_bytes()
            subprocess.run(command, env={}, check=True, capture_output=True)
            self.assertEqual(first, agent.read_bytes())
            self.assertEqual(agent.stat().st_mode & 0o777, 0o600)
            self.assertEqual(before, daemon.read_bytes())
            self.assertNotIn(b"root-only", daemon.read_bytes())

    def test_invalid_override_or_missing_database_preserves_old_file(self):
        with tempfile.TemporaryDirectory() as directory:
            daemon, agent = Path(directory) / "daemon.env", Path(directory) / "agent.env"
            daemon.write_text(document(daemon_values()))
            agent.write_text("URMAIL_FUTURE_ROOT_SECRET=original\n")
            old = agent.read_bytes()
            with self.assertRaises(ValueError):
                module.write(daemon, agent, {"URMAIL_NEW_SECRET": "bad\nURMAIL_EVIL=true"})
            self.assertEqual(old, agent.read_bytes())
            daemon.write_text("URMAIL_POLICY_ENABLED=true\n")
            with self.assertRaises(ValueError):
                module.write(daemon, agent, {})
            self.assertEqual(old, agent.read_bytes())

    def test_replace_failure_cleans_temporary_and_preserves_old_file(self):
        with tempfile.TemporaryDirectory() as directory:
            daemon, agent = Path(directory) / "daemon.env", Path(directory) / "agent.env"
            daemon.write_text(document(daemon_values()))
            agent.write_text("URMAIL_FUTURE_ROOT_SECRET=original\n")
            old = agent.read_bytes()
            with mock.patch.object(module.env.os, "replace", side_effect=OSError("injected failure")):
                with self.assertRaises(OSError):
                    module.write(daemon, agent, {})
            self.assertEqual(old, agent.read_bytes())
            self.assertEqual(set(Path(directory).iterdir()), {daemon, agent})

    def test_rejects_alias_and_symlink_without_writing_daemon(self):
        with tempfile.TemporaryDirectory() as directory:
            daemon, agent = Path(directory) / "daemon.env", Path(directory) / "agent.env"
            daemon.write_text(document(daemon_values()))
            before = daemon.read_bytes()
            with self.assertRaises(ValueError):
                module.write(daemon, daemon, {})
            agent.symlink_to(daemon)
            with self.assertRaises(ValueError):
                module.write(daemon, agent, {})
            self.assertEqual(before, daemon.read_bytes())


if __name__ == "__main__":
    unittest.main()
