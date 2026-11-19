#!/usr/bin/env bash
# urMail compose upgrader skeleton (Phase 2, ARCH-PLAN §1.4, QA round 2).
#
# IMPORTANT: the compose-form upgrade chain must NOT be used on production
# until the compose form itself passes its Phase-3 acceptance (compose/README.md).
#
# Gate order (QA round 2 design):
#   clean tree -> tag -> fresh-backup pre-flight (one-off urmailctl container;
#   reuses the binary's own pre-flight, never reimplemented; a rejection
#   aborts BEFORE anything is touched) -> NATIVE compose fence (hold marker in
#   the data root + stop urmaild/postfix/dovecot, pg stays) -> checkout tag ->
#   compose build -> URMAIL_AUTO_MIGRATE=0 up -d -> controlled migrate
#   (one-off container) -> readiness probe -> readiness-record -> start
#   writers -> clear hold.
#
# Failure semantics (QA ruling):
#   - failure before `up -d`: old stack is intact -> writers restarted, hold
#     cleared (auto resume+release equivalent);
#   - failure after `up -d` (migrate/probe/record): fence/hold is KEPT (an
#     un-migrated new stack must not receive writers) and a rollback runbook
#     is printed into the evidence file; the script never auto-releases an
#     unverified new stack.
# Exit paths all funnel through finish_failure with a re-entry guard so ERR
# and EXIT cannot double-fire the release logic. Never prints secrets.
#
# Usage: ./upgrade.sh vX [--dry-run] [-h]
set -euo pipefail

V=""
DRY_RUN=0
FENCED=0
RELEASED=0
HANDLED=0
PHASE=pre-up
HOLD=""
EVIDENCE=""
T0=0
PREV_TAG=""

log()  { printf '[upgrade] %s\n' "$*"; }
warn() { printf '[upgrade] WARNING: %s\n' "$*" >&2; }
die()  { printf '[upgrade] ABORT: %s\n' "$*" >&2; return 4; }
usage(){ sed -n '2,15p' "$0"; exit 0; }

main() {
    while [ $# -gt 0 ]; do
        case "$1" in
            --dry-run) DRY_RUN=1; shift ;;
            -h|--help) usage ;;
            -*) die "unknown flag: $1" ;;
            *)  if [ -n "$V" ]; then die "unexpected argument: $1"; fi
                V=$1; shift ;;
        esac
    done
    [ -n "$V" ] || usage
    printf '%s' "$V" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$' || die "version must look like vX.Y.Z (got: $V)"

    # Self-rewrite guard (QA): `git checkout $V` rewrites this very script
    # while bash may still be reading it (undefined behaviour). Re-exec from
    # a private copy in TMPDIR before entering the gated section.
    if [ "${URMAIL_UPGRADE_EXECED:-}" != "1" ]; then
        exec_copy="${TMPDIR:-/tmp}/upgrade-exec-$(id -u).sh"
        cp "$0" "$exec_copy"
        chmod 0755 "$exec_copy"
        export URMAIL_UPGRADE_EXECED=1
        exec bash "$exec_copy" "$@"
    fi

    [ "$EUID" = 0 ] || [ "$DRY_RUN" = 1 ] || die "root is required (container fence + docker need it)"
    git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "must run from the urMail checkout"
    [ -f compose.yaml ] || die "compose.yaml not found — run from the checkout root"

    # ---- 1. clean tree (plan §1.8) ----------------------------------------
    [ -z "$(git status --porcelain)" ] || die "work tree is dirty; commit or stash before upgrading"
    log "work tree clean at $(git rev-parse HEAD)"

    # ---- 2. tag resolvable (read-only) ------------------------------------
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
    PREV_TAG=$(git describe --tags --abbrev=0 HEAD 2>/dev/null || echo "<previous-tag>")

    docker version >/dev/null 2>&1 || die "docker not reachable"
    docker compose version >/dev/null 2>&1 || die "docker compose v2 not reachable"

    DATA_ROOT="${URMAIL_DATA_ROOT:-data}"
    HOLD="$DATA_ROOT/upgrade.hold"

    if [ "$DRY_RUN" = 1 ]; then
        cat <<PLAN
[dry-run] plan (QA round-2 semantics):
  1. native compose fence: write hold marker $HOLD,
     docker compose stop urmaild postfix dovecot (pg stays up)
  2. fresh-backup pre-flight in a one-off urmaild container (binary's own
     gate; a rejection aborts before the migration)
  3. git checkout $V (HEAD == $tag_commit); docker compose build
  4. URMAIL_AUTO_MIGRATE=0 up -d; controlled migrate (one-off urmailctl)
  5. readiness probe (healthz inside urmaild, max 5m); readiness-record
  6. start writers (postfix/dovecot), clear hold
  failure before up -d : writers restarted, hold cleared (old stack keeps serving)
  failure after  up -d : fence/hold KEPT + rollback runbook printed into the
                         evidence file (never auto-release an unverified stack)
