#!/usr/bin/env bash
# urMail snapshot restore + verification (Phase 4 cut-over tool).
#
# Restores a backup.sh snapshot into a NEW EMPTY directory and verifies it:
#   1. MANIFEST.sha256 of the snapshot (per-file)
#   2. restored SQLite: PRAGMA integrity_check, user_version, table count
#   3. Maildir: per-file sha256 of a random sample vs the restored files
#   4. one random message fetched by Message-ID over IMAPS and compared
#   5. DKIM key sha256 and admin bootstrap hash unchanged
# Fail-closed: any failed check exits non-zero.
#
# Usage:
#   ./restore-verify.sh --snapshot DIR --target DIR [--sample N] [--sqlite-bin PATH]
#
# Env (IMAPS check; skipped when unset):
#   URMAIL_IMAP_HOST (127.0.0.1) URMAIL_IMAP_PORT (1993)
#   URMAIL_IMAP_USER URMAIL_IMAP_PASS
set -euo pipefail

SNAP=""; TARGET=""; SAMPLE=5; SQLITE_BIN=""
fails=0
die()  { printf '[restore] FATAL: %s\n' "$*" >&2; exit 5; }
log()  { printf '[restore] %s\n' "$*"; }
ok()   { printf '[restore] PASS  %s\n' "$*"; }
bad()  { printf '[restore] FAIL  %s\n' "$*" >&2; fails=$((fails+1)); }
usage(){ sed -n '2,18p' "$0"; exit 0; }

while [ $# -gt 0 ]; do
    case "$1" in
        --snapshot) SNAP=$2; shift 2 ;;
        --target)   TARGET=$2; shift 2 ;;
        --sample)   SAMPLE=$2; shift 2 ;;
        --sqlite-bin) SQLITE_BIN=$2; shift 2 ;;
        -h|--help)  usage ;;
        *) die "unknown argument: $1" ;;
    esac
done
[ -n "$SNAP" ] && [ -n "$TARGET" ] || usage
SNAP=$(realpath -m "$SNAP")
TARGET=$(realpath -m "$TARGET")
[ -d "$SNAP" ] || die "snapshot not found: $SNAP"
[ -f "$SNAP/MANIFEST.sha256" ] || die "snapshot has no MANIFEST.sha256"
[ -f "$SNAP/COMPLETE" ] || die "snapshot is not marked COMPLETE (unfinished/failed backup)"
if [ -e "$TARGET" ] && [ -n "$(ls -A "$TARGET" 2>/dev/null)" ]; then
    die "target dir is not empty: $TARGET (restore requires a fresh directory)"
fi

meta() { sed -n "s/^$1=//p" "$SNAP/META.txt" | head -n1; }
sqlq() { # query the RESTORED copy
    if [ -n "$SQLITE_BIN" ] || command -v sqlite3 >/dev/null 2>&1; then
        local bin="${SQLITE_BIN:-sqlite3}"
        "$bin" "$TARGET/db/urmail.sqlite" "$1"
        return
    fi
    local img="${URMAIL_SQLITE_IMAGE:-urmail-local:drill-new}"
    docker run --rm -u 0 -v "$TARGET/db":/db "$img" sqlite3 /db/urmail.sqlite "$1"
}

log "1/5 snapshot manifest"
if ( cd "$SNAP" && sha256sum -c --quiet MANIFEST.sha256 ); then
    ok "MANIFEST.sha256 verified ($(wc -l < "$SNAP/MANIFEST.sha256" | tr -d ' ') files)"
else
    bad "MANIFEST.sha256 mismatch in $SNAP"
fi

log "2/5 restore into fresh dir $TARGET"
mkdir -p "$TARGET"
for d in db mail config archive; do
    [ -d "$SNAP/$d" ] || continue
    mkdir -p "$TARGET/$d"
    cp -a "$SNAP/$d/." "$TARGET/$d/"
done

