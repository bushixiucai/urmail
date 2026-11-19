#!/usr/bin/env bash
# Phase 2 upgrade-chain acceptance suite (QA evidence, CI runner).
# Scenarios: A preflight reject | D happy path | E post-up hold kept
#            | C1a absent skip | C1b start failure | C2 uid5000 lock
# Must run as root (sudo bash scripts/upgrade-rig-acceptance.sh) from the
# compose checkout root. Each scenario resets the stack for determinism.
set -uo pipefail
cd "$(git rev-parse --show-toplevel)" || exit 9

V_GOOD=v9.9.9
export URMAIL_DATA_ROOT="$PWD/data"
export URMAIL_WRITERS="postfix dovecot"
export URMAIL_ALLOW_RECORDLESS_RIG=1
PASS=0
FAIL=0
SUMMARY=""

record() { SUMMARY="$SUMMARY$1"$'\n'; }
assert() {
    local label=$1 got=$2 want=$3
    if [ "$got" = "$want" ]; then PASS=$((PASS+1)); record "PASS $label got=$got"
    else FAIL=$((FAIL+1)); record "FAIL $label got=$got want=$want"; fi
}
ev_count() { cat "$URMAIL_DATA_ROOT"/upgrade-evidence-"$V_GOOD"-*.log 2>/dev/null | grep -c "$1"; }
clear_ev() { rm -f "$URMAIL_DATA_ROOT"/upgrade-evidence-*.log; }
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
    # include writers so production semantics are exercised end-to-end
    docker compose up -d pg urmaild postfix dovecot --wait >/dev/null 2>&1
    clear_ev
}
mock_row() {
    docker compose exec -T pg psql -U urmail -d urmail -q -c \
        "DELETE FROM backup_runs; INSERT INTO backup_runs (run_uid,state,scope,started_at,finished_at,error,manifest_name,offsite_uri) VALUES ('mock-fresh-run-1','success','local-mail-and-metadata',now(),now(),'', 'urmail-'||to_char(now() at time zone 'UTC','YYYYMMDD')||'T000000Z.manifest.json','s3://mock/urmail')"
}
clear_rows() {
    docker compose exec -T pg psql -U urmail -d urmail -q -c "DELETE FROM backup_runs" 2>/dev/null || true
}
hold_state() { [ -f "$URMAIL_DATA_ROOT/holds/upgrade.hold" ] && echo yes || echo no; }

echo "==================== A: preflight rejection ======================"
reset_stack
clear_rows
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "A_rc" "$rc" "4"
assert "A_hold" "$(hold_state)" "no"
assert "A_urmaild" "$(docker inspect --format '{{.State.Status}}' "$(docker compose ps -q urmaild)")" "running"
assert "A_reject_in_evidence" "$(ev_count 'no offsite backup run recorded yet')" "1"

echo "==================== D: happy path upgrade ======================="
reset_stack
mock_row
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "D_rc" "$rc" "0"
assert "D_hold" "$(hold_state)" "no"
assert "D_healthz" "$(docker compose exec -T urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz 2>/dev/null | grep -c '"status":"ok"')" "1"
assert "D_verified_note" "$(ev_count 'writers verified running')" "1"
assert "D_postfix_running" "$(docker compose ps --status running -q postfix | wc -l)" "1"
assert "D_dovecot_running" "$(docker compose ps --status running -q dovecot | wc -l)" "1"

echo "==================== E: post-up failure keeps hold ================"
reset_stack
mock_row
# E injection: create a writer container that will die on start
# (config poisoned at create-time, then config file restored —
# the container carries the broken config into its start)
echo "E_INJECTION_BROKEN" >> compose/dovecot/dovecot.conf
docker compose create dovecot >/dev/null 2>&1
git checkout -f HEAD -- compose/dovecot/dovecot.conf
git diff --quiet -- compose/dovecot/dovecot.conf || { record "FAIL E_fixture_conf_restore"; FAIL=$((FAIL+1)); }
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "E_rc" "$([ "$rc" != 0 ] && echo nonzero || echo zero)" "nonzero"
assert "E_hold" "$(hold_state)" "yes"
assert "E_runbook" "$(ev_count 'Rollback runbook')" "1"
assert "E_no_started_note" "$(ev_count 'writers started, hold cleared')" "0"
rm -f "$URMAIL_DATA_ROOT/holds/upgrade.hold"

echo "==================== C1a: absent writer skip ====================="
reset_stack
mock_row
docker compose rm -sf dovecot >/dev/null 2>&1 || true
out=$(URMAIL_SMOKE_RIG=1 URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "C1a_rc" "$rc" "0"
assert "C1a_skip_noted" "$(ev_count 'absent from this rig')" "1"
assert "C1a_hold" "$(hold_state)" "no"

echo "==================== C1b: writer start failure ===================="
reset_stack
mock_row
echo "C1B_INJECTION_BROKEN" >> compose/dovecot/dovecot.conf
docker compose create dovecot >/dev/null 2>&1
git checkout -f HEAD -- compose/dovecot/dovecot.conf
git diff --quiet -- compose/dovecot/dovecot.conf || { record "FAIL C1b_fixture_conf_restore"; FAIL=$((FAIL+1)); }
out=$(URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" 2>&1); rc=$?
assert "C1b_rc" "$([ "$rc" != 0 ] && echo nonzero || echo zero)" "nonzero"
assert "C1b_hold" "$(hold_state)" "yes"
assert "C1b_no_started_note" "$(ev_count 'writers started, hold cleared')" "0"
rm -f "$URMAIL_DATA_ROOT/holds/upgrade.hold"

echo "==================== C2: uid5000 maintenance lock ================"
reset_stack
mock_row
URMAIL_DATA_ROOT="$URMAIL_DATA_ROOT" ./upgrade.sh "$V_GOOD" >/dev/null 2>&1
lock_probe=$(docker compose run --rm --no-deps --user 5000:5000 \
    --entrypoint /bin/sh urmaild \
    -c 'exec 9<>/srv/urmail/data/maintenance/operation.lock && echo LOCK_RDWR_OK' 2>&1)
assert "C2_lock" "$(printf '%s' "$lock_probe" | grep -c LOCK_RDWR_OK)" "1"
assert "C2_owner" "$(docker compose run --rm --no-deps --user 5000:5000 \
    --entrypoint /bin/stat urmaild -c '%u:%g' /srv/urmail/data/maintenance 2>/dev/null)" "5000:5000"

echo "==================== orphan hold check ==========================="
docker compose down --remove-orphans --timeout 0 >/dev/null 2>&1 || true
assert "orphan_hold" "$(hold_state)" "no"

echo "==================== SUMMARY ======================================"
printf '%s\n' "$SUMMARY"
echo "PASS=$PASS FAIL=$FAIL"
[ "$FAIL" = 0 ] && echo "RIG_ACCEPTANCE_ALL_PASS" || echo "RIG_ACCEPTANCE_HAS_FAILURES"
exit $([ "$FAIL" = 0 ] && echo 0 || echo 1)
