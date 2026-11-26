#!/bin/bash
# backup_chain_metrics.sh — GRAFANA-DESIGN §3 dashboard 7 probe.
# Reads the backup_runs ledger through a read-only SQLite connection and
# publishes the id-continuity and freshness gauges the backup-chain hard
# gate watches (E8/E10 thresholds; epoch micros match the pool codec).
# Schedule after every backup run plus a periodic freshness sweep.
set -euo pipefail

TEXTFILE_DIR="${URMAIL_TEXTFILE_DIR:-/srv/urmail/textfile}"
OUT="$TEXTFILE_DIR/urmail_backup_chain.prom"
DB="${URMAIL_SQLITE_PATH:?URMAIL_SQLITE_PATH must point at the live database}"

tmp="$(mktemp "$OUT.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
sql="
.mode list
SELECT 'urmail_backup_last_id '||COALESCE(MAX(id),0) FROM backup_runs;
SELECT 'urmail_backup_id_gap '||COALESCE(MAX(id)-MIN(id)+1-COUNT(*),0) FROM backup_runs;
SELECT 'urmail_backup_last_success_timestamp_seconds '||(SELECT COALESCE(MAX(finished_at),0)/1000000 FROM backup_runs WHERE state='success');
SELECT 'urmail_backup_offsite_last_success_timestamp_seconds '||(SELECT COALESCE(MAX(finished_at),0)/1000000 FROM backup_runs WHERE state='success' AND offsite_state='uploaded');
"
timestamp="$(date +%s)"
{
  echo "# HELP urmail_backup_last_id Newest backup_runs ledger id."
  echo "# TYPE urmail_backup_last_id gauge"
  echo "# HELP urmail_backup_id_gap Missing ledger ids; >0 means the chain is not contiguous."
  echo "# TYPE urmail_backup_id_gap gauge"
  echo "# HELP urmail_backup_last_success_timestamp_seconds Last successful local backup completion."
  echo "# TYPE urmail_backup_last_success_timestamp_seconds gauge"
  echo "# HELP urmail_backup_offsite_last_success_timestamp_seconds Last successful offsite upload completion."
  echo "# TYPE urmail_backup_offsite_last_success_timestamp_seconds gauge"
  sqlite3 "file:$DB?mode=ro" "$sql"
  echo "# EOF appended at $timestamp"
} >"$tmp"
mv "$tmp" "$OUT"
