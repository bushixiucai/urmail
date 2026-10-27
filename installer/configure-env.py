#!/usr/bin/env python3
"""Merge installer defaults without executing or resetting operator settings."""
import os
from pathlib import Path
import re
import sys
import tempfile

KEY = re.compile(r"URMAIL_[A-Z0-9_]+")
BOOLEAN_KEYS = {
    "URMAIL_WEBMAIL_NATIVE", "URMAIL_CARDDAV_ENABLED", "URMAIL_CALDAV_ENABLED",
    "URMAIL_EAS_ENABLED", "URMAIL_POLICY_ENABLED", "URMAIL_ARCHIVE_ENABLED",
}
AUTHORITATIVE_DEFAULTS = {
    "URMAIL_DATABASE_URL", "URMAIL_DATABASE_URL_PLATFORM", "URMAIL_DATABASE_URL_MIGRATE",
    "URMAIL_DATABASE_URL_DOVECOT", "URMAIL_MYHOSTNAME", "URMAIL_PRIMARY_DOMAIN",
    "URMAIL_PUBLIC_IPV4", "URMAIL_NAMESERVERS", "URMAIL_POLICY_ENABLED",
}


def parse(document):
    values = {}
    for number, raw in enumerate(document.splitlines(), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        key, sep, value = line.partition("=")
        if not sep or not KEY.fullmatch(key):
            raise ValueError(f"Invalid environment key on line {number}")
        if value[:1] in ("'", '"') and value[-1:] == value[:1]:
            value = value[1:-1]
        if any(char in value for char in ("\n", "\r", "\x00")):
            raise ValueError(f"Invalid line break in {key}")
        values[key] = value
    return values


def merge(previous, defaults, overrides):
    values = dict(defaults)
    values.update(previous)
    for key in AUTHORITATIVE_DEFAULTS:
        if key in defaults:
            values[key] = defaults[key]
    for key in set(defaults) | set(previous):
        if key in overrides:
            values[key] = overrides[key]
    for key, value in values.items():
        if any(char in value for char in ("\n", "\r", "\x00")):
            raise ValueError(f"Invalid line break in {key}")
        if key in BOOLEAN_KEYS and value not in ("true", "false"):
            raise ValueError(f"{key} must be true or false")
    # Billing enforcement must not silently become optional after an upgrade.
    if values.get("URMAIL_POLICY_ENABLED") != "true":
        raise ValueError("URMAIL_POLICY_ENABLED must remain true")
    return values


def write(path, defaults, environment):
    previous = parse(path.read_text()) if path.exists() else {}
    if path.exists():
        # Before deployment flags existed native Webmail defaulted to off.
        previous.setdefault("URMAIL_WEBMAIL_NATIVE", "false")
    values = merge(previous, parse(defaults), environment)
    # Replace only after validation; interrupted writes never truncate secrets.
    fd, temporary = tempfile.mkstemp(prefix=".urmail-env-", dir=path.parent)
    try:
        with os.fdopen(fd, "w") as stream:
            stream.write("".join(f"{key}={value}\n" for key, value in values.items()))
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(temporary, path)
    finally:
        if os.path.exists(temporary):
            os.unlink(temporary)


if __name__ == "__main__":
    try:
        write(Path(sys.argv[1]), sys.stdin.read(), os.environ)
    except (ValueError, OSError) as error:
        raise SystemExit(str(error)) from error
