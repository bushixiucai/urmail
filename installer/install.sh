#!/usr/bin/env bash
# urMail.cn installer.
#
# The installer's job is narrow on purpose: install the dependencies, install
# the package, initialise the database and run the first-time setup. Every
# later configuration change is driven by the service itself, not by re-running
# a pile of shell scripts.
set -euo pipefail

REPO="${URMAIL_REPO:-https://github.com/bushixiucai/urmail}"
VERSION="${URMAIL_VERSION:-latest}"
MIRROR="${URMAIL_MIRROR:-}"          # optional mirror base URL
APT_MIRROR="${URMAIL_APT_MIRROR:-}"  # optional Ubuntu mirror

log() { printf '\033[36m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33m!!!\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31mxxx\033[0m %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "Run this script with sudo."
[ -r /etc/os-release ] || die "Cannot detect the operating system."
. /etc/os-release

case "${ID}:${VERSION_ID}" in
ubuntu:22.04|ubuntu:24.04) ;;
*) die "urMail.cn supports Ubuntu 22.04 and 24.04 only (found ${ID} ${VERSION_ID})." ;;
esac

ARCH="$(dpkg --print-architecture)"
[ "$ARCH" = amd64 ] || die "urMail.cn supports amd64 only (found ${ARCH})."

# ---------------------------------------------------------------------------
# Dependencies
# ---------------------------------------------------------------------------

if [ -n "$APT_MIRROR" ]; then
	log "switching apt sources to ${APT_MIRROR}"
	sed -i "s|http://archive.ubuntu.com/ubuntu|${APT_MIRROR}|g; s|http://security.ubuntu.com/ubuntu|${APT_MIRROR}|g" \
		/etc/apt/sources.list || true
	for file in /etc/apt/sources.list.d/*.sources; do
		[ -e "$file" ] || continue
		sed -i "s|http://archive.ubuntu.com/ubuntu|${APT_MIRROR}|g; s|http://security.ubuntu.com/ubuntu|${APT_MIRROR}|g" "$file"
	done
fi

export DEBIAN_FRONTEND=noninteractive
log "installing system dependencies"
apt-get update -qq
apt-get install -y --no-install-recommends \
	nginx postfix dovecot-core postgresql ca-certificates curl gnupg \
	openssl certbot rsync duplicity python3-venv

# ---------------------------------------------------------------------------
# Package
# ---------------------------------------------------------------------------

PKG_URL=""
if [ "$VERSION" = "latest" ]; then
	MIRROR_BASE="${MIRROR:-${REPO}}"
	PKG_URL="${MIRROR_BASE}/releases/latest/download/urmail_latest_amd64.deb"
else
	MIRROR_BASE="${MIRROR:-${REPO}}"
	PKG_URL="${MIRROR_BASE}/releases/download/v${VERSION}/urmail_${VERSION}_amd64.deb"
fi

log "downloading ${PKG_URL}"
TMP_DEB="$(mktemp -d)/urmail.deb"
if ! curl -fLsS --retry 3 --connect-timeout 20 --max-time 600 "$PKG_URL" -o "$TMP_DEB"; then
	warn "the release could not be downloaded from ${PKG_URL}"
	die "Set URMAIL_MIRROR to a reachable mirror, or install the .deb manually."
fi

log "installing the package"
dpkg -i "$TMP_DEB"

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

log "preparing PostgreSQL"
systemctl enable --now postgresql >/dev/null 2>&1 || true

su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='urmail'\"" | grep -q 1 || {
	su - postgres -c "psql -c \"CREATE ROLE urmail LOGIN PASSWORD 'urmail'\"" >/dev/null
}
su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='urmail'\"" | grep -q 1 || {
	su - postgres -c "createdb -O urmail urmail"
}

DB_URL="postgres://urmail@127.0.0.1:5432/urmail?sslmode=disable"
ENV_FILE=/etc/urmail/urmaild.env
if [ ! -f "$ENV_FILE" ]; then
	log "writing ${ENV_FILE}"
	install -d -o root -g mailinabox -m 0750 /etc/urmail
	cat >"$ENV_FILE" <<EOF
URMAIL_DATABASE_URL=${DB_URL}
URMAIL_DATABASE_URL_PLATFORM=${DB_URL}
URMAIL_LISTEN_ADDR=127.0.0.1:8080
URMAIL_STORAGE_ROOT=/var/mail
URMAIL_AGENT_SOCKET=/run/urmail/agent.sock
URMAIL_POLICY_SOCKET=/run/urmail/policy.sock
URMAIL_MASTER_KEY_PATH=/etc/urmail/master.key
URMAIL_LOG_LEVEL=info
URMAIL_LOCALE=zh-CN
EOF
	chown root:mailinabox "$ENV_FILE"
	chmod 0640 "$ENV_FILE"
fi

if [ ! -f /etc/urmail/master.key ]; then
	log "generating the master key"
	install -m 0640 -o root -g mailinabox /dev/null /etc/urmail/master.key
	openssl rand -base64 48 >> /etc/urmail/master.key
fi

log "applying database migrations"
urmailctl migrate up

# ---------------------------------------------------------------------------
# Services
# ---------------------------------------------------------------------------

log "starting the services"
systemctl daemon-reload
systemctl enable --now urmaild-agent.service
systemctl enable --now urmaild.service
systemctl reload nginx 2>/dev/null || systemctl restart nginx

log "urMail.cn is installed."
cat <<'EOF'

Next steps:

  1. Point a DNS A/AAAA record for your panel hostname at this machine, or use
     the IP address directly.
  2. Open the panel and complete the first-time setup.

Useful commands:

  urmailctl status           service status
  urmailctl migrate up       apply database migrations
  journalctl -u urmaild -f   follow the service log

EOF
