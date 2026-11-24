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

# DATA_ROOT must be on a LOCAL filesystem: flock(2) is only reliable there.
# Network filesystems (NFS/SMB/9p/sshfs/…) may silently allow two holders, so
# the lease above would be worthless.  FAIL-CLOSED: if the filesystem type
# cannot be determined (no findmnt, no usable /proc/self/mountinfo entry) the
# caller must not proceed.  URMAIL_MOUNTINFO overrides the mount table (tests,
# containers with a hidden /proc).
proc_mount_fstype() { # longest-prefix mountpoint match in mountinfo
    local mf="${URMAIL_MOUNTINFO:-/proc/self/mountinfo}"
    command -v awk >/dev/null 2>&1 || return 1
    [ -r "$mf" ] || return 1
    awk -v p="$1" '
        {
            mp = $5
            sep = 0
            for (i = 6; i <= NF; i++) if ($i == "-") { sep = i; break }
            if (!sep) next
            fstype = $(sep + 1)
            hit = (mp == p) || (p ~ ("^" mp "/")) || (mp == "/")
            if (hit && length(mp) > best) { best = length(mp); got = fstype }
        }
        END { if (got == "") exit 1; print got }' "$mf"
}
data_root_fs_preflight() {
    root=${1:-${DATA_ROOT:?DATA_ROOT not set}}
    probe=$(realpath -m "$root")
    while [ ! -e "$probe" ] && [ "$probe" != "/" ] && [ "$probe" != "." ]; do
        probe=$(dirname "$probe")
    done
    fs=""
    if command -v findmnt >/dev/null 2>&1; then
        fs=$(findmnt -no FSTYPE --target "$probe" 2>/dev/null | head -n1 || true)
    fi
    if [ -z "$fs" ]; then
        fs=$(proc_mount_fstype "$probe" 2>/dev/null || true)
        [ -n "$fs" ] && log "filesystem type resolved from mountinfo: $fs"
    fi
    if [ -z "$fs" ]; then
        die "cannot determine the filesystem type for $root (probe=$probe; findmnt and mountinfo both unavailable) — refusing, the local-disk check is fail-closed"
        return 4
    fi
    case "$fs" in
        nfs|nfs4|cifs|smb3|smbfs|9p|afs|ceph|glusterfs|sshfs|fuse.*)
            die "DATA_ROOT $root is on a network filesystem ($fs): flock is not reliable there; use a local block device"
            return 4
            ;;
    esac
    log "DATA_ROOT filesystem: $fs (local, flock supported): $root"
    return 0
}
