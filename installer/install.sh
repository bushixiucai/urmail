#!/usr/bin/env bash
# urMail.cn installer.
#
# The installer's job is narrow on purpose: install the dependencies, install
# the package, initialise the database and run the first-time setup. Every
# later configuration change is driven by the service itself, not by re-running
# a pile of shell scripts.
set -euo pipefail

REPO="${URMAIL_REPO:-https://github.com/bushixiucai/urmail}"
RELEASE_VERSION="${URMAIL_VERSION:-latest}"
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
	MIRROR_BASE="${MIRROR:-${REPO}}"
	if [ "$RELEASE_VERSION" = "latest" ]; then
		RELEASE_BASE="${MIRROR_BASE}/releases/latest/download"
	else
		RELEASE_VERSION="${RELEASE_VERSION#v}"
		RELEASE_BASE="${MIRROR_BASE}/releases/download/v${RELEASE_VERSION}"
	fi
	TMP_DIR="$(mktemp -d)"
	trap 'rm -rf "$TMP_DIR"' EXIT
	curl -fLsS --retry 3 --connect-timeout 20 --max-time 60 "$RELEASE_BASE/manifest-${ARCH}.json" -o "$TMP_DIR/manifest.json"
	mapfile -t ASSET < <(python3 - "$TMP_DIR/manifest.json" "$ARCH" "$RELEASE_VERSION" <<'PYCODE'
import json, re, sys
m = json.load(open(sys.argv[1]))
assert m["architecture"] == sys.argv[2], "architecture mismatch"
assert sys.argv[3] == "latest" or m["version"] == sys.argv[3], "version mismatch"
assert re.fullmatch(r"urmail_[A-Za-z0-9.+~-]+_(amd64|arm64)\.deb", m["filename"]), "invalid filename"
assert re.fullmatch(r"[0-9a-f]{64}", m["sha256"]), "invalid checksum"
print(m["filename"])
print(m["sha256"])
PYCODE
)
	[ "${#ASSET[@]}" -eq 2 ] || die "Invalid release manifest."
	PKG_URL="$RELEASE_BASE/${ASSET[0]}"
	TMP_DEB="$TMP_DIR/${ASSET[0]}"
	log "downloading ${PKG_URL}"
	curl -fLsS --retry 3 --connect-timeout 20 --max-time 600 "$PKG_URL" -o "$TMP_DEB" || die "Release download failed; configure URMAIL_MIRROR or URMAIL_PACKAGE."
	printf '%s  %s\n' "${ASSET[1]}" "$TMP_DEB" | sha256sum -c - || die "Package checksum mismatch."

fi

log "installing the package"
if [ -f /etc/urmail/urmaild.env ]; then
  install -d -o root -g root -m 0700 /root/urmail-upgrade-backups
  BACKUP_DIR="$(mktemp -d /root/urmail-upgrade-backups/config.XXXXXX)"
  install -o root -g root -m 0600 /etc/urmail/urmaild.env "$BACKUP_DIR/urmaild.env"
  if [ -f /etc/urmail/agent.env ]; then
    install -o root -g root -m 0600 /etc/urmail/agent.env "$BACKUP_DIR/agent.env"
  fi
fi
# Stop the old protocol pair before installing either replacement binary.
systemctl stop urmaild.service urmaild-agent.service 2>/dev/null || true
dpkg -i "$TMP_DEB"

# ---------------------------------------------------------------------------
# Database
# ---------------------------------------------------------------------------

log "preparing PostgreSQL"
systemctl daemon-reload
systemctl enable --now postgresql >/dev/null 2>&1 || die "Cannot start PostgreSQL service."
if ! pg_isready -q -h /var/run/postgresql -p 5432; then
  mapfile -t PG_CLUSTERS < <(pg_lsclusters --no-header | awk '$3 == 5432 { print $1 " " $2 }')
  [ "${#PG_CLUSTERS[@]}" -eq 1 ] || die "Expected exactly one PostgreSQL cluster on port 5432."
  read -r PG_MAJOR PG_CLUSTER <<<"${PG_CLUSTERS[0]}"
  [[ "$PG_MAJOR" =~ ^[0-9]+$ && "$PG_CLUSTER" =~ ^[A-Za-z0-9_-]+$ ]] || die "Invalid PostgreSQL cluster identity."
  systemctl start "postgresql@${PG_MAJOR}-${PG_CLUSTER}" || die "Cannot start PostgreSQL cluster."
fi
for _attempt in $(seq 1 60); do
  pg_isready -q -h /var/run/postgresql -p 5432 && break
  sleep 1
done
pg_isready -q -h /var/run/postgresql -p 5432 || die "PostgreSQL did not become ready."

