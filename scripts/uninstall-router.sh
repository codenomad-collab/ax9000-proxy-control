#!/bin/sh

set -eu

INIT_FILE=/etc/init.d/router-proxy-web

if [ -x "$INIT_FILE" ]; then
    "$INIT_FILE" stop >/dev/null 2>&1 || true
    "$INIT_FILE" disable >/dev/null 2>&1 || true
fi

rm -f "$INIT_FILE"
rm -f /etc/rc.d/S96router-proxy-web /etc/rc.d/K14router-proxy-web
rm -f /data/router-proxy-web/launcher.sh /data/router-proxy-web/config.json
rmdir /data/router-proxy-web 2>/dev/null || true

echo "The service and persistent configuration were removed."
echo "The USB application and backups remain under /extdisks/sda1/router-proxy-control."
