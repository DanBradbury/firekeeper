#!/bin/bash
# One-time host setup for Firekeeper behind the existing nginx, in the style
# of the other subdomain setup scripts. Run on the host:
#
#   sudo bash setup-nginx.sh
#
# It creates /opt/firekeeper, installs the nginx vhost, and reloads nginx.
# It does not touch DNS, run certbot, or start the app.
set -euo pipefail

SUBDOMAIN="firekeeper.danbradbury.net"
DEPLOY_USER="deploy"
APP_DIR="/opt/firekeeper"
here=$(cd "$(dirname "$0")" && pwd)
conf="$here/nginx/$SUBDOMAIN.conf"
[ -f "$conf" ] || { echo "missing $conf" >&2; exit 1; }

echo "Creating $APP_DIR..."
mkdir -p "$APP_DIR"
chown "$DEPLOY_USER:$DEPLOY_USER" "$APP_DIR"

echo "Installing nginx config for $SUBDOMAIN..."
install -m 644 "$conf" "/etc/nginx/sites-available/$SUBDOMAIN"
ln -sf "/etc/nginx/sites-available/$SUBDOMAIN" /etc/nginx/sites-enabled/
nginx -t
systemctl reload nginx

cat <<MSG

Done. Next:
1. DNS: an A record for $SUBDOMAIN pointing at this server.
2. Create the first account (see docs/hosting.md), then run the deploy workflow.
3. After DNS resolves: sudo certbot --nginx -d $SUBDOMAIN
MSG
