#!/bin/sh

set -eu

STAGING_DIR=/tmp/ax9000-proxy-control-install
APP_DIR=/extdisks/sda1/router-proxy-control
STATE_DIR=/data/router-proxy-web
INIT_FILE=/etc/init.d/router-proxy-web
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
BACKUP_DIR="$APP_DIR/backups/$TIMESTAMP"

[ -x "$STAGING_DIR/ax9000-proxy-control" ] || {
    echo "Missing staged ARM64 binary" >&2
    exit 1
}
[ -r "$STAGING_DIR/router-proxy-web.init" ] || {
    echo "Missing staged init script" >&2
    exit 1
}
[ -r "$STAGING_DIR/launcher.sh" ] || {
    echo "Missing staged launcher" >&2
    exit 1
}
[ -r "$STAGING_DIR/config.json" ] || {
    echo "Missing staged configuration" >&2
    exit 1
}
[ -d /extdisks/sda1 ] || {
    echo "ShellCrash USB volume is not mounted" >&2
    exit 1
}

if [ -x "$INIT_FILE" ]; then
    "$INIT_FILE" stop >/dev/null 2>&1 || true
fi

mkdir -p "$APP_DIR/bin" "$APP_DIR/backups" "$STATE_DIR" "$BACKUP_DIR"

[ ! -f "$APP_DIR/bin/ax9000-proxy-control" ] || cp "$APP_DIR/bin/ax9000-proxy-control" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/config.json" ] || cp "$STATE_DIR/config.json" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/launcher.sh" ] || cp "$STATE_DIR/launcher.sh" "$BACKUP_DIR/"
[ ! -f "$INIT_FILE" ] || cp "$INIT_FILE" "$BACKUP_DIR/router-proxy-web.init"

cp "$STAGING_DIR/ax9000-proxy-control" "$APP_DIR/bin/ax9000-proxy-control.new"
chmod 755 "$APP_DIR/bin/ax9000-proxy-control.new"
mv "$APP_DIR/bin/ax9000-proxy-control.new" "$APP_DIR/bin/ax9000-proxy-control"

if [ ! -f "$STATE_DIR/config.json" ]; then
    cp "$STAGING_DIR/config.json" "$STATE_DIR/config.json.new"
    chmod 600 "$STATE_DIR/config.json.new"
    mv "$STATE_DIR/config.json.new" "$STATE_DIR/config.json"
else
    chmod 600 "$STATE_DIR/config.json"
fi

cp "$STAGING_DIR/launcher.sh" "$STATE_DIR/launcher.sh"
chmod 755 "$STATE_DIR/launcher.sh"
cp "$STAGING_DIR/router-proxy-web.init" "$INIT_FILE"
chmod 755 "$INIT_FILE"

"$INIT_FILE" enable
"$INIT_FILE" start
sleep 2

LISTEN_ADDRESS="$(sed -n 's/^[[:space:]]*"listen":[[:space:]]*"\([^"]*\)".*/\1/p' "$STATE_DIR/config.json" | head -n 1)"
[ -n "$LISTEN_ADDRESS" ] || {
    echo "Unable to read listen address from configuration" >&2
    exit 1
}

if ! netstat -lnt 2>/dev/null | awk -v target="$LISTEN_ADDRESS" '$4 == target { found = 1 } END { exit !found }'; then
    echo "Service did not open $LISTEN_ADDRESS" >&2
    logread -l 80 -e router-proxy-web 2>/dev/null || true
    exit 1
fi

for staged_file in \
    "$STAGING_DIR/ax9000-proxy-control" \
    "$STAGING_DIR/router-proxy-web.init" \
    "$STAGING_DIR/launcher.sh" \
    "$STAGING_DIR/config.json" \
    "$STAGING_DIR/install-router.sh"; do
    [ ! -e "$staged_file" ] || /bin/rm -f "$staged_file"
done
rmdir "$STAGING_DIR" 2>/dev/null || true

echo "AX9000 Proxy Control installed successfully"
echo "URL: http://$LISTEN_ADDRESS/"
echo "Backup: $BACKUP_DIR"
