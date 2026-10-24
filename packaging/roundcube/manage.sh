#!/usr/bin/env bash
# Manage the optional distro-supplied Roundcube runtime, never mailbox storage.
set -euo pipefail
[ "$(id -u)" -eq 0 ] || { echo 'Run as root.' >&2; exit 1; }
asset_root=/usr/share/urmail/roundcube
[ "$#" -eq 1 ] || { echo 'Exactly one fixed mode is required.' >&2; exit 2; }
case "${1:-}" in
 preflight-v80|configure-v80)
  exec python3 "$asset_root/configure.py" "$1"
  ;;
 install-v80|upgrade-v80)
  # Preflight is read-only. Activation needs an authenticated import manifest
  # and reviewed password/Z-Push adapters, never table-presence heuristics.
  python3 "$asset_root/configure.py" preflight-v80
  echo 'v80 activation refused: verified metadata import and credential/identity activation proof are unavailable. No runtime, package, database or source files changed.' >&2
  exit 1
  ;;
 configure)
  [ "$#" -eq 1 ] || { echo 'configure accepts no arguments.' >&2; exit 2; }
  exec python3 "$asset_root/configure.py"
  ;;
 install|upgrade)
  [ ! -e /usr/local/lib/roundcubemail ] && [ ! -L /usr/local/lib/roundcubemail ] || {
   echo 'Retained source Roundcube detected. Distro installation is forbidden; use the explicit v80 preflight mode.' >&2; exit 1;
  }
  [ -r /etc/urmail-roundcube/secrets.php ] && [ -r /etc/urmail-roundcube/pg-service.conf ] || {
   echo 'Provision dedicated same-database Roundcube credentials before enabling webmail.' >&2; exit 1;
  }
  # The core package depends on dbconfig-common. Disable its database creation
  # explicitly; the configured role owns only our same-database schema.
  printf '%s\n' 'roundcube-core roundcube/dbconfig-install boolean false' 'roundcube-core roundcube/dbconfig-upgrade boolean false' | debconf-set-selections
  export DEBIAN_FRONTEND=noninteractive
  apt-get update
  apt-get install -y --no-install-recommends roundcube-core roundcube-plugins php-fpm php-pgsql php-curl php-mbstring php-xml php-intl php-zip
  vendor_root=/usr/share/roundcube
  runtime_root=/var/lib/roundcube
  [ -f "$vendor_root/index.php" ] && [ -f "$runtime_root/index.php" ] && [ -d /etc/roundcube ] || { echo 'Roundcube package layout unsupported.' >&2; exit 1; }
  php_version=$(php -r 'echo PHP_MAJOR_VERSION,".",PHP_MINOR_VERSION;')
  [ -d "/etc/php/$php_version/fpm/pool.d" ] || { echo 'PHP FPM version mismatch.' >&2; exit 1; }
  install -d -o root -g www-data -m 0750 /etc/urmail-roundcube
  install -o root -g www-data -m 0640 "$asset_root/config.inc.php" /etc/urmail-roundcube/config.inc.php
  install -d "$vendor_root/plugins/urmail_bridge"
  install -m 0644 "$asset_root/plugins/urmail_bridge/urmail_bridge.php" "$asset_root/plugins/urmail_bridge/bridge.js" "$vendor_root/plugins/urmail_bridge/"
  [ -f "$vendor_root/plugins/password/password.php" ] || { echo 'Roundcube password plugin package is missing.' >&2; exit 1; }
  install -d "$vendor_root/plugins/password/drivers"
  install -m 0644 "$asset_root/plugins/urmail_bridge/password_driver.php" "$vendor_root/plugins/password/drivers/urmail.php"
  # Override the plugin-level configuration too: its load_config runs later
  # than the main wrapper and must never re-enable a SQLite SQL driver.
  install -o root -g www-data -m 0640 "$asset_root/password-config.inc.php" /etc/urmail-roundcube/password-config.inc.php
  ln -sfn /etc/urmail-roundcube/password-config.inc.php "$vendor_root/plugins/password/config.inc.php"
  if [ -d "$runtime_root/plugins/password" ] && [ ! -L "$runtime_root/plugins/password" ]; then
   echo 'Unexpected writable runtime password plugin directory; refusing to hide existing files.' >&2; exit 1;
  fi
  ln -sfn "$vendor_root/plugins/password" "$runtime_root/plugins/password"
  # Debian's config symlink is replaced with the managed, non-secret wrapper.
  ln -sfn /etc/urmail-roundcube/config.inc.php /etc/roundcube/config.inc.php
  ln -sfn "$vendor_root/plugins/urmail_bridge" "$runtime_root/plugins/urmail_bridge"
  install -m 0644 "$asset_root/pool.conf" "/etc/php/$php_version/fpm/pool.d/urmail-roundcube.conf"
  install -m 0644 "$asset_root/nginx.conf" /etc/nginx/conf.d/urmail-roundcube.conf
  if [ ! -f "$runtime_root/public_html/index.php" ]; then
   # Older supported distro packages keep the entry point at the runtime root.
   sed -i 's|/var/lib/roundcube/public_html|/var/lib/roundcube|g' /etc/nginx/conf.d/urmail-roundcube.conf
  fi
  export PGSERVICEFILE=/etc/urmail-roundcube/pg-service.conf
  pg_options='-c search_path=roundcube'
  initialized=$(runuser -u www-data -- env PGSERVICEFILE="$PGSERVICEFILE" PGOPTIONS="$pg_options" psql service=urmail-roundcube -Atqc "SELECT to_regclass('roundcube.users') IS NOT NULL")
  if [ "$initialized" = f ]; then
   sql_file="$vendor_root/SQL/postgres.initial.sql"
   [ -r "$sql_file" ] || { echo 'Roundcube vendor SQL missing.' >&2; exit 1; }
   runuser -u www-data -- env PGSERVICEFILE="$PGSERVICEFILE" PGOPTIONS="$pg_options" psql service=urmail-roundcube --set=ON_ERROR_STOP=1 --single-transaction -f "$sql_file"
  else
   runuser -u www-data -- php "$vendor_root/bin/updatedb.sh" --dir="$vendor_root/SQL" --package=roundcube
  fi
  install -d -m 0755 /var/lib/urmail/webmail
  dpkg-query -W -f='${Package}\t${Version}\n' roundcube-core php-fpm php-pgsql > /var/lib/urmail/webmail/runtime-manifest.txt
  php -l /etc/urmail-roundcube/config.inc.php
  nginx -t
  systemctl enable --now "php$php_version-fpm"
  systemctl reload "php$php_version-fpm"
  systemctl reload nginx
  # Both processes read their own root-controlled enable flag. The agent
  # renders the external /mail proxy; restarting only the panel leaves it off.
  systemctl restart urmaild-agent
  systemctl restart urmaild
  ;;
 disable)
  [ "$#" -eq 1 ] || { echo 'disable accepts no arguments.' >&2; exit 2; }
  python3 "$asset_root/configure.py" disable
  # Retain metadata, sessions, attachments and all mailbox originals.
  rm -f /etc/nginx/conf.d/urmail-roundcube.conf
  nginx -t
  systemctl reload nginx
  systemctl restart urmaild-agent
  systemctl restart urmaild
  ;;
 *) echo 'Usage: manage.sh configure|install|upgrade|disable|preflight-v80|configure-v80|install-v80|upgrade-v80' >&2; exit 2 ;;
esac
