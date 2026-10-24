import copy
import importlib.util
from pathlib import Path
import unittest

SPEC = importlib.util.spec_from_file_location('continuity', Path(__file__).with_name('continuity.py'))
MODULE = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(MODULE)


class ContinuityTests(unittest.TestCase):
    def setUp(self):
        self.source = {
            'oc_users': {'columns': [('uid', 'text')], 'rows': [['stable-uid'], ['second-uid']]},
            'oc_calendars': {'columns': [('id', 'integer'), ('principaluri', 'text')], 'rows': [[17, 'principals/users/stable-uid']]},
            'oc_cards': {'columns': [('addressbookid', 'integer'), ('carddata', 'text')], 'rows': [[23, 'BEGIN:VCARD\r\nFN:Fixture\r\nEND:VCARD']]},
            'oc_calendarobjects': {'columns': [('calendarid', 'integer'), ('calendardata', 'text')], 'rows': [[17, 'BEGIN:VCALENDAR']]},
            'oc_filecache': {'columns': [('fileid', 'integer'), ('storage', 'integer'), ('path', 'text')], 'rows': [[31, 37, 'stable-uid/files/test.ics']]},
        }

    def test_preserved_identity_dav_and_file_relationships_match(self):
        target = copy.deepcopy(self.source)
        target['oc_users']['rows'].reverse()
        target['oc_calendars']['rows'][0][0] = '17'
        self.assertEqual(MODULE.compare_snapshots(self.source, target)['tables_checked'], 5)

    def test_same_row_count_cannot_hide_uid_principal_body_or_fileid_changes(self):
        for table, column in [('oc_users', 0), ('oc_calendars', 1), ('oc_cards', 1), ('oc_calendarobjects', 0), ('oc_filecache', 1)]:
            target = copy.deepcopy(self.source)
            target[table]['rows'][0][column] = '999' if table in ('oc_filecache', 'oc_calendarobjects') else 'different'
            with self.assertRaisesRegex(MODULE.ContinuityError, 'SEMANTIC_ROW_MISMATCH'):
                MODULE.compare_snapshots(self.source, target)

    def test_removed_app_and_unreviewed_target_tables_block(self):
        target = copy.deepcopy(self.source)
        del target['oc_cards']
        with self.assertRaisesRegex(MODULE.ContinuityError, 'SOURCE_TABLE_MISSING'):
            MODULE.compare_snapshots(self.source, target)
        target = copy.deepcopy(self.source)
        target['oc_unreviewed'] = target['oc_users']
        with self.assertRaisesRegex(MODULE.ContinuityError, 'UNREVIEWED_TARGET_TABLE'):
            MODULE.compare_snapshots(self.source, target)

    def test_uid_and_business_tables_cannot_be_excluded(self):
        with self.assertRaisesRegex(MODULE.ContinuityError, 'UNAPPROVED_SEMANTIC_EXCEPTION'):
            MODULE.compare_snapshots(self.source, {}, {'oc_users': 'Do not permit bypassing preserved identity comparison'})

    def test_no_guessing_and_no_row_data_in_errors(self):
        with self.assertRaisesRegex(MODULE.ContinuityError, 'UNSUPPORTED_COLUMN_REPRESENTATION') as raised:
            MODULE.table_fingerprint([('uid', 'text')], [[b'private-row']])
        self.assertNotIn('private-row', str(raised.exception))
        before = MODULE.table_fingerprint([('enabled', 'boolean')], [[1]])
        after = MODULE.table_fingerprint([('enabled', 'boolean')], [[True]])
        self.assertEqual(before, after)


if __name__ == '__main__':
    unittest.main()
