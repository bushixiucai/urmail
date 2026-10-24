"""Compare private, typed conversion snapshots without exposing any row data."""
import base64
import hashlib
import json
import re


class ContinuityError(Exception):
    pass


def scalar(value, kind):
    if value is None:
        return None
    if kind == 'integer':
        if isinstance(value, bool) or not re.fullmatch(r'-?[0-9]+', str(value)):
            raise ContinuityError('INTEGER_REPRESENTATION_INVALID')
        return str(int(value))
    if kind == 'boolean':
        if not isinstance(value, (bool, int, str)):
            raise ContinuityError('BOOLEAN_REPRESENTATION_INVALID')
        if value is True or value in (1, '1', 't', 'true'):
            return True
        if value is False or value in (0, '0', 'f', 'false'):
            return False
        raise ContinuityError('BOOLEAN_REPRESENTATION_INVALID')
    if kind == 'text' and isinstance(value, str):
        return value
    if kind == 'binary' and isinstance(value, bytes):
        return base64.b64encode(value).decode('ascii')
    raise ContinuityError('UNSUPPORTED_COLUMN_REPRESENTATION')


def table_fingerprint(columns, rows):
    """Column types come from audited schema mapping, never guessed by value."""
    if not columns or len({name for name, _ in columns}) != len(columns):
        raise ContinuityError('COLUMN_MAPPING_INVALID')
    encoded = []
    for row in rows:
        if len(row) != len(columns):
            raise ContinuityError('COLUMN_MAPPING_INVALID')
        values = [scalar(value, kind) for value, (_, kind) in zip(row, columns)]
        encoded.append(json.dumps(values, ensure_ascii=True, separators=(',', ':')))
    encoded.sort()
    digest = hashlib.sha256()
    digest.update(json.dumps(columns, separators=(',', ':')).encode())
    for row in encoded:
        digest.update(b'\n')
        digest.update(row.encode())
    return {'row_count': len(encoded), 'sha256': digest.hexdigest()}


def compare_snapshots(source, target, approved_exceptions=()):
    """Exceptions require a separate operator-reviewed session-revocation record."""
    if approved_exceptions:
        allowed = {'oc_authtoken', 'oc_file_locks', 'oc_bruteforce_attempts'}
        if not isinstance(approved_exceptions, dict) or any(
                name not in allowed or not isinstance(reason, str) or len(reason.strip()) < 20
                for name, reason in approved_exceptions.items()):
            raise ContinuityError('UNAPPROVED_SEMANTIC_EXCEPTION')
    exceptions = set(approved_exceptions)
    if set(source) - set(target) - exceptions:
        raise ContinuityError('SOURCE_TABLE_MISSING')
    if set(target) - set(source) - exceptions:
        raise ContinuityError('UNREVIEWED_TARGET_TABLE')
    for table in set(source) & set(target) - exceptions:
        before = source[table]
        after = target[table]
        if before['columns'] != after['columns']:
            raise ContinuityError('COLUMN_MAPPING_CHANGED')
        if table_fingerprint(before['columns'], before['rows']) != table_fingerprint(after['columns'], after['rows']):
            raise ContinuityError('SEMANTIC_ROW_MISMATCH')
    return {'state': 'SEMANTIC_SNAPSHOTS_MATCH', 'tables_checked': len(set(source) & set(target) - exceptions),
            'explicit_exception_count': len(exceptions)}
