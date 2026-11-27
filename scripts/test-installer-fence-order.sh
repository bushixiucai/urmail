#!/usr/bin/env bash
# Regression guard for the upgrade preparation order. The contract is:
#   1. an in-flight configuration apply gets a bounded, stable window BEFORE
#      the fence stops urmaild-agent (a journal written mid-apply must be able
#      to finish; a fence-first order makes it an orphan and the installer
#      reports a false "prepared-held" refusal);
#   2. a journal that survives the window still refuses with exit 1 + fence
#      retained (never mapped to the prepared-held exit 2).
# The behavioural half (exit 2 + banner on success) is covered by
# scripts/test-release-installer.py; this file pins the order so a refactor
# cannot silently restore the fence-first sequence.
set -uo pipefail
here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
installer="$here/../installer/install.sh"
fails=0
fail(){ printf 'FAIL %s\n' "$*"; fails=$((fails+1)); }

loop_line=$(grep -n 'journal=/var/lib/urmail-agent/pending.json' "$installer" | head -n1 | cut -d: -f1)
fence_line=$(grep -n 'upgrade fence --reason' "$installer" | head -n1 | cut -d: -f1)
check_line=$(grep -n 'Pending apply journal requires explicit recovery' "$installer" | head -n1 | cut -d: -f1)
[ -n "$loop_line" ] || fail "journal settle loop not found"
[ -n "$fence_line" ] || fail "fence invocation not found"
[ -n "$check_line" ] || fail "pending-journal refusal check not found"
if [ -n "$loop_line" ] && [ -n "$fence_line" ] && [ "$loop_line" -ge "$fence_line" ]; then
    fail "journal settle loop (line $loop_line) must run before the fence (line $fence_line)"
fi
if [ -n "$check_line" ] && [ -n "$fence_line" ] && [ "$check_line" -le "$fence_line" ]; then
    fail "pending-journal refusal (line $check_line) unexpectedly runs before the fence"
fi
# The refusal must stay a SystemExit (exit 1); the upgrade path must not carry
# an exit 2 assignment near the journal check.
if sed -n "$((check_line-12)),$((check_line+6))p" "$installer" | grep -q 'exit 2'; then
    fail "journal refusal must not be mapped to exit 2"
fi
if [ "$(grep -c 'UPGRADE PREPARATION & MIGRATIONS APPLIED' "$installer")" -ne 1 ]; then
    fail "prepared-held banner must exist exactly once (only after a real preparation)"
fi
[ "$fails" -eq 0 ] && echo "PASS installer fence order (loop:$loop_line fence:$fence_line check:$check_line)"
exit $((fails > 0 ? 1 : 0))
