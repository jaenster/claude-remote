#!/bin/sh
set -e

if [ -n "$TS_AUTHKEY" ]; then
  tailscaled --tun=userspace-networking --state=/workspace/.tailscale &
  sleep 2
  tailscale up --authkey="$TS_AUTHKEY" --accept-routes --hostname=claude-remote
fi

exec claude-remote "$@"
