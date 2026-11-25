#!/bin/sh
# urMail compose skeleton entrypoint (Phase 2).
# Only the urmaild container manages shared secret/config state (QA review
# 2026-11-19): postfix/dovecot boot through this image untouched — they never
# create or modify the master key or the env file. That removes the race
# where a root-started postfix would leave a root-owned 0600 key/env that
# uid-5000 urmaild could not read.
#   - urmaild: master key (exclusive create, atomic), env file, SQLite first
#     boot (real schema chain), DKIM key, one-time admin bootstrap URL, PG
#     migrations (PG remains the runtime data plane on this branch until the
#     Phase 1 SQLite store merges).
set -eu

# Hold gate (QA final ③): writer containers must not start while a compose
# upgrade hold marker exists — a manual `compose start` cannot bypass the
# fence. urmaild deliberately sets URMAIL_HOLD_GUARD=0 (probe/readiness need
# it up during the hold). The marker lives in the (read-only) data root.
hold_guard="${URMAIL_HOLD_GUARD:-1}"
hold_file="${URMAIL_HOLD_FILE:-/srv/urmail/holds/upgrade.hold}"
if [ "$hold_guard" = "1" ] && [ -f "$hold_file" ]; then
    while [ -f "$hold_file" ]; do
        echo "[entrypoint] upgrade hold active ($hold_file); waiting"
        sleep 5
    done
    echo "[entrypoint] hold cleared; continuing"
fi

cmd="${1:-}"

# --------------------------------------------------------------------------
# Single-writer lease (P3-DW-01).  Exactly one urmaild may own a data root.
# The lease is an exclusive flock(2) on <DATA_ROOT>/.urmail-writer.lease; the
# kernel drops the lock when the holder dies, so a crashed writer is taken
# over on the next boot while a LIVE writer can never be preempted.  The lock
# fd stays open across `exec "$@"` and is released by the kernel on SIGTERM.
# Metadata (pid/boot_id/version/started_at/container) is written for the
# operator and for the install.sh/upgrade.sh pre-flight.
# --------------------------------------------------------------------------
acquire_writer_lease() {
    lease="${URMAIL_WRITER_LEASE:-/srv/urmail/data-root/.urmail-writer.lease}"
    boot_id=$(cat /proc/sys/kernel/random/boot_id 2>/dev/null || echo unknown)
    version="${URMAIL_VERSION:-unknown}"
    container="${URMAIL_WRITER_NAME:-$(cat /etc/hostname 2>/dev/null || echo unknown)}"
    mkdir -p "$(dirname "$lease")" 2>/dev/null || true
    # fd 9 is deliberately NOT close-on-exec: the lock must live as long as
    # the daemon does, and the kernel releases it when the process exits.
    if ! exec 9>>"$lease"; then
        echo "[lease] FATAL: cannot open lease $lease" >&2
        exit 73
    fi
    if ! flock -n 9; then
        holder=$(tr '\n' ' ' < "$lease" 2>/dev/null || true)
        echo "[lease] REFUSED: data root already has a live writer: ${holder:-metadata unreadable}" >&2
        echo "[lease] lease=$lease; refusing to start a second writer (fail-closed)" >&2
        exit 75
    fi
    # Explicit contract for the product binary (urmaild): it validates the
    # inherited descriptor instead of locking a second time.  fd 9 stays open
    # across exec, so the daemon owns the lock for its whole lifetime.
    export URMAIL_WRITER_LEASE_FD=9
    echo "[lease] exported URMAIL_WRITER_LEASE_FD=9 (fd open, lock held)"
    prev=$(cat "$lease" 2>/dev/null || true)
    prev_pid=$(printf '%s\n' "$prev" | sed -n 's/^pid=//p')
    prev_boot=$(printf '%s\n' "$prev" | sed -n 's/^boot_id=//p')
    if [ -n "$prev_pid" ]; then
        if [ "$prev_boot" != "$boot_id" ]; then
            echo "[lease] stale holder (boot_id ${prev_boot:-none} != $boot_id); taking over"
        elif ! kill -0 "$prev_pid" 2>/dev/null; then
            echo "[lease] stale holder (pid $prev_pid gone); taking over"
        else
            echo "[lease] previous holder pid $prev_pid not visible here; lock was free, taking over"
        fi
    fi
    : > "$lease"
    {
        printf 'pid=%s\n' "$$"
        printf 'boot_id=%s\n' "$boot_id"
        printf 'version=%s\n' "$version"
        printf 'started_at=%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
        printf 'container=%s\n' "$container"
    } >&9
    echo "[lease] acquired $lease pid=$$ version=$version container=$container"
}


