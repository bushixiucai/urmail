#!/usr/bin/env bash
# urMail compose installer skeleton (Phase 2, ARCH-PLAN §1.3).
# One-shot: checks -> .env -> data dirs -> build -> up -> healthy -> hints.
# Never prints secrets; --dry-run performs read-only checks and prints the
# plan without writing anything.
#
# Usage: ./install.sh [--data-root DIR] [--domain FQDN] [--admin-email ADDR]
#                     [--pg-password SECRET] [--proxy URL] [--yes] [--dry-run]
set -euo pipefail

DATA_ROOT="${URMAIL_DATA_ROOT:-data}"
DOMAIN=""           # myhostname (FQDN of this machine)
PRIMARY_DOMAIN=""   # mail domain (defaults to DOMAIN when empty)
ADMIN_EMAIL=""
PG_PASSWORD=""
PROXY=""
DRY_RUN=0
log()  { printf '[install] %s\n' "$*"; }
warn() { printf '[install] WARNING: %s\n' "$*" >&2; }
die()  { printf '[install] ABORT: %s\n' "$*" >&2; exit 4; }

usage() { sed -n '2,12p' "$0"; exit 0; }

while [ $# -gt 0 ]; do
    case "$1" in
        --data-root)    DATA_ROOT=$2; shift 2 ;;
        --domain)       DOMAIN=$2; shift 2 ;;
        --primary-domain) PRIMARY_DOMAIN=$2; shift 2 ;;
        --admin-email)  ADMIN_EMAIL=$2; shift 2 ;;
        --pg-password)  PG_PASSWORD=$2; PG_PW_SOURCE="flag"; shift 2 ;;
        --pg-password-file)
            PG_PASSWORD=$(cat "$2")
            PG_PW_SOURCE="file"
            shift 2 ;;
        --proxy)        PROXY=$2; shift 2 ;;
        --dry-run)      DRY_RUN=1; shift ;;
        -h|--help)      usage ;;
        *) die "unknown argument: $1" ;;
    esac
done
[ -n "$DOMAIN" ] || DOMAIN=${URMAIL_MYHOSTNAME:-}
[ -n "$PRIMARY_DOMAIN" ] || PRIMARY_DOMAIN=$DOMAIN

# Normalise the data root to an ABSOLUTE path (QA rig finding): a relative
# URMAIL_DATA_ROOT like "data" turns compose volume sources into
# "data/certs"-style names, which are invalid Docker volume names and abort
# the whole project. Everything downstream consumes the absolute path.
DATA_ROOT=$(realpath -m "$DATA_ROOT")
export URMAIL_DATA_ROOT="$DATA_ROOT"
[ -n "$DOMAIN" ] || { [ "$DRY_RUN" = 1 ] && DOMAIN="example.test"; } || die "--domain (or URMAIL_MYHOSTNAME) is required"

run() { # run <description> <cmd...> — honours --dry-run
    local desc=$1; shift
    if [ "$DRY_RUN" = 1 ]; then
        printf '[dry-run] would %s: %s\n' "$desc" "$*"
    else
        log "$desc"
        "$@"
    fi
}

# ---- 1. prerequisites ----------------------------------------------------
[ "$(uname -s)" = "Linux" ] || die "Linux is required (got: $(uname -s))"
[ "$EUID" = 0 ] || [ "$DRY_RUN" = 1 ] || die "root is required (install -d -o 5000 needs it)"

docker_major=$(docker version --format '{{.Server.Version}}' 2>/dev/null | cut -d. -f1) || true
[ "${docker_major:-0}" -ge 24 ] 2>/dev/null || die "Docker >= 24 required (server: ${docker_major:-not reachable})"
compose_version=$(docker compose version --short 2>/dev/null) || true
compose_minor_ok=$(printf '%s\n' "$compose_version" | awk '{
    v=$1; sub(/^v/, "", v); n=split(v, a, ".");
    if (n >= 2 && (a[1] > 2 || (a[1] == 2 && a[2] >= 20))) print "yes"; else print "no"
}') || true
[ "$compose_minor_ok" = "yes" ] || die "docker compose v2.20+ required (got: ${compose_version:-not reachable})"
log "prerequisites: docker ${docker_major}.x, compose $compose_version"

for port in 25 80 443 465 587 993; do
    if ! command -v ss >/dev/null 2>&1; then
        warn "ss not found; skipping the port-availability check for $port"
        continue
    fi
    if ss -tln 2>/dev/null | awk '{print $4}' | grep -qE "(^|:)$port\$"; then
        die "port $port is already in use on this host (free all of 25/80/443/465/587/993 first)"
    fi
done
log "ports 25/80/443/465/587/993 are free"

