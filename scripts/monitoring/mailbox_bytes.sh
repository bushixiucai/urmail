#!/bin/bash
# mailbox_bytes.sh — GRAFANA-DESIGN §3 user-system linkage row.
# Aggregates Maildir occupancy to the DOMAIN level only and writes
# urmail_mailbox_bytes{domain=...}. Local parts are never emitted (privacy
# red line): the walk descends into each per-domain directory and reports
# the single domain-level total.
set -euo pipefail

TEXTFILE_DIR="${URMAIL_TEXTFILE_DIR:-/srv/urmail/textfile}"
# URMAIL_STORAGE_ROOT must match the urmaild render input exactly: the
# Maildir layout is <storage root>/mail/mailboxes/<domain>/<local part>
# (dovecot render: maildir:%s/mail/mailboxes/%d/%n). With the compose
# default /srv/urmail/data/mail the walk root therefore is
# /srv/urmail/data/mail/mail/mailboxes — the doubled /mail is the real
# layout, not a bug; warn loudly when the walk root is missing so a
# mis-set storage root never silently publishes zeros.
MAIL_ROOT="${URMAIL_STORAGE_ROOT:-/srv/urmail/data/mail}/mail/mailboxes"
OUT="$TEXTFILE_DIR/urmail_mailbox_bytes.prom"

tmp="$(mktemp "$OUT.XXXXXX")"
trap 'rm -f "$tmp"' EXIT
collected="$(date -u +%FT%TZ)"
{
  echo "# HELP urmail_mailbox_bytes Maildir bytes per mail domain (domain-level aggregation only)."
  echo "# TYPE urmail_mailbox_bytes gauge"
  echo "# collected $collected walk_root $MAIL_ROOT"
  if [ -d "$MAIL_ROOT" ]; then
    for domain_dir in "$MAIL_ROOT"/*; do
      [ -d "$domain_dir" ] || continue
      domain="$(basename "$domain_dir")"
      bytes="$(du -sk "$domain_dir" 2>/dev/null | awk '{print $1*1024}')"
      echo "urmail_mailbox_bytes{domain=\"${domain}\"} ${bytes:-0}"
    done
  else
    echo "# WARNING: walk root $MAIL_ROOT does not exist; no samples published"
  fi
} >"$tmp"
mv "$tmp" "$OUT"
