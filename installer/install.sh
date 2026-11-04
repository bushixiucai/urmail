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

UPGRADE_SUCCESS=0
cleanup_installer() {
	if [ -n "${TMP_DIR:-}" ] && [ -d "$TMP_DIR" ]; then
		rm -rf "$TMP_DIR"
	fi
	if [ -n "${CLI_EXTRACT_DIR:-}" ] && [ -d "$CLI_EXTRACT_DIR" ]; then
		rm -rf "$CLI_EXTRACT_DIR"
	fi
	if [ -f /var/lib/urmail-backup/upgrade.hold ]; then
		warn "===================================================================="
		warn "UPGRADE FENCE RETAINED (/var/lib/urmail-backup/upgrade.hold)"
		warn "The upgrade fence and service drop-in guards remain PRESERVED."
		warn "All mail writers and background daemons remain held."
		warn "Inspect status or complete Phase B release when ready:"
		warn "  urmailctl upgrade status"
		warn "  urmailctl upgrade resume"
		warn "  urmailctl upgrade rollback"
		warn "Automatic release is unavailable; manual risk acceptance is a separate operator decision."
		warn "===================================================================="
	fi
}
trap cleanup_installer EXIT INT TERM

# BEGIN LEGACY INSTALLATION GUARD
# Legacy MIAB data is not a PostgreSQL backup and cannot use this installer.
# Guard legacy source data before this entry point mutates installation state,
# including mixed installations with a partially created urMail environment.
for legacy_input in /etc/mailinabox.conf /home/user-data/mail/users.sqlite /var/lib/mailinabox/mail/users.sqlite; do
  if [ -e "$legacy_input" ] || [ -L "$legacy_input" ]; then
    die "Legacy Mail-in-a-Box data detected. In-place installation is blocked: use the reviewed M8 inventory, import dry-run and migration procedure; preserve the source installation."
  fi
done
# END LEGACY INSTALLATION GUARD

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

# System dependency mutations are deferred until after the upgrade checkpoint.

# Package
# ---------------------------------------------------------------------------
install -d -o root -g root -m 0755 /var/lib/nsd/urmail-secondary /etc/systemd/system/nsd.service.d

# Download/verification bootstrap must already exist. Never install dependencies
# ahead of the upgrade fence merely to obtain the replacement fencing CLI.
for bootstrap_tool in curl python3 openssl dpkg-deb sha256sum; do
  command -v "$bootstrap_tool" >/dev/null 2>&1 || die "Missing bootstrap tool: $bootstrap_tool. Install bootstrap tools before starting installation; no dependencies have been changed."
done
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
	curl -fLsS --retry 3 --connect-timeout 20 --max-time 60 "$RELEASE_BASE/manifest-${ARCH}.json" -o "$TMP_DIR/manifest.json"
	# HOST-01: the manifest carries the release authority. Verify an Ed25519
	# signature against the key pinned below before trusting any hash in it;
	# a compromised mirror or release upload then fails closed.
	cat > "$TMP_DIR/urmail-release.pub" <<'PUBKEY'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEA1bFMpOvlJfO//7iurp/YGZuJvzN1nY61E5wAZXh8jTk=