# rDNS hint (non-fatal; needs a reachable resolver, skipped on failure)
if command -v dig >/dev/null 2>&1; then
    ip=$(curl -4 -s -m 5 https://api.ipify.org 2>/dev/null || true)
    rdns=""
    if [ -n "$ip" ]; then
        rdns=$(dig +short -x "$ip" 2>/dev/null | head -n1)
    fi
    if [ -n "$rdns" ]; then
        log "rDNS hint: $ip -> $rdns (must resolve back to $DOMAIN before production mail)"
    else
        warn "rDNS lookup unavailable now; set PTR of your public IP to $DOMAIN before production mail"
    fi
else
    warn "dig not installed; set rDNS (PTR) of your public IP to $DOMAIN before production mail"
fi

if [ "$DRY_RUN" = 1 ]; then
    cat <<PLAN
[dry-run] plan:
  1. write .env (chmod 600): URMAIL_MYHOSTNAME=$DOMAIN URMAIL_PRIMARY_DOMAIN=$PRIMARY_DOMAIN
     admin=$ADMIN_EMAIL PG password: ${PG_PASSWORD:+from flag (hidden)}${PG_PASSWORD:-generated via openssl rand -hex 24 (hidden)}
  2. install -d -o 5000 -g 5000 -m 0750 $DATA_ROOT/{db,db/pg,mail,certs,config,backup}
  3. docker compose build${PROXY:+ with proxy $PROXY}
  4. docker compose up -d
  5. wait for urmaild health, then print admin bootstrap + DNS checklist
PLAN
    exit 0
fi

# ---- 2. .env (secrets land only in the 0600 file, never in logs) ---------
if [ -f .env ]; then
    log ".env already exists; keeping it (edit it manually if needed)"
else
    [ -f .env.example ] || die ".env.example missing from this checkout"
    cp .env.example .env
    chmod 600 .env
    if [ -z "$PG_PASSWORD" ] && [ -t 0 ]; then
        printf '[install] PG password (input hidden): '
        read -r PG_PASSWORD
        PG_PW_SOURCE="tty"
    fi
    if [ -z "$PG_PASSWORD" ]; then
        PG_PASSWORD=$(openssl rand -hex 24)
        PG_PW_SOURCE="generated"
    fi
    case $PG_PW_SOURCE in
        flag)  log "PG password: taken from --pg-password (stored in .env only)" ;;
        file)  log "PG password: taken from --pg-password-file (stored in .env only)" ;;
        tty)   log "PG password: read interactively (stored in .env only)" ;;
        *)     log "generated PG password (stored in .env only)" ;;
    esac
    sed -i "s|^URMAIL_MYHOSTNAME=.*|URMAIL_MYHOSTNAME=$DOMAIN|" .env
    sed -i "s|^URMAIL_PRIMARY_DOMAIN=.*|URMAIL_PRIMARY_DOMAIN=$PRIMARY_DOMAIN|" .env
    sed -i "s|^URMAIL_PG_PASSWORD=.*|URMAIL_PG_PASSWORD=$PG_PASSWORD|" .env
    sed -i "s|^URMAIL_DATA_ROOT=.*|URMAIL_DATA_ROOT=$DATA_ROOT|" .env
    if [ -n "$PROXY" ]; then
        sed -i "s|^# COMPOSE_BUILD_https_proxy=.*|COMPOSE_BUILD_https_proxy=$PROXY|" .env
        sed -i "s|^# COMPOSE_BUILD_http_proxy=.*|COMPOSE_BUILD_http_proxy=$PROXY|" .env
        sed -i "s|^# COMPOSE_BUILD_no_proxy=.*|COMPOSE_BUILD_no_proxy=goproxy.cn,.cn,localhost,127.0.0.1|" .env
    fi
    log ".env written (chmod 600)"
fi
[ -n "$ADMIN_EMAIL" ] || { warn "--admin-email not given; first-boot bootstrap URL will be printed by urmaild"; }

# ---- 3. data dirs (uid 5000 owns everything under the root) --------------
for d in db mail certs config backup holds; do
    install -d -o 5000 -g 5000 -m 0750 "$DATA_ROOT/$d"
done
# db/pg is intentionally NOT pre-created: PGDATA belongs to the postgres
# container user (uid 70 in postgres:16-alpine) and is initialised by the
# image entrypoint (bootstrap.sql runs there on first init).
log "data dirs ready under $DATA_ROOT (0750, 5000:5000)"

# ---- 4./5. build + up ----------------------------------------------------
run "docker compose build" docker compose build
run "docker compose up -d" docker compose up -d

# ---- 6. wait for health --------------------------------------------------
log "waiting for urmaild health (max 10m)"
deadline=$(( $(date +%s) + 600 ))
svc=""
while [ "$(date +%s)" -lt "$deadline" ]; do
    svc=$(docker compose ps --format '{{.Service}} {{.Status}}' 2>/dev/null | grep '^urmaild' || true)
    case $svc in *healthy*) break ;; esac
    sleep 5
done
case $svc in *healthy*) log "urmaild healthy" ;;
    *) die "urmaild did not become healthy in 10m; inspect: docker compose logs urmaild" ;;
esac
docker compose exec urmaild curl -fsS http://127.0.0.1:8080/api/v1/healthz
echo

# ---- 7. admin bootstrap + DNS checklist ---------------------------------
cat <<HINT

Bootstrap & DNS checklist (domain: $PRIMARY_DOMAIN)
  admin bootstrap : open https://$PRIMARY_DOMAIN and follow the one-time
                    admin URL printed by urmaild on first boot
                    (docker compose logs urmaild | grep -i bootstrap)
                    ${ADMIN_EMAIL:+admin email: $ADMIN_EMAIL}
  MX   : $PRIMARY_DOMAIN.  IN MX 10 $DOMAIN.
  SPF  : $PRIMARY_DOMAIN.  IN TXT "v=spf1 mx -all"
  DKIM : selector/config rendered by the agent (Phase 2 continuation);
         publish the key shown in the admin console after first boot
  DMARC: _dmarc.$PRIMARY_DOMAIN.  IN TXT "v=DMARC1; p=quarantine; rua=mailto:dmarc@$PRIMARY_DOMAIN"
  PTR  : public IP -> $DOMAIN (set at your ISP/VPS provider)
HINT
log "install complete"
