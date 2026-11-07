#!/usr/bin/env bash
# Build the urMail.cn Debian package.
#
# The package contains urMail.cn's own artifacts and service configuration.
# Native Go webmail replaces the retired PHP, Roundcube, Nextcloud and Z-Push
# stacks; those applications are neither bundled nor installed by this package.
set -euo pipefail

VERSION="${1:-0.1.0}"
ARCH="${ARCH:-$(go env GOARCH)}"
case "$ARCH" in amd64|arm64) ;; *) echo "Unsupported package architecture: $ARCH" >&2; exit 1 ;; esac
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN_DIR="${URMAIL_BIN_DIR:-$ROOT/bin}"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ ! -x "$BIN_DIR/urmaild" ]; then
	echo "bin/urmaild is missing; run 'make build' first." >&2
	exit 1
fi

# The package metadata must describe the executable architecture, not the host.
for binary in urmaild urmaild-agent urmailctl urmail-archive; do
  magic=$(od -An -tx1 -N4 "$BIN_DIR/$binary" | tr -d ' \n')
  machine=$(od -An -tu1 -j18 -N2 "$BIN_DIR/$binary" | xargs)
  [ "$magic" = 7f454c46 ] || { echo "$binary is not a Linux ELF executable" >&2; exit 1; }
  case "$ARCH:$machine" in "amd64:62 0"|"arm64:183 0") ;; *) echo "$binary architecture does not match $ARCH" >&2; exit 1 ;; esac
done

install -d "$STAGE/DEBIAN"
install -d "$STAGE/usr/bin"
install -d "$STAGE/usr/lib/urmail"
install -d "$STAGE/lib/systemd/system"
install -d "$STAGE/etc/systemd/system/opendkim.service.d"
install -d "$STAGE/etc/urmail"
install -d "$STAGE/etc/rsyslog.d"
install -m 0644 "$ROOT/packaging/rsyslog/10-urmail-mail.conf" "$STAGE/etc/rsyslog.d/10-urmail-mail.conf"
install -d "$STAGE/etc/nginx/conf.d/urmail"
install -d "$STAGE/etc/nginx/snippets"
install -d "$STAGE/etc/dovecot/conf.d"
install -d "$STAGE/etc/nsd/nsd.conf.d"
install -d "$STAGE/etc/postfix/urmail"
install -d "$STAGE/etc/opendkim/keys"
install -d "$STAGE/usr/share/urmail"

install -m 0755 "$BIN_DIR/urmaild" "$STAGE/usr/bin/urmaild"
install -m 0755 "$BIN_DIR/urmaild-agent" "$STAGE/usr/bin/urmaild-agent"
install -m 0755 "$BIN_DIR/urmailctl" "$STAGE/usr/bin/urmailctl"
install -m 0755 "$BIN_DIR/urmail-archive" "$STAGE/usr/lib/urmail/archive-pipe"

install -m 0644 "$ROOT/packaging/systemd/urmaild.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmaild-agent.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-cert-renew.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-cert-renew.timer" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-usage-sync.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-usage-sync.timer" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-backup-auto.service" "$ROOT/packaging/systemd/urmail-backup-auto.timer" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmail-prometheus.service" "$ROOT/packaging/systemd/urmail-alertmanager.service" "$STAGE/lib/systemd/system/"
install -d "$STAGE/usr/share/urmail/observability"
install -m 0644 "$ROOT/packaging/observability/"*.yml "$STAGE/usr/share/urmail/observability/"
install -m 0644 "$ROOT/packaging/systemd/opendkim.service.d/urmail.conf" "$STAGE/etc/systemd/system/opendkim.service.d/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail.conf" "$STAGE/etc/nginx/conf.d/urmail/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/99-urmail.conf" "$STAGE/etc/nginx/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail-ssl.conf" "$STAGE/etc/nginx/snippets/urmail-ssl.conf"
install -m 0644 "$ROOT/packaging/dovecot/99-urmail.conf" "$STAGE/etc/dovecot/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/internal/db/bootstrap.sql" "$STAGE/usr/share/urmail/bootstrap.sql"
install -m 0644 "$ROOT/installer/configure-env.py" "$STAGE/usr/share/urmail/configure-env.py"
install -m 0644 "$ROOT/installer/configure-agent-env.py" "$STAGE/usr/share/urmail/configure-agent-env.py"
install -m 0644 "$ROOT/installer/upgrade-checkpoint.py" "$STAGE/usr/share/urmail/upgrade-checkpoint.py"
install -m 0644 "$ROOT/packaging/selfhost-capabilities.json" "$STAGE/usr/share/urmail/selfhost-capabilities.json"
install -m 0644 "$ROOT/packaging/SELFHOST.md" "$STAGE/usr/share/urmail/SELFHOST.md"
install -d "$STAGE/usr/share/urmail/locales"
install -m 0644 "$ROOT/internal/i18n/locales/en.json" "$ROOT/internal/i18n/locales/zh-CN.json" "$STAGE/usr/share/urmail/locales/"

