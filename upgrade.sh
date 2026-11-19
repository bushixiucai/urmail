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
    # Snapshot BEFORE parsing: `shift` empties "$@", and the TMPDIR re-exec
    # below must forward the original arguments.
    UPGRADE_ARGS=("$@")
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
        exec bash "$exec_copy" "${UPGRADE_ARGS[@]}"
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
    # Dedicated holds dir (QA condition): the hold marker lives in its own
    # subdirectory so writer containers can ro-mount JUST the marker without
    # gaining read access to config/master.key or the database directory.
    HOLD="$DATA_ROOT/holds/upgrade.hold"

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
    # Evidence lands in the DATA ROOT (persistent, operator-retrievable) —
    # never in TMPDIR, which dies with the invoking session.
    EVIDENCE="$DATA_ROOT/upgrade-evidence-$V-$(date -u +%Y%m%dT%H%M%SZ).log"
    : > "$EVIDENCE"
    note() { printf '[upgrade] %s\n' "$*" | tee -a "$EVIDENCE" >&2; }

    # ---- 2b. fresh-backup pre-flight: one-off urmailctl container ----------
    # The binary runs its own gate first (step 0 of upgrade fence), before any
    # systemd touch — a one-off container therefore yields the verdict even
    # though the systemd parts of `fence` cannot work inside a container.
    note "fresh-backup pre-flight (one-off urmailctl container)"
    preflight_out=$(docker compose run --rm --no-deps --user 0:0 \
        -e URMAIL_ENV_FILE=/srv/urmail/config/urmail.env \
        -e URMAIL_MAINTENANCE_ROOT=/tmp/maint \
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
    mkdir -p "$(dirname "$HOLD")"
    printf 'compose upgrade %s (%s)\n' "$V" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "$HOLD"
    chmod 0644 "$HOLD"
    # FENCED=1 must be set BEFORE the stop loop (QA P2-1): if a stop fails
    # midway, the already-stopped urmaild must be restored by the failure
    # handler rather than left behind with no fence registration.
    FENCED=1
    # intentional word split: URMAIL_WRITERS is a space-separated service list
    # shellcheck disable=SC2086
    # Fence stop: absent writer services (smoke rigs) must not abort the
    # fence; a failed stop of a PRESENT writer still dies (fail-closed).
    fence_stop_ok=1
    for w in urmaild ${URMAIL_WRITERS:-postfix dovecot}; do
        # shellcheck disable=SC2086
        if [ -z "$(docker compose ps -aq "$w" 2>/dev/null)" ]; then
            echo "fence stop: $w has no container; skipped" >>"$EVIDENCE"
            continue
        fi
        docker compose stop "$w" >>"$EVIDENCE" 2>&1 || fence_stop_ok=0
    done
    [ "$fence_stop_ok" = "1" ] || die "aborting during fence stop; hold kept"
    note "native fence: hold written ($HOLD), writers stopped (pg kept)"

    # ---- 4. checkout the tag ------------------------------------------------
    git fetch origin --tags >>"$EVIDENCE" 2>&1 || note "git fetch failed; using local tags"
    git checkout "$V" >>"$EVIDENCE" 2>&1
    [ "$(git rev-parse HEAD)" = "$tag_commit" ] || die "HEAD $(git rev-parse HEAD) != tag $V ($tag_commit)"
    note "checked out $V ($tag_commit)"

    # ---- 5. build + up (auto-migrate off on the release path) ---------------
    # Caddy render (§1.3, fail-closed): a configured renderer must succeed
    # BEFORE the new stack starts; a failure lands in evidence and the
    # failure handler keeps the fence / restores the old writers.
    if [ -n "${URMAIL_CADDY_RENDER_CMD:-}" ]; then
        note "rendering Caddyfile (URMAIL_CADDY_RENDER_CMD)"
        bash -c "$URMAIL_CADDY_RENDER_CMD" >>"$EVIDENCE" 2>&1
    fi
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
    # P1 (QA): the maintenance root must be ROOT-owned for every urmailctl
    # one-off (AcquireLock validates uid==0 when euid==0). The previous
    # upgrade's chown-back to 5000 breaks the NEXT upgrade — normalize to
    # root before each one-off, hand back to 5000 after the one-offs.
    docker compose run --rm --no-deps --user 0:0 \
        --entrypoint /bin/chown urmaild -R 0:0 \
        /srv/urmail/data/maintenance >>"$EVIDENCE" 2>&1
    # P1 (PM ruling): readiness-record is fail-closed by default. The
    # compose-native fence does not produce the urmailctl v2 hold+receipt,
    # so the record step fails; only the explicit rig switch may skip it.
    if [ "${URMAIL_ALLOW_RECORDLESS_RIG:-0}" = "1" ]; then
        note "readiness-record: skipped (URMAIL_ALLOW_RECORDLESS_RIG=1, compose-native fence has no v2 receipt; healthz probe is the readiness evidence)"
    else
        docker compose run --rm --no-deps --user 0:0 \
            -e URMAIL_ENV_FILE=/srv/urmail/config/urmail.env \
            -e URMAIL_MAINTENANCE_ROOT=/srv/urmail/data/maintenance \
            --entrypoint /usr/local/bin/urmailctl urmaild upgrade readiness-record >>"$EVIDENCE" 2>&1
    fi
    # QA P2-1: hand the dir back to uid 5000 (backup-auto/usage-sync locks)
    docker compose run --rm --no-deps --user 0:0 \
        --entrypoint /bin/chown urmaild -R 5000:5000 \
        /srv/urmail/data/maintenance >>"$EVIDENCE" 2>&1
    # QA P2-1: keep the receipt persistent, then hand the dir back to uid 5000
    # the root one-off leaves root-owned files in the 5000-owned maintenance
    # dir — hand them back so backup-auto/usage-sync (uid 5000) can lock
    docker compose run --rm --no-deps --user 0:0 \
        --entrypoint /bin/chown urmaild -R 5000:5000 \
        /srv/urmail/data/maintenance >>"$EVIDENCE" 2>&1
    # Writers: fail-closed on the production path (QA/PM ruling). A service
    # missing from the compose project is only tolerated with the explicit
    # URMAIL_SMOKE_RIG=1 switch, and every skip is recorded in the evidence.
    writers_ok=1
    writers_absent=""
    for w in ${URMAIL_WRITERS:-postfix dovecot}; do
        # shellcheck disable=SC2086
        ids=$(docker compose ps -aq "$w" 2>/dev/null)
        if [ -z "$ids" ]; then
            if [ "${URMAIL_SMOKE_RIG:-0}" = "1" ]; then
                echo "writer $w absent from this rig; skipped (URMAIL_SMOKE_RIG=1)" >>"$EVIDENCE"
                writers_absent+=" $w"
                continue
            fi
            echo "writer $w has no container; refusing (production fail-closed)" >>"$EVIDENCE"
            writers_ok=0
            continue
        fi
        docker compose start "$w" >>"$EVIDENCE" 2>&1 || writers_ok=0
    done
    # The running-assertion set excludes writers skipped under URMAIL_SMOKE_RIG=1
    # (they have no container here); the rig mode passes when the whole list
    # resolved absent, recorded in the evidence. Production fail-closed
    # semantics are unchanged.
    for w in ${URMAIL_WRITERS:-postfix dovecot}; do
        # shellcheck disable=SC2086
        case " $writers_absent " in *" $w "*) continue ;; esac
        if ! docker compose ps --status running -q "$w" 2>/dev/null | grep -q .; then
            echo "writer $w not running after start" >>"$EVIDENCE"
            writers_ok=0
        fi
    done
    if [ -n "$(printf '%s' "$writers_absent" | tr -d ' ')" ]; then
        echo "rig mode: absent writers skipped:$writers_absent (URMAIL_SMOKE_RIG=1)" >>"$EVIDENCE"
    fi
    if [ "$writers_ok" != "1" ]; then
        die "writers failed to start; fence/hold kept (post-up)"
    fi
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
                rm -f "${URMAIL_DATA_ROOT:-data}/holds/upgrade.hold" && echo "hold cleared"
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
  5. clear the hold: rm -f ${URMAIL_DATA_ROOT:-data}/holds/upgrade.hold
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
