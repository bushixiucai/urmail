"""Memory-private reviewed-source loader. Its stdout must never be logged."""
import hashlib
import json
import os
import sqlite3
import sys
import urllib.parse


def main():
    filename, expected = sys.argv[1:3]
    if any(os.path.lexists(filename + suffix) for suffix in ('-wal', '-shm', '-journal')):
        raise RuntimeError('standalone source required')
    uri = 'file:' + urllib.parse.quote(os.path.abspath(filename), safe='/') + '?mode=ro'
    database = sqlite3.connect(uri, uri=True, timeout=5)
    try:
        if hasattr(database, 'enable_load_extension'):
            database.enable_load_extension(False)
        database.execute('PRAGMA query_only=ON')
        database.execute('PRAGMA temp_store=MEMORY')
        database.execute('PRAGMA trusted_schema=OFF')
        database.execute('BEGIN')
        objects = database.execute("SELECT type,CASE WHEN length(name)<=128 THEN name ELSE '' END,CASE WHEN length(tbl_name)<=128 THEN tbl_name ELSE '' END,CASE WHEN length(sql)<=65536 THEN sql ELSE NULL END FROM sqlite_master ORDER BY type,name LIMIT 129").fetchall()
        if len(objects) > 128 or database.execute('SELECT count(*) FROM mfa').fetchone()[0]:
            raise RuntimeError('unsupported private source scope')
        counts = {table: database.execute('SELECT count(*) FROM ' + table).fetchone()[0]
                  for table in ('users', 'aliases', 'auto_aliases')}
        if any(count > 50000 for count in counts.values()):
            raise RuntimeError('private source row bound')
        allowed = {sqlite3.SQLITE_SELECT, sqlite3.SQLITE_READ, sqlite3.SQLITE_FUNCTION}
        database.set_authorizer(lambda action, *unused: sqlite3.SQLITE_OK if action in allowed else sqlite3.SQLITE_DENY)
        digest = hashlib.sha256(json.dumps(objects, sort_keys=True).encode())
        result = {'fingerprint': '', 'users': [], 'aliases': []}
        for row in database.execute("SELECT id,email,password,extra,privileges,quota,extra IS NOT NULL AND extra<>'' FROM users ORDER BY id"):
            row_id, email, password, extra, privileges, quota, extra_flag = row
            if extra_flag or extra not in (None, ''):
                raise RuntimeError('unreviewed extra metadata')
            public_row = (row_id, email, password, privileges, quota, extra_flag)
            digest.update(json.dumps(public_row, ensure_ascii=True).encode())
            result['users'].append({'id': row_id, 'email': email, 'password_hash': password,
                                    'extra': extra, 'privileges': privileges, 'quota': quota})
        for table in ('aliases', 'auto_aliases'):
            for row in database.execute('SELECT id,source,destination,permitted_senders FROM ' + table + ' ORDER BY id'):
                if row[3] is not None:
                    raise RuntimeError('unreviewed sender restriction')
                digest.update(json.dumps(row, ensure_ascii=True).encode())
                result['aliases'].append({'table': table, 'id': row[0], 'source': row[1],
                                          'destination': row[2], 'permitted_senders': row[3]})
        result['fingerprint'] = digest.hexdigest()
        if result['fingerprint'] != expected:
            raise RuntimeError('review fingerprint changed')
        encoded = json.dumps(result, ensure_ascii=True, separators=(',', ':'))
        if len(encoded) > 16 << 20:
            raise RuntimeError('private output bound')
        print(encoded)
    finally:
        database.close()


try:
    main()
except Exception:
    print('PRIVATE_SOURCE_READ_FAILED', file=sys.stderr)
    sys.exit(2)
