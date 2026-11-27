#!/usr/bin/env bash
# Phase 2 upgrade-chain acceptance suite (QA evidence, CI runner).
# A preflight reject | D happy | D2 back-to-back | E post-up hold kept
# C1a absent skip | C1b start failure | R1 fail-closed no-switch
# C2 uid5000 lock | orphan hold
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 9

V_GOOD=v9.9.9
export URMAIL_DATA_ROOT="$PWD/data"
export URMAIL_WRITERS="postfix dovecot"
PASS=0
FAIL=0
SUMMARY=""
EV_ARCHIVE="$PWD/evidence-archive"

# Rig fixture spool (QA revival, run 45989953746): the fence preflight reads
# the newest generation from the image-hardcoded /var/backups/urmail inside
# the one-off container. Expose the suite's writable fixture spool there via
# a project-scoped override file (gitignored; COMPOSE_FILE keeps the base
# project definition untouched).
RIG_OVERRIDE="$PWD/compose.rig.yml"
export COMPOSE_FILE="compose.yaml:compose.rig.yml"
cat > "$RIG_OVERRIDE" <<'YAML'
services:
  urmaild:
    volumes:
      - ${URMAIL_DATA_ROOT:-./data}/backup:/var/backups/urmail:ro
YAML

record() { SUMMARY="$SUMMARY$1"$'\n'; }
assert() {
    local label=$1 got=$2 want=$3
    if [ "$got" = "$want" ]; then PASS=$((PASS+1)); record "PASS $label got=$got"
    else FAIL=$((FAIL+1)); record "FAIL $label got=$got want=$want"; fi
}
ev_count() { cat "$URMAIL_DATA_ROOT"/upgrade-evidence-"$V_GOOD"-*.log 2>/dev/null | grep -c "$1"; }
# path_reached <marker>: occurrences of an upgrade-chain path marker in the
# current scenario's evidence (same basis as ev_count). Introduced when the
# D/E fence-path assertions landed (QA #27) — the helper itself was missing
# from every prior commit, which made both assertions fail with got="".
path_reached() { ev_count "$1"; }
archive_ev() { # archive <label>
    local lbl=${1:-misc}
    mkdir -p "$EV_ARCHIVE/$lbl"
    for f in "$URMAIL_DATA_ROOT"/upgrade-evidence-*.log; do
        [ -f "$f" ] && cp "$f" "$EV_ARCHIVE/$lbl/"
    done
    rm -f "$URMAIL_DATA_ROOT"/upgrade-evidence-*.log
}
prep_dirs() {
    install -d -o root -g root -m 0750 "$URMAIL_DATA_ROOT/maintenance"
    for d in db mail certs config backup holds; do
        install -d -o 5000 -g 5000 -m 0750 "$URMAIL_DATA_ROOT/$d"
    done
    install -d -m 0700 "$URMAIL_DATA_ROOT/db/pg"
    chown -R 70:70 "$URMAIL_DATA_ROOT/db/pg"
}
reset_stack() {
    docker compose down -v --remove-orphans --timeout 0 >/dev/null 2>&1 || true
    rm -rf "$URMAIL_DATA_ROOT/db/pg"
    prep_dirs
    docker compose up -d pg urmaild postfix dovecot --wait >/dev/null 2>&1
    # Schema readiness: urmaild's boot auto-migrate races the first mock_row /
    # clear_rows after a fresh pg volume; wait it out (90s cap).
    local i=0
    until docker compose exec -T pg psql -U urmail -d urmail -q -c "SELECT 1 FROM backup_runs LIMIT 1" >/dev/null 2>&1; do
        i=$((i + 1))
        [ "$i" -ge 90 ] && break
        sleep 1
    done
    rm -f "$URMAIL_DATA_ROOT"/upgrade-evidence-*.log
}
mock_row() {
    # 0160 added offsite_state; the fence preflight (main binary) demands
    # 'uploaded' (or pending_local_only) on the newest successful run, a
    # fresh pg has no schema until urmaild's boot auto-migrate lands, and the
    # preflight also verifies the recorded generation on disk (manifest plus
    # the complete dump/config/mail artifact set, sha256-checked). Build the
    # full fixture generation, then record the row.
    local i=0
    until docker compose exec -T pg psql -U urmail -d urmail -q -c "SELECT 1 FROM backup_runs LIMIT 1" >/dev/null 2>&1; do
        i=$((i + 1))
        if [ "$i" -ge 90 ]; then
            echo "mock_row: backup_runs schema never appeared; preflight verdicts will not be deterministic" >&2
            return 1
        fi
        sleep 1
    done
    local stamp dump conf mail manifest bdir
    stamp=$(date -u +%Y%m%dT%H%M%SZ)
    bdir="$URMAIL_DATA_ROOT/backup"
    rm -rf "$bdir"
    mkdir -p "$bdir"
    dump="urmail-$stamp.dump"
    conf="urmail-config-$stamp.tar.gz"
    mail="urmail-mail-$stamp.tar.gz"
    manifest="urmail-$stamp.manifest.json"
    printf 'rig fixture payload: %s\n' "$dump" > "$bdir/$dump"
    printf 'rig fixture payload: %s\n' "$conf" > "$bdir/$conf"
    printf 'rig fixture payload: %s\n' "$mail" > "$bdir/$mail"
    {
        printf '{"version":2,"artifacts":['
        printf '{"name":"%s","kind":"pg_dump","size":%s,"sha256":"%s"},' \
            "$dump" "$(stat -c %s "$bdir/$dump")" "$(sha256sum "$bdir/$dump" | cut -d' ' -f1)"
        printf '{"name":"%s","kind":"config_tar","size":%s,"sha256":"%s"},' \
            "$conf" "$(stat -c %s "$bdir/$conf")" "$(sha256sum "$bdir/$conf" | cut -d' ' -f1)"
        printf '{"name":"%s","kind":"mail_tar","size":%s,"sha256":"%s"}' \
            "$mail" "$(stat -c %s "$bdir/$mail")" "$(sha256sum "$bdir/$mail" | cut -d' ' -f1)"
        printf ']}\n'
    } > "$bdir/$manifest"
    docker compose exec -T pg psql -U urmail -d urmail -q -c \
        "DELETE FROM backup_runs; INSERT INTO backup_runs (run_uid,state,scope,started_at,finished_at,error,manifest_name,offsite_uri,offsite_state) VALUES ('mock-fresh-run-1','success','local-mail-and-metadata',now(),now(),'', '$manifest','s3://mock/urmail','uploaded')"
}
clear_rows() {
    docker compose exec -T pg psql -U urmail -d urmail -q -c "DELETE FROM backup_runs" 2>/dev/null || true
}
# E5 #12: main's upgrade.sh keeps the protocol hold under the unified
# maintenance/ dir (the shadow holds/ dir is retired).
hold_state() { [ -f "$URMAIL_DATA_ROOT/maintenance/upgrade.hold" ] && echo yes || echo no; }
# port_occupy <port> : block a dovecot bind with a python listener.
# Ubuntu runner python3 is guaranteed; dovecot must be stopped first so the
# port is actually free when we bind it. N1 (PM): wait until the port is
# REALLY bound (connect probe, 2s cap) and the listener is alive; a silent
# no-op would make E/C1b assertions meaningless.
port_occupy() {
    local port=$1 inj i=0
    python3 -c 'import socket,time,sys
s=socket.socket(); s.bind(("127.0.0.1",int(sys.argv[1]))); s.listen(1); time.sleep(600)' "$port" >/dev/null 2>&1 &
    inj=$!
    until python3 -c "import socket,sys; socket.create_connection(('127.0.0.1',int(sys.argv[1])),0.5).close()" "$port" 2>/dev/null; do
        i=$((i+1))
        if [ "$i" -ge 20 ] || ! kill -0 "$inj" 2>/dev/null; then
            echo "INJECTION_FAILURE: port $port never bound (listener dead/timeout)" >&2
            kill "$inj" 2>/dev/null
            return 1
        fi
        sleep 0.1
    done
    echo "$inj"
}
port_release() { [ -n "${1:-}" ] && kill "$1" 2>/dev/null; true; }

