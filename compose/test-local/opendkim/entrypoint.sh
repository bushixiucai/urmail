#!/bin/sh
# LOCAL TEST OpenDKIM launcher: reuse the product first-boot DKIM key when it
# exists (bind-mounted data/config/dkim), otherwise generate one.  Test-only.
set -eu
domain="${URMAIL_PRIMARY_DOMAIN:-local.test}"
selector="${URMAIL_DKIM_SELECTOR:-mail}"
mkdir -p "/etc/dkimkeys/$domain" /etc/opendkim /run/opendkim
keyfile="/etc/dkimkeys/$domain/$selector.key"
if [ ! -s "$keyfile" ]; then
    keyfile="/etc/dkimkeys/$domain/$selector.private"
    if [ ! -s "$keyfile" ]; then
        opendkim-genkey -b 2048 -d "$domain" -s "$selector" -D "/etc/dkimkeys/$domain"
        keyfile="/etc/dkimkeys/$domain/$selector.private"
        echo "[opendkim] generated test key $selector._domainkey.$domain"
    fi
fi
chown -R opendkim:opendkim /etc/dkimkeys /run/opendkim /etc/opendkim
printf '%s._domainkey.%s %s:%s:%s\n' \
    "$selector" "$domain" "$domain" "$selector" "$keyfile" > /etc/opendkim/KeyTable
printf '*@%s %s._domainkey.%s\n' "$domain" "$selector" "$domain" > /etc/opendkim/SigningTable
printf '127.0.0.1\n::1\n%s\n' "$domain" > /etc/opendkim/TrustedHosts
exec opendkim -f -x /etc/opendkim/opendkim.conf