cat >"$STAGE/DEBIAN/control" <<EOF
Package: urmail
Version: $VERSION
Section: mail
Priority: optional
Architecture: $ARCH
Maintainer: urMail.cn <support@urmail.cn>
Depends: rsyslog, nginx, postfix, dovecot-core, dovecot-imapd, dovecot-pop3d, dovecot-lmtpd, dovecot-sieve, dovecot-managesieved, dovecot-pgsql, nsd, bind9-utils, bind9-dnsutils, opendkim, postgresql, ca-certificates, certbot, age, openssh-client, python3
Description: urMail.cn multi-tenant email SaaS platform
 A self-hosted platform for selling domain mailboxes: multi-tenancy, quotas,
 billing, payment, DNS onboarding, certificates, observability and compliance.
EOF

cat >"$STAGE/DEBIAN/preinst" <<'EOF'
#!/bin/sh
set -e
die() { printf '%s\n' "$*" >&2; exit 1; }
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

# Snapshot existing observability configuration and enabled markers before unpack
MIGRATION_BASE="/var/lib/urmail-config-migration"
MIGRATION_DIR="$MIGRATION_BASE/observability"
OBS_DIR="/etc/urmail/observability"

if [ -d "$OBS_DIR" ] && [ -n "$(ls -A "$OBS_DIR" 2>/dev/null)" ]; then
  install -d -m 0700 "$MIGRATION_BASE"
  rm -rf "$MIGRATION_DIR"
  # cp -a replicates the live directory itself: the snapshot mirrors the
  # operator's mode and ownership (0750 root:urmail-monitor stays 0750
  # root:urmail-monitor), so restoring it in postinst never downgrades the
  # target to a hardcoded mode. No "chmod 0700" here anymore — that stamp
  # was what leaked into /etc/urmail/observability on every upgrade.
  cp -a "$OBS_DIR" "$MIGRATION_DIR"
  # Belt and braces: record the live metadata for the postinst restore.
  stat -c '%a %U %G' "$OBS_DIR" > "$MIGRATION_BASE/observability.dir-mode"
else
  rm -rf "$MIGRATION_DIR"
fi

exit 0
EOF

cat >"$STAGE/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e

if ! getent group mailinabox >/dev/null; then
	addgroup --system mailinabox
fi
if ! getent passwd mailinabox >/dev/null; then
	adduser --system --ingroup mailinabox --home /var/lib/urmail --no-create-home mailinabox
fi

# Idempotent directory creation: never touch permissions of directories that
# already exist, so operator customization (e.g. alertmanager's 0750
# root:urmail-monitor on /etc/urmail/observability) survives upgrades.
ensure_dir() {
  for dir in "$@"; do :; done  # dir is the last argument
  [ -d "$dir" ] || install -d "$@" "$dir"
}
ensure_dir -o mailinabox -g mailinabox -m 0750 /var/lib/urmail
ensure_dir -o mailinabox -g mailinabox -m 0750 /var/lib/urmail/backups
ensure_dir -o mailinabox -g mailinabox -m 0700 /var/lib/urmail/archive
ensure_dir -o root -g mailinabox -m 0750 /etc/urmail
ensure_dir -o root -g mailinabox -m 0755 /etc/postfix/urmail
for shared_dir in /etc/nginx/snippets /etc/dovecot/conf.d /etc/nsd/nsd.conf.d; do
  ensure_dir -m 0755 "$shared_dir"
done
ensure_dir -m 0755 /etc/urmail/observability