echo "==================== A: preflight rejection ======================"
reset_stack
clear_rows
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "A_rc" "$rc" "4"
assert "A_hold" "$(hold_state)" "no"
assert "A_urmaild" "$(docker inspect --format '{{.State.Status}}' "$(docker compose ps -q urmaild)")" "running"
assert "A_reject_in_evidence" "$(ev_count 'no offsite backup run recorded yet')" "1"
archive_ev "A"

echo "==================== D: happy path upgrade ======================="
reset_stack
mock_row
out=$(URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
echo "$out" | head -4
assert "D_path_reached" "$(path_reached 'native fence: hold written')" "1"
assert "D_rc" "$rc" "0"
assert "D_hold" "$(hold_state)" "no"
assert "D_healthz" "$(docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz 2>/dev/null | grep -c '"status":"ok"')" "1"
assert "D_started_note" "$(ev_count 'writers started, hold cleared')" "1"
assert "D_postfix_running" "$(docker compose ps --status running -q postfix | wc -l)" "1"
assert "D_dovecot_running" "$(docker compose ps --status running -q dovecot | wc -l)" "1"
archive_ev "D"

echo "==================== D2: back-to-back (no reset) ================"
rm -f "$URMAIL_DATA_ROOT"/upgrade-evidence-*.log
mock_row
out=$(URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "D2_rc" "$rc" "0"
assert "D2_hold" "$(hold_state)" "no"
assert "D2_healthz" "$(docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz 2>/dev/null | grep -c '"status":"ok"')" "1"
archive_ev "D2"

echo "==================== E: post-up failure keeps hold ================"
reset_stack
mock_row
docker compose stop dovecot >/dev/null 2>&1 || true
INJ_PID=$(port_occupy 10143) || INJ_PID=""
assert "E_injection_ready" "$([ -n "$INJ_PID" ] && echo yes || echo no)" "yes"
e_restarts_pre=$(docker inspect --format "{{.RestartCount}}" "$(docker compose ps -q dovecot)" 2>/dev/null || echo 0)
# ALLOW_RECORDLESS: E targets the post-up WRITER-START failure path; without
# the switch readiness-record fails closed first and the injection is dead
# code (same fail-closed default R1 covers).
out=$(URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
port_release "$INJ_PID"
printf '%s\n' "$out" | grep -E "not running after start|post-up failure|writers failed" | head -3
assert "E_path_fence" "$(path_reached 'native fence: hold written')" "1"
# E failure assertions restored (PR #168/2642e146 landed b3 settle + D2
# set -E): the product writer-start check is RestartCount-based with a
# settling window and finish_failure is reachable, so post-up failure
# semantics are deterministic again. The RestartCount delta stays as a
# deterministic injection-effect witness (baseline at injection).
assert "E_rc" "$([ "$rc" != 0 ] && echo nonzero || echo zero)" "nonzero"
assert "E_hold" "$(hold_state)" "yes"
assert "E_runbook" "$(ev_count 'Rollback runbook')" "1"
assert "E_no_started_note" "$(ev_count 'writers started, hold cleared')" "0"
e_restarts_post=$(docker inspect --format "{{.RestartCount}}" "$(docker compose ps -q dovecot)" 2>/dev/null || echo 0)
assert "E_injection_effect" "$([ "$e_restarts_post" -gt "$e_restarts_pre" ] && echo yes || echo no)" "yes"
rm -f "$URMAIL_DATA_ROOT/maintenance/upgrade.hold"
archive_ev "E"

echo "==================== C1a: absent writer skip ====================="
reset_stack
mock_row
docker compose rm -sf dovecot >/dev/null 2>&1 || true
# ALLOW_RECORDLESS: C1a must exercise the ABSENT-WRITER skip through to a
# successful completion; without the switch the readiness-record fails first.
out=$(URMAIL_SMOKE_RIG=1 URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "C1a_rc" "$rc" "0"
assert "C1a_skip_noted" "$(ev_count 'absent from this rig')" "1"
assert "C1a_hold" "$(hold_state)" "no"
archive_ev "C1a"

echo "==================== C1b: writer start failure ===================="
reset_stack
mock_row
docker compose stop dovecot >/dev/null 2>&1 || true
INJ_PID=$(port_occupy 10143) || INJ_PID=""
assert "C1b_injection_ready" "$([ -n "$INJ_PID" ] && echo yes || echo no)" "yes"
c1b_restarts_pre=$(docker inspect --format "{{.RestartCount}}" "$(docker compose ps -q dovecot)" 2>/dev/null || echo 0)
# ALLOW_RECORDLESS: C1b targets the writer-start failure path (see E).
out=$(URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
port_release "$INJ_PID"
printf '%s\n' "$out" | grep -E "not running after start|writers failed" | head -2
# b3 (PR #168/2642e146) settled the product writer-start check; original
# assertions restored. Keep the RestartCount delta as a deterministic
# injection-effect witness (baseline captured at injection).
assert "C1b_rc" "$([ "$rc" != 0 ] && echo nonzero || echo zero)" "nonzero"
assert "C1b_hold" "$(hold_state)" "yes"
assert "C1b_not_running" "$(ev_count 'not running after start')" "1"
assert "C1b_no_started_note" "$(ev_count 'writers started, hold cleared')" "0"
c1b_restarts_post=$(docker inspect --format "{{.RestartCount}}" "$(docker compose ps -q dovecot)" 2>/dev/null || echo 0)
assert "C1b_injection_effect" "$([ "$c1b_restarts_post" -gt "$c1b_restarts_pre" ] && echo yes || echo no)" "yes"
rm -f "$URMAIL_DATA_ROOT/maintenance/upgrade.hold"
archive_ev "C1b"

echo "==================== R1: fail-closed (no rig switch) ============="
# TEMPORARY SENTINEL (N2, PM #26): asserts the CURRENT known gap —
# urmailctl readiness-record requires a v2 hold+receipt pair (bound digests)
# plus agent prepared-release evidence at fence time; the compose-native
# fence produces neither, so the DEFAULT path must fail closed. When the
# readiness-record fix lands (options A/B, PM ruling pending), this scenario
# MUST flip to: rc=0, hold=cleared, and a red R1 then is a fixture
# regression rather than a product failure.
unset URMAIL_ALLOW_RECORDLESS_RIG
reset_stack
mock_row
echo "R1 switch=${URMAIL_ALLOW_RECORDLESS_RIG:-unset}; sentinel=expected-fail until readiness-record fix (then rc=0/hold=cleared)" | tee -a "$URMAIL_DATA_ROOT"/upgrade-evidence-r1-selfcert.log
mock_row
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
printf '%s\n' "$out" | grep -E "read live upgrade fence|readiness-record" | head -3
assert "R1_rc" "$([ "$rc" != 0 ] && echo nonzero || echo zero)" "nonzero"
assert "R1_hold" "$(hold_state)" "yes"
assert "R1_record_failed" "$(ev_count 'read live upgrade fence')" "1"
rm -f "$URMAIL_DATA_ROOT/maintenance/upgrade.hold"
archive_ev "R1"

echo "==================== C2: uid5000 maintenance lock ================"
reset_stack
mock_row
URMAIL_ALLOW_RECORDLESS_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" >/dev/null 2>&1
lock_probe=$(docker compose run --rm --no-deps --user 5000:5000 \
    --entrypoint /bin/sh urmaild \
    -c 'exec 9<>/srv/urmail/data/maintenance/operation.lock && echo LOCK_RDWR_OK' 2>&1)
assert "C2_lock" "$(printf '%s' "$lock_probe" | grep -c LOCK_RDWR_OK)" "1"
assert "C2_owner" "$(docker compose run --rm --no-deps --user 5000:5000 \
    --entrypoint /bin/stat urmaild -c '%u:%g' /srv/urmail/data/maintenance 2>/dev/null)" "5000:5000"
archive_ev "C2"

echo "==================== orphan hold check ==========================="
docker compose down --remove-orphans --timeout 0 >/dev/null 2>&1 || true
assert "orphan_hold" "$(hold_state)" "no"

echo "==================== SUMMARY ======================================"
printf '%s\n' "$SUMMARY"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = 0 ] && echo "RIG_ACCEPTANCE_ALL_PASS" || echo "RIG_ACCEPTANCE_HAS_FAILURES"
exit "$([ "$FAIL" = 0 ] && echo 0 || echo 1)"
