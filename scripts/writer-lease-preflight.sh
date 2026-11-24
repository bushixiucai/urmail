#!/bin/sh
# Single-writer lease pre-flight (P3-DW-01).  Sourced by install.sh and
# upgrade.sh; the callers define log(), warn() and die().
#
# The running urmaild holds an exclusive flock(2) on
# <DATA_ROOT>/.urmail-writer.lease for its whole lifetime.  A second stack
# against the same data root must never start, so both scripts refuse early
# and name the holder instead of letting two writers race the database.
#
# Allowed cases:
#   - no lease file yet (fresh data root)               -> proceed
#   - lease is free (no live holder; stale metadata)    -> proceed
#   - holder IS this compose stack's urmaild             -> proceed
#     (upgrade.sh stops that very container in its fence; install.sh re-runs
#      on an existing install must stay idempotent)
# Everything else is a second stack -> die() with the holder metadata.
lease_preflight() {
    lease="${1:-${DATA_ROOT:?DATA_ROOT not set}/.urmail-writer.lease}"
    if [ ! -f "$lease" ]; then
        log "writer lease absent (fresh data root): $lease"
        return 0
    fi
    if ! command -v flock >/dev/null 2>&1; then
        warn "flock missing; cannot verify the writer lease ($lease)"
        return 0
    fi
    if flock -n "$lease" true 2>/dev/null; then
        log "writer lease free: $lease"
        return 0
    fi
    holder=$(tr '\n' ' ' < "$lease" 2>/dev/null || true)
    self=$(docker compose ps -q urmaild 2>/dev/null | head -n1)
    hc=$(printf '%s\n' "$holder" | sed -n 's/.*container=\([^ ]*\).*/\1/p' | tail -n1)
    self_ok=0
    if [ -n "$self" ] && [ -n "$hc" ]; then
        case "$self" in
            "$hc"*) self_ok=1 ;;
        esac
    fi
    if [ "$self_ok" = 1 ]; then
        log "writer lease held by this stack's urmaild: $holder"
        return 0
    fi
    die "another writer holds $lease (${holder:-metadata unreadable}); refusing a second stack"
}
