#!/bin/sh
# LOCAL TEST harness: poll the rendered postfix config and reload on change.
# The product entrypoint (docker-entrypoint.sh) has no config watcher on this
# branch; this script only exercises the 2s reload budget from §1.4.
set -eu
CFG=/srv/urmail/config/postfix
STAMP="$CFG/.reload-postfix.ts"
hash_cfg() {
    sha256sum "$CFG/main.cf" "$CFG/master.cf" 2>/dev/null | sha256sum | cut -d' ' -f1
}
last=$(hash_cfg)
(
    while :; do
        sleep 1
        cur=$(hash_cfg || true)
        if [ -n "$cur" ] && [ "$cur" != "$last" ]; then
            if postfix -c "$CFG" reload; then
                date +%s.%N >"$STAMP"
                echo "[watch-postfix] config change detected; reload at $(cat "$STAMP")"
            else
                echo "[watch-postfix] reload failed" >&2
            fi
            last=$cur
        fi
    done
) &
exec "$@"
