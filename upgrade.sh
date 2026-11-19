#!/usr/bin/env bash
# urMail compose upgrader skeleton (Phase 2, ARCH-PLAN §1.4).
# Gate order: clean tree -> fresh-backup pre-flight (inside `urmailctl upgrade
# fence`, reused verbatim — never reimplemented) -> git checkout vX -> compose
# build -> up (AUTO_MIGRATE=0) -> controlled migrate -> readiness probe ->
# readiness-record -> release. Any failure after the fence is fenced releases
# automatically (resume -> release, break-glass as last resort) and leaves an
# evidence file in $TMPDIR.
# Never prints secrets; --dry-run performs read-only checks and prints the plan.
#
# Usage: ./upgrade.sh vX [--dry-run] [-h]
set -euo pipefail

V=""
DRY_RUN=0

log()  { printf '[upgrade] %s\n' "$*"; }
warn() { printf '[upgrade] WARNING: %s\n' "$*" >&2; }
die()  { printf '[upgrade] ABORT: %s\n' "$*" >&2; exit 4; }
usage(){ sed -n '2,13p' "$0"; exit 0; }

while [ $# -gt 0 ]; do
    case "$1" in
        --dry-run) DRY_RUN=1; shift ;;
        -h|--help) usage ;;
        -*) die "unknown flag: $1" ;;
        *)  if [ -n "$V" ]; then die "unexpected argument: $1"; fi
            V=$1; shift ;;
    esac
done
[ -n "$V" ] || { usage; }
printf '%s' "$V" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || die "version must look like vX.Y.Z (got: $V)"

[ "$EUID" = 0 ] || [ "$DRY_RUN" = 1 ] || die "root is required (urmailctl upgrade needs it)"
git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "must run from the urMail checkout"
[ -f compose.yaml ] || die "compose.yaml not found — run from the checkout root"

# ---- 1. clean tree (plan §1.8: refuse dirty trees) -----------------------
[ -z "$(git status --porcelain)" ] || die "work tree is dirty; commit or stash before upgrading"
log "work tree clean at $(git rev-parse HEAD)"

# ---- 2. tag available + resolvable (read-only) ---------------------------
if ! git rev-parse -q --verify "refs/tags/$V" >/dev/null; then
    if [ "$DRY_RUN" = 1 ]; then
        printf '[dry-run] would run: git fetch origin --tags (tag %s not local yet)\n' "$V"
    else
        git fetch origin --tags
    fi
fi
git rev-parse -q --verify "refs/tags/$V" >/dev/null || die "tag $V not found after fetch"
tag_commit=$(git rev-parse "$V^{commit}")
log "tag $V resolves to $tag_commit"

docker version >/dev/null 2>&1 || die "docker not reachable"
command -v urmailctl >/dev/null 2>&1 || [ -n "${URMAIL_ENV_FILE:-}" ] || \
    warn "urmailctl not on PATH — fence/release steps will fail unless URMAIL_ENV_FILE points at the runtime env"

if [ "$DRY_RUN" = 1 ]; then
    cat <<PLAN
[dry-run] plan (gate order per plan §1.4):
  1. urmailctl upgrade fence --reason "compose upgrade $V"
       (built-in fresh-backup pre-flight must PASS; a rejection aborts here)
  2. git fetch origin --tags && git checkout $V   (HEAD == $tag_commit)
  3. docker compose build
  4. URMAIL_AUTO_MIGRATE=0 docker compose up -d
  5. docker compose run --rm --entrypoint /usr/local/bin/urmailctl urmaild migrate up
  6. readiness probe: compose exec urmaild curl /api/v1/healthz (max 5m)
  7. urmailctl upgrade readiness-record
  8. urmailctl upgrade release        (fence window = steps 1..8, target < 60s
       on a warm build cache; first run includes the build and is longer)
  failure after step 1: automatic resume -> release (break-glass last) and an
  evidence file under \${TMPDIR:-/tmp}
PLAN
    exit 0
fi

# ---- gated section: everything below touches the running stack -----------
FENCED=0
RELEASED=0
EVIDENCE="${TMPDIR:-/tmp}/urmail-upgrade-$V-$(date -u +%Y%m%dT%H%M%SZ).log"
T0=0

note() { printf '[upgrade] %s\n' "$*" | tee -a "$EVIDENCE" >&2; }

finish_failure() { # trap handler: never leave writers fenced without trying
    rc=$?
    if [ "$FENCED" = 1 ] && [ "$RELEASED" = 0 ]; then
        note "FAILURE (rc=$rc) inside the fence — automatic release attempt"
        urmailctl upgrade resume >>"$EVIDENCE" 2>&1 || true
        if urmailctl upgrade release >>"$EVIDENCE" 2>&1; then
            note "release ok (clean) — old stack state preserved, evidence: $EVIDENCE"
        elif urmailctl upgrade release --break-glass --ack-risk >>"$EVIDENCE" 2>&1; then
            note "release ok (break-glass) — evidence: $EVIDENCE"
        else
            note "RELEASE FAILED — writers still held; escalate with $EVIDENCE"
        fi
    fi
    exit "$rc"
}
trap finish_failure ERR
trap 'note "interrupted"; exit 130' INT TERM

# ---- step 1: fence (fresh-backup pre-flight is built into this command) --
T0=$(date +%s)
note "fence start $(date -u +%Y-%m-%dT%H:%M:%SZ)"
urmailctl upgrade fence --reason "compose upgrade $V" 2>&1 | tee -a "$EVIDENCE"
FENCED=1
note "fence established; fresh-backup pre-flight verdict above (a rejection aborts before this point)"
urmailctl upgrade status | head -4 | tee -a "$EVIDENCE"

# ---- step 2: checkout the tag --------------------------------------------
git fetch origin --tags >>"$EVIDENCE" 2>&1 || note "git fetch failed; using local tags"
git checkout "$V" >>"$EVIDENCE" 2>&1
[ "$(git rev-parse HEAD)" = "$tag_commit" ] || die "HEAD $(git rev-parse HEAD) != tag $V ($tag_commit)"
note "checked out $V ($tag_commit)"

# ---- step 3/4: build + up (auto-migrate off on the release path) ---------
docker compose build >>"$EVIDENCE" 2>&1
URMAIL_AUTO_MIGRATE=0 docker compose up -d >>"$EVIDENCE" 2>&1
note "image rebuilt and stack updated (AUTO_MIGRATE=0)"

# ---- step 5: controlled migration inside the fence -----------------------
docker compose run --rm --entrypoint /usr/local/bin/urmailctl urmaild migrate up >>"$EVIDENCE" 2>&1
note "migrations applied under the fence"

# ---- step 6: readiness probe --------------------------------------------
deadline=$(( $(date +%s) + 300 ))
until docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz \
        | grep -q '"status":"ok"'; do
    [ "$(date +%s)" -lt "$deadline" ] || die "readiness probe timed out (5m)"
    sleep 5
done
note "readiness ok"

# ---- step 7/8: readiness-record + release --------------------------------
urmailctl upgrade readiness-record >>"$EVIDENCE" 2>&1
urmailctl upgrade release >>"$EVIDENCE" 2>&1
RELEASED=1
T1=$(date +%s)
note "fence released; window $((T1 - T0))s (fence $(date -u -d "@$T0" +%H:%M:%SZ) -> $(date -u -d "@$T1" +%H:%M:%SZ))"
log "upgrade to $V complete; evidence: $EVIDENCE"
