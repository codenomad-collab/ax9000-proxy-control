#!/bin/sh

set -eu

STAGING_DIR=/tmp/router-proxy-control-install
STATE_DIR=/data/router-proxy-web
INIT_FILE=/etc/init.d/router-proxy-web
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"

[ -x "$STAGING_DIR/router-proxy-control" ] || {
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
[ -r "$STAGING_DIR/persist.sh" ] || {
    echo "Missing staged persistence guard" >&2
    exit 1
}
[ -r "$STAGING_DIR/config.json" ] || {
    echo "Missing staged configuration" >&2
    exit 1
}
configured_storage="$(sed -n 's/^[[:space:]]*"external_storage":[[:space:]]*"\([^"]*\)".*/\1/p' "$STAGING_DIR/config.json" | head -n 1)"

discover_external_storage() {
    best_path=""
    best_native=0
    best_available=0
    while read -r device mountpoint filesystem options rest; do
        case "$device" in /dev/*) ;; *) continue ;; esac
        case ",$options," in *,rw,*) ;; *) continue ;; esac
        case "$filesystem" in ext2|ext3|ext4|f2fs|btrfs|xfs|exfat|vfat|ntfs|ntfs3) ;; *) continue ;; esac
        case "$mountpoint" in /|/data|/data/*|/etc|/etc/*|/tmp|/tmp/*|/proc|/proc/*|/sys|/sys/*|/dev|/dev/*|/rom|/rom/*|/overlay|/overlay/*|/userdisk|/userdisk/*|*/mi_docker/lib/docker*) continue ;; esac
        [ -d "$mountpoint" ] && [ -w "$mountpoint" ] || continue
        available="$(df -Pk "$mountpoint" 2>/dev/null | awk 'NR==2 {print $4}')"
        case "$available" in ''|*[!0-9]*) continue ;; esac
        [ "$available" -ge 65536 ] || continue
        native=0
        case "$filesystem" in ext2|ext3|ext4|f2fs|btrfs|xfs) native=1 ;; esac
        if [ "$native" -gt "$best_native" ] || { [ "$native" -eq "$best_native" ] && [ "$available" -gt "$best_available" ]; }; then
            best_path="$mountpoint"
            best_native="$native"
            best_available="$available"
        fi
    done </proc/mounts
    printf '%s' "$best_path"
}

EXTERNAL_STORAGE="$configured_storage"
[ -n "$EXTERNAL_STORAGE" ] || EXTERNAL_STORAGE="$(discover_external_storage)"
case "$EXTERNAL_STORAGE" in
    /*) ;;
    *) echo "No suitable writable external storage was detected; set external_storage explicitly" >&2; exit 1 ;;
esac
[ -d "$EXTERNAL_STORAGE" ] && [ -w "$EXTERNAL_STORAGE" ] || {
    echo "External storage is not writable: $EXTERNAL_STORAGE" >&2
    exit 1
}

APP_DIR="$EXTERNAL_STORAGE/router-proxy-control"
APP="$APP_DIR/bin/router-proxy-control"
BACKUP_DIR="$APP_DIR/backups/$TIMESTAMP"

if [ -x "$INIT_FILE" ]; then
    "$INIT_FILE" stop >/dev/null 2>&1 || true
fi

mkdir -p "$APP_DIR/bin" "$APP_DIR/backups" "$STATE_DIR" "$BACKUP_DIR"

[ ! -f "$APP" ] || cp "$APP" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/config.json" ] || cp "$STATE_DIR/config.json" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/launcher.sh" ] || cp "$STATE_DIR/launcher.sh" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/persist.sh" ] || cp "$STATE_DIR/persist.sh" "$BACKUP_DIR/"
[ ! -f "$STATE_DIR/router-proxy-web.init" ] || cp "$STATE_DIR/router-proxy-web.init" "$BACKUP_DIR/"
[ ! -f "$INIT_FILE" ] || cp "$INIT_FILE" "$BACKUP_DIR/router-proxy-web.init"

cp "$STAGING_DIR/router-proxy-control" "$APP.new"
chmod 755 "$APP.new"
mv "$APP.new" "$APP"

if [ ! -f "$STATE_DIR/config.json" ]; then
    cp "$STAGING_DIR/config.json" "$STATE_DIR/config.json.new"
    chmod 600 "$STATE_DIR/config.json.new"
    mv "$STATE_DIR/config.json.new" "$STATE_DIR/config.json"
else
    chmod 600 "$STATE_DIR/config.json"
fi

printf '%s\n' "$APP" >"$STATE_DIR/app-path.new"
chmod 600 "$STATE_DIR/app-path.new"
mv "$STATE_DIR/app-path.new" "$STATE_DIR/app-path"

cp "$STAGING_DIR/launcher.sh" "$STATE_DIR/launcher.sh.new"
chmod 755 "$STATE_DIR/launcher.sh.new"
mv "$STATE_DIR/launcher.sh.new" "$STATE_DIR/launcher.sh"
cp "$STAGING_DIR/persist.sh" "$STATE_DIR/persist.sh.new"
chmod 755 "$STATE_DIR/persist.sh.new"
mv "$STATE_DIR/persist.sh.new" "$STATE_DIR/persist.sh"
cp "$STAGING_DIR/router-proxy-web.init" "$STATE_DIR/router-proxy-web.init.new"
chmod 755 "$STATE_DIR/router-proxy-web.init.new"
mv "$STATE_DIR/router-proxy-web.init.new" "$STATE_DIR/router-proxy-web.init"
cp "$STAGING_DIR/router-proxy-web.init" "$INIT_FILE.new"
chmod 755 "$INIT_FILE.new"
mv "$INIT_FILE.new" "$INIT_FILE"

CRON_FILE=/data/etc/crontabs/root
if [ -r "$CRON_FILE" ]; then
    awk '!/Router-Proxy-Control-persist/' "$CRON_FILE" > "$CRON_FILE.new"
    printf '%s\n' '* * * * * /bin/sh /data/router-proxy-web/persist.sh >/dev/null 2>&1 #Router-Proxy-Control-persist' >> "$CRON_FILE.new"
    chmod 600 "$CRON_FILE.new"
    mv "$CRON_FILE.new" "$CRON_FILE"
    /etc/init.d/cron restart >/dev/null 2>&1 || true
fi

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
    "$STAGING_DIR/router-proxy-control" \
    "$STAGING_DIR/router-proxy-web.init" \
    "$STAGING_DIR/launcher.sh" \
    "$STAGING_DIR/persist.sh" \
    "$STAGING_DIR/config.json" \
    "$STAGING_DIR/install-router.sh"; do
    [ ! -e "$staged_file" ] || /bin/rm -f "$staged_file"
done
rmdir "$STAGING_DIR" 2>/dev/null || true

echo "Router Proxy Control installed successfully"
echo "URL: http://$LISTEN_ADDRESS/"
echo "Backup: $BACKUP_DIR"
