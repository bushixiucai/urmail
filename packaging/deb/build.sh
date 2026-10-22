#!/usr/bin/env bash
# Build the urMail.cn Debian package.
#
# The package contains only urMail.cn's own artefacts. Third-party components
# (Nextcloud, Roundcube, Z-Push) are installed from their upstream sources at
# install time, so they are deliberately not bundled: bundling them would
# pull their AGPL obligations into the distribution.
set -euo pipefail

VERSION="${1:-0.1.0}"
ARCH="${ARCH:-amd64}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

if [ ! -x "$ROOT/bin/urmaild" ]; then
	echo "bin/urmaild is missing; run 'make build' first." >&2
	exit 1
fi

install -d "$STAGE/DEBIAN"
install -d "$STAGE/usr/bin"
install -d "$STAGE/lib/systemd/system"
install -d "$STAGE/etc/systemd/system/opendkim.service.d"
install -d "$STAGE/etc/urmail"
install -d "$STAGE/etc/nginx/conf.d/urmail"
install -d "$STAGE/etc/nginx/snippets"
install -d "$STAGE/etc/dovecot/conf.d"
install -d "$STAGE/etc/nsd/nsd.conf.d"
install -d "$STAGE/etc/postfix/urmail"
install -d "$STAGE/etc/opendkim/keys"
install -d "$STAGE/usr/share/urmail"

install -m 0755 "$ROOT/bin/urmaild" "$STAGE/usr/bin/urmaild"
install -m 0755 "$ROOT/bin/urmaild-agent" "$STAGE/usr/bin/urmaild-agent"
install -m 0755 "$ROOT/bin/urmailctl" "$STAGE/usr/bin/urmailctl"

install -m 0644 "$ROOT/packaging/systemd/urmaild.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/urmaild-agent.service" "$STAGE/lib/systemd/system/"
install -m 0644 "$ROOT/packaging/systemd/opendkim.service.d/urmail.conf" "$STAGE/etc/systemd/system/opendkim.service.d/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail.conf" "$STAGE/etc/nginx/conf.d/urmail/urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/99-urmail.conf" "$STAGE/etc/nginx/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/packaging/nginx/urmail-ssl.conf" "$STAGE/etc/nginx/snippets/urmail-ssl.conf"
install -m 0644 "$ROOT/packaging/dovecot/99-urmail.conf" "$STAGE/etc/dovecot/conf.d/99-urmail.conf"
install -m 0644 "$ROOT/internal/db/bootstrap.sql" "$STAGE/usr/share/urmail/bootstrap.sql"

cat >"$STAGE/DEBIAN/control" <<EOF
Package: urmail
Version: $VERSION
Section: mail
Priority: optional
Architecture: $ARCH
Maintainer: urMail.cn <support@urmail.cn>
Depends: nginx, postfix, dovecot-core, dovecot-imapd, dovecot-pop3d, dovecot-lmtpd, dovecot-sieve, dovecot-managesieved, dovecot-pgsql, nsd, opendkim, postgresql, ca-certificates, certbot
Description: urMail.cn multi-tenant email SaaS platform
 A self-hosted platform for selling domain mailboxes: multi-tenancy, quotas,
 billing, payment, DNS onboarding, certificates, observability and compliance.
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
install -d -o mailinabox -g mailinabox -m 0750 /var/lib/urmail/acme
install -d -o mailinabox -g mailinabox -m 0750 /var/lib/urmail/certs
install -d -o mailinabox -g mailinabox -m 0750 /var/lib/urmail/backups
install -d -o root -g mailinabox -m 0750 /etc/urmail
install -d -m 0755 /etc/nginx/snippets /etc/dovecot/conf.d /etc/nsd/nsd.conf.d /etc/postfix/urmail
install -d -o root -g opendkim -m 0750 /etc/opendkim /etc/opendkim/keys

# The Debian nsd package does not always include nsd.conf.d; add the drop-in
# once so generated zones are loaded.
if [ -f /etc/nsd/nsd.conf ] && ! grep -q 'nsd.conf.d' /etc/nsd/nsd.conf; then
	printf '\ninclude: "/etc/nsd/nsd.conf.d/*.conf"\n' >>/etc/nsd/nsd.conf
fi

systemctl daemon-reload
systemctl enable urmaild-agent.service >/dev/null 2>&1 || true
systemctl enable urmaild.service >/dev/null 2>&1 || true

exit 0
EOF

cat >"$STAGE/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
systemctl disable --now urmaild.service >/dev/null 2>&1 || true
systemctl disable --now urmaild-agent.service >/dev/null 2>&1 || true
exit 0
EOF

chmod 0755 "$STAGE/DEBIAN/postinst" "$STAGE/DEBIAN/prerm"

OUTPUT="$ROOT/dist"
mkdir -p "$OUTPUT"
PACKAGE="$OUTPUT/urmail_${VERSION}_${ARCH}.deb"

if command -v nfpm >/dev/null 2>&1; then
	cd "$STAGE/DEBIAN"
	nfpm pack --packager deb --target "$PACKAGE" --name urmail --version "$VERSION" --arch "$ARCH" --config nfpm.yaml
elif command -v dpkg-deb >/dev/null 2>&1; then
	dpkg-deb --build "$STAGE" "$PACKAGE"
else
	echo "Neither nfpm nor dpkg-deb is available." >&2
	echo "Debian packages can only be built on Linux, or with nfpm installed." >&2
	exit 1
fi

echo "built $PACKAGE"
