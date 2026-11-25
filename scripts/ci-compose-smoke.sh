#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT_DIR"

DATA_ROOT="$(mktemp -d /tmp/urmail-compose-smoke.XXXXXX)"
PROJECT_NAME="urmail-smoke-$$"
IMAGE_TAG="ci-smoke-$$"

cleanup() {
    local rc=$?
    set +e
    if [ "$rc" -ne 0 ]; then
        echo "=== Compose smoke failed (rc=$rc); dumping container status & logs ===" >&2
        URMAIL_DATA_ROOT="$DATA_ROOT" URMAIL_PG_PASSWORD="urmail-ci-smoke-pg-pw" URMAIL_IMAGE_VERSION="$IMAGE_TAG" \
            docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml ps -a >&2 || true
        URMAIL_DATA_ROOT="$DATA_ROOT" URMAIL_PG_PASSWORD="urmail-ci-smoke-pg-pw" URMAIL_IMAGE_VERSION="$IMAGE_TAG" \
            docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml logs --tail=60 >&2 || true
    fi
    URMAIL_DATA_ROOT="$DATA_ROOT" URMAIL_PG_PASSWORD="urmail-ci-smoke-pg-pw" URMAIL_IMAGE_VERSION="$IMAGE_TAG" \
        docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml down -v --remove-orphans >/dev/null 2>&1 || true
    docker run --rm --user 0:0 -v "$DATA_ROOT:/data" "urmail-local:${IMAGE_TAG}" rm -rf /data/* >/dev/null 2>&1 || true
    rm -rf "$DATA_ROOT" >/dev/null 2>&1 || true
    exit "$rc"
}
trap cleanup EXIT

mkdir -p \
    "$DATA_ROOT/config/postfix" \
    "$DATA_ROOT/config/dovecot" \
    "$DATA_ROOT/config/caddy" \
    "$DATA_ROOT/config/tls" \
    "$DATA_ROOT/config/dkim" \
    "$DATA_ROOT/config/run" \
    "$DATA_ROOT/config/rendered" \
    "$DATA_ROOT/config/spool" \
    "$DATA_ROOT/db/pg" \
    "$DATA_ROOT/backup" \
    "$DATA_ROOT/maintenance" \
    "$DATA_ROOT/archive" \
    "$DATA_ROOT/agent-run" \
    "$DATA_ROOT/agent-state" \
    "$DATA_ROOT/spool" \
    "$DATA_ROOT/holds" \
    "$DATA_ROOT/mail" \
    "$DATA_ROOT/litestream" \
    "$DATA_ROOT/prometheus"

cp compose/test-local/postfix/main.cf "$DATA_ROOT/config/postfix/main.cf"
cp compose/test-local/postfix/master.cf "$DATA_ROOT/config/postfix/master.cf"
cp compose/test-local/dovecot/dovecot.conf "$DATA_ROOT/config/dovecot/dovecot.conf"

openssl req -x509 -newkey rsa:2048 -nodes -days 2 \
    -subj "/CN=mail.local.test" \
    -keyout "$DATA_ROOT/config/tls/key.pem" \
    -out "$DATA_ROOT/config/tls/cert.pem" >/dev/null 2>&1

export URMAIL_DATA_ROOT="$DATA_ROOT"
export URMAIL_PG_PASSWORD="urmail-ci-smoke-pg-pw"
export URMAIL_IMAGE_VERSION="$IMAGE_TAG"
export URMAIL_PRIMARY_DOMAIN="local.test"
export URMAIL_MYHOSTNAME="mail.local.test"

# Ensure bind-mounted repo configs are readable by non-root container UIDs (e.g. postgres/nobody).
find compose internal/db/bootstrap.sql -type d -exec chmod a+rx {} +
find compose internal/db/bootstrap.sql -type f -exec chmod a+r {} +

# Build the single urmail image first so we can normalize bind-mount ownership to 5000:5000
# without pulling an extra helper image or requiring host sudo.
echo "[smoke] 1/4 Building compose images..."
docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml build

docker run --rm --user 0:0 -v "$DATA_ROOT:/data" "urmail-local:${IMAGE_TAG}" \
    sh -c 'chown -R 5000:5000 /data && chown -R 70:70 /data/db/pg && chown -R 65534:65534 /data/prometheus && chown -R 0:0 /data/spool && chmod 0755 /data/config/tls /data/spool && chmod 0644 /data/config/tls/cert.pem /data/config/tls/key.pem'

echo "[smoke] 2/4 Starting compose stack..."
docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml up -d

echo "[smoke] Waiting for urmaild healthz (HTTP 200)..."
health_ok=0
for _ in $(seq 1 60); do
    if docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml exec -T urmaild \
        curl -fsS http://127.0.0.1:8080/api/v1/healthz >/dev/null 2>&1; then
        health_ok=1
        break
    fi
    sleep 2
done
if [ "$health_ok" -ne 1 ]; then
    echo "FAIL: urmaild /api/v1/healthz did not return 200 within 120s" >&2
    exit 1
fi
docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml exec -T urmaild \
    curl -fsS -w "\nHTTP %{http_code}\n" http://127.0.0.1:8080/api/v1/healthz

echo "[smoke] 3/4 Running first-boot config render and checking artifacts..."
docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml exec -T \
    -e URMAIL_RENDERED_CONFIG=1 \
    -e URMAIL_RENDERED_SOURCE=render \
    -e URMAIL_RENDERED_CONFIG_DIR=/srv/urmail/config/rendered \
    -e URMAIL_AGENT_STATE_ROOT=/srv/urmail/data-root/agent-state \
    -e URMAIL_STORAGE_ROOT=/srv/urmail/data-root/mail \
    -e URMAIL_MAIL_USER=vmail \
    -e URMAIL_MAIL_GROUP=vmail \
    urmaild urmailctl config prepare --first-boot

docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml exec -T urmaild sh -c '
    for f in \
        /srv/urmail/config/rendered/postfix/main.cf \
        /srv/urmail/config/rendered/postfix/master.cf \
        /srv/urmail/config/rendered/dovecot/dovecot.conf \
        /srv/urmail/config/rendered/caddy/Caddyfile \
        /srv/urmail/data-root/agent-state/manifest.json; do
        if [ ! -s "$f" ]; then
            echo "FAIL: expected rendered config artifact missing or empty: $f" >&2
            exit 1
        fi
        echo "  OK: $f ($(wc -c < "$f") bytes)"
    done
'

echo "[smoke] 4/4 Verifying container exit-code set..."
ps_json="$(docker compose -p "$PROJECT_NAME" -f compose.yaml -f compose.test.local.yaml ps -a --format json)"
python3 - "$ps_json" <<'PY'
import json
import sys

raw = sys.argv[1].strip()
if not raw:
    raise SystemExit("FAIL: empty docker compose ps output")
if raw.startswith("["):
    items = json.loads(raw)
else:
    items = [json.loads(line) for line in raw.splitlines() if line.strip()]

expected = {"pg", "urmaild", "postfix", "dovecot", "caddy", "urmail-signer-test", "litestream", "prometheus", "alertmanager"}
seen = {}
bad = []
for item in items:
    svc = item.get("Service") or item.get("Name")
    state = item.get("State", "")
    exit_code = item.get("ExitCode", -1)
    health = item.get("Health", "")
    seen[svc] = (state, exit_code, health)
    print(f"  container={svc} state={state} exit={exit_code} health={health}")
    if state != "running" or exit_code != 0:
        bad.append(f"{svc}(state={state}, exit={exit_code})")

missing = sorted(expected - set(seen.keys()))
if missing:
    raise SystemExit(f"FAIL: missing expected containers: {missing}")
if bad:
    raise SystemExit(f"FAIL: non-running or non-zero exit containers: {bad}")
print("ALL_CONTAINERS_OK")
PY
