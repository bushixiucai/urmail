#!/usr/bin/env python3
"""Controlled fixtures only; never invokes copied PHP/occ or Docker."""
import importlib.util
import json
import os
from pathlib import Path
import sqlite3
import tempfile
import unittest

SPEC = importlib.util.spec_from_file_location('nc_isolation', Path(__file__).with_name('isolation.py'))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class IsolationTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.addCleanup(self.temporary.cleanup)
        self.root = Path(self.temporary.name)
        self.code = self.root / 'source-code'
        self.data = self.root / 'private-data'
        self.socket = self.root / 'private-pg'
        for directory in [self.code, self.data, self.socket]:
            directory.mkdir(mode=0o700)
        (self.code / 'config').mkdir()
        (self.code / 'version.php').write_text("<?php $OC_VersionString = '34.0.5';")
        (self.code / 'occ').write_text('<?php throw new Exception("Host must never execute this");')
        (self.code / 'config' / 'config.php').symlink_to('/home/user-data/owncloud/config.php')
        (self.data / 'config.php').write_text('<?php $CONFIG = ["secret"=>"private-fixture-secret"];')
        with sqlite3.connect(self.data / 'owncloud.db') as connection:
            connection.execute('CREATE TABLE oc_users(uid TEXT PRIMARY KEY,displayname TEXT)')
            connection.execute("INSERT INTO oc_users VALUES('stable-fixture-uid','Fixture')")
        self.output = self.root / 'bundle'
        self.image = 'nextcloud@sha256:' + 'a' * 64

    def prepare(self):
        return MODULE.prepare(self.code, self.data, self.output, self.image, 'urmail_owned_fixture', self.socket)

    def test_private_absolute_mapping_and_no_execution(self):
        result = self.prepare()
        self.assertEqual(result['conversion'], 'NOT_RUN')
        self.assertEqual(result['state'], 'PREPARED_NOT_EXECUTABLE')
        self.assertNotIn('private-fixture-secret', json.dumps(result))
        self.assertEqual(os.readlink(self.output / 'code/config/config.php'), '/home/user-data/owncloud/config.php')
        plan = json.loads((self.output / 'plan.json').read_text())
        command = plan['trusted_argv']
        self.assertEqual(command[command.index('--network') + 1], 'none')
        self.assertNotIn('--clear-schema', command)
        self.assertFalse(any(value.startswith('--password') for value in command))
        self.assertIn('--all-apps', command)
        self.assertEqual(command[-4:], ['pgsql', MODULE.ROLE, '/run/urmail-pg', 'urmail_owned_fixture'])
        self.assertEqual(plan['table_coverage'], 'BLOCKED_TABLE_COVERAGE_NOT_PROVEN')
        self.assertEqual((self.output / 'plan.json').stat().st_mode & 0o777, 0o600)
        self.assertNotIn('private-fixture-secret', (self.output / 'plan.json').read_text())

    def test_unmapped_absolute_and_escaping_relative_links_rejected(self):
        for target in ['/etc/passwd', '../../production/config.php']:
            link = self.code / 'unsafe'
            link.symlink_to(target)
            with self.assertRaises(MODULE.PreparationError):
                self.prepare()
            link.unlink()
        self.assertFalse(self.output.exists())

    def test_relative_link_cannot_follow_an_absolute_mapping_on_host(self):
        (self.code / 'alias').symlink_to('config/config.php')
        with self.assertRaisesRegex(MODULE.PreparationError, 'CHAINED_SOURCE_SYMLINK'):
            self.prepare()

    def test_special_files_and_mutable_permissions_rejected(self):
        fifo = self.data / 'fifo'
        os.mkfifo(fifo)
        with self.assertRaisesRegex(MODULE.PreparationError, 'SPECIAL_SOURCE_ENTRY'):
            self.prepare()
        fifo.unlink()
        (self.data / 'config.php').chmod(0o666)
        with self.assertRaisesRegex(MODULE.PreparationError, 'WRITABLE_SOURCE_ENTRY'):
            self.prepare()

    def test_nonempty_output_never_deleted(self):
        self.output.mkdir()
        sentinel = self.output / 'existing'
        sentinel.write_text('preserve')
        with self.assertRaises(MODULE.PreparationError):
            self.prepare()
        self.assertEqual(sentinel.read_text(), 'preserve')

    def test_nonprivate_root_wrong_version_and_wal_rejected(self):
        self.code.chmod(0o755)
        with self.assertRaises(MODULE.PreparationError):
            self.prepare()
        self.code.chmod(0o700)
        (self.code / 'version.php').write_text("<?php $OC_VersionString='35.0.0';")
        with self.assertRaisesRegex(MODULE.PreparationError, 'EXACT_NEXTCLOUD_VERSION'):
            self.prepare()
        (self.code / 'version.php').write_text("<?php $OC_VersionString='34.0.5';")
        (self.data / 'owncloud.db-wal').write_bytes(b'pending changes')
        with self.assertRaisesRegex(MODULE.PreparationError, 'CONSISTENT_SQLITE'):
            self.prepare()

    def test_removed_app_tables_and_wrong_binding_block_coverage(self):
        with self.assertRaisesRegex(MODULE.PreparationError, 'REMOVED_APP_TABLES'):
            MODULE.validate_coverage({'code_sha256':'code','sqlite_sha256':'db','source_tables':['oc_users','oc_removedapp'],'covered_tables':['oc_users']}, 'code','db')
        with self.assertRaisesRegex(MODULE.PreparationError, 'BINDING_MISMATCH'):
            MODULE.validate_coverage({'code_sha256':'wrong'}, 'code','db')

    def test_socket_mount_cannot_expose_other_private_application_files(self):
        (self.socket / 'platform-config.php').write_text('not a socket')
        with self.assertRaisesRegex(MODULE.PreparationError, 'DEDICATED_POSTGRES_SOCKET'):
            self.prepare()
        self.assertFalse(self.output.exists())

    def test_pinned_image_and_same_database_identifier_enforced(self):
        self.image = 'nextcloud:latest'
        with self.assertRaisesRegex(MODULE.PreparationError, 'PINNED_NEXTCLOUD_IMAGE'):
            self.prepare()
        self.assertFalse(self.output.exists())
        with self.assertRaises(MODULE.PreparationError):
            MODULE.runtime_command(self.code, 'nextcloud@sha256:'+'a'*64, 'urmail;DROP DATABASE', self.socket)


if __name__ == '__main__':
    unittest.main()
