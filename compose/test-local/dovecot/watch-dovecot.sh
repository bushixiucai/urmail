#!/bin/sh
# LOCAL TEST harness: poll the rendered dovecot config and reload on change.
# The product entrypoint (docker-entrypoint.sh) has no config watcher on this
# branch; this script only exercises the 2s reload budget from §1.4.
set -eu
CFG=/srv/urmail/config/dovecot/dovecot.conf
STAMP=/srv/urmail/config/dovecot/.reload-dovecot.ts
last=$(sha256sum "$CFG" 2>/dev/null | cut -d' ' -f1)
(
    while :; do
        sleep 1
        cur=$(sha256sum "$CFG" 2>/dev/null | cut -d' ' -f1 || true)
        if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
            if doveadm -c "$CFG" reload; then
                date +%s.%N >"$STAMP"
                echo "[watch-dovecot] config change detected; reload at $(cat "$STAMP")"
            else
                echo "[watch-dovecot] reload failed" >&2
            fi
            last=$cur
        fi
    done
) &
exec "$@"
