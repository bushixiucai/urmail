#!/bin/sh
# urMail compose skeleton entrypoint (Phase 2).
# Only the urmaild container manages shared secret/config state (QA review
# 2026-11-19): postfix/dovecot boot through this image untouched — they never
# create or modify the master key or the env file. That removes the race
# where a root-started postfix would leave a root-owned 0600 key/env that
# uid-5000 urmaild could not read.
#   - urmaild: master key (exclusive create, atomic), env file, migrations.
set -eu

cmd="${1:-}"

if [ "$cmd" = "urmaild" ] || [ "$cmd" = "serve" ]; then
    CONFIG_DIR="${URMAIL_CONFIG_DIR:-/srv/urmail/config}"
    mkdir -p "$CONFIG_DIR"

    if [ ! -f "$CONFIG_DIR/master.key" ]; then
        # Exclusive + atomic create (QA P1-4/P1-5): noclobber makes the
        # redirect fail when another instance won the race; the loser keeps
        # the winner's key instead of overwriting it. If this instance runs
        # as root, hand the key to uid 5000 (the urmaild runtime user).
        umask 077
        if ! (set -C; head -c 32 /dev/urandom > "$CONFIG_DIR/master.key") 2>/dev/null; then
            echo "[entrypoint] master key already present (lost exclusive create)"
        fi
        umask 022
        if [ "$(id -u)" = "0" ] && [ "$(stat -c %u "$CONFIG_DIR/master.key")" = "0" ]; then
            chown 5000:5000 "$CONFIG_DIR/master.key"
        fi
        echo "[entrypoint] generated master key at $CONFIG_DIR/master.key"
    fi
    export URMAIL_MASTER_KEY_PATH="${URMAIL_MASTER_KEY_PATH:-$CONFIG_DIR/master.key}"
    export URMAIL_ENV_FILE="${URMAIL_ENV_FILE:-$CONFIG_DIR/urmail.env}"
    if [ ! -f "$URMAIL_ENV_FILE" ]; then
        : > "$URMAIL_ENV_FILE"
        chmod 600 "$URMAIL_ENV_FILE"
        if [ "$(id -u)" = "0" ]; then
            chown 5000:5000 "$URMAIL_ENV_FILE"
        fi
    fi
    if [ "${URMAIL_AUTO_MIGRATE:-1}" = "1" ]; then
        echo "[entrypoint] applying pending schema migrations (urmailctl migrate up)"
        urmailctl migrate up
    fi
fi

exec "$@"
