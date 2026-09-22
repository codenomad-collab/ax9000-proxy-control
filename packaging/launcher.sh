#!/bin/sh

CONFIG=/data/router-proxy-web/config.json
APP_PATH_FILE=/data/router-proxy-web/app-path

while :; do
    APP="$(cat "$APP_PATH_FILE" 2>/dev/null || true)"
    case "$APP" in
        /*) ;;
        *) APP="" ;;
    esac
    [ -n "$APP" ] && [ -x "$APP" ] && [ -r "$CONFIG" ] && break
    logger -t router-proxy-web "waiting for USB application or configuration"
    sleep 5
done

if command -v nice >/dev/null 2>&1; then
    exec nice -n 10 "$APP" -config "$CONFIG"
fi

exec "$APP" -config "$CONFIG"
