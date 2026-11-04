#!/usr/bin/env python3
"""Test installer upgrade fence lifecycle, checkpoint ordering, and failure retention."""
from pathlib import Path
import re
import unittest

SOURCE = Path(__file__).resolve().parents[1] / "installer/install.sh"


class InstallerUpgradeFenceTests(unittest.TestCase):
    def setUp(self):
        self.script = SOURCE.read_text()

    def test_upgrade_fence_and_checkpoint_before_apt_and_dpkg(self):
        # Verify fence creation happens before apt-get update and dpkg -i
        fence_idx = self.script.index('"$NEW_URMAILCTL" upgrade fence')
        apt_idx = self.script.index('apt-get update -qq')
        dpkg_idx = self.script.index('dpkg -i "$TMP_DEB"')
        self.assertLess(fence_idx, apt_idx, "Upgrade fence must be established BEFORE apt-get")
        self.assertLess(apt_idx, dpkg_idx, "apt-get must precede dpkg -i")
        dump_idx = self.script.index('os.fsync(output.fileno())')
        self.assertLess(dump_idx, apt_idx, "Durable dump must precede dependency mutation")

    def test_daemons_stopped_before_checkpoint(self):
        # Verify urmaild and agent stopped before CHECKPOINT and pg_dump
        # The typed fencing CLI stops and verifies every allowlisted writer,
        # including daemon and agent; no separate pre-fence stop loses receipt state.
        stop_idx = self.script.index('"$NEW_URMAILCTL" upgrade fence')
        checkpoint_idx = self.script.index('"-v", "ON_ERROR_STOP=1", "-c", "CHECKPOINT"')
        self.assertIn('"-h", "/var/run/postgresql", "-p", "5432"', self.script)
        dump_idx = self.script.index('python3 - "$BACKUP_DIR/urmaild.env" "$BACKUP_DIR/database.dump"')
        self.assertLess(stop_idx, checkpoint_idx, "systemctl stop urmaild must happen BEFORE CHECKPOINT")
        self.assertLess(checkpoint_idx, dump_idx, "CHECKPOINT must happen BEFORE database dump")

    def test_cli_extracted_from_deb(self):
        extract_idx = self.script.index('dpkg-deb -x "$TMP_DEB" "$CLI_EXTRACT_DIR"')
        fence_idx = self.script.index('"$NEW_URMAILCTL" upgrade fence')
        self.assertLess(extract_idx, fence_idx, "CLI extraction must happen BEFORE upgrade fence")

    def test_failure_trap_retains_fence(self):
        self.assertIn("cleanup_installer()", self.script)
        self.assertIn("UPGRADE FENCE RETAINED", self.script)
        self.assertIn("trap cleanup_installer EXIT INT TERM", self.script)
        self.assertIn("manual risk acceptance is a separate operator decision", self.script)

    def test_upgrade_retains_hold_for_phase_b(self):
        # On upgrade, fence must remain held and NOT automatically released
        self.assertIn('IS_UPGRADE:-0', self.script)
        self.assertIn('IS_UPGRADE=1', self.script)
        self.assertIn('UPGRADE PREPARATION & MIGRATIONS APPLIED', self.script)
        self.assertIn('Upgrade fence (/var/lib/urmail-backup/upgrade.hold) remains HELD.', self.script)
        self.assertIn('urmailctl config prepare || die', self.script)
        self.assertIn('exit 2', self.script)
        self.assertNotIn('urmailctl config prepare || warn', self.script)

    def test_no_database_dump_after_dpkg(self):
        dpkg_idx = self.script.index('dpkg -i "$TMP_DEB"')
        # Ensure there is no pg_dump after dpkg -i
        self.assertNotIn('python3 - "$BACKUP_DIR/urmaild.env" "$BACKUP_DIR/database.dump"', self.script[dpkg_idx:])


if __name__ == "__main__":
    unittest.main(verbosity=2)