-----END PUBLIC KEY-----
PUBKEY
	curl -fLsS --retry 3 --connect-timeout 20 --max-time 60 "$RELEASE_BASE/manifest-${ARCH}.json.sig" -o "$TMP_DIR/manifest.json.sig" \
		|| die "Release manifest signature missing; refusing unverified install."
	openssl pkeyutl -verify -pubin -inkey "$TMP_DIR/urmail-release.pub" -rawin \
		-in "$TMP_DIR/manifest.json" -sigfile "$TMP_DIR/manifest.json.sig" \
		> /dev/null 2>&1 || die "Release manifest signature verification failed."
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
IS_UPGRADE=0
if [ -f /etc/urmail/urmaild.env ]; then
  IS_UPGRADE=1
  # 1. Extract new CLI from the deb package into a controlled temp directory
  CLI_EXTRACT_DIR="$(mktemp -d /tmp/urmail-upgrade-cli.XXXXXX)"
  chmod 0700 "$CLI_EXTRACT_DIR"
  NEW_URMAILCTL=""
  if dpkg-deb -x "$TMP_DEB" "$CLI_EXTRACT_DIR" 2>/dev/null && [ -x "$CLI_EXTRACT_DIR/usr/bin/urmailctl" ]; then
    NEW_URMAILCTL="$CLI_EXTRACT_DIR/usr/bin/urmailctl"
  else
    die "Upgrade requires a verified package containing the new fencing CLI; no dependency changes have been made."
  fi

  # One root CLI owns operation.lock across this entire installer transaction.
  # The child re-enters only with an inode-validated inherited lock descriptor.
  if ! "$NEW_URMAILCTL" upgrade check-installer-lease; then
    install -o root -g root -m 0700 "${BASH_SOURCE[0]}" "$CLI_EXTRACT_DIR/installer.sh"
    installer_status=0
    "$NEW_URMAILCTL" upgrade run-installer --installer "$CLI_EXTRACT_DIR/installer.sh" -- "$@" || installer_status=$?
    exit "$installer_status"
  fi

  # 2. Acquire operation.lock, create upgrade.hold and inject drop-in guards to Postfix, Dovecot, Nginx
  [ -n "$NEW_URMAILCTL" ] || die "Upgrade fencing CLI is unavailable."
  log "establishing upgrade fence and pausing mail writers"
  "$NEW_URMAILCTL" upgrade fence --reason "managed upgrade to $(basename "$TMP_DEB")"

  # Refuse unresolved recovery state and unsupported database identities before
  # package, environment or schema changes. Never start the agent to recover here.
  python3 - /etc/urmail/urmaild.env /var/lib/urmail-agent/pending.json <<'PYCODE'
import os, stat, sys, urllib.parse
try:
    os.lstat(sys.argv[2])
except FileNotFoundError:
    pass
else:
    raise SystemExit("Pending apply journal requires explicit recovery before upgrade; fence retained")
values = {}
for line in open(sys.argv[1]):
    key, sep, value = line.strip().partition("=")
    if sep and not key.startswith("#"):
        values[key] = value.strip().strip('"').strip("'")
for flag in ("URMAIL_DEV_MODE", "URMAIL_QUARANTINED", "URMAIL_WORKERS_HELD", "URMAIL_DNS_SECONDARY_ONLY"):
    if values.get(flag, "false").lower() not in ("", "false", "0"):
        raise SystemExit("Managed upgrade refuses restricted deployment mode before changes: " + flag)
for key, role in (("URMAIL_DATABASE_URL", "urmail_app"),
                  ("URMAIL_DATABASE_URL_PLATFORM", "urmail_platform"),
                  ("URMAIL_DATABASE_URL_MIGRATE", "urmail_migrate"),
                  ("URMAIL_DATABASE_URL_DOVECOT", "urmail_dovecot")):
    parsed = urllib.parse.urlsplit(values.get(key, ""))
    query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
    if (parsed.scheme not in ("postgres", "postgresql") or parsed.username != role
        or parsed.hostname not in (None, "127.0.0.1", "localhost", "::1")
        or parsed.port not in (None, 5432) or parsed.fragment
        or urllib.parse.unquote(parsed.path) != "/urmail"
        or set(query) - {"sslmode", "host"}
        or ("host" in query and query["host"] != ["/var/run/postgresql"])):
        raise SystemExit("Upgrade requires all four managed local urmail database identities; fence retained")
key_path = values.get("URMAIL_MASTER_KEY_PATH") or "/etc/urmail/master.key"
if not os.path.isabs(key_path):
    raise SystemExit("Existing master key must have an absolute path; fence retained")
