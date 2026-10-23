#!/usr/bin/env bash
# API acceptance smoke for a running urMail.cn instance.
# Usage:
#   URMAIL_BASE_URL=https://box.example URMAIL_ADMIN_EMAIL=admin@example \
#   URMAIL_ADMIN_PASSWORD='...' scripts/acceptance-smoke.sh
set -euo pipefail

BASE="${URMAIL_BASE_URL:-http://127.0.0.1:8080}"
EMAIL="${URMAIL_ADMIN_EMAIL:-admin@urmail.test}"
PASSWORD="${URMAIL_ADMIN_PASSWORD:?set URMAIL_ADMIN_PASSWORD}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

check() {
	local path="$1" expected="${2:-200}"
	local code
	code="$(curl -sS -o /dev/null -w '%{http_code}' "$BASE$path")"
	if [ "$code" != "$expected" ]; then
		echo "FAIL $path: expected $expected got $code" >&2
		exit 1
	fi
	echo "ok   $path"
}

check /api/v1/healthz
check /api/v1/billing/plans 401
check /admin/system/status 401
check /admin/does-not-exist 404

curl -sS -f -H 'Content-Type: application/json' \
	-d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}" \
	"$BASE/api/v1/auth/login" >"$tmp/login.json"
token="$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["token"])' "$tmp/login.json")"

auth_check() {
	local path="$1"
	local code
	code="$(curl -sS -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $token" "$BASE$path")"
	if [ "$code" != "200" ]; then
		echo "FAIL $path: expected 200 got $code" >&2
		exit 1
	fi
	echo "ok   $path"
}
auth_check /api/v1/auth/me
auth_check /api/v1/auth/sessions
auth_check /api/v1/auth/mfa/status
auth_check /api/v1/billing/subscription
auth_check /api/v1/billing/invoices
auth_check /api/v1/audit
auth_check /api/v1/platform/overview
auth_check /api/v1/platform/tenants

legacy="$(curl -sS -o /dev/null -w '%{http_code}' -u "$EMAIL:$PASSWORD" "$BASE/admin/system/status")"
if [ "$legacy" != "200" ]; then
	echo "FAIL legacy admin: got $legacy" >&2
	exit 1
fi
echo "ok   /admin/system/status (basic auth)"

echo "PASS: urMail.cn API acceptance smoke"
