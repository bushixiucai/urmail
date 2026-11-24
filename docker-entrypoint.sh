#!/bin/sh
# urMail compose skeleton entrypoint (Phase 2).
# Only the urmaild container manages shared secret/config state (QA review
# 2026-11-19): postfix/dovecot boot through this image untouched — they never
# create or modify the master key or the env file. That removes the race
# where a root-started postfix would leave a root-owned 0600 key/env that
# uid-5000 urmaild could not read.
#   - urmaild: master key (exclusive create, atomic), env file, migrations.
set -eu

# Hold gate (QA final ③): writer containers must not start while a compose
# upgrade hold marker exists — a manual `compose start` cannot bypass the
# fence. urmaild deliberately sets URMAIL_HOLD_GUARD=0 (probe/readiness need
# it up during the hold). The marker lives in the (read-only) data root.
hold_guard="${URMAIL_HOLD_GUARD:-1}"
hold_file="${URMAIL_HOLD_FILE:-/srv/urmail/holds/upgrade.hold}"
if [ "$hold_guard" = "1" ] && [ -f "$hold_file" ]; then
    while [ -f "$hold_file" ]; do
        echo "[entrypoint] upgrade hold active ($hold_file); waiting"
        sleep 5
    done
    echo "[entrypoint] hold cleared; continuing"
fi

cmd="${1:-}"

if [ "$cmd" = "urmaild" ] || [ "$cmd" = "serve" ]; then
    CONFIG_DIR="${URMAIL_CONFIG_DIR:-/srv/urmail/config}"
    mkdir -p "$CONFIG_DIR"
    key="$CONFIG_DIR/master.key"

    if [ ! -f "$key" ]; then
        # Exclusive + atomic create (QA final): build the key in a tmp file
        # (a killed download leaves only a harmless tmp), then mv -n — atomic
        # rename that never overwrites the winner of a concurrent race.
        tmp="$CONFIG_DIR/master.key.tmp.$$"
        umask 077
        head -c 32 /dev/urandom > "$tmp"
        mv -n "$tmp" "$key"
        umask 022
        if [ -f "$tmp" ]; then
            rm -f "$tmp"
            echo "[entrypoint] master key already present (lost exclusive create)"
        else
            echo "[entrypoint] generated master key at $key"
        fi
    fi
    # Owner self-heal runs on every boot (QA final): a key/env created by an
    # older root-run entrypoint must become readable/writable by uid 5000.
    if [ "$(id -u)" = "0" ]; then
        for f in "$key" "$CONFIG_DIR/urmail.env"; do
            if [ -f "$f" ] && [ "$(stat -c %u "$f")" = "0" ]; then
                chown 5000:5000 "$f"
                echo "[entrypoint] healed owner of $f (was root)"
            fi
        done
    fi
    export URMAIL_MASTER_KEY_PATH="${URMAIL_MASTER_KEY_PATH:-$key}"
    export URMAIL_ENV_FILE="${URMAIL_ENV_FILE:-$CONFIG_DIR/urmail.env}"
    if [ ! -f "$URMAIL_ENV_FILE" ]; then
        : > "$URMAIL_ENV_FILE"
        chmod 600 "$URMAIL_ENV_FILE"
    fi
    if [ "${URMAIL_AUTO_MIGRATE:-1}" = "1" ]; then
        echo "[entrypoint] applying pending schema migrations (urmailctl migrate up)"
        urmailctl migrate up
    fi
fi

exec "$@"