log "3/5 restored SQLite"
INT=$(sqlq "PRAGMA integrity_check;" | head -n1)
UV=$(sqlq "PRAGMA user_version;" | head -n1)
TAB=$(sqlq "SELECT count(*) FROM sqlite_master WHERE type='table';" | head -n1)
[ "$INT" = ok ] && ok "integrity_check=ok" || bad "integrity_check=$INT"
[ "$UV" = "$(meta sqlite_user_version)" ] && ok "user_version=$UV (matches snapshot)" || bad "user_version=$UV != snapshot $(meta sqlite_user_version)"
[ "$TAB" = "$(meta sqlite_tables)" ] && ok "tables=$TAB (matches snapshot)" || bad "tables=$TAB != snapshot $(meta sqlite_tables)"
AH=$(sqlq "SELECT value FROM firstboot_state WHERE key='admin_bootstrap_token_hash';" | head -n1 || true)
[ -n "$AH" ] && [ "$AH" = "$(meta admin_bootstrap_token_hash)" ] && ok "admin bootstrap hash unchanged" || bad "admin hash differs"
DKIM_KEY=$(find "$TARGET/config/dkim" -name '*.key' 2>/dev/null | head -n1)
DKIM_SHA=$([ -n "$DKIM_KEY" ] && sha256sum "$DKIM_KEY" | cut -d' ' -f1 || true)
[ -n "$DKIM_SHA" ] && [ "$DKIM_SHA" = "$(meta dkim_key_sha256)" ] && ok "DKIM private key sha256 unchanged" || bad "DKIM key missing or changed"

log "4/5 Maildir sample ($SAMPLE files)"
mapfile -t MSGS < <(find "$TARGET/mail" -type f \( -path '*/cur/*' -o -path '*/new/*' \) | sort)
if [ "${#MSGS[@]}" -eq 0 ]; then
    bad "no mail files in the restored tree"
else
    n=0; mism=0
    for f in $(printf '%s\n' "${MSGS[@]}" | shuf -n "$SAMPLE"); do
        rel="./${f#"$TARGET"/}"
        want=$(awk -v p="$rel" '$2 == p { print $1 }' "$SNAP/MANIFEST.sha256" | head -n1)
        got=$(sha256sum "$f" | cut -d' ' -f1)
        n=$((n+1))
        [ "$want" = "$got" ] || { mism=$((mism+1)); printf '[restore]   mismatch %s\n' "$rel"; }
    done
    [ "$mism" -eq 0 ] && ok "Maildir sample: $n/$n sha256 match" || bad "Maildir sample: $mism/$n mismatch"
fi

log "5/5 IMAPS round-trip (Message-ID)"
if [ -z "${URMAIL_IMAP_USER:-}" ] || [ -z "${URMAIL_IMAP_PASS:-}" ]; then
    bad "IMAPS check NOT_RUN: URMAIL_IMAP_USER/URMAIL_IMAP_PASS not set (cannot claim a mail round-trip)"
elif [ "${#MSGS[@]}" -eq 0 ]; then
    bad "IMAPS check NOT_RUN: no message to fetch"
else
    PICK=$(printf '%s\n' "${MSGS[@]}" | shuf -n1)
    if URMAIL_IMAP_HOST="${URMAIL_IMAP_HOST:-127.0.0.1}" URMAIL_IMAP_PORT="${URMAIL_IMAP_PORT:-1993}" \
       python3 - "$PICK" <<'PY'
import imaplib, os, re, ssl, sys
path = sys.argv[1]
raw = open(path, "rb").read()
m = re.search(rb"(?im)^Message-ID:\s*(<[^>]+>)", raw)
if not m:
    print("[restore]   file has no Message-ID header"); sys.exit(2)
mid = m.group(1).decode()
ctx = ssl._create_unverified_context()
c = imaplib.IMAP4_SSL(os.environ["URMAIL_IMAP_HOST"], int(os.environ["URMAIL_IMAP_PORT"]), ssl_context=ctx)
c.login(os.environ["URMAIL_IMAP_USER"], os.environ["URMAIL_IMAP_PASS"])
c.select("INBOX")
typ, data = c.search(None, "HEADER", "Message-ID", mid)
ids = data[0].split()
if not ids:
    print(f"[restore]   Message-ID {mid} not found over IMAPS"); sys.exit(3)
typ, data = c.fetch(ids[0], "(BODY.PEEK[])")
body = data[0][1]
c.logout()
norm = lambda b: b.replace(b"\r\n", b"\n").strip()
print(f"[restore]   fetched {mid} ({len(body)} bytes) from IMAPS")
print(f"[restore]   restored file {len(raw)} bytes")
sys.exit(0 if norm(body) == norm(raw) else 4)
PY
    then ok "IMAPS body matches the restored Maildir file"; else bad "IMAPS round-trip failed (exit $?)"; fi
fi

echo
if [ "$fails" -eq 0 ]; then
    printf '[restore] RESULT=PASS (target=%s)\n' "$TARGET"; exit 0
else
    printf '[restore] RESULT=FAIL failures=%s (target=%s)\n' "$fails" "$TARGET" >&2; exit 1
fi
