#!/usr/bin/env bash
# Fixture test for QA P3-a (compose/README Phase 2 batch #9, block 1):
# an EXISTING .env with a relative URMAIL_DATA_ROOT must be normalised to an
# absolute path by install.sh, idempotently, touching no other key.
#
# The test stubs `docker` so the run deterministically aborts at the build
# step — the normalisation happens BEFORE that abort and is asserted here.
# Run as root (or inside the phase2 rig container).
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
T=$(mktemp -d /tmp/urmail-p3a.XXXXXX)
trap 'rm -rf "$T"' EXIT

# --- fixture bin: docker answers the version probes, fails everything else
mkdir -p "$T/bin"
cat > "$T/bin/docker" <<'EOF'
#!/bin/sh
case "$*" in
    *--format*Server.Version*) echo "29.9.3" ;;
    *compose*version*) echo "Docker Compose version v2.32.4" ;;
    *) echo "docker stub: not available in this fixture" >&2; exit 1 ;;
esac
EOF
chmod 0755 "$T/bin/docker"
printf '#!/bin/sh\nexit 1\n' > "$T/bin/ss" && chmod 0755 "$T/bin/ss"   # ports reported free
cp "$ROOT/install.sh" "$T/install.sh"
chmod 0755 "$T/install.sh"

# --- fixture .env with a RELATIVE data root and a comment to preserve
cat > "$T/.env" <<'EOF'
# fixture comment that must survive
URMAIL_DATA_ROOT=data
URMAIL_MYHOSTNAME=old.example.test
URMAIL_PG_PASSWORD=fixture-secret-do-not-log
EOF

echo "== run 1 (real mode, build aborted by the docker stub — by design)"
cd "$T"
rc1=0
PATH="$T/bin:$PATH" bash "$T/install.sh" --domain fixture.test >/tmp/p3a-run1.log 2>&1 || rc1=$?
echo "run1_rc=$rc1 (expected non-zero: the stub docker refuses to build)"

normalised=$(grep -E '^URMAIL_DATA_ROOT=' .env | cut -d= -f2-)
echo "normalised=$normalised"
case $normalised in
    /*) : ;;
    *) echo "FAIL: URMAIL_DATA_ROOT not normalised to an absolute path" >&2; exit 1 ;;
esac
[ "$normalised" = "$T/data" ] || { echo "FAIL: normalised to $normalised, expected $T/data" >&2; exit 1; }

grep -q "^# fixture comment that must survive" .env && echo "comment_preserved=yes" || { echo "FAIL: comment lost" >&2; exit 1; }
grep -q "^URMAIL_PG_PASSWORD=fixture-secret-do-not-log" .env && echo "secret_untouched=yes" || { echo "FAIL: secret key altered" >&2; exit 1; }
grep -q "^URMAIL_MYHOSTNAME=old.example.test" .env && echo "hostname_untouched=yes" || { echo "FAIL: unrelated key altered" >&2; exit 1; }

echo "== run 2 (idempotency: second install run must not change the value)"
rc2=0
PATH="$T/bin:$PATH" bash "$T/install.sh" --domain fixture.test >/tmp/p3a-run2.log 2>&1 || rc2=$?
normalised2=$(grep -E '^URMAIL_DATA_ROOT=' .env | cut -d= -f2-)
[ "$normalised2" = "$normalised" ] || { echo "FAIL: second run changed the value" >&2; exit 1; }
count=$(grep -cE '^URMAIL_DATA_ROOT=' .env)
[ "$count" = "1" ] || { echo "FAIL: duplicated URMAIL_DATA_ROOT keys ($count)" >&2; exit 1; }

echo "== expected-failure log sample (run1 tail):"
tail -3 /tmp/p3a-run1.log
echo "P3A_ALL_PASS rc1=$rc1 rc2=$rc2 normalised=$normalised"
