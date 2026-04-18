#!/bin/sh
# 50-awg-manager.sh — NDMS hook forwarder for awg-manager.
#
# NDMS copies this script into 4 hook directories:
#   /opt/etc/ndm/iflayerchanged.d/
#   /opt/etc/ndm/ifcreated.d/
#   /opt/etc/ndm/ifdestroyed.d/
#   /opt/etc/ndm/ifipchanged.d/
# The HOOK_TYPE is derived from the directory name at invocation time.
#
# Runs under NDMS with BusyBox /bin/sh. Uses absolute paths for Entware
# tools (ip, curl) and BusyBox-portable text extraction (sed/awk) — the
# /bin/grep on Keenetic is BusyBox grep and does NOT support -P/\K.

HOOK_TYPE=$(basename "$(dirname "$0")" .d)

AWG_SETTINGS="/opt/etc/awg-manager/settings.json"
AWG_PORT=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$AWG_SETTINGS" 2>/dev/null | head -1)
[ -z "$AWG_PORT" ] && AWG_PORT="2222"

AWG_HOST=$(/opt/sbin/ip -4 addr show br0 2>/dev/null | awk '/inet /{split($2,a,"/"); print a[1]; exit}')
[ -z "$AWG_HOST" ] && AWG_HOST="192.168.1.1"

# Forward all relevant env vars. Unspecified vars are empty strings — the
# server side ignores them per EventType discriminator. Max 3s timeout so
# the NDMS hook queue never stalls on our process being slow or down.
/opt/bin/curl -s -o /dev/null --max-time 3 -X POST \
    "http://${AWG_HOST}:${AWG_PORT}/api/hook/ndms" \
    --data-urlencode "type=${HOOK_TYPE}" \
    --data-urlencode "id=${id}" \
    --data-urlencode "system_name=${system_name}" \
    --data-urlencode "layer=${layer}" \
    --data-urlencode "level=${level}" \
    --data-urlencode "address=${address}" \
    --data-urlencode "up=${up}" \
    --data-urlencode "connected=${connected}" \
    2>/dev/null
exit 0
