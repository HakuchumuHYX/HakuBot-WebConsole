#!/bin/sh
set -eu

NGINX=${NGINX_BIN:-nginx}
CONFIG=${NGINX_CONFIG:-/etc/nginx/nginx.conf}

if ! "$NGINX" -t -c "$CONFIG" >/dev/null 2>&1; then
    "$NGINX" -t -c "$CONFIG"
    exit 1
fi

"$NGINX" -s reload >/dev/null 2>&1
