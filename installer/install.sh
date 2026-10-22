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
case "$ARCH" in
amd64|arm64) ;;
*) die "urMail.cn supports amd64 and arm64 (found ${ARCH})." ;;
esac

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
	nginx postfix dovecot-core dovecot-imapd dovecot-pop3d dovecot-lmtpd \
	dovecot-sieve dovecot-managesieved dovecot-pgsql nsd opendkim postgresql \
	ca-certificates curl gnupg openssl certbot rsync duplicity python3-venv

# ---------------------------------------------------------------------------
# Package
# ---------------------------------------------------------------------------

PKG_URL=""
TMP_DEB=""
if [ -n "${URMAIL_PACKAGE:-}" ]; then
	TMP_DEB="$URMAIL_PACKAGE"
	log "using the local package ${TMP_DEB}"
else
	if [ "$VERSION" = "latest" ]; then
		MIRROR_BASE="${MIRROR:-${REPO}}"
		PKG_URL="${MIRROR_BASE}/releases/latest/download/urmail_latest_${ARCH}.deb"
	else
		MIRROR_BASE="${MIRROR:-${REPO}}"
		PKG_URL="${MIRROR_BASE}/releases/download/v${VERSION}/urmail_${VERSION}_${ARCH}.deb"
	fi

	log "downloading ${PKG_URL}"
	TMP_DEB="$(mktemp -d)/urmail.deb"
	if ! curl -fLsS --retry 3 --connect-timeout 20 --max-time 600 "$PKG_URL" -o "$TMP_DEB"; then
		warn "the release could not be downloaded from ${PKG_URL}"
		die "Set URMAIL_MIRROR to a reachable mirror, or install the .deb manually."
	fi
fi

log "installing the package"
dpkg -i "$TMP_DEB"

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

log "preparing PostgreSQL"
systemctl enable --now postgresql >/dev/null 2>&1 || true

DB_NAME=urmail
DB_APP_PASS="$(openssl rand -hex 24)"
DB_PLATFORM_PASS="$(openssl rand -hex 24)"
DB_MIGRATE_PASS="$(openssl rand -hex 24)"
DB_DOVECOT_PASS="$(openssl rand -hex 24)"

create_role() {
	role="$1"
	pass="$2"
	if su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='$role'\"" | grep -q 1; then
		su - postgres -c "psql -v ON_ERROR_STOP=1 -c \"ALTER ROLE $role LOGIN PASSWORD '$pass'\"" >/dev/null
	else
		su - postgres -c "psql -v ON_ERROR_STOP=1 -c \"CREATE ROLE $role LOGIN PASSWORD '$pass'\"" >/dev/null
	fi
}

create_role urmail_migrate "$DB_MIGRATE_PASS"
create_role urmail_app "$DB_APP_PASS"
create_role urmail_platform "$DB_PLATFORM_PASS"
create_role urmail_dovecot "$DB_DOVECOT_PASS"

su - postgres -c "psql -tAc \"SELECT 1 FROM pg_database WHERE datname='$DB_NAME'\"" | grep -q 1 || {
	su - postgres -c "createdb -O urmail_migrate $DB_NAME"
}
su - postgres -c "psql -v ON_ERROR_STOP=1 -c \"ALTER DATABASE $DB_NAME OWNER TO urmail_migrate\"" >/dev/null
su - postgres -c "psql -v ON_ERROR_STOP=1 -d $DB_NAME -f /usr/share/urmail/bootstrap.sql" >/dev/null

MYHOSTNAME="${URMAIL_MYHOSTNAME:-$(hostname -f 2>/dev/null || hostname)}"
[ -n "$MYHOSTNAME" ] || die "Cannot determine the panel hostname; set URMAIL_MYHOSTNAME."
PRIMARY_DOMAIN="${URMAIL_PRIMARY_DOMAIN:-}"
if [ -z "$PRIMARY_DOMAIN" ]; then
	PRIMARY_DOMAIN="${MYHOSTNAME#*.}"
fi
[ "$PRIMARY_DOMAIN" != "$MYHOSTNAME" ] || die "Set URMAIL_PRIMARY_DOMAIN to the mail domain."
PUBLIC_IPV4="${URMAIL_PUBLIC_IPV4:-}"
if [ -z "$PUBLIC_IPV4" ]; then
	PUBLIC_IPV4="$(curl -4 -fsS --max-time 5 https://api.ipify.org 2>/dev/null || true)"
fi
[ -n "$PUBLIC_IPV4" ] || die "Set URMAIL_PUBLIC_IPV4; refusing to publish a loopback address."
NAMESERVERS="${URMAIL_NAMESERVERS:-ns1.$PRIMARY_DOMAIN}"

DB_APP_URL="postgres://urmail_app:$DB_APP_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"
DB_PLATFORM_URL="postgres://urmail_platform:$DB_PLATFORM_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"
DB_MIGRATE_URL="postgres://urmail_migrate:$DB_MIGRATE_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"
DB_DOVECOT_URL="postgres://urmail_dovecot:$DB_DOVECOT_PASS@127.0.0.1:5432/$DB_NAME?sslmode=disable"

ENV_FILE=/etc/urmail/urmaild.env
log "writing ${ENV_FILE}"
install -d -o root -g mailinabox -m 0750 /etc/urmail
if [ -f "$ENV_FILE" ]; then
	sed -i '/^URMAIL_DATABASE_URL/d' "$ENV_FILE"
