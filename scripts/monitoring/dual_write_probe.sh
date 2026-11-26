#!/bin/bash
# dual_write_probe.sh — GRAFANA-DESIGN §3 dashboard 6 probe.
# Attempts a second flock on the single-writer lease file. The holder keeps
# the lease for the whole run, so the only healthy outcome is rc=75
# (flock's "already held" sentinel this probe exports). Any other result —
# a free lease (a dead writer) or an unexpected error — is an E8 stop-write
# isolation signal and must page immediately.
# Schedule every ~12s (5/min). Pushes to the pushgateway with honor_labels.
set -euo pipefail

PUSHGATEWAY="${URMAIL_PUSHGATEWAY:-http://127.0.0.1:9091}"
LEASE="${URMAIL_WRITER_LEASE:?URMAIL_WRITER_LEASE must point at the live .urmail-writer.lease}"

rc=0
flock -n "$LEASE" true 2>/dev/null || rc=$?
case "$rc" in
  75) healthy=1 ;;
  *) healthy=0 ;;
esac

cat <<EOF | curl -sf --data-binary @- "$PUSHGATEWAY/metrics/job/urmail_dual_write_probe" >/dev/null
# TYPE urmail_dual_write_probe_rc gauge
# HELP urmail_dual_write_probe_rc Second-writer flock result: 75 = lease held as expected.
urmail_dual_write_probe_rc $rc
# TYPE urmail_dual_write_probe_healthy gauge
# HELP urmail_dual_write_probe_healthy 1 when the writer lease is exclusively held.
urmail_dual_write_probe_healthy $healthy
EOF
