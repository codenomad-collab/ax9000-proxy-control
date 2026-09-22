#!/bin/sh

set -eu

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
PROJECT_DIR="$(dirname "$SCRIPT_DIR")"
BUILD_DIR="$PROJECT_DIR/build"
ROUTER_HOST="${ROUTER_HOST:-}"
ROUTER_LAN_IP="${ROUTER_LAN_IP:-}"
CONTROL_PORT="${CONTROL_PORT:-9098}"
LISTEN_ADDRESS="${LISTEN_ADDRESS:-$ROUTER_LAN_IP:$CONTROL_PORT}"
EXTERNAL_STORAGE="${EXTERNAL_STORAGE:-}"
ROUTER_KEYCHAIN_SERVICE="${ROUTER_KEYCHAIN_SERVICE:-router-proxy-control-ssh}"
CONTROL_KEYCHAIN_SERVICE="${CONTROL_KEYCHAIN_SERVICE:-router-proxy-control-admin}"

[ -n "$ROUTER_HOST" ] || { echo "ROUTER_HOST is required" >&2; exit 1; }
[ -n "$ROUTER_LAN_IP" ] || { echo "ROUTER_LAN_IP is required" >&2; exit 1; }

case "$LISTEN_ADDRESS" in
    ""|*[!0-9A-Za-z.:-]*) echo "LISTEN_ADDRESS contains unsupported characters" >&2; exit 1 ;;
esac
case "$EXTERNAL_STORAGE" in
    ""|/*) ;;
    *) echo "EXTERNAL_STORAGE must be empty or an absolute path" >&2; exit 1 ;;
esac

command -v go >/dev/null 2>&1 || { echo "Go is required" >&2; exit 1; }
command -v sshpass >/dev/null 2>&1 || { echo "sshpass is required" >&2; exit 1; }

router_password="$(security find-generic-password -w -a root -s "$ROUTER_KEYCHAIN_SERVICE" 2>/dev/null || true)"
[ -n "$router_password" ] || { echo "Router SSH credential is not available in macOS Keychain" >&2; exit 1; }

control_password="$(security find-generic-password -w -a admin -s "$CONTROL_KEYCHAIN_SERVICE" 2>/dev/null || true)"
if [ -z "$control_password" ]; then
    control_password="$(openssl rand -hex 16)"
    security add-generic-password -U -a admin -s "$CONTROL_KEYCHAIN_SERVICE" -l "路由器代理控制台" -w "$control_password" >/dev/null
fi

password_salt="$(openssl rand -hex 16)"
password_hash="$(printf '%s' "$password_salt$control_password" | shasum -a 256 | awk '{print $1}')"

mkdir -p "$BUILD_DIR"
(
    cd "$PROJECT_DIR"
    CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags='-s -w' -o "$BUILD_DIR/router-proxy-control-linux-arm64" .
)

staging_dir="$(mktemp -d -t router-proxy-control.XXXXXX)"
cleanup() {
    rm -R "$staging_dir"
}
trap cleanup EXIT INT TERM

cp "$BUILD_DIR/router-proxy-control-linux-arm64" "$staging_dir/router-proxy-control"
cp "$PROJECT_DIR/packaging/router-proxy-web.init" "$staging_dir/router-proxy-web.init"
cp "$PROJECT_DIR/packaging/launcher.sh" "$staging_dir/launcher.sh"
cp "$PROJECT_DIR/packaging/persist.sh" "$staging_dir/persist.sh"
cp "$PROJECT_DIR/scripts/install-router.sh" "$staging_dir/install-router.sh"

sed \
	-e "s|__LISTEN_ADDRESS__|$LISTEN_ADDRESS|" \
	-e "s|__EXTERNAL_STORAGE__|$EXTERNAL_STORAGE|" \
    -e "s/__PASSWORD_SALT__/$password_salt/" \
    -e "s/__PASSWORD_SHA256__/$password_hash/" \
    "$PROJECT_DIR/packaging/config.json.template" >"$staging_dir/config.json"
chmod 600 "$staging_dir/config.json"

export SSHPASS="$router_password"
sshpass -e ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no "$ROUTER_HOST" \
    'mkdir -p /tmp/router-proxy-control-install'
sshpass -e scp -O -o PreferredAuthentications=password -o PubkeyAuthentication=no \
    "$staging_dir/router-proxy-control" \
    "$staging_dir/router-proxy-web.init" \
    "$staging_dir/launcher.sh" \
    "$staging_dir/persist.sh" \
    "$staging_dir/install-router.sh" \
    "$staging_dir/config.json" \
    "$ROUTER_HOST:/tmp/router-proxy-control-install/"
sshpass -e ssh -o PreferredAuthentications=password -o PubkeyAuthentication=no "$ROUTER_HOST" \
    'chmod 755 /tmp/router-proxy-control-install/install-router.sh && /tmp/router-proxy-control-install/install-router.sh'

echo "Deployment completed. Open http://$LISTEN_ADDRESS/"
echo "The admin password is stored in macOS Keychain service: $CONTROL_KEYCHAIN_SERVICE"
