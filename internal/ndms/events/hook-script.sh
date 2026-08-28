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
# tools (ip) and BusyBox-portable text extraction (sed/awk) — the
# /bin/grep on Keenetic is BusyBox grep and does NOT support -P/\K.
#
# Prefers curl and falls back to BusyBox wget. Порядок именно такой, потому
# что apple wget в BusyBox 1.37.0 на этой прошивке падает с SIGSEGV на ЛЮБОМ
# запросе, включая обычный GET (стенд 2026-08-28): каждое событие NDMS
# оставляло в kernel-логе «sending SIGSEGV to wget». Ошибка не наша, но звали
# его мы. wget остаётся запасным — curl ставится не везде.
#
# Form values are passed raw inside --data/--post-data because all hook
# parameters (interface names, layer strings, IPv4/IPv6 addresses) use
# characters safe for application/x-www-form-urlencoded without extra
# encoding.

HOOK_TYPE=$(basename "$(dirname "$0")" .d)

AWG_SETTINGS="/opt/etc/awg-manager/settings.json"
AWG_PORT=$(sed -n 's/.*"port"[[:space:]]*:[[:space:]]*\([0-9][0-9]*\).*/\1/p' "$AWG_SETTINGS" 2>/dev/null | head -1)
[ -z "$AWG_PORT" ] && AWG_PORT="2222"

AWG_HOST=$(/opt/sbin/ip -4 addr show br0 2>/dev/null | awk '/inet /{split($2,a,"/"); print a[1]; exit}')
[ -z "$AWG_HOST" ] && AWG_HOST="192.168.1.1"

# Forward all relevant env vars. Unspecified vars are empty strings — the
# server side ignores them per EventType discriminator. Max 3s timeout so
# the NDMS hook queue never stalls on our process being slow or down.

# Build the POST body. Values are safe URL characters (alphanumeric, dot,
# colon, underscore, hyphen) so no extra encoding is required.
BODY="type=${HOOK_TYPE}&id=${id}&system_name=${system_name}&layer=${layer}&level=${level}&address=${address}&up=${up}&connected=${connected}"

# -T is the portable BusyBox spelling of the timeout (--timeout requires
# FEATURE_WGET_LONG_OPTIONS, which not every firmware build enables).
# --post-data has no short form, so на прошивке без длинных опций попытка
# просто не проходит; второй адрес 127.0.0.1 покрывает демона, привязанного не
# к br0.
for AWG_URL in "http://${AWG_HOST}:${AWG_PORT}/api/hook/ndms" \
               "http://127.0.0.1:${AWG_PORT}/api/hook/ndms"; do
    [ -x /opt/bin/curl ] && \
        /opt/bin/curl -s -m 3 --data "$BODY" "$AWG_URL" >/dev/null 2>&1 && exit 0
    /bin/wget -q -O - -T 3 --post-data="$BODY" "$AWG_URL" >/dev/null 2>&1 && exit 0
    [ -x /opt/bin/wget ] && \
        /opt/bin/wget -q -O - -T 3 --post-data="$BODY" "$AWG_URL" >/dev/null 2>&1 && exit 0
done

exit 0