# --------------------------------------------------------------------------
# First boot (ARCH-PLAN §1.3): real SQLite DB from the Phase 1 schema chain,
# DKIM key, one-time admin bootstrap URL.  Idempotent: every step checks the
# existing artifact first.  All paths live under the bind-mounted data root.
# --------------------------------------------------------------------------
init_sqlite() {
    db="${URMAIL_SQLITE_PATH:-/srv/urmail/data/db/urmail.sqlite}"
    mkdir -p "$(dirname "$db")"
    # The migration authority is the product binary (internal/sqlitedb); the
    # entrypoint holds no DDL, version number or migration file-name literals.
    if ! migration=$(URMAIL_SQLITE_PATH="$db" "${URMAIL_CTL:-urmailctl}" sqlite migrate up 2>&1); then
        printf '%s\n' "$migration" >&2
        echo "[entrypoint] SQLite migration failed; refusing to start" >&2
        exit 1
    fi
    printf '%s\n' "$migration"
    expected=$(printf '%s\n' "$migration" | sed -n 's/^sqlite_migrate_ok user_version=[0-9][0-9]* expected=\([0-9][0-9]*\)$/\1/p')
    version=$(sqlite3 "$db" 'PRAGMA user_version;')
    if [ -z "$expected" ] || [ "$version" != "$expected" ]; then
        echo "[entrypoint] SQLite user_version=$version does not match the migrator expected=${expected:-unreported}; refusing to start" >&2
        exit 1
    fi
    integrity=$(sqlite3 "$db" 'PRAGMA integrity_check;')
    if [ "$integrity" != "ok" ]; then
        echo "[entrypoint] SQLite integrity_check=$integrity; refusing to start" >&2
        exit 1
    fi
    tables=$(sqlite3 "$db" "SELECT count(*) FROM sqlite_master WHERE type='table';")
    echo "[entrypoint] sqlite ready path=$db user_version=$version tables=$tables integrity_check=$integrity"
    export URMAIL_SQLITE_PATH="$db"
}

if [ "$cmd" = "postfix" ] || [ "$cmd" = "dovecot" ]; then
    rendered_dir="${URMAIL_RENDERED_CONFIG_DIR:-/srv/urmail/data/config}"
    # Rendered config hook:
    # - URMAIL_RENDERED_CONFIG=1 (require mode): require non-empty rendered
    #   artifacts under $rendered_dir/<svc>; fail closed (exit 1) and redirect
    #   default placeholder -c paths to $rendered_dir so placeholder & rendered
    #   configs can never silently mix.
    # - URMAIL_RENDERED_SOURCE=render (with URMAIL_RENDERED_CONFIG=1): invoke
    #   `urmailctl config prepare --first-boot` first, then run the same
    #   non-empty assertions; any generation failure or missing file exits 1.
    if [ "${URMAIL_RENDERED_CONFIG:-0}" = "1" ]; then
        if [ "${URMAIL_RENDERED_SOURCE:-require}" = "render" ]; then
            echo "[entrypoint] URMAIL_RENDERED_SOURCE=render: running urmailctl config prepare --first-boot"
            if ! "${URMAIL_CTL:-urmailctl}" config prepare --first-boot; then
                echo "[entrypoint] FATAL: urmailctl config prepare --first-boot failed (fail-closed)" >&2
                exit 1
            fi
        fi
        if [ "$cmd" = "postfix" ]; then
            for req in "$rendered_dir/postfix/main.cf" "$rendered_dir/postfix/master.cf"; do
                if [ ! -s "$req" ]; then
                    echo "[entrypoint] FATAL: URMAIL_RENDERED_CONFIG=1 requires non-empty $req (fail-closed)" >&2
                    exit 1
                fi
            done
        elif [ "$cmd" = "dovecot" ]; then
            if [ ! -s "$rendered_dir/dovecot/dovecot.conf" ]; then
                echo "[entrypoint] FATAL: URMAIL_RENDERED_CONFIG=1 requires non-empty $rendered_dir/dovecot/dovecot.conf (fail-closed)" >&2
                exit 1
            fi
        fi
    fi
    # Canary inbound 4xx deferral switch (default off). Fail closed until
    # postfix 4xx defer config generation/wiring is implemented (no silent accept/5xx).
    if [ "$cmd" = "postfix" ] && [ "${URMAIL_CANARY_INBOUND_DEFER:-0}" = "1" ]; then
        defer_cfg="$rendered_dir/postfix/canary-defer.cf"
        if [ ! -s "$defer_cfg" ]; then
            echo "[entrypoint] FATAL: URMAIL_CANARY_INBOUND_DEFER=1 requested, but $defer_cfg is absent (TODO: wire postfix 4xx defer config; refusing to start fail-closed)" >&2
            exit 1
        fi
    fi
    if [ "${URMAIL_RENDERED_CONFIG:-0}" = "1" ]; then
        if [ "$cmd" = "postfix" ] && [ "${*}" = "postfix -c /srv/urmail/config/postfix start-fg" ]; then
            echo "[entrypoint] URMAIL_RENDERED_CONFIG=1: redirecting postfix -c to $rendered_dir/postfix"
            exec postfix -c "$rendered_dir/postfix" start-fg
        elif [ "$cmd" = "dovecot" ] && [ "${*}" = "dovecot -F -c /srv/urmail/config/dovecot/dovecot.conf" ]; then
            echo "[entrypoint] URMAIL_RENDERED_CONFIG=1: redirecting dovecot -c to $rendered_dir/dovecot/dovecot.conf"
            exec dovecot -F -c "$rendered_dir/dovecot/dovecot.conf"
        fi
    fi
