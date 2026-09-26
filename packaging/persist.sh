#!/bin/sh

STATE_DIR=/data/router-proxy-web
PERSISTED_INIT=$STATE_DIR/router-proxy-web.init
RUNTIME_INIT=/etc/init.d/router-proxy-web
CONFIG=$STATE_DIR/config.json

[ -r "$PERSISTED_INIT" ] && [ -r "$CONFIG" ] || exit 0

if [ ! -x "$RUNTIME_INIT" ] || ! cmp -s "$PERSISTED_INIT" "$RUNTIME_INIT"; then
    cp "$PERSISTED_INIT" "$RUNTIME_INIT.new" || exit 1
    chmod 755 "$RUNTIME_INIT.new" || exit 1
    mv "$RUNTIME_INIT.new" "$RUNTIME_INIT" || exit 1
    "$RUNTIME_INIT" enable >/dev/null 2>&1 || true
    logger -t router-proxy-web "restored volatile procd entry"
fi

LISTEN_ADDRESS="$(sed -n 's/^[[:space:]]*"listen":[[:space:]]*"\([^"]*\)".*/\1/p' "$CONFIG" | head -n 1)"
[ -n "$LISTEN_ADDRESS" ] || exit 0
if ! netstat -lnt 2>/dev/null | awk -v target="$LISTEN_ADDRESS" '$4 == target { found = 1 } END { exit !found }'; then
    "$RUNTIME_INIT" start >/dev/null 2>&1 || exit 1
    logger -t router-proxy-web "started control service after persistence check"
fi
