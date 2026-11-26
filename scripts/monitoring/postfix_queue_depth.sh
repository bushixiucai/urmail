#!/bin/bash
# postfix_queue_depth.sh — GRAFANA-DESIGN §3 dashboard 4 probe.
# Counts the active Postfix queue once per invocation and writes the
# node-exporter textfile metric urmail_postfix_queue_depth. Schedule it
# every minute from a systemd timer (deployment wiring is DevOps-owned).
# Read-only against the live queue; prints nothing on success.
set -euo pipefail

TEXTFILE_DIR="${URMAIL_TEXTFILE_DIR:-/srv/urmail/textfile}"
OUT="$TEXTFILE_DIR/urmail_postfix_queue_depth.prom"

tmp="$(mktemp "$OUT.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
depth="$("/usr/sbin/postqueue" -p 2>/dev/null | tail -n +2 | grep -c '^[A-F0-9]' || true)"
timestamp="$(date +%s)"
cat >"$tmp" <<EOF
# HELP urmail_postfix_queue_depth Messages currently in the Postfix queue.
# TYPE urmail_postfix_queue_depth gauge
urmail_postfix_queue_depth $depth $timestamp
EOF
mv "$tmp" "$OUT"
