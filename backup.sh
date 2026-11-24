#!/usr/bin/env bash
# urMail full-snapshot backup (Phase 4 cut-over tool).
#
# One verifiable snapshot of a DATA_ROOT: SQLite (consistent online export),
# Maildir, rendered config (incl. master.key + DKIM private key), archive
# volume, version/commit metadata, per-file MANIFEST.sha256 and size stats.
# Fail-closed: any step error exits non-zero and marks the snapshot FAILED.
#
# Usage:
#   ./backup.sh --data-root DIR --out DIR [--label TEXT] [--sqlite-bin PATH]
#
# Env:
#   URMAIL_SQLITE_IMAGE   image used when the host has no sqlite3
#                         (default: urmail-local:drill-new)
set -euo pipefail

DATA_ROOT=""; OUT=""; LABEL=""; SQLITE_BIN=""
die()  { printf '[backup] FATAL: %s\n' "$*" >&2; exit 5; }
log()  { printf '[backup] %s\n' "$*"; }
usage(){ sed -n '2,15p' "$0"; exit 0; }

while [ $# -gt 0 ]; do
    case "$1" in
        --data-root) DATA_ROOT=$2; shift 2 ;;
        --out)       OUT=$2; shift 2 ;;
        --label)     LABEL=$2; shift 2 ;;
        --sqlite-bin) SQLITE_BIN=$2; shift 2 ;;
        -h|--help)   usage ;;
        *) die "unknown argument: $1" ;;
    esac
done
[ -n "$DATA_ROOT" ] || usage
[ -n "$OUT" ] || usage
DATA_ROOT=$(realpath -m "$DATA_ROOT")
OUT=$(realpath -m "$OUT")
[ -d "$DATA_ROOT" ] || die "data root not found: $DATA_ROOT"

DB="$DATA_ROOT/db/urmail.sqlite"
[ -f "$DB" ] || die "SQLite DB missing: $DB (refusing an incomplete snapshot)"
# The data root is owned by uid 5000 and the DB is 0600: a non-root operator
# cannot read it.  In that case the export runs in the helper container below
# (root inside, file mounted read-only); the run must then be authorized.
HOST_CAN_READ_DB=no; [ -r "$DB" ] && HOST_CAN_READ_DB=yes

# Target must be new/empty: never merge two snapshots.
if [ -e "$OUT" ] && [ -n "$(ls -A "$OUT" 2>/dev/null)" ]; then
    die "output dir is not empty: $OUT (refusing to overwrite)"
fi
mkdir -p "$OUT"/{db,mail,config,archive}
chmod 700 "$OUT"
FAILED=0
trap 'rc=$?; if [ "$rc" -ne 0 ]; then printf "failed rc=%s at %s\n" "$rc" "$(date -u +%FT%TZ)" > "$OUT/BACKUP.FAILED"; fi' EXIT

T0=$(date -u +%FT%TZ)
SECONDS=0
log "snapshot start $T0 data_root=$DATA_ROOT out=$OUT"

# ---- 1. SQLite: online consistent export (.backup) -----------------------
SNAP_DB="$OUT/db/urmail.sqlite"
sqlite_export() {
    if [ "$HOST_CAN_READ_DB" = yes ] && { [ -n "$SQLITE_BIN" ] || command -v sqlite3 >/dev/null 2>&1; }; then
        local bin="${SQLITE_BIN:-sqlite3}"
        "$bin" "$DB" ".backup '$SNAP_DB'"
        return
    fi
    local img="${URMAIL_SQLITE_IMAGE:-urmail-local:drill-new}"
    command -v docker >/dev/null 2>&1 || die "no sqlite3 and no docker for the SQLite export"
    docker image inspect "$img" >/dev/null 2>&1 || die "sqlite helper image missing: $img"
    docker run --rm -u 0 -v "$DATA_ROOT/db":/db:ro -v "$OUT/db":/out "$img" \
        sqlite3 "/db/$(basename "$DB")" ".backup /out/$(basename "$SNAP_DB")"
}
sqlite_snap_query() { # query the SNAPSHOT copy
    if [ -n "$SQLITE_BIN" ] || command -v sqlite3 >/dev/null 2>&1; then
        local bin="${SQLITE_BIN:-sqlite3}"
        "$bin" "$SNAP_DB" "$1"
        return
    fi
    local img="${URMAIL_SQLITE_IMAGE:-urmail-local:drill-new}"
    docker run --rm -u 0 -v "$OUT/db":/out "$img" \
        sqlite3 "/out/$(basename "$SNAP_DB")" "$1"
}
log "sqlite: .backup -> $SNAP_DB"
sqlite_export || die "sqlite .backup failed"
[ -s "$SNAP_DB" ] || die "sqlite backup is empty"
INT=$(sqlite_snap_query "PRAGMA integrity_check;" | head -n1)
[ "$INT" = "ok" ] || die "snapshot SQLite integrity_check=$INT"
UV=$(sqlite_snap_query "PRAGMA user_version;" | head -n1)
TAB=$(sqlite_snap_query "SELECT count(*) FROM sqlite_master WHERE type='table';" | head -n1)
AH=$(sqlite_snap_query "SELECT value FROM firstboot_state WHERE key='admin_bootstrap_token_hash';" 2>/dev/null | head -n1 || true)
log "sqlite: integrity=ok user_version=$UV tables=$TAB admin_hash=${AH:0:16}..."

