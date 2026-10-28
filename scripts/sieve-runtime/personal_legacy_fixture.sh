#!/bin/sh
# Bootstrap one legacy-mode fixture only on this run's disposable Linux target.
set -eu
[ "$(hostname)" = urmail-recovery-target-24 ]
[ "$(cat /root/core-runtime-authorized)" = isolated-target24 ]
install -d -m 0755 /var/mail/mail/sieve/mailfix24.test
install -d -m 0700 -o mail -g mail /var/mail/mail/sieve/mailfix24.test/sieve-peer
mode() {
 runuser -u postgres -- psql -d urmail -v ON_ERROR_STOP=1 -c "BEGIN; ALTER TABLE mailboxes DISABLE TRIGGER mailbox_sieve_mode_guard; UPDATE mailboxes SET sieve_mode='$1' WHERE address='sieve-peer@mailfix24.test'; ALTER TABLE mailboxes ENABLE TRIGGER mailbox_sieve_mode_guard; COMMIT;" >/dev/null
}
# Transition is fixture bootstrap, not a supported application mode transition.
# The single guard is disabled only under a transaction/table lock, then restored.
trap 'mode managed' EXIT HUP INT TERM
mode legacy_v80
python3 /root/sievegate-personal-capability.py
