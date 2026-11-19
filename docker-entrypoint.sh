#!/bin/sh
# urMail compose skeleton entrypoint (Phase 2).
# Guarantees, per service that boots through this image:
#   - a master key exists under $URMAIL_CONFIG_DIR (generated once, 0600);
#   - URMAIL_ENV_FILE points at a writable env file (config.Load merges
#     process environment over it, so compose `environment:` wins);
#   - `urmaild` boots after pending schema migrations are applied.
set -eu

CONFIG_DIR="${URMAIL_CONFIG_DIR:-/srv/urmail/config}"
mkdir -p "$CONFIG_DIR"

if [ ! -f "$CONFIG_DIR/master.key" ]; then
    # Atomic create (QA P1-4): write to a temp name then rename, so a half
    # written key can never be observed at the canonical path.
    umask 077
    tmp="$CONFIG_DIR/master.key.tmp.$$"
    head -c 32 /dev/urandom > "$tmp"
    mv -f "$tmp" "$CONFIG_DIR/master.key"
    umask 022
    echo "[entrypoint] generated master key at $CONFIG_DIR/master.key"
fi
export URMAIL_MASTER_KEY_PATH="${URMAIL_MASTER_KEY_PATH:-$CONFIG_DIR/master.key}"
export URMAIL_ENV_FILE="${URMAIL_ENV_FILE:-$CONFIG_DIR/urmail.env}"
if [ ! -f "$URMAIL_ENV_FILE" ]; then
    : > "$URMAIL_ENV_FILE"
    chmod 600 "$URMAIL_ENV_FILE"
fi

cmd="${1:-}"
if [ "$cmd" = "urmaild" ] || [ "$cmd" = "serve" ]; then
    if [ "${URMAIL_AUTO_MIGRATE:-1}" = "1" ]; then
        echo "[entrypoint] applying pending schema migrations (urmailctl migrate up)"
        urmailctl migrate up
    fi
fi

exec "$@"
