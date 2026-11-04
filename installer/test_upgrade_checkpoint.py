#!/usr/bin/env python3
"""Unprivileged sandbox tests; production CLI has no sandbox bypass."""
import importlib.util
import json
import os
from pathlib import Path
import stat
import tarfile
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location("checkpoint", Path(__file__).with_name("upgrade-checkpoint.py"))
cp = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(cp)


class CheckpointTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.helper = cp.Checkpoint(self.root, os.geteuid())
        self.name = cp.BASE + "/config.test"
        self.directory = self.helper.path(self.name)
        self.directory.mkdir(parents=True, mode=0o700)
        self.directory.chmod(0o700)
        env = b"URMAIL_MASTER_KEY_PATH='/secure/active.key'\nURMAIL_IGNORED=$(touch /tmp/DO_NOT_EXECUTE)\n"
        for path, raw in {
            "/etc/urmail/urmaild.env": env,
            "/etc/urmail/agent.env": b"URMAIL_ACME_SERVER=secret\n",
            "/secure/active.key": b"active-secret-key",
            cp.MAINT + "/upgrade.hold": json.dumps({"version": 1, "created_at": "2026-11-03T00:00:00Z"}).encode(),
            cp.MAINT + "/upgrade_receipt.json": json.dumps({"version": 1, "created_at": "2026-11-03T00:00:00Z", "upgrade_held": True}).encode(),
            "/var/lib/urmail-agent/dns-security/example.test/key.private": b"dnssec-secret",
            "/etc/nsd/zones/example.test.signed": b"signed-zone",
            "/var/mail/EXCLUDED": b"mail-payload",
            "/var/lib/postgresql/EXCLUDED": b"database-payload",
        }.items():
            self.put(path, raw)
        for binary in cp.BINARIES:
            self.put(binary, b"old executable", 0o755)
        for name in ("urmaild.env", "agent.env"):
            self.put(self.name + "/" + name, self.helper.path("/etc/urmail/" + name).read_bytes())
        self.put(self.name + "/database.dump", b"PGDMP-fixture")

    def put(self, name, raw, mode=0o600):
        path = self.helper.path(name)
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_bytes(raw)
        path.chmod(mode)
        return path

    def create(self):
        self.helper.create(self.name, lambda: b"urmail\t0.9.0\tarm64\tinstalled\npostfix\t3\tarm64\tinstalled\n",
                           lambda unit: "inactive")

    def reject_create(self):
        with self.assertRaises((ValueError, OSError)):
            self.create()
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_create_verify_contents_permissions_and_exclusions(self):
        self.create()
        manifest = self.helper.verify(self.name)
        self.assertEqual((self.directory / "master.key").read_bytes(), b"active-secret-key")
        self.assertEqual(manifest["master_key_source"], "/secure/active.key")
        self.assertIn("Not complete package/OS rollback", manifest["rollback_scope"])
        with tarfile.open(self.directory / "state.tar") as archive:
            names = archive.getnames()
            self.assertIn("var/lib/urmail-agent/dns-security/example.test/key.private", names)
            self.assertIn("usr/lib/urmail/archive-pipe", names)
            self.assertIn("etc/nsd/zones/example.test.signed", names)
            self.assertFalse(any("EXCLUDED" in name for name in names))
            self.assertTrue(all(not name.startswith("/") and ".." not in Path(name).parts for name in names))
        for path in self.directory.iterdir():
            self.assertEqual(stat.S_IMODE(path.stat().st_mode), 0o600)

    def test_partial_and_missing_marker_rejected(self):
        with self.assertRaises(ValueError):
            self.helper.verify(self.name)
        self.create()
        (self.directory / "COMPLETE").unlink()
        with self.assertRaises(ValueError):
            self.helper.verify(self.name)
        self.reject_create()

    def test_empty_dump_rejected(self):
        (self.directory / "database.dump").write_bytes(b"")
        self.reject_create()

    def test_corrupt_artifact_rejected(self):
        self.create()
        (self.directory / "database.dump").write_bytes(b"corrupt")
        with self.assertRaisesRegex(ValueError, "mismatch"):
            self.helper.verify(self.name)

    def test_manifest_and_marker_corruption_rejected(self):
        self.create()
        (self.directory / "manifest.json").write_bytes(b"{}")
        with self.assertRaisesRegex(ValueError, "marker"):
            self.helper.verify(self.name)

    def test_mode_change_rejected(self):
        self.create()
        (self.directory / "master.key").chmod(0o640)
        with self.assertRaises(ValueError):
            self.helper.verify(self.name)

    def test_current_receipt_and_hold_must_remain_exact(self):
        self.create()
        for filename in ("upgrade_receipt.json", "upgrade.hold"):
            path = self.helper.path(cp.MAINT + "/" + filename)
            previous = path.read_bytes()
            path.write_bytes(previous + b" ")
            with self.assertRaisesRegex(ValueError, "another upgrade"):
                self.helper.verify(self.name)
            path.write_bytes(previous)
        self.helper.path(cp.MAINT + "/upgrade.hold").unlink()
        with self.assertRaises(OSError):
            self.helper.verify(self.name)

    def test_receipt_false_or_mismatched_timestamp_rejected(self):
        path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        path.write_text('{"version":1,"upgrade_held":false,"created_at":"other"}')
        self.reject_create()

    def test_external_symlink_saved_without_dereference(self):
        link = self.helper.path("/var/lib/urmail-agent/external")
        link.symlink_to("/etc/shadow")
        self.create()
        with tarfile.open(self.directory / "state.tar") as archive:
            member = archive.getmember("var/lib/urmail-agent/external")
            self.assertTrue(member.issym())
            self.assertEqual(member.linkname, "/etc/shadow")
            self.assertNotIn("etc/shadow", archive.getnames())

    def test_parent_symlink_escape_rejected_even_for_missing_root(self):
        self.helper.path("/etc/dovecot").symlink_to(self.root / "nonexistent")
        self.reject_create()

    def test_key_symlink_rejected(self):
        key = self.helper.path("/secure/active.key")
        key.unlink()
        key.symlink_to("/etc/shadow")
        self.reject_create()

    def test_key_parent_symlink_rejected(self):
        self.helper.path("/secure").rename(self.helper.path("/real-secure"))
        self.helper.path("/secure").symlink_to(self.helper.path("/real-secure"))
        self.reject_create()

    def test_receipt_symlink_rejected(self):
        path = self.helper.path(cp.MAINT + "/upgrade_receipt.json")
        raw = path.read_bytes()
        path.unlink()
        path.symlink_to(self.put("/alternate", raw))
        self.reject_create()

    def test_input_hardlink_rejected(self):
        os.link(self.directory / "database.dump", self.root / "dump-link")
        self.reject_create()

    def test_group_writable_directory_rejected(self):
        self.helper.path("/var/lib/urmail-agent").chmod(0o770)
        self.reject_create()

    def test_backup_mode_and_paths_rejected(self):
        self.directory.chmod(0o750)
        self.reject_create()
        for name in ("/tmp/test", cp.BASE + "/config.test/../config.test", cp.BASE + "/config.test/child", "relative"):
            with self.assertRaises(ValueError):
                self.helper.backup(name)

    def test_owner_mismatch_rejected(self):
        self.helper.owner = os.geteuid() + 1
        self.reject_create()

    def test_fence_change_during_capture_leaves_no_marker(self):
        def query():
            self.helper.path(cp.MAINT + "/upgrade.hold").write_bytes(b"{}")
            return b"urmail\t1\tarm64\tinstalled\n"
        with self.assertRaises(ValueError):
            self.helper.create(self.name, query, lambda unit: "inactive")
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_cli_requires_root_no_environment_test_bypass(self):
        with mock.patch.object(cp.os, "geteuid", return_value=123), mock.patch.dict(os.environ, {"URMAIL_TEST_ROOT": str(self.root)}):
            with self.assertRaises(SystemExit) as result:
                cp.main(["verify", self.name])
        self.assertEqual(result.exception.code, 1)

    def test_separate_quoted_agent_master_key_is_captured(self):
        raw = b"URMAIL_MASTER_KEY_PATH=\"/secure/agent.key\"\n"
        self.put("/etc/urmail/agent.env", raw)
        self.put(self.name + "/agent.env", raw)
        self.put("/secure/agent.key", b"agent-key")
        self.create()
        manifest = self.helper.verify(self.name)
        self.assertEqual((self.directory / "agent-master.key").read_bytes(), b"agent-key")
        self.assertEqual(manifest["agent_master_key_source"], "/secure/agent.key")

    def test_saved_artifact_symlink_rejected(self):
        self.create()
        target = self.directory / "database.dump"
        target.unlink()
        target.symlink_to(self.helper.path("/secure/active.key"))
        with self.assertRaises(OSError):
            self.helper.verify(self.name)

    def test_backup_directory_symlink_rejected(self):
        actual = self.directory.with_name("real")
        self.directory.rename(actual)
        self.directory.symlink_to(actual)
        self.reject_create()

    def test_failed_final_sync_does_not_report_success(self):
        with mock.patch.object(self.helper, "syncdir", side_effect=OSError("power failure")):
            with self.assertRaises(OSError):
                self.create()
        self.assertFalse((self.directory / "COMPLETE").exists())


if __name__ == "__main__":
    unittest.main()