fi
cat >>"$ENV_FILE" <<EOF
URMAIL_DATABASE_URL=$DB_APP_URL
URMAIL_DATABASE_URL_PLATFORM=$DB_PLATFORM_URL
URMAIL_DATABASE_URL_MIGRATE=$DB_MIGRATE_URL
URMAIL_DATABASE_URL_DOVECOT=$DB_DOVECOT_URL
URMAIL_LISTEN_ADDR=127.0.0.1:8080
URMAIL_STORAGE_ROOT=/var/mail
URMAIL_AGENT_SOCKET=/run/urmail/agent.sock
URMAIL_POLICY_SOCKET=/run/urmail-policy/policy.sock
URMAIL_POLICY_ENABLED=true
URMAIL_ARCHIVE_ENABLED=true
URMAIL_DNS_PROVIDER=
URMAIL_CLOUDFLARE_TOKEN=
URMAIL_DNSPOD_TOKEN=
URMAIL_ALIYUN_ACCESS_KEY_ID=
URMAIL_ALIYUN_ACCESS_KEY_SECRET=
URMAIL_STRIPE_SECRET_KEY=
URMAIL_STRIPE_WEBHOOK_SECRET=
URMAIL_MASTER_KEY_PATH=/etc/urmail/master.key
URMAIL_LOG_LEVEL=info
URMAIL_LOCALE=zh-CN
URMAIL_MYHOSTNAME=$MYHOSTNAME
URMAIL_PRIMARY_DOMAIN=$PRIMARY_DOMAIN
URMAIL_PUBLIC_IPV4=$PUBLIC_IPV4
URMAIL_NAMESERVERS=$NAMESERVERS
EOF
chown root:mailinabox "$ENV_FILE"
chmod 0640 "$ENV_FILE"

if [ ! -f /etc/urmail/master.key ]; then
	log "generating the master key"
	(umask 077; openssl genrsa -traditional -out /etc/urmail/master.key 2048 >/dev/null 2>&1)
fi
chown root:mailinabox /etc/urmail/master.key
chmod 0640 /etc/urmail/master.key

# Self-signed certificates are a bootstrap fallback so nginx/Postfix/Dovecot can
# start before ACME succeeds. The renewal path replaces them later.
PANEL_CERT_DIR=/var/lib/urmail/certs/panel
MAIL_CERT_DIR=/var/mail/ssl
install -d -o mailinabox -g mailinabox -m 0750 "$PANEL_CERT_DIR"
install -d -o root -g mailinabox -m 0755 "$MAIL_CERT_DIR"
if [ ! -f "$PANEL_CERT_DIR/fullchain.pem" ]; then
	log "generating the bootstrap panel certificate for ${MYHOSTNAME}"
	openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
		-keyout "$PANEL_CERT_DIR/privkey.pem" \
		-out "$PANEL_CERT_DIR/fullchain.pem" \
		-subj "/CN=$MYHOSTNAME" \
		-addext "subjectAltName=DNS:$MYHOSTNAME" >/dev/null 2>&1
	chown root:mailinabox "$PANEL_CERT_DIR/privkey.pem" "$PANEL_CERT_DIR/fullchain.pem"
	chmod 0600 "$PANEL_CERT_DIR/privkey.pem"
	chmod 0644 "$PANEL_CERT_DIR/fullchain.pem"
fi
if [ ! -f "$MAIL_CERT_DIR/$MYHOSTNAME.cert" ]; then
	log "generating the bootstrap mail certificate for ${MYHOSTNAME}"
	openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
		-keyout "$MAIL_CERT_DIR/$MYHOSTNAME.key" \
		-out "$MAIL_CERT_DIR/$MYHOSTNAME.cert" \
		-subj "/CN=$MYHOSTNAME" \
		-addext "subjectAltName=DNS:$MYHOSTNAME" >/dev/null 2>&1
	chmod 0600 "$MAIL_CERT_DIR/$MYHOSTNAME.key"
	chmod 0644 "$MAIL_CERT_DIR/$MYHOSTNAME.cert"
fi

log "applying database migrations"
urmailctl migrate up

ADMIN_EMAIL="${URMAIL_ADMIN_EMAIL:-admin@$PRIMARY_DOMAIN}"
ADMIN_PASSWORD="${URMAIL_ADMIN_PASSWORD:-$(openssl rand -hex 12)}"
log "creating the initial administrator ${ADMIN_EMAIL}"
urmailctl setup --email "$ADMIN_EMAIL" --password "$ADMIN_PASSWORD" --reset-password --brand default --tenant default --tenant-name "Default"
umask 077
cat >/root/urmail-initial-admin.txt <<EOF
email=$ADMIN_EMAIL
password=$ADMIN_PASSWORD
panel=https://$MYHOSTNAME
EOF
chmod 0600 /root/urmail-initial-admin.txt


# ---------------------------------------------------------------------------
# Services
# ---------------------------------------------------------------------------

# NSD's Debian package does not include a conf.d drop-in by default. Ensure
# the generated zone configuration is loaded before starting the service.
if [ -f /etc/nsd/nsd.conf ] && ! grep -q 'nsd.conf.d' /etc/nsd/nsd.conf; then
	printf '\ninclude: "/etc/nsd/nsd.conf.d/*.conf"\n' >>/etc/nsd/nsd.conf
fi

log "starting the services"
systemctl daemon-reload
systemctl enable urmaild-agent.service >/dev/null 2>&1 || true
systemctl enable urmaild.service >/dev/null 2>&1 || true
# restart, not just enable --now: an upgrade must replace an already-running
# binary and reload the newly written environment file.
systemctl restart urmaild-agent.service
systemctl restart urmaild.service
systemctl enable --now urmail-cert-renew.timer >/dev/null 2>&1 || true
systemctl reload nginx 2>/dev/null || systemctl restart nginx
# urmaild renders and reloads Dovecot/Postfix shortly after startup. Give the
# first reconcile time to settle so IMAP/SMTP are immediately usable.
sleep 5

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
