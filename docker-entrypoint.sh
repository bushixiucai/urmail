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
    migrations="${URMAIL_SQLITE_MIGRATIONS_DIR:-/usr/local/share/urmail/migrations_sqlite}"
    mkdir -p "$(dirname "$db")"
    [ -f "$db" ] || : > "$db"
    chmod 600 "$db" 2>/dev/null || true
    sqlite3 "$db" "PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL; PRAGMA foreign_keys=ON; PRAGMA busy_timeout=5000;" >/dev/null
    version=$(sqlite3 "$db" 'PRAGMA user_version;')
    case "$version" in
        0)
            echo "[entrypoint] initialising SQLite schema (0001_init)"
            sqlite3 -bail "$db" < "$migrations/0001_init.sql"
            version=1
            ;;
    esac
    if [ "$version" = "1" ]; then
        echo "[entrypoint] applying SQLite schema 0002_write_side_effects"
        sqlite3 -bail "$db" < "$migrations/0002_write_side_effects.sql"
        sqlite3 "$db" 'PRAGMA user_version = 2;'
        version=2
    fi
    if [ "$version" = "2" ]; then
        echo "[entrypoint] applying SQLite schema 0003_firstboot"
        sqlite3 -bail "$db" < "$migrations/0003_firstboot.sql"
        version=$(sqlite3 "$db" 'PRAGMA user_version;')
    fi
    if [ "$version" -lt 3 ] 2>/dev/null; then
        echo "[entrypoint] SQLite schema version $version is older than expected (>=3)" >&2
        exit 1
    fi
    integrity=$(sqlite3 "$db" 'PRAGMA integrity_check;')
    tables=$(sqlite3 "$db" "SELECT count(*) FROM sqlite_master WHERE type='table';")
    echo "[entrypoint] sqlite ready path=$db user_version=$version tables=$tables integrity_check=$integrity"
    export URMAIL_SQLITE_PATH="$db"
}

state_get() {
    sqlite3 "$URMAIL_SQLITE_PATH" "SELECT value FROM firstboot_state WHERE key='$1';"
}

state_set() {
    sqlite3 -bail "$URMAIL_SQLITE_PATH" \
        "INSERT INTO firstboot_state(key,value) VALUES('$1','$2')
         ON CONFLICT(key) DO UPDATE SET value=excluded.value,
           updated_at=(CAST(strftime('%s','now') AS INTEGER)*1000000);"
}

ensure_dkim() {
    domain="${URMAIL_PRIMARY_DOMAIN:-${URMAIL_MYHOSTNAME:-urmail.local}}"
    selector="${URMAIL_DKIM_SELECTOR:-mail}"
    dir="$CONFIG_DIR/dkim/$domain"
    keyfile="$dir/$selector.key"
    txtfile="$dir/$selector.txt"
    if [ ! -s "$keyfile" ]; then
        mkdir -p "$dir"
        umask 077
        openssl genrsa -out "$keyfile" 2048 >/dev/null 2>&1
        pub=$(openssl rsa -in "$keyfile" -pubout -outform DER 2>/dev/null | base64 | tr -d '\n')
        printf 'v=DKIM1; k=rsa; p=%s\n' "$pub" > "$txtfile"
        umask 022
        chmod 600 "$keyfile"
        chmod 644 "$txtfile"
        state_set dkim_domain "$domain"
        state_set dkim_selector "$selector"
        state_set dkim_public_txt "v=DKIM1; k=rsa; p=$pub"
        echo "[entrypoint] generated DKIM key domain=$domain selector=$selector path=$keyfile"
    else
        chmod 644 "$txtfile" 2>/dev/null || true
        echo "[entrypoint] DKIM key present domain=$domain selector=$selector path=$keyfile"
    fi
}

ensure_admin_bootstrap() {
    domain="${URMAIL_PRIMARY_DOMAIN:-${URMAIL_MYHOSTNAME:-urmail.local}}"
    url_file="$CONFIG_DIR/admin-bootstrap.url"
    existing_hash=$(state_get admin_bootstrap_token_hash 2>/dev/null || true)
    if [ -s "$url_file" ]; then
        echo "[entrypoint] admin bootstrap URL pending; urmaild will print it once"
        return 0
    fi
    if [ -n "$existing_hash" ]; then
        # urmaild removes the URL file after printing it.  A restart/upgrade
        # must NOT mint a new admin token: the hash in SQLite is the durable
        # "already issued" marker.
        echo "[entrypoint] admin bootstrap already issued; not regenerating"
        return 0
    fi
    token=$(openssl rand -hex 32)
    token_hash=$(printf '%s' "$token" | sha256sum | cut -d' ' -f1)
    state_set admin_bootstrap_token_hash "$token_hash"
    scheme="${URMAIL_PUBLIC_SCHEME:-https}"
    umask 077
    printf '%s://%s/admin/bootstrap?token=%s\n' "$scheme" "$domain" "$token" > "$url_file"
    umask 022
    chmod 600 "$url_file"
    # The token is deliberately not echoed here: urmaild logs the URL once and
    # removes the file, so the secret appears in exactly one startup log line.
    echo "[entrypoint] admin bootstrap URL issued (one-time); urmaild will print it"
}

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
    init_sqlite
    ensure_dkim
    ensure_admin_bootstrap
    if [ "${URMAIL_AUTO_MIGRATE:-1}" = "1" ]; then
        echo "[entrypoint] applying pending schema migrations (urmailctl migrate up)"
        urmailctl migrate up
    fi
fi

exec "$@"
