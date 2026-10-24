"""Fixed, isolated read-only MIAB SQLite snapshot reader. Never import source code."""
import hashlib
import json
import os
import re
import sqlite3
import sys
import urllib.parse

LIMIT = 50000
EXPECTED = {
    'users': [('id', 'INTEGER', 0, 1), ('email', 'TEXT', 1, 0),
              ('password', 'TEXT', 1, 0), ('extra', '', 0, 0),
              ('privileges', 'TEXT', 1, 0), ('quota', 'TEXT', 1, 0)],
    'aliases': [('id', 'INTEGER', 0, 1), ('source', 'TEXT', 1, 0),
                ('destination', 'TEXT', 1, 0), ('permitted_senders', 'TEXT', 0, 0)],
    'auto_aliases': [('id', 'INTEGER', 0, 1), ('source', 'TEXT', 1, 0),
                     ('destination', 'TEXT', 1, 0), ('permitted_senders', 'TEXT', 0, 0)],
    'mfa': [('id', 'INTEGER', 0, 1), ('user_id', 'INTEGER', 1, 0),
            ('type', 'TEXT', 1, 0), ('secret', 'TEXT', 1, 0),
            ('mru_token', 'TEXT', 0, 0), ('label', 'TEXT', 0, 0)],
}


def main():
    # Do not use immutable: the read transaction must observe committed WAL.
    if any(os.path.lexists(sys.argv[1] + suffix) for suffix in ('-wal', '-shm', '-journal')):
        raise RuntimeError('source sidecars require a consistent snapshot')
    uri = 'file:' + urllib.parse.quote(os.path.abspath(sys.argv[1]), safe='/') + '?mode=ro'
    database = sqlite3.connect(uri, uri=True, timeout=5)
    try:
        if hasattr(database, "enable_load_extension"):
            database.enable_load_extension(False)
        database.execute('PRAGMA query_only=ON')
        database.execute('PRAGMA temp_store=MEMORY')
        database.execute('PRAGMA trusted_schema=OFF')
        database.execute('BEGIN')
        objects = database.execute("SELECT type,CASE WHEN length(name)<=128 THEN name ELSE '' END,CASE WHEN length(tbl_name)<=128 THEN tbl_name ELSE '' END,CASE WHEN length(sql)<=65536 THEN sql ELSE NULL END FROM sqlite_master ORDER BY type,name LIMIT 129").fetchall()
        if len(objects) > 128:
            print(json.dumps({'schema_known': False, 'object_limit_exceeded': True, 'counts': {}, 'users': [], 'aliases': [], 'mfa': []}))
            return
        known = True
        for kind, name, table, sql in objects:
            if kind == 'table' and name == 'sqlite_sequence':
                continue
            if kind == 'index' and name.startswith('sqlite_autoindex_') and table in EXPECTED:
                continue
            if kind != 'table' or name not in EXPECTED or not sql or re.search(r'\b(VIRTUAL|CHECK|COLLATE|GENERATED|WITHOUT)\b', sql, flags=re.IGNORECASE):
                known = False
        if not known:
            print(json.dumps({'schema_known': False, 'counts': {}, 'users': [], 'aliases': [], 'mfa': []}))
            return
        for table, expected in EXPECTED.items():
            # Identifiers are fixed package constants, not source/CLI strings.
            rows = database.execute('PRAGMA table_xinfo(' + table + ')').fetchall()
            actual = [(r[1], r[2].upper(), r[3], r[5]) for r in rows]
            if actual != expected or any(r[6] for r in rows):
                known = False
        for table in EXPECTED:
            rows = database.execute('PRAGMA table_info(' + table + ')').fetchall()
            for row in rows:
                expected_default = "''" if table == 'users' and row[1] == 'privileges' else "'0'" if table == 'users' and row[1] == 'quota' else None
                if row[4] != expected_default:
                    known = False
            indexes = database.execute('PRAGMA index_list(' + table + ')').fetchall()
            if table != 'mfa':
                fixed_index = 'sqlite_autoindex_' + table + '_1'
                if len(indexes) != 1 or indexes[0][1] != fixed_index or tuple(indexes[0][2:]) != (1, 'u', 0):
                    known = False
                else:
                    columns = database.execute('PRAGMA index_info(' + fixed_index + ')').fetchall()
                    expected_column = 'email' if table == 'users' else 'source'
                    if len(columns) != 1 or columns[0][2] != expected_column:
                        known = False
            elif indexes:
                known = False
            foreign = database.execute('PRAGMA foreign_key_list(' + table + ')').fetchall()
            if table == 'mfa':
                if len(foreign) != 1 or tuple(foreign[0][2:]) != ('users', 'user_id', 'id', 'NO ACTION', 'CASCADE', 'NONE'):
                    known = False
            elif foreign:
                known = False
        counts = {}
        if not known:
            print(json.dumps({'schema_known': False, 'counts': {}, 'users': [], 'aliases': [], 'mfa': []}))
            return
        for table in EXPECTED:
            counts[table] = database.execute('SELECT count(*) FROM ' + table).fetchone()[0]
        if any(count > LIMIT for count in counts.values()):
            print(json.dumps({'schema_known': True, 'limit_exceeded': True, 'counts': counts, 'users': [], 'aliases': [], 'mfa': []}))
            return
        # Prevent source reads from creating journals, attaching files, invoking
        # user code, writes or extension loading even if a later edit regresses.
        allowed = {sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, sqlite3.SQLITE_FUNCTION}
        database.set_authorizer(lambda action, *unused: sqlite3.SQLITE_OK if action in allowed else sqlite3.SQLITE_DENY)
        digest = hashlib.sha256(json.dumps(objects, sort_keys=True).encode())
        result = {'schema_known': True, 'counts': counts, 'users': [], 'aliases': [], 'mfa': []}
        for row in database.execute('SELECT id,email,password,privileges,quota,extra IS NOT NULL AND extra<>\'\' FROM users ORDER BY id'):
            row_id, email, password, privileges, quota, extra = row
            digest.update(json.dumps(row, ensure_ascii=True).encode())
            match = re.fullmatch(r'(?:\{SHA512-CRYPT\})?\$6\$(?:rounds=([0-9]+)\$)?[./A-Za-z0-9]{1,16}\$[./A-Za-z0-9]{86}', password) if isinstance(password, str) else None
            supported = bool(match) and (not match.group(1) or 1000 <= int(match.group(1)) <= 999999999)
            result['users'].append({'id': row_id, 'email': email, 'hash_supported': supported,
                                    'privileges': privileges, 'quota': quota, 'extra_present': bool(extra)})
        for table in ('aliases', 'auto_aliases'):
            for row in database.execute('SELECT id,source,destination,permitted_senders FROM ' + table + ' ORDER BY id'):
                digest.update(json.dumps(row, ensure_ascii=True).encode())
                result['aliases'].append({'table': table, 'id': row[0], 'source': row[1],
                                          'destination': row[2], 'permitted_senders': row[3]})
        for row in database.execute('SELECT id,user_id,type FROM mfa ORDER BY id'):
            digest.update(json.dumps(row).encode())
            result['mfa'].append({'id': row[0], 'user_id': row[1], 'type': row[2]})
        result['reviewed_snapshot_sha256'] = digest.hexdigest()
        # Password hashes/extra/MFA seeds are never serialized into child output.
        print(json.dumps(result, ensure_ascii=True, separators=(',', ':')))
        # Closing the read-only connection ends its snapshot without COMMIT.
    finally:
        database.close()


try:
    main()
except Exception:
    # sqlite/Python diagnostics may quote source SQL or private paths.
    print('MIAB_READ_FAILED', file=sys.stderr)
    sys.exit(2)
