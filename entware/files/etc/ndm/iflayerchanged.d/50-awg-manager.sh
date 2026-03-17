#!/bin/sh
# Keenetic NDM iflayerchanged hook for awg-manager
#
# Env vars from NDM: $id, $system_name, $layer, $level
#
# OpkgTun conf changes (user toggles tunnel in router UI):
#   layer=conf, level=running  -> tunnel enabled
#   layer=conf, level=disabled -> tunnel disabled
#
# WAN state changes:
#   layer=ipv4, level=running  -> WAN UP (IPv4 address assigned)
#   layer=ipv4, level=disabled -> WAN DOWN (IPv4 lost)

AWG_SETTINGS="/opt/etc/awg-manager/settings.json"

# Read awg-manager endpoint
AWG_PORT=$(grep '"port"' "$AWG_SETTINGS" 2>/dev/null | tr -cd '0-9')
[ -z "$AWG_PORT" ] && AWG_PORT="2222"
AWG_HOST=$(/opt/sbin/ip -4 addr show br0 2>/dev/null | grep -oP 'inet \K[\d.]+' | head -1)
[ -z "$AWG_HOST" ] && AWG_HOST="192.168.1.1"

BASE_URL="http://${AWG_HOST}:${AWG_PORT}"

# === OpkgTun interface changes (tunnel toggled in router UI) ===
case "$id" in
    OpkgTun*)
        [ "$layer" = "conf" ] || exit 0
        RESULT=$(/opt/bin/curl -s -o /dev/null -w '%{http_code}' \
            --max-time 5 -X POST \
            "${BASE_URL}/api/hook/iface-changed?id=${id}&layer=${layer}&level=${level}" 2>&1)
        exit 0
        ;;
esac

# === WAN interface changes ===
# No client-side filtering — the server decides what's a WAN interface
# (security-level: "public" + isNonISPInterface exclusion).

# WAN UP: IPv4 address assigned (actual connectivity ready)
if [ "$layer" = "ipv4" ] && [ "$level" = "running" ]; then
    RESULT=$(/opt/bin/curl -s -o /dev/null -w '%{http_code}' \
        --max-time 5 -X POST "${BASE_URL}/api/wan/event?action=up&interface=${system_name}" 2>&1)
    exit 0
fi

# WAN DOWN: IPv4 disabled (admin toggled off or PPPoE session lost)
if [ "$layer" = "ipv4" ] && [ "$level" = "disabled" ]; then
    RESULT=$(/opt/bin/curl -s -o /dev/null -w '%{http_code}' \
        --max-time 3 -X POST "${BASE_URL}/api/wan/event?action=down&interface=${system_name}" 2>&1)
    exit 0
fi