# Restore existing observability configuration from preinst snapshot if present
MIGRATION_DIR="/var/lib/urmail-config-migration/observability"
if [ -d "$MIGRATION_DIR" ] && [ -n "$(ls -A "$MIGRATION_DIR" 2>/dev/null)" ]; then
  cp -a "$MIGRATION_DIR"/. /etc/urmail/observability/
  # cp -a stamps the target top-level directory with the migration
  # directory's mode. Restore the operator's recorded mode and ownership so
  # operator settings (0750 root:urmail-monitor) survive every upgrade.
  MODE_RECORD="/var/lib/urmail-config-migration/observability.dir-mode"
  if [ -f "$MODE_RECORD" ]; then
    # shellcheck disable=SC2046 # exactly three fields: mode user group
    set -- $(cat "$MODE_RECORD")
    chmod "$1" /etc/urmail/observability
    if [ -n "${2:-}" ] && [ -n "${3:-}" ]; then
      chown "${2}:${3}" /etc/urmail/observability
    fi
    rm -f "$MODE_RECORD"
  fi
fi

# Seed templates only when target configuration never existed
if [ -d /usr/share/urmail/observability ]; then
  for tmpl in /usr/share/urmail/observability/*.yml; do
    [ -e "$tmpl" ] || continue
    target="/etc/urmail/observability/$(basename "$tmpl")"
    if [ ! -e "$target" ]; then
      install -m 0644 "$tmpl" "$target"
    fi
  done
fi

ensure_dir -o root -g opendkim -m 0750 /etc/opendkim
ensure_dir -o root -g opendkim -m 0750 /etc/opendkim/keys
# Seed the canonical file before the service drop-in is loaded, so a failed
# first generation can restore a valid stock configuration atomically.
if [ ! -e /etc/opendkim/opendkim.conf ] && [ -f /etc/opendkim.conf ]; then
  install -o root -g opendkim -m 0644 /etc/opendkim.conf /etc/opendkim/opendkim.conf
fi
for root_dir in /etc/nsd/zones /var/lib/urmail-acme /var/lib/nsd/urmail-secondary /etc/systemd/system/nsd.service.d; do
  ensure_dir -o root -g root -m 0755 "$root_dir"
done
for private_dir in /var/lib/urmail-agent /etc/letsencrypt /var/log/letsencrypt; do
  ensure_dir -o root -g root -m 0700 "$private_dir"
done

# The Debian nsd package does not always include nsd.conf.d; add the drop-in
# once so generated zones are loaded.
if [ -f /etc/nsd/nsd.conf ] && ! grep -q 'nsd.conf.d' /etc/nsd/nsd.conf; then
	printf '\ninclude: "/etc/nsd/nsd.conf.d/*.conf"\n' >>/etc/nsd/nsd.conf
fi

systemctl daemon-reload
systemctl enable rsyslog.service >/dev/null 2>&1 || true
systemctl try-restart rsyslog.service >/dev/null 2>&1 || true
systemctl enable urmaild-agent.service >/dev/null 2>&1 || true
systemctl enable urmaild.service >/dev/null 2>&1 || true
systemctl enable urmail-cert-renew.timer >/dev/null 2>&1 || true
systemctl enable urmail-backup-auto.timer >/dev/null 2>&1 || true

exit 0
EOF

cat >"$STAGE/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
systemctl disable --now urmail-backup-auto.timer >/dev/null 2>&1 || true
systemctl stop urmail-backup-auto.service >/dev/null 2>&1 || true
systemctl disable --now urmaild.service >/dev/null 2>&1 || true
systemctl disable --now urmaild-agent.service >/dev/null 2>&1 || true
systemctl disable --now urmail-cert-renew.timer >/dev/null 2>&1 || true
exit 0
EOF

chmod 0755 "$STAGE/DEBIAN/preinst" "$STAGE/DEBIAN/postinst" "$STAGE/DEBIAN/prerm"

OUTPUT="$ROOT/dist"
mkdir -p "$OUTPUT"
PACKAGE="$OUTPUT/urmail_${VERSION}_${ARCH}.deb"

if command -v dpkg-deb >/dev/null 2>&1; then
	dpkg-deb --root-owner-group --build "$STAGE" "$PACKAGE"
else
	echo "dpkg-deb is required." >&2
	echo "Build Debian packages in a Linux environment." >&2
	exit 1
fi

echo "built $PACKAGE"
