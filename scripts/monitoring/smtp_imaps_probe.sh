#!/bin/bash
# smtp_imaps_probe.sh — GRAFANA-DESIGN §3 dashboards 2/3 probe.
# End-to-end synthetic delivery (SMTP submission with STARTTLS + AUTH) and
# IMAPS session (TLS login + SELECT INBOX + LOGOUT), pushing
# urmail_probe_smtp_* and urmail_probe_imaps_* to the pushgateway. This is
# the result/dimension-completeness backstop for delivery success rate and
# IMAPS session rate until the delivery counter grows domain labels
# (GRAFANA-DESIGN §7.1: urmail_delivery_events_total carries status only).
# Credentials come from the environment, never from arguments or logs.
set -euo pipefail

PUSHGATEWAY="${URMAIL_PUSHGATEWAY:-http://127.0.0.1:9091}"
SMTP_HOST="${URMAIL_PROBE_SMTP_HOST:-127.0.0.1}"
SMTP_PORT="${URMAIL_PROBE_SMTP_PORT:-587}"
SMTP_USER="${URMAIL_PROBE_SMTP_USER:?probe mailbox required}"
SMTP_PASS="${URMAIL_PROBE_SMTP_PASSWORD:?probe mailbox password required}"
IMAPS_HOST="${URMAIL_PROBE_IMAPS_HOST:-127.0.0.1}"
IMAPS_PORT="${URMAIL_PROBE_IMAPS_PORT:-993}"
TLS_VERIFY="${URMAIL_PROBE_TLS_VERIFY:-}"

export SMTP_HOST SMTP_PORT SMTP_USER SMTP_PASS IMAPS_HOST IMAPS_PORT TLS_VERIFY
python3 - <<'PY' | curl -sf --data-binary @- "$PUSHGATEWAY/metrics/job/urmail_probe" >/dev/null
import imaplib, os, smtplib, ssl, time
from email.message import EmailMessage

start = time.monotonic()
smtp_ok = 0
try:
    ctx = ssl.create_default_context()
    if os.environ.get("TLS_VERIFY") != "1":
        ctx = ssl._create_unverified_context()
    msg = EmailMessage()
    msg["From"] = os.environ["SMTP_USER"]
    msg["To"] = os.environ["SMTP_USER"]
    msg["Subject"] = "urmail-probe synthetic delivery"
    msg.set_content("synthetic probe; safe to ignore")
    with smtplib.SMTP(os.environ["SMTP_HOST"], int(os.environ["SMTP_PORT"]), timeout=30) as s:
        s.ehlo()
        s.starttls(context=ctx)
        s.ehlo()
        s.login(os.environ["SMTP_USER"], os.environ["SMTP_PASS"])
        s.send_message(msg)
    smtp_ok = 1
except Exception:
    smtp_ok = 0
smtp_seconds = time.monotonic() - start

start = time.monotonic()
imaps_ok = 0
try:
    ctx = ssl.create_default_context()
    if os.environ.get("TLS_VERIFY") != "1":
        ctx = ssl._create_unverified_context()
    with imaplib.IMAP4_SSL(os.environ["IMAPS_HOST"], int(os.environ["IMAPS_PORT"]), ssl_context=ctx) as m:
        m.login(os.environ["SMTP_USER"], os.environ["SMTP_PASS"])
        m.select("INBOX")
        m.logout()
    imaps_ok = 1
except Exception:
    imaps_ok = 0
imaps_seconds = time.monotonic() - start

print("# TYPE urmail_probe_smtp_success gauge")
print("# HELP urmail_probe_smtp_success Synthetic submission reached the spool.")
print(f"urmail_probe_smtp_success {smtp_ok}")
print("# TYPE urmail_probe_smtp_duration_seconds gauge")
print("# HELP urmail_probe_smtp_duration_seconds Synthetic submission wall time.")
print(f"urmail_probe_smtp_duration_seconds {smtp_seconds:.3f}")
print("# TYPE urmail_probe_imaps_success gauge")
print("# HELP urmail_probe_imaps_success Synthetic IMAPS session succeeded.")
print(f"urmail_probe_imaps_success {imaps_ok}")
print("# TYPE urmail_probe_imaps_duration_seconds gauge")
print("# HELP urmail_probe_imaps_duration_seconds Synthetic IMAPS wall time.")
print(f"urmail_probe_imaps_duration_seconds {imaps_seconds:.3f}")
PY
