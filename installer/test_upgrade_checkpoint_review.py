#!/usr/bin/env python3
"""Review regressions using temporary roots only; never touch installed state."""
import os
from pathlib import Path
import unittest
from unittest import mock

import test_upgrade_checkpoint as fixtures

cp = fixtures.cp


SECONDARY = "/var/lib/nsd/urmail-secondary"


class ProductionLayoutReviewTests(unittest.TestCase):
    setUp = fixtures.CheckpointTests.setUp
    put = fixtures.CheckpointTests.put
    create = fixtures.CheckpointTests.create

    def nsd_owned_lstat(self):
        """Model Ubuntu nsd ownership without privileged chown or host writes."""
        original = Path.lstat
        nsd = self.helper.path("/var/lib/nsd")
        nsd.mkdir(parents=True, exist_ok=True)
        nsd.chmod(0o755)

        def observed(path, *args, **kwargs):
            info = original(path, *args, **kwargs)
            if path == nsd or path == nsd / "urmail-secondary":
                values = list(info)
                values[4] = os.geteuid() + 1  # uid=nsd, not root
                return os.stat_result(values)
            return info

        return mock.patch.object(Path, "lstat", observed)

    def test_allowlisting_secondary_rejects_stock_nsd_parent_even_when_child_absent(self):
        # archive() validates parents before lexists(): no secondary role needed.
        roots = tuple(dict.fromkeys((*cp.ROOTS, SECONDARY)))
        with self.nsd_owned_lstat(), mock.patch.object(cp, "ROOTS", roots):
            self.assertFalse(self.helper.path(SECONDARY).exists())
            with self.assertRaisesRegex(ValueError, "unsafe directory: .*var/lib/nsd$"):
                self.create()
        self.assertFalse((self.directory / "COMPLETE").exists())

    def test_excluding_secondary_does_not_inspect_stock_nsd_parent(self):
        roots = tuple(root for root in cp.ROOTS if root != SECONDARY)
        with self.nsd_owned_lstat(), mock.patch.object(cp, "ROOTS", roots):
            self.create()
            manifest = self.helper.verify(self.name)
        self.assertNotIn(SECONDARY, manifest["roots"])
        self.assertFalse(any(item["path"].startswith("/var/lib/nsd/")
                             for item in manifest["inventory"]))

    def test_root_owned_daemon_group_readable_state_is_accepted(self):
        # The predicate checks UID and writable mode bits, not GID==root.
        # This models root:mailinabox agent state and root:dovecot config.
        target = self.put("/etc/dovecot/conf.d/urmail/db.conf", b"secret", 0o640)
        target.parent.chmod(0o750)
        original = Path.lstat

        def observed(path, *args, **kwargs):
            info = original(path, *args, **kwargs)
            values = list(info)
            values[5] = info.st_gid + 100  # daemon group, root UID unchanged
            return os.stat_result(values)

        with mock.patch.object(cp, "ROOTS", tuple(root for root in cp.ROOTS if root != SECONDARY)), mock.patch.object(Path, "lstat", observed):
            self.create()
            self.helper.verify(self.name)


if __name__ == "__main__":
    unittest.main()