# ---- 2. Maildir / config / archive ---------------------------------------
mail_list() { find "$DATA_ROOT/mail" -type f -printf '%p %s %T@\n' 2>/dev/null | sort; }
BEFORE=$(mail_list | sha256sum | cut -d' ' -f1)
log "mail: copying $DATA_ROOT/mail"
if [ -d "$DATA_ROOT/mail" ]; then cp -a "$DATA_ROOT/mail/." "$OUT/mail/"; fi
AFTER=$(mail_list | sha256sum | cut -d' ' -f1)
QUIET=$([ "$BEFORE" = "$AFTER" ] && echo yes || echo no)
[ "$QUIET" = yes ] || log "WARNING: mail tree changed during the copy (snapshot may miss a concurrent delivery)"

log "config: copying rendered config (secrets stay inside the 0700 snapshot)"
if [ -d "$DATA_ROOT/config" ]; then
    mkdir -p "$OUT/config"
    ( cd "$DATA_ROOT/config" && tar -cf - --exclude=run . ) | ( cd "$OUT/config" && tar -xf - )
fi
log "archive: copying $DATA_ROOT/archive"
if [ -d "$DATA_ROOT/archive" ]; then cp -a "$DATA_ROOT/archive/." "$OUT/archive/"; fi

# ---- 3. metadata ---------------------------------------------------------
REPO=$(cd "$(dirname "$0")" && pwd)
VERSION="${URMAIL_VERSION:-}"; COMMIT="${URMAIL_COMMIT:-}"
if [ -z "$VERSION" ] && [ -f "$REPO/VERSION" ]; then VERSION=$(cat "$REPO/VERSION"); fi
if [ -z "$COMMIT" ] && git -C "$REPO" rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    COMMIT=$(git -C "$REPO" rev-parse HEAD 2>/dev/null || true)
fi
DKIM_SHA=""
DKIM_KEY=$(find "$OUT/config/dkim" -name '*.key' 2>/dev/null | head -n1)
[ -n "$DKIM_KEY" ] && DKIM_SHA=$(sha256sum "$DKIM_KEY" | cut -d' ' -f1)
MAIL_FILES=$(find "$OUT/mail" -type f 2>/dev/null | wc -l | tr -d ' ')
SQLITE_SHA=$(sha256sum "$OUT/db/urmail.sqlite" | cut -d' ' -f1)
SZ_DB=$(du -sb "$OUT/db" | cut -f1); SZ_MAIL=$(du -sb "$OUT/mail" | cut -f1)
SZ_CFG=$(du -sb "$OUT/config" | cut -f1); SZ_ARC=$(du -sb "$OUT/archive" | cut -f1)

cat > "$OUT/META.txt" <<META
label=$LABEL
snapshot_started=$T0
snapshot_finished=$(date -u +%FT%TZ)
duration_s=$SECONDS
data_root=$DATA_ROOT
repo_version=$VERSION
repo_commit=$COMMIT
sqlite_path=db/urmail.sqlite
sqlite_sha256=$SQLITE_SHA
sqlite_user_version=$UV
sqlite_tables=$TAB
sqlite_integrity=$INT
admin_bootstrap_token_hash=$AH
dkim_key_path=${DKIM_KEY#"$OUT"/}
dkim_key_sha256=$DKIM_SHA
mail_files=$MAIL_FILES
mail_tree_changed_during_copy=$([ "$QUIET" = yes ] && echo no || echo yes)
bytes_db=$SZ_DB
bytes_mail=$SZ_MAIL
bytes_config=$SZ_CFG
bytes_archive=$SZ_ARC
META

# ---- 4. per-file manifest ------------------------------------------------
( cd "$OUT" && find . -type f \
    ! -name MANIFEST.sha256 ! -name META.txt ! -name BACKUP.FAILED ! -name COMPLETE \
    -print0 | sort -z | xargs -0 -r sha256sum ) > "$OUT/MANIFEST.sha256"
MF_LINES=$(wc -l < "$OUT/MANIFEST.sha256" | tr -d ' ')
[ "$MF_LINES" -ge 3 ] || die "manifest too small ($MF_LINES files)"
TOTAL=$(du -sb "$OUT" | cut -f1)
printf 'manifest_files=%s\ntotal_bytes=%s\n' "$MF_LINES" "$TOTAL" >> "$OUT/META.txt"
printf 'ok %s\n' "$(date -u +%FT%TZ)" > "$OUT/COMPLETE"
log "snapshot complete: $MF_LINES files, $TOTAL bytes, ${SECONDS}s -> $OUT"
