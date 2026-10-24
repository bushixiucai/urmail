import datetime
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

SPEC = importlib.util.spec_from_file_location('container_entry', Path(__file__).with_name('container_entry.py'))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ExecutableGuardTests(unittest.TestCase):
    def test_host_is_rejected_before_any_copied_code_or_subprocess(self):
        with mock.patch.object(MODULE.os, 'getuid', return_value=501), mock.patch.object(MODULE.subprocess, 'run') as process:
            with self.assertRaisesRegex(MODULE.Blocked, 'CONTAINER_ROOT_REQUIRED'):
                MODULE.guard()
            process.assert_not_called()

    def test_host_namespace_marker_cannot_authorize_execution(self):
        marker = {'php_provenance':'original-v80-build-checkpoint-not-release-bundle','runtime_image_id':'sha256:'+'a'*64,'purpose':'urmail-nextcloud-v06-rehearsal-only', 'host_namespaces':{'mnt':'mnt:[123]','net':'net:[123]','pid':'pid:[123]'}}
        with mock.patch.object(MODULE.os, 'getuid', return_value=0), mock.patch.object(MODULE.Path, 'exists', return_value=True), mock.patch.object(MODULE.Path, 'read_text', return_value=json.dumps(marker)), mock.patch.object(MODULE.os, 'readlink', return_value='mnt:[123]'):
            with self.assertRaisesRegex(MODULE.Blocked, 'HOST_NAMESPACE_REJECTED'):
                MODULE.guard()

    def test_exact_absolute_link_mapping_never_reads_host_target(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root/'config').mkdir()
            (root/'config/config.php').symlink_to('/home/user-data/owncloud/config.php')
            facts = MODULE.tree_manifest(root, True)
            self.assertEqual(facts['config/config.php']['link'], '/home/user-data/owncloud/config.php')
            with self.assertRaisesRegex(MODULE.Blocked, 'UNMAPPED_SOURCE_LINK'):
                MODULE.tree_manifest(root, False)
            (root/'config/config.php').unlink()
            (root/'config/config.php').symlink_to('/etc/passwd')
            with self.assertRaisesRegex(MODULE.Blocked, 'UNMAPPED_SOURCE_LINK'):
                MODULE.tree_manifest(root, True)

    def test_full_typed_rows_detect_same_count_identity_or_binary_changes(self):
        before = MODULE.rows_digest([('stable-uid', b'body', 31)], ['text','bytea','int8'])
        equivalent = MODULE.rows_digest([('stable-uid', memoryview(b'body'), '31')], ['text','bytea','int8'])
        changed = MODULE.rows_digest([('other-uid', b'body', 31)], ['text','bytea','int8'])
        self.assertEqual(before, equivalent)
        self.assertNotEqual(before, changed)
        self.assertEqual(MODULE.canonical('body','bytea'), MODULE.canonical(b'body','bytea'))
        with self.assertRaisesRegex(MODULE.Blocked, 'BINARY_REPRESENTATION_INVALID'):
            MODULE.canonical(999999999,'bytea')

    def test_sequence_resync_preserves_deleted_and_already_allocated_highwaters(self):
        floor,original_next,collision = MODULE.sequence_floor(100,1,1,1,False,200)
        self.assertEqual((floor,original_next,collision),(200,1,True))
        self.assertEqual(MODULE.sequence_floor(100,1,1,300,True,200),(300,301,False))
        self.assertEqual(MODULE.sequence_floor(None,1,1,1,False,None),(0,1,False))
        self.assertEqual(MODULE.sequence_floor(100,1,1,100,True,100),(100,101,False))
        with self.assertRaisesRegex(MODULE.Blocked,'UNSUPPORTED_SEQUENCE_DIRECTION'):
            MODULE.sequence_floor(1,1,-1,1,True,None)

    def test_timestamp_conversion_uses_explicit_utc_semantics(self):
        before = MODULE.canonical('2026-10-24 12:00:00','timestamptz')
        after = MODULE.canonical(datetime.datetime(2026,10,24,12,tzinfo=datetime.timezone.utc),'timestamptz')
        self.assertEqual(before,after)

    def test_residual_type_map_and_unknown_missing_tables_block(self):
        self.assertEqual(MODULE.map_sqlite_column_type('VARCHAR(64)'), 'varchar(64)')
        self.assertEqual(MODULE.map_sqlite_column_type('CLOB'), 'text')
        self.assertEqual(MODULE.map_sqlite_column_type('BIGINT UNSIGNED'), 'bigint')
        self.assertEqual(MODULE.map_sqlite_column_type('SMALLINT'), 'smallint')
        with self.assertRaisesRegex(MODULE.Blocked, 'RESIDUAL_COLUMN_TYPE_UNSUPPORTED'):
            MODULE.map_sqlite_column_type('GEOMETRY')
        self.assertIn('oc_file_metadata', MODULE.REVIEWED_RESIDUAL_TABLES)
        self.assertIn('oc_group_folders', MODULE.REVIEWED_RESIDUAL_TABLES)
        self.assertEqual(MODULE.REVIEWED_MIGRATION_INJECTED_APPCONFIG, {
            ('core', 'default_encryption_module'),
            ('user_ldap', 'configuration_prefixes'),
        })

    def test_sqlite_column_map_preserves_historical_casing(self):
        class FakeSource:
            def execute(self, _sql):
                return [(0, 'remote_Url', 'TEXT', 0, None, 0), (1, 'token', 'TEXT', 0, None, 0)]
        mapping = MODULE.sqlite_column_map(FakeSource(), 'oc_calendars_federated')
        self.assertEqual(mapping, {'remote_url': 'remote_Url', 'token': 'token'})
        class Collision:
            def execute(self, _sql):
                return [(0, 'remote_Url', 'TEXT', 0, None, 0), (1, 'remote_url', 'TEXT', 0, None, 0)]
        with self.assertRaisesRegex(MODULE.Blocked, 'SQLITE_COLUMN_CASE_COLLISION'):
            MODULE.sqlite_column_map(Collision(), 'oc_calendars_federated')

    def test_json_canonical_matches_sqlite_text_and_pg_object(self):
        text = MODULE.canonical('{"b":2,"a":1}', 'json')
        obj = MODULE.canonical({'a': 1, 'b': 2}, 'jsonb')
        self.assertEqual(text, obj)
        self.assertEqual(text, ['json', '{"a":1,"b":2}'])

    def test_bpchar_strips_postgres_padding(self):
        self.assertEqual(MODULE.canonical('abc', 'bpchar'), MODULE.canonical('abc' + ' ' * 37, 'bpchar'))
        self.assertEqual(MODULE.canonical('deadbeef', 'char'), ['text', 'deadbeef'])


if __name__ == '__main__':
    unittest.main()