fd = os.open(key_path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
try:
    info = os.fstat(fd)
    if not stat.S_ISREG(info.st_mode) or info.st_size == 0 or info.st_mode & 0o007:
        raise SystemExit("Existing master key must be nonempty, regular and inaccessible to other users; fence retained")
finally:
    os.close(fd)
PYCODE

  # 3. Create pre-upgrade configuration checkpoint
  install -d -o root -g root -m 0700 /root/urmail-upgrade-backups
  BACKUP_DIR="$(mktemp -d /root/urmail-upgrade-backups/config.XXXXXX)"
  install -o root -g root -m 0600 /etc/urmail/urmaild.env "$BACKUP_DIR/urmaild.env"
  [ -f /etc/urmail/agent.env ] || die "Existing agent configuration is required for an upgrade checkpoint; fence retained."
  install -o root -g root -m 0600 /etc/urmail/agent.env "$BACKUP_DIR/agent.env"

  # 4. Ensure PostgreSQL is accessible to perform CHECKPOINT and database dump BEFORE unpacking new binaries
  systemctl daemon-reload
  systemctl enable --now postgresql >/dev/null 2>&1 || die "Cannot start PostgreSQL service."
  if ! pg_isready -q -h /var/run/postgresql -p 5432; then
    mapfile -t PG_CLUSTERS < <(pg_lsclusters --no-header 2>/dev/null | awk '$3 == 5432 { print $1 " " $2 }')
    if [ "${#PG_CLUSTERS[@]}" -eq 1 ]; then
      read -r PG_MAJOR PG_CLUSTER <<<"${PG_CLUSTERS[0]}"
      if [[ "$PG_MAJOR" =~ ^[0-9]+$ && "$PG_CLUSTER" =~ ^[A-Za-z0-9_-]+$ ]]; then
        systemctl start "postgresql@${PG_MAJOR}-${PG_CLUSTER}" || true
      fi
    fi
  fi
  for _attempt in $(seq 1 60); do
    pg_isready -q -h /var/run/postgresql -p 5432 && break
    sleep 1
  done
  if pg_isready -q -h /var/run/postgresql -p 5432; then
    python3 - <<'PYCODE'
import os, subprocess
environment = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
subprocess.run(["/usr/sbin/runuser", "-u", "postgres", "--", "/usr/bin/psql",
                "-h", "/var/run/postgresql", "-p", "5432", "-d", "postgres",
                "-v", "ON_ERROR_STOP=1", "-c", "CHECKPOINT"],
               env=environment, stdout=subprocess.DEVNULL, check=True)
PYCODE
    log "saving the pre-upgrade database checkpoint"
    python3 - "$BACKUP_DIR/urmaild.env" "$BACKUP_DIR/database.dump" <<'PYCODE'
import os, re, subprocess, sys, urllib.parse
values = {}
for line in open(sys.argv[1]):
    key, sep, value = line.strip().partition("=")
    if sep and not key.startswith("#"):
        values[key] = value.strip().strip('"').strip("'")
uri = values.get("URMAIL_DATABASE_URL_MIGRATE")
if not uri:
    raise SystemExit("Pre-upgrade backup requires the controlled local migration database URL")
parsed = urllib.parse.urlsplit(uri)
query = urllib.parse.parse_qs(parsed.query, keep_blank_values=True)
try:
    port = parsed.port
except ValueError:
    raise SystemExit("Invalid local backup database port")
name = urllib.parse.unquote(parsed.path.removeprefix("/"))
if (parsed.scheme not in ("postgres", "postgresql") or parsed.username != "urmail_migrate"
        or parsed.hostname not in (None, "127.0.0.1", "localhost", "::1")
        or port not in (None, 5432) or parsed.fragment
        or name != "urmail"
        or set(query) - {"sslmode", "host"}
        or ("host" in query and query["host"] != ["/var/run/postgresql"])):
    raise SystemExit("Pre-upgrade backup requires the managed local PostgreSQL target")
# FORCE RLS is not bypassed by inherited role membership. Root uses the
# fixed local peer identity; runtime and migration privileges stay limited.
peer_uri = "postgresql://postgres/" + name + "?host=/var/run/postgresql&port=5432&sslmode=disable"
environment = {key: value for key, value in os.environ.items() if not key.startswith("PG")}
fd = os.open(sys.argv[2], os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
with os.fdopen(fd, "wb") as output:
    subprocess.run(["/usr/sbin/runuser", "-u", "postgres", "--", "/usr/bin/pg_dump",
                    "--no-owner", "--format=custom", "--dbname", peer_uri],
                   env=environment, stdout=output, stderr=subprocess.DEVNULL, check=True)
    output.flush()
    os.fsync(output.fileno())
PYCODE
  else
    die "PostgreSQL is unavailable for the pre-upgrade dump; fence retained."
  fi
  python3 "$CLI_EXTRACT_DIR/usr/share/urmail/upgrade-checkpoint.py" create "$BACKUP_DIR" || die "Cannot finalize upgrade checkpoint; fence retained."
fi

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
	rsyslog nginx postfix dovecot-core dovecot-imapd dovecot-pop3d dovecot-lmtpd \
	dovecot-sieve dovecot-managesieved dovecot-pgsql nsd bind9-utils bind9-dnsutils opendkim postgresql \
	ca-certificates curl gnupg openssl certbot rsync duplicity python3-venv age openssh-client

# ---------------------------------------------------------------------------

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

# Pre-upgrade checkpoint and database dump were completed above before package unpacking.

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
    printf -v "$name" "%s" "$value"
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
python3 /usr/share/urmail/configure-env.py "$ENV_FILE" <<EOF
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
URMAIL_WEBMAIL_NATIVE=true
URMAIL_CARDDAV_ENABLED=true
URMAIL_CALDAV_ENABLED=true
URMAIL_EAS_ENABLED=true
URMAIL_DNS_PROVIDER=
URMAIL_CLOUDFLARE_TOKEN=
URMAIL_DNSPOD_TOKEN=
URMAIL_ALIYUN_ACCESS_KEY_ID=
URMAIL_ALIYUN_ACCESS_KEY_SECRET=
URMAIL_STRIPE_SECRET_KEY=
URMAIL_STRIPE_WEBHOOK_SECRET=
URMAIL_ACME_SERVER=
URMAIL_ACME_ROOT_CA=
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
# Preserve all operator root-service settings without copying secrets back to the daemon.
python3 /usr/share/urmail/configure-agent-env.py "$ENV_FILE" /etc/urmail/agent.env
chown root:root /etc/urmail/agent.env
chmod 0600 /etc/urmail/agent.env

if [ "$IS_UPGRADE" -eq 0 ]; then
if [ ! -f /etc/urmail/master.key ]; then
	log "generating the master key"
	(umask 077; openssl genrsa -traditional -out /etc/urmail/master.key 2048 >/dev/null 2>&1)
fi
chown root:mailinabox /etc/urmail/master.key
chmod 0640 /etc/urmail/master.key
fi

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
# Historical migrations briefly need retired cluster roles. Use the fixed local
# peer bootstrap identity for this one command, with ordinary migration work
# still running as urmail_migrate. The migrator temporarily clears that role
# only for guarded cluster-role operations; no superuser URL is persisted.
MIGRATION_BOOTSTRAP_URL="postgresql://postgres/$DB_NAME?host=/var/run/postgresql&port=5432&sslmode=disable&role=urmail_migrate"
runuser -u postgres -- env URMAIL_ENV_FILE=/dev/null URMAIL_POLICY_ENABLED=true \
  URMAIL_DATABASE_URL="$MIGRATION_BOOTSTRAP_URL" \
  URMAIL_DATABASE_URL_PLATFORM="$MIGRATION_BOOTSTRAP_URL" \
  URMAIL_DATABASE_URL_MIGRATE="$MIGRATION_BOOTSTRAP_URL" \
  /usr/bin/urmailctl migrate up

ADMIN_EMAIL="${URMAIL_ADMIN_EMAIL:-admin@$PRIMARY_DOMAIN}"
ADMIN_PASSWORD="${URMAIL_ADMIN_PASSWORD:-$(openssl rand -hex 12)}"
if [ "$IS_UPGRADE" -eq 0 ]; then
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
fi


# ---------------------------------------------------------------------------
# Services
# ---------------------------------------------------------------------------

# NSD's Debian package does not include a conf.d drop-in by default. Ensure
# the generated zone configuration is loaded before starting the service.
if [ -f /etc/nsd/nsd.conf ] && ! grep -q 'nsd.conf.d' /etc/nsd/nsd.conf; then
	printf '\ninclude: "/etc/nsd/nsd.conf.d/*.conf"\n' >>/etc/nsd/nsd.conf
fi

if [ "${IS_UPGRADE:-0}" -eq 1 ]; then
  log "preparing offline configuration while under fence"
  URMAIL_ENV_FILE=/etc/urmail/agent.env urmailctl config prepare || die "Offline preparation failed; upgrade fence retained."
  warn "===================================================================="
  warn "UPGRADE PREPARATION & MIGRATIONS APPLIED"
  warn "Upgrade fence (/var/lib/urmail-backup/upgrade.hold) remains HELD."
  warn "All mail writers and daemons remain drop-in guarded and stopped."
  warn "Full protocol verification and persistent traffic isolation are still required."
  warn "  urmailctl upgrade status"
  warn "Default release rejects without a bound readiness receipt; retain the hold."
  warn "===================================================================="
  # Exit 2 distinguishes prepared-but-held from a fully completed installation.
  exit 2
fi

UPGRADE_SUCCESS=1

log "starting the services"
rsyslogd -N1
systemctl enable --now rsyslog.service
systemctl restart rsyslog.service
logger -p mail.info -t urmail "managed mail telemetry source initialized"
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
systemctl enable --now urmail-backup-auto.timer
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
