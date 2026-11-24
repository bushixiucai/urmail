#!/usr/bin/env bash
# Negative/positive cases for the upgrade pre-flights. The functions live in
# writer-lease-preflight.sh and are sourced by install.sh/upgrade.sh with their
# own log/die helpers; this harness supplies the same contract.
set -uo pipefail
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
log(){ printf '[log] %s\n' "$*"; }
warn(){ printf '[warn] %s\n' "$*" >&2; }
die(){ printf '[DIE] %s\n' "$*" >&2; return 4; }
. "$SCRIPT_DIR/writer-lease-preflight.sh"

fails=0
check(){ # name expected actual
    if [ "$2" != "$3" ]; then printf 'FAIL %s: want=%s got=%s\n' "$1" "$2" "$3"; fails=$((fails+1));
    else printf 'PASS %s (%s)\n' "$1" "$3"; fi
}

tmp=$(mktemp -d /var/tmp/urmail-preflight-test.XXXXXX)
trap 'chmod -R u+w "$tmp" 2>/dev/null; rm -rf "$tmp"' EXIT

rc=0; data_root_fs_preflight "$tmp/missing" >/dev/null 2>&1 || rc=$?
check "missing data root is refused" 4 "$rc"

mkdir -p "$tmp/readonly"; chmod 0555 "$tmp/readonly"
rc=0; data_root_fs_preflight "$tmp/readonly" >/dev/null 2>&1 || rc=$?
check "read-only (0555) data root is refused" 4 "$rc"
chmod 0755 "$tmp/readonly"

rc=0; data_root_fs_preflight "$tmp" >/dev/null 2>&1 || rc=$?
check "writable data root passes" 0 "$rc"

lease="$tmp/.urmail-writer.lease"
( flock -n 9 && sleep 5 ) 9>"$lease" &
holder=$!
sleep 1
rc=0; lease_preflight "$lease" >/dev/null 2>&1 || rc=$?
check "held writer lease is refused" 4 "$rc"
wait "$holder" 2>/dev/null

exit $((fails > 0 ? 1 : 0))