if [ -n "${BACKUP_DIR:-}" ]; then
  log "saving the pre-upgrade database"
  python3 - "$BACKUP_DIR/urmaild.env" "$BACKUP_DIR/database.dump" <<'PYCODE'
import os, subprocess, sys, urllib.parse
values = {}
for line in open(sys.argv[1]):
    key, sep, value = line.strip().partition("=")
    if sep and not key.startswith("#"):
        values[key] = value.strip().strip('"').strip("'")
uri = values.get("URMAIL_DATABASE_URL_PLATFORM")
if not uri:
    raise SystemExit("Pre-upgrade backup requires the platform database role")
parsed = urllib.parse.urlsplit(uri)
environment = dict(os.environ)
if parsed.password is not None:
    environment["PGPASSWORD"] = urllib.parse.unquote(parsed.password)
netloc = parsed.netloc.rsplit("@", 1)[-1]
if parsed.username is not None:
    netloc = urllib.parse.quote(urllib.parse.unquote(parsed.username), safe="") + "@" + netloc
safe_uri = urllib.parse.urlunsplit(parsed._replace(netloc=netloc))
fd = os.open(sys.argv[2], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
os.close(fd)
subprocess.run(["pg_dump", "--no-owner", "--format=custom", "--file=" + sys.argv[2], safe_uri],
               env=environment, check=True)
PYCODE
fi

DB_NAME=urmail
# Keep runtime credentials stable across upgrades so rollback configurations
# continue to authenticate. New installations receive random credentials.
read_database_password() {
  python3 - "${BACKUP_DIR:-}/urmaild.env" "$1" <<'PYCODE'
import pathlib, secrets, sys, urllib.parse
values = {}
source = pathlib.Path(sys.argv[1])
if source.is_file():
    for line in source.read_text().splitlines():
        key, sep, value = line.strip().partition("=")
        if sep and not key.startswith("#"):
            values[key] = value.strip().strip('"').strip("'")
password = urllib.parse.urlsplit(values.get(sys.argv[2], "")).password
print(urllib.parse.unquote(password) if password else secrets.token_hex(24))
PYCODE
}
DB_APP_PASS="$(read_database_password URMAIL_DATABASE_URL)"
DB_PLATFORM_PASS="$(read_database_password URMAIL_DATABASE_URL_PLATFORM)"
DB_MIGRATE_PASS="$(read_database_password URMAIL_DATABASE_URL_MIGRATE)"
DB_DOVECOT_PASS="$(read_database_password URMAIL_DATABASE_URL_DOVECOT)"

create_role() {
	role="$1"
	pass="$(printf '%s' "$2" | sed "s/'/''/g")"
	if su - postgres -c "psql -tAc \"SELECT 1 FROM pg_roles WHERE rolname='$role'\"" | grep -q 1; then
		printf "ALTER ROLE %s LOGIN PASSWORD '%s';\n" "$role" "$pass" | runuser -u postgres -- psql -v ON_ERROR_STOP=1 >/dev/null
	else
		printf "CREATE ROLE %s LOGIN PASSWORD '%s';\n" "$role" "$pass" | runuser -u postgres -- psql -v ON_ERROR_STOP=1 >/dev/null
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

# Preserve operator configuration without evaluating the env file as shell code.
ACME_SERVER_OVERRIDE="${URMAIL_ACME_SERVER+x}"
ACME_ROOT_CA_OVERRIDE="${URMAIL_ACME_ROOT_CA+x}"
for name in URMAIL_DATABASE_URL URMAIL_DATABASE_URL_PLATFORM URMAIL_DATABASE_URL_MIGRATE URMAIL_DATABASE_URL_DOVECOT URMAIL_MYHOSTNAME URMAIL_PRIMARY_DOMAIN URMAIL_PUBLIC_IPV4 URMAIL_NAMESERVERS URMAIL_STORAGE_ROOT URMAIL_DNS_PROVIDER URMAIL_CLOUDFLARE_TOKEN URMAIL_DNSPOD_TOKEN URMAIL_ALIYUN_ACCESS_KEY_ID URMAIL_ALIYUN_ACCESS_KEY_SECRET URMAIL_STRIPE_SECRET_KEY URMAIL_STRIPE_WEBHOOK_SECRET URMAIL_ACME_SERVER URMAIL_ACME_ROOT_CA; do
  if [ -z "${!name+x}" ] && [ -f /etc/urmail/urmaild.env ]; then
    value="$(python3 - /etc/urmail/urmaild.env "$name" <<'PYCODE'
import sys
values = {}
for line in open(sys.argv[1]):
    key, sep, value = line.strip().partition("=")
    if sep and not key.startswith("#"):
        values[key] = value.strip().strip('"').strip("'")
print(values.get(sys.argv[2], ""))
PYCODE
)"
    export "$name=$value"
  fi
done
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
URMAIL_STORAGE_ROOT=${URMAIL_STORAGE_ROOT:-/var/mail}
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
python3 - "$ENV_FILE" <<'PYCODE'
import os, pathlib, sys
path = pathlib.Path(sys.argv[1])
values = {}
for line in path.read_text().splitlines():
    key, sep, value = line.partition("=")
    if sep and not key.startswith("#"):
        values[key] = value
for key in ("URMAIL_DNS_PROVIDER", "URMAIL_CLOUDFLARE_TOKEN", "URMAIL_DNSPOD_TOKEN", "URMAIL_ALIYUN_ACCESS_KEY_ID", "URMAIL_ALIYUN_ACCESS_KEY_SECRET", "URMAIL_STRIPE_SECRET_KEY", "URMAIL_STRIPE_WEBHOOK_SECRET", "URMAIL_ACME_SERVER", "URMAIL_ACME_ROOT_CA"):
    if key in os.environ:
        values[key] = os.environ[key]
if any("\n" in value or "\r" in value for value in values.values()):
    raise SystemExit("Environment values must not contain line breaks")
path.write_text("".join(f"{key}={value}\n" for key, value in values.items()))
PYCODE
# Root service configuration is not shared with the unprivileged daemon.
install -o root -g root -m 0600 "$ENV_FILE" /etc/urmail/agent.env
# Root-only ACME trust settings remain authoritative across an upgrade unless
# the operator supplied an explicit replacement to this installer.
python3 - /etc/urmail/agent.env "${BACKUP_DIR:-}/agent.env" "$ACME_SERVER_OVERRIDE" "$ACME_ROOT_CA_OVERRIDE" <<'PYCODE'
import os, pathlib, sys
current, previous = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
def read(path):
    values = {}
    if path.is_file():
        for line in path.read_text().splitlines():
            key, sep, value = line.strip().partition("=")
            if sep and not key.startswith("#"):
                values[key] = value.strip().strip('"').strip("'")
    return values
values, old = read(current), read(previous)
for key, explicit in zip(("URMAIL_ACME_SERVER", "URMAIL_ACME_ROOT_CA"), sys.argv[3:]):
    if not explicit and key in old:
        values[key] = old[key]
current.write_text("".join(key + "=" + value + "\n" for key, value in values.items()))
os.chmod(current, 0o600)
PYCODE

if [ ! -f /etc/urmail/master.key ]; then
	log "generating the master key"
	(umask 077; openssl genrsa -traditional -out /etc/urmail/master.key 2048 >/dev/null 2>&1)
fi
chown root:mailinabox /etc/urmail/master.key
chmod 0640 /etc/urmail/master.key

# Self-signed certificates are a bootstrap fallback so nginx/Postfix/Dovecot can
# start before ACME succeeds. The renewal path replaces them later.
PANEL_CERT_DIR=/var/lib/urmail-agent/certs/panel
MAIL_CERT_DIR=/var/lib/urmail-agent/certs/mail
install -d -o root -g root -m 0700 /var/lib/urmail-agent
install -d -o root -g root -m 0700 "$PANEL_CERT_DIR"
install -d -o root -g root -m 0700 "$MAIL_CERT_DIR"
install -d -o root -g root -m 0755 /var/lib/urmail-acme
if [ ! -f "$PANEL_CERT_DIR/fullchain.pem" ]; then
	log "generating the bootstrap panel certificate for ${MYHOSTNAME}"
	openssl req -x509 -newkey rsa:2048 -sha256 -days 365 -nodes \
		-keyout "$PANEL_CERT_DIR/privkey.pem" \
		-out "$PANEL_CERT_DIR/fullchain.pem" \
		-subj "/CN=$MYHOSTNAME" \
		-addext "subjectAltName=DNS:$MYHOSTNAME" >/dev/null 2>&1
	chown root:root "$PANEL_CERT_DIR/privkey.pem" "$PANEL_CERT_DIR/fullchain.pem"
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
URMAIL_SETUP_PASSWORD="$ADMIN_PASSWORD" urmailctl setup --email "$ADMIN_EMAIL" --brand default --tenant default --tenant-name "Default"
umask 077
if [ ! -f /root/urmail-initial-admin.txt ]; then
cat >/root/urmail-initial-admin.txt <<EOF
email=$ADMIN_EMAIL
password=$ADMIN_PASSWORD
panel=https://$MYHOSTNAME
EOF
chmod 0600 /root/urmail-initial-admin.txt
fi


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
systemctl enable --now urmail-cert-renew.timer
systemctl enable --now urmail-usage-sync.timer
systemctl reload nginx 2>/dev/null || systemctl restart nginx
# Allow the bounded three-minute apply and initial snapshot/bootstrap to finish.
# Readiness still requires the applied generation and successful daemon probes.
urmailctl config status --wait 240s

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