fi

if [ "$cmd" = "urmaild" ] || [ "$cmd" = "serve" ]; then
    # P3-DW-01: take the data-root writer lease BEFORE touching the DB, the
    # Maildir or the rendered config.  A live holder makes this container exit
    # non-zero instead of becoming a second writer.
    acquire_writer_lease
    CONFIG_DIR="${URMAIL_CONFIG_DIR:-/srv/urmail/config}"
    mkdir -p "$CONFIG_DIR"
    key="$CONFIG_DIR/master.key"

    if [ ! -f "$key" ]; then
        # Exclusive + atomic create (QA final): build the key in a tmp file
        # (a killed download leaves only a harmless tmp), then mv -n — atomic
        # rename that never overwrites the winner of a concurrent race.
        tmp="$CONFIG_DIR/master.key.tmp.$$"
        umask 077
        head -c 32 /dev/urandom > "$tmp"
        mv -n "$tmp" "$key"
        umask 022
        if [ -f "$tmp" ]; then
            rm -f "$tmp"
            echo "[entrypoint] master key already present (lost exclusive create)"
        else
            echo "[entrypoint] generated master key at $key"
        fi
    fi
    # Owner self-heal runs on every boot (QA final): a key/env created by an
    # older root-run entrypoint must become readable/writable by uid 5000.
    if [ "$(id -u)" = "0" ]; then
        for f in "$key" "$CONFIG_DIR/urmail.env"; do
            if [ -f "$f" ] && [ "$(stat -c %u "$f")" = "0" ]; then
                chown 5000:5000 "$f"
                echo "[entrypoint] healed owner of $f (was root)"
            fi
        done
    fi
    export URMAIL_MASTER_KEY_PATH="${URMAIL_MASTER_KEY_PATH:-$key}"
    export URMAIL_ENV_FILE="${URMAIL_ENV_FILE:-$CONFIG_DIR/urmail.env}"
    if [ ! -f "$URMAIL_ENV_FILE" ]; then
        : > "$URMAIL_ENV_FILE"
        chmod 600 "$URMAIL_ENV_FILE"
    fi
    # Engine selection is explicit and fail-closed.  DB_ENGINE wins; otherwise
    # any PostgreSQL DSN selects the PG assembly, and the absence of all of
    # them selects the SQLite assembly.  The two migrations never run for the
    # other engine.
    engine="${DB_ENGINE:-}"
    if [ -z "$engine" ]; then
        if [ -n "${URMAIL_DATABASE_URL_MIGRATE:-}${URMAIL_DATABASE_URL_PLATFORM:-}${URMAIL_DATABASE_URL:-}" ]; then
            engine=postgres
        else
            engine=sqlite
        fi
    fi
    case "$engine" in
        sqlite)
            if [ -n "${URMAIL_DATABASE_URL_MIGRATE:-}${URMAIL_DATABASE_URL_PLATFORM:-}${URMAIL_DATABASE_URL:-}" ]; then
                echo "[entrypoint] engine=sqlite conflicts with PostgreSQL database URLs; refusing to start" >&2
                exit 1
            fi
            init_sqlite
            ;;
        postgres)
            if [ -n "${URMAIL_SQLITE_PATH:-}" ]; then
                echo "[entrypoint] engine=postgres; ignoring URMAIL_SQLITE_PATH (no SQLite assembly in this container)"
                unset URMAIL_SQLITE_PATH
            fi
            if [ "${URMAIL_AUTO_MIGRATE:-1}" = "1" ] && \
               { [ -n "${URMAIL_DATABASE_URL_MIGRATE:-}" ] || [ -n "${URMAIL_DATABASE_URL_PLATFORM:-}" ]; }; then
                echo "[entrypoint] applying pending PG schema migrations (urmailctl migrate up)"
                urmailctl migrate up
            else
                echo "[entrypoint] PG migration skipped (engine=postgres, auto-migrate off or no migrate/platform DSN)"
            fi
            ;;
        *)
            echo "[entrypoint] unknown DB_ENGINE='$engine' (want sqlite or postgres); refusing to start" >&2
            exit 1
            ;;
    esac
fi

exec "$@"
