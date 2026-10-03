#!/bin/sh
# susanin.sh — Susanin.Keenetic daemon control.
#
# Usage:
#   sh susanin.sh start          # start daemon in background (writes log)
#   sh susanin.sh stop           # graceful stop (SIGTERM, then KILL if needed)
#   sh susanin.sh restart        # restart
#   sh susanin.sh status         # daemon + data plane state
#   sh susanin.sh log [N]        # last N log lines (default 30)
#   sh susanin.sh install        # data plane setup (datapath up + setup)
#   sh susanin.sh update [ver]   # update binary/scripts (keeps config and state)
#   sh susanin.sh uninstall [--purge]
#   sh susanin.sh reload         # перечитать susanin.conf на лету (SIGHUP)
#   sh susanin.sh rescan         # заново найти LAN/VPN, обновить конфиг и перечитать
#   sh susanin.sh down           # remove data plane rules (daemon keeps running)
#   sh susanin.sh forget <ip>    # drop an address from cache/ipsets (allow direct)
#   sh susanin.sh add <ip> tcp|udp test|ok
#   sh susanin.sh del <ip> tcp|udp

set -eu

BIN=/opt/susanin/bin/susanin-agent
CONF=/opt/susanin/etc/susanin.conf
LOG=/opt/susanin/var/susanin.log
TOOLS=/opt/susanin/tools

is_running() {
    ps | grep susanin-agent | grep -v grep >/dev/null 2>&1
}

# Rotate log above 5 MiB; keep compressed archives susanin.log.1.gz .. .3.gz
rotate_log() {
    [ -f "$LOG" ] || return 0
    _sz=$(wc -c < "$LOG" 2>/dev/null || echo 0)
    [ "${_sz:-0}" -lt 5242880 ] && return 0
    rm -f "$LOG.3.gz"
    [ -f "$LOG.2.gz" ] && mv -f "$LOG.2.gz" "$LOG.3.gz"
    [ -f "$LOG.1.gz" ] && mv -f "$LOG.1.gz" "$LOG.2.gz"
    mv -f "$LOG" "$LOG.1"
    if command -v gzip >/dev/null 2>&1; then
        gzip -f "$LOG.1" 2>/dev/null || true
    else
        rm -f "$LOG.3.gz"
    fi
    echo "[susanin] log rotated (archives: $LOG.N.gz, keep 3)"
}

cmd_start() {
    if is_running; then
        echo "[susanin] already running"
        return 0
    fi
    mkdir -p /opt/susanin/var
    rotate_log
    (
        trap '' HUP
        SUSANIN_CONF="$CONF" SUSANIN_LOG="$LOG" exec "$BIN" run
    ) >>"$LOG" 2>&1 </dev/null &
    echo $! > /opt/susanin/var/susanin-agent.pid
    sleep 2
    if is_running; then
        echo "[susanin] started, log: $LOG"
    else
        echo "[susanin] failed to start, see log: $LOG" >&2
        tail -n 10 "$LOG" 2>/dev/null || true
        return 1
    fi
}

cmd_stop() {
    if ! is_running; then
        echo "[susanin] not running"
        return 0
    fi
    for p in $(ps | grep susanin-agent | grep -v grep | awk '{print $1}'); do
        kill -TERM "$p" 2>/dev/null || true
    done
    _i=0
    while [ "$_i" -lt 10 ] && is_running; do
        sleep 1
        _i=$((_i + 1))
    done
    if is_running; then
        for p in $(ps | grep susanin-agent | grep -v grep | awk '{print $1}'); do
            kill -9 "$p" 2>/dev/null || true
        done
    fi
    sleep 1
    echo "[susanin] stopped"
}

cmd_status() {
    if is_running; then
        echo "daemon: RUNNING"
    else
        echo "daemon: stopped"
    fi
    "$BIN" status
}

cmd_log() {
    n="${1:-30}"
    tail -n "$n" "$LOG" 2>/dev/null || echo "log is empty: $LOG"
}

cmd_reload() {
    pids=$(ps | grep susanin-agent | grep -v grep | awk '{print $1}')
    if [ -z "${pids:-}" ]; then
        echo "[susanin] not running" >&2
        return 1
    fi
    for p in $pids; do
        kill -HUP "$p" 2>/dev/null && echo "[susanin] reload signal sent (pid $p)"
    done
}

# rescan: заново определить текущие LAN/VPN из системы, обновить конфиг
# (egress_interface/egress_address/lan_interfaces/lan_subnets) и перечитать его.
cmd_rescan() {
    echo "[susanin] re-scan network/VPN -> $CONF"
    d=$("$BIN" discover 2>/dev/null || true)
    [ -n "$d" ] || { echo "[susanin] discover failed" >&2; return 1; }
    printf '%s\n' "$d"
    printf '%s\n' "$d" | while IFS='=' read -r k v; do
        case "$k" in
            egress_interface|egress_address|lan_interfaces|lan_subnets)
                if grep -q "^$k=" "$CONF" 2>/dev/null; then
                    sed -i "s|^$k=.*|$k=$v|" "$CONF"
                else
                    echo "$k=$v" >> "$CONF"
                fi
                ;;
        esac
    done
    echo "[susanin] config updated"
    cmd_reload || true
}

case "${1:-}" in
    start) cmd_start ;;
    stop) cmd_stop ;;
    restart) cmd_stop; sleep 1; cmd_start ;;
    status) cmd_status ;;
    reload) cmd_reload ;;
    rescan) cmd_rescan ;;
    log) cmd_log "${2:-30}" ;;
    install) sh "$TOOLS/datapath.sh" up; "$BIN" setup ;;
    update) shift || true; sh "$TOOLS/update.sh" "$@" ;;
    uninstall) shift || true; sh "$TOOLS/uninstall.sh" "$@" ;;
    down) sh "$TOOLS/datapath.sh" down ;;
    forget) "$BIN" forget "$2" ;;
    add) sh "$TOOLS/datapath.sh" add "$2" "$3" "$4" ;;
    del) sh "$TOOLS/datapath.sh" del "$2" "$3" ;;
    *)
        echo "usage: $0 {start|stop|restart|status|reload|rescan|log [N]|install|update [ver]|uninstall [--purge]|down|forget <ip>|add <ip> <tcp|udp> <test|ok>|del <ip> <tcp|udp>}" >&2
        exit 2 ;;
esac
