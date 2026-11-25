#!/bin/sh
# Compose first-boot bootstrap: render the mail configuration once on an empty
# data root. The product supports it without an upgrade fence:
#   urmailctl config prepare --first-boot
# Idempotent: an already rendered configuration is left untouched.
set -eu
root=/srv/urmail/config
if [ -f "$root/dovecot/dovecot.conf" ] && [ -f "$root/postfix/main.cf" ]; then
  echo "[bootstrap] rendered configuration already present; nothing to do"
  exit 0
fi
echo "[bootstrap] empty data root: applying schema migrations"
urmailctl migrate up
echo "[bootstrap] rendering configuration (first-boot, no fence)"
urmailctl config prepare --first-boot
echo "[bootstrap] done"
