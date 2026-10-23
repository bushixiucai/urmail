#!/usr/bin/env bash
# Run the local panel against a dedicated development database.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE="$ROOT/tmp/dev"
mkdir -p "$STATE"
chmod 700 "$STATE"
stop() {
  for service in api web; do
    if [ -f "$STATE/$service.pid" ]; then
      pid="$(cat "$STATE/$service.pid")"
      case "$pid" in ''|*[!0-9]*) continue ;; esac
      command_line="$(ps -p "$pid" -o args= 2>/dev/null || true)"
      case "$command_line" in *"$ROOT/tmp/dev/urmaild"*|*"$ROOT/web/node_modules/vite/bin/vite.js"*) kill "$pid" 2>/dev/null || true ;; esac
      rm -f "$STATE/$service.pid"
    fi
  done
}
if [ "${1:-up}" = down ]; then stop; exit 0; fi
cd "$ROOT"
for tool in go node npm docker openssl curl; do command -v "$tool" >/dev/null || { echo "Missing dependency: $tool" >&2; exit 1; }; done
node -e 'const net=require("net"); Promise.all([8080,5173].map(port=>new Promise((resolve,reject)=>{const s=net.createServer();s.once("error",reject);s.listen(port,"127.0.0.1",()=>s.close(resolve));}))).catch(e=>{console.error("Development port is occupied:",e.message);process.exitCode=1;});'
docker compose -f docker-compose.dev.yml up -d --wait postgres
if [ "$(docker compose -f docker-compose.dev.yml exec -T postgres psql -U urmail -d postgres -tAc "SELECT count(*) FROM pg_database WHERE datname='urmail_development'")" = 0 ]; then
  docker compose -f docker-compose.dev.yml exec -T postgres createdb -U urmail urmail_development
fi
export URMAIL_ENV_FILE="$STATE/unused.env"
export URMAIL_DATABASE_URL='postgres://urmail:urmail@127.0.0.1:55432/urmail_development?sslmode=disable'
export URMAIL_DATABASE_URL_PLATFORM="$URMAIL_DATABASE_URL"
export URMAIL_DATABASE_URL_MIGRATE="$URMAIL_DATABASE_URL"
export URMAIL_DATABASE_URL_DOVECOT="$URMAIL_DATABASE_URL"
export URMAIL_LISTEN_ADDR=127.0.0.1:8080
export URMAIL_DEV_MODE=true
export URMAIL_STORAGE_ROOT="$STATE/mail"
export URMAIL_MASTER_KEY_PATH="$STATE/master.key"
export URMAIL_PRIMARY_DOMAIN=example.test
export URMAIL_MYHOSTNAME=box.example.test
export URMAIL_PUBLIC_IPV4=127.0.0.1
export URMAIL_NAMESERVERS=ns1.example.test
export URMAIL_POLICY_ENABLED=false
export URMAIL_ARCHIVE_ENABLED=false
if [ ! -f "$URMAIL_MASTER_KEY_PATH" ]; then (umask 077; openssl genrsa -traditional -out "$URMAIL_MASTER_KEY_PATH" 2048 2>/dev/null); fi
if [ ! -f "$STATE/admin-password" ]; then (umask 077; openssl rand -hex 16 >"$STATE/admin-password"); fi
go run ./cmd/urmailctl bootstrap --database-url "$URMAIL_DATABASE_URL"
go run ./cmd/urmailctl migrate up
URMAIL_SETUP_PASSWORD="$(cat "$STATE/admin-password")" go run ./cmd/urmailctl setup --email admin@example.test
go build -o "$STATE/urmaild" ./cmd/urmaild
if [ ! -d web/node_modules ]; then (cd web && npm ci); fi
trap stop EXIT INT TERM
"$STATE/urmaild" >"$STATE/api.log" 2>&1 &
echo $! >"$STATE/api.pid"
node "$ROOT/web/node_modules/vite/bin/vite.js" --host 127.0.0.1 --strictPort --config "$ROOT/web/vite.config.ts" "$ROOT/web" >"$STATE/web.log" 2>&1 &
echo $! >"$STATE/web.pid"
for _attempt in $(seq 1 60); do
  if curl -fsS http://127.0.0.1:8080/api/v1/healthz >/dev/null && curl -fsS http://127.0.0.1:5173/ >/dev/null; then
    echo "Development panel: http://127.0.0.1:5173"
    echo "Account: admin@example.test; password file: $STATE/admin-password"
    echo "Development mode does not apply system mail configuration. Logs: $STATE"
    wait
    exit 0
  fi
  sleep 1
done
echo "Development services did not become ready; inspect $STATE/*.log" >&2
exit 1
