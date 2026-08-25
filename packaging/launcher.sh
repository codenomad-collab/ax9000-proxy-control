#!/bin/sh

APP=/extdisks/sda1/router-proxy-control/bin/ax9000-proxy-control
CONFIG=/data/router-proxy-web/config.json

while [ ! -x "$APP" ] || [ ! -r "$CONFIG" ]; do
    logger -t router-proxy-web "waiting for USB application or configuration"
    sleep 5
done

if command -v nice >/dev/null 2>&1; then
    exec nice -n 10 "$APP" -config "$CONFIG"
fi

exec "$APP" -config "$CONFIG"
