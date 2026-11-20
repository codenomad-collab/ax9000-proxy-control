#!/bin/sh

set -eu

INIT_FILE=/etc/init.d/router-proxy-web

if [ -x "$INIT_FILE" ]; then
    "$INIT_FILE" stop >/dev/null 2>&1 || true
    "$INIT_FILE" disable >/dev/null 2>&1 || true
fi

rm -f "$INIT_FILE"
rm -f /etc/rc.d/S96router-proxy-web /etc/rc.d/K14router-proxy-web
APP_PATH="$(cat /data/router-proxy-web/app-path 2>/dev/null || true)"
CRON_FILE=/data/etc/crontabs/root
if [ -r "$CRON_FILE" ]; then
    awk '!/Router-Proxy-Control-persist/' "$CRON_FILE" > "$CRON_FILE.new"
    chmod 600 "$CRON_FILE.new"
    mv "$CRON_FILE.new" "$CRON_FILE"
    /etc/init.d/cron restart >/dev/null 2>&1 || true
fi
rm -f /data/router-proxy-web/launcher.sh /data/router-proxy-web/config.json /data/router-proxy-web/app-path /data/router-proxy-web/persist.sh /data/router-proxy-web/router-proxy-web.init
rmdir /data/router-proxy-web 2>/dev/null || true

echo "The service and persistent configuration were removed."
case "$APP_PATH" in
    /*) echo "The external-storage application and backups remain under $(dirname "$(dirname "$APP_PATH")")." ;;
    *) echo "The external-storage application and backups were preserved." ;;
esac
