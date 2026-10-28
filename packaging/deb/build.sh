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
install -d "$STAGE/etc/urmail/observability"
install -m 0644 "$ROOT/packaging/observability/"*.yml "$STAGE/etc/urmail/observability/"
install -m 0644 "$ROOT/packaging/systemd/opendkim.service.d/urmail.conf" "$STAGE/etc/systemd/system/opendkim.service.d/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail.conf" "$STAGE/etc/nginx/conf.d/urmail/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/99-urmail.conf" "$STAGE/etc/nginx/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail-ssl.conf" "$STAGE/etc/nginx/snippets/urmail-ssl.conf"
install -m 0644 "$ROOT/packaging/dovecot/99-urmail.conf" "$STAGE/etc/dovecot/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/internal/db/bootstrap.sql" "$STAGE/usr/share/urmail/bootstrap.sql"
install -m 0644 "$ROOT/installer/configure-env.py" "$STAGE/usr/share/urmail/configure-env.py"
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

install -d -o mailinabox -g mailinabox -m 0750 /var/lib/urmail
install -d -o mailinabox -g mailinabox -m 0750 /var/lib/urmail/backups
install -d -o mailinabox -g mailinabox -m 0700 /var/lib/urmail/archive
install -d -o root -g mailinabox -m 0750 /etc/urmail
install -d -m 0755 /etc/nginx/snippets /etc/dovecot/conf.d /etc/nsd/nsd.conf.d /etc/postfix/urmail
install -d -o root -g opendkim -m 0750 /etc/opendkim /etc/opendkim/keys
# Seed the canonical file before the service drop-in is loaded, so a failed
# first generation can restore a valid stock configuration atomically.
if [ ! -e /etc/opendkim/opendkim.conf ] && [ -f /etc/opendkim.conf ]; then
  install -o root -g opendkim -m 0644 /etc/opendkim.conf /etc/opendkim/opendkim.conf
fi
install -d -o root -g root -m 0755 /etc/nsd/zones /var/lib/urmail-acme /var/lib/nsd/urmail-secondary /etc/systemd/system/nsd.service.d
install -d -o root -g root -m 0700 /var/lib/urmail-agent /etc/letsencrypt /var/log/letsencrypt
chown root:mailinabox /etc/postfix/urmail
chmod 0755 /etc/postfix/urmail

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