PLAN
        exit 0
    fi

    # ---- gated section -----------------------------------------------------
    EVIDENCE="${TMPDIR:-/tmp}/urmail-upgrade-$V-$(date -u +%Y%m%dT%H%M%SZ).log"
    : > "$EVIDENCE"
    note() { printf '[upgrade] %s\n' "$*" | tee -a "$EVIDENCE" >&2; }

    # ---- 2b. fresh-backup pre-flight: one-off urmailctl container ----------
    # The binary runs its own gate first (step 0 of upgrade fence), before any
    # systemd touch — a one-off container therefore yields the verdict even
    # though the systemd parts of `fence` cannot work inside a container.
    note "fresh-backup pre-flight (one-off urmailctl container)"
    preflight_out=$(docker compose run --rm --no-deps \
        --entrypoint /usr/local/bin/urmailctl urmaild \
        upgrade fence --reason "fresh-backup pre-flight check (compose native)" 2>&1) || true
    printf '%s\n' "$preflight_out" >>"$EVIDENCE"
    if printf '%s\n' "$preflight_out" | grep -q \
        "refusing to fence.*fresh-backup pre-flight"; then
        note "fresh-backup pre-flight REJECTED the upgrade; original text above; nothing was touched"
        die "fresh-backup pre-flight rejected (see evidence)"
    fi
    if ! printf '%s\n' "$preflight_out" | grep -qE \
        "newest complete successful run|not provisioned on this installation|preflight degraded"; then
        note "pre-flight verdict unreadable (unexpected output above); aborting"
        die "pre-flight verdict unreadable"
    fi
    note "fresh-backup pre-flight passed"

    # ---- 3. native compose fence -------------------------------------------
    T0=$(date +%s)
    printf 'compose upgrade %s (%s)\n' "$V" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOLD"
    chmod 0644 "$HOLD"
    # intentional word split: URMAIL_WRITERS is a space-separated service list
    # shellcheck disable=SC2086
    docker compose stop urmaild ${URMAIL_WRITERS:-postfix dovecot} >>"$EVIDENCE" 2>&1
    FENCED=1
    note "native fence: hold written ($HOLD), writers stopped (pg kept)"

    # ---- 4. checkout the tag ------------------------------------------------
    git fetch origin --tags >>"$EVIDENCE" 2>&1 || note "git fetch failed; using local tags"
    git checkout "$V" >>"$EVIDENCE" 2>&1
    [ "$(git rev-parse HEAD)" = "$tag_commit" ] || die "HEAD $(git rev-parse HEAD) != tag $V ($tag_commit)"
    note "checked out $V ($tag_commit)"

    # ---- 5. build + up (auto-migrate off on the release path) ---------------
    docker compose build >>"$EVIDENCE" 2>&1
    # PHASE flips to post-up BEFORE up: once the new image's urmaild is being
    # started, failures must keep the fence (QA timing ruling).
    PHASE=post-up
    URMAIL_AUTO_MIGRATE=0 docker compose up -d --no-deps urmaild >>"$EVIDENCE" 2>&1
    note "new stack up (urmaild); failures from here keep the fence and print the rollback runbook"

    # ---- 6. controlled migration (one-off urmailctl) ------------------------
    docker compose run --rm --no-deps \
        --entrypoint /usr/local/bin/urmailctl urmaild migrate up >>"$EVIDENCE" 2>&1
    note "migrations applied under the fence"

    # ---- 7. readiness probe (healthz inside urmaild) ------------------------
    deadline=$(( $(date +%s) + 300 ))
    until docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz \
            | grep -q '"status":"ok"'; do
        [ "$(date +%s)" -lt "$deadline" ] || die "readiness probe timed out (5m)"
        sleep 5
    done
    note "readiness ok"

    # ---- 8. readiness-record + start writers + clear hold -------------------
    docker compose run --rm --no-deps \
        --entrypoint /usr/local/bin/urmailctl urmaild upgrade readiness-record >>"$EVIDENCE" 2>&1
    # intentional word split on the writer list (URMAIL_WRITERS, space list)
    # shellcheck disable=SC2086
    docker compose start ${URMAIL_WRITERS:-postfix dovecot} >>"$EVIDENCE" 2>&1
    rm -f "$HOLD"
    RELEASED=1
    T1=$(date +%s)
    note "writers started, hold cleared; fence window $((T1 - T0))s"
    log "upgrade to $V complete; evidence: $EVIDENCE"
}

finish_failure() {
    local rc=${1:-$?}
    [ "$HANDLED" = 1 ] && return 0
    HANDLED=1
    trap - ERR EXIT INT TERM
    if [ "$FENCED" = 1 ] && [ "$RELEASED" = 0 ]; then
        if [ "$PHASE" = "pre-up" ]; then
            {
                echo "== pre-up failure (rc=$rc): old stack intact; restoring writers"
                # intentional word split: URMAIL_WRITERS is a service list
                # shellcheck disable=SC2086
                docker compose start urmaild ${URMAIL_WRITERS:-postfix dovecot} 2>&1 || true
                rm -f "${URMAIL_DATA_ROOT:-data}/upgrade.hold" && echo "hold cleared"
                echo "old stack keeps serving; no data-plane change was made"
            } >>"$EVIDENCE" 2>&1
            note "pre-up failure: writers restored, hold cleared (old stack keeps serving); evidence: $EVIDENCE"
        else
            {
                echo "== post-up failure (rc=$rc): fence/hold KEPT by design (un-migrated new stack)"
                cat <<RUNBOOK
Rollback runbook (execute in a second maintenance window, hold stays in place):
  1. git checkout $PREV_TAG
  2. docker compose build
  3. docker compose up -d --no-deps urmaild   (migrations of the new stack may
     need manual assessment first: docker compose logs urmaild)
  4. confirm healthz ok, then start writers: docker compose start postfix dovecot
  5. clear the hold: rm -f ${URMAIL_DATA_ROOT:-data}/upgrade.hold
The script deliberately does NOT auto-release an unverified new stack.
RUNBOOK
            } >>"$EVIDENCE" 2>&1
            note "post-up failure: fence/hold KEPT; rollback runbook in $EVIDENCE"
        fi
    fi
    exit "$rc"
}
trap 'finish_failure $?' ERR
trap 'finish_failure 130' INT TERM
trap 'finish_failure $?' EXIT

main "$@"
