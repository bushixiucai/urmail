#!/bin/bash
# mailbox_bytes.sh — GRAFANA-DESIGN §3 user-system linkage row.
# Aggregates Maildir occupancy to the DOMAIN level only and writes
# urmail_mailbox_bytes{domain=...}. Local parts are never emitted (privacy
# red line): the walk descends into each per-domain directory and reports
# the single domain-level total.
set -euo pipefail

TEXTFILE_DIR="${URMAIL_TEXTFILE_DIR:-/srv/urmail/textfile}"
MAIL_ROOT="${URMAIL_STORAGE_ROOT:-/srv/urmail/data/mail}/mail/mailboxes"
OUT="$TEXTFILE_DIR/urmail_mailbox_bytes.prom"

tmp="$(mktemp "$OUT.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
timestamp="$(date +%s)"
{
  echo "# HELP urmail_mailbox_bytes Maildir bytes per mail domain (domain-level aggregation only)."
  echo "# TYPE urmail_mailbox_bytes gauge"
  if [ -d "$MAIL_ROOT" ]; then
    for domain_dir in "$MAIL_ROOT"/*; do
      [ -d "$domain_dir" ] || continue
      domain="$(basename "$domain_dir")"
      bytes="$(du -sk "$domain_dir" 2>/dev/null | awk '{print $1*1024}')"
      echo "urmail_mailbox_bytes{domain=\"${domain}\"} ${bytes:-0} $timestamp"
    done
  fi
} >"$tmp"
mv "$tmp" "$OUT"
