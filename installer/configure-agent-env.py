#!/usr/bin/env python3
"""Merge the root environment; never copy root-only settings back to the daemon.

Usage: configure-agent-env.py DAEMON_ENV AGENT_ENV
The daemon input must already contain the final merged installation settings.
Explicit process overrides apply only to non-shared keys: shared settings always
follow the daemon, including absence, so stale agent deployment flags cannot win.
"""
import importlib.util
import os
from pathlib import Path
import sys

spec = importlib.util.spec_from_file_location("configure_env", Path(__file__).with_name("configure-env.py"))
env = importlib.util.module_from_spec(spec)
spec.loader.exec_module(env)

DATABASE_KEYS = {
    "URMAIL_DATABASE_URL", "URMAIL_DATABASE_URL_PLATFORM",
    "URMAIL_DATABASE_URL_MIGRATE", "URMAIL_DATABASE_URL_DOVECOT",
}
DEPLOYMENT_KEYS = env.BOOLEAN_KEYS | {
    "URMAIL_DEV_MODE", "URMAIL_WORKERS_HELD", "URMAIL_QUARANTINED",
    "URMAIL_DNS_SECONDARY_ONLY", "URMAIL_POSTFIX_V80_PRESERVE",
    "URMAIL_PRESERVE_EXTERNAL_NSD", "URMAIL_PRESERVE_EXTERNAL_WEB",
    "URMAIL_PRESERVE_EXTERNAL_OPENDKIM", "URMAIL_ROUNDCUBE_ENABLED",
    "URMAIL_MARKETING_ENABLED", "URMAIL_REGISTRATION_ENABLED",
    "URMAIL_NOTIFICATIONS_ENABLED", "URMAIL_REFUNDS_ENABLED",
}
SHARED_KEYS = DATABASE_KEYS | DEPLOYMENT_KEYS


def merge(daemon, previous, overrides):
    for key in DATABASE_KEYS:
        if not daemon.get(key):
            raise ValueError("Merged daemon environment is missing " + key)
    values = dict(daemon)
    values.update(previous)
    for key, value in overrides.items():
        if env.KEY.fullmatch(key) and key not in SHARED_KEYS:
            values[key] = value
    for key in SHARED_KEYS:
        if key in daemon:
            values[key] = daemon[key]
        else:
            values.pop(key, None)
    env.validate(values)
    return values


def write(daemon_path, agent_path, environment):
    if daemon_path.resolve() == agent_path.resolve():
        raise ValueError("Daemon and agent environment paths must differ")
    if agent_path.is_symlink():
        raise ValueError("Agent environment must not be a symlink")
    daemon = env.parse(daemon_path.read_text())
    previous = env.parse(agent_path.read_text()) if agent_path.exists() else {}
    values = merge(daemon, previous, environment)
    env.atomic_write(agent_path, values)  # private 0600 temp file, fsync + rename


if __name__ == "__main__":
    try:
        if len(sys.argv) != 3:
            raise ValueError("usage: configure-agent-env.py DAEMON_ENV AGENT_ENV")
        write(Path(sys.argv[1]), Path(sys.argv[2]), os.environ)
    except (ValueError, OSError) as error:
        raise SystemExit(str(error)) from error
