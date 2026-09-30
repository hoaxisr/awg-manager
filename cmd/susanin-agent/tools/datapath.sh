#!/bin/sh
# datapath.sh - Susanin.Keenetic data plane (iptables + ipset) on/off + manual words.
# Enhanced for AWG Manager & Keenetic Access Policy coexistence.
set -eu

PREFIX=/opt
find_bin() { for b in /opt/sbin /opt/bin /usr/sbin /usr/bin; do [ -x "$b/$1" ] && { echo "$b/$1"; return; }; done; command -v "$1" 2>/dev/null || true; }
IPT=$(find_bin iptables); IPSET=$(find_bin ipset); IPCMD=$(find_bin ip)
[ -n "$IPT" ] || { echo "iptables not found" >&2; exit 2; }
[ -n "$IPSET" ] || { echo "ipset not found" >&2; exit 2; }
[ -n "$IPCMD" ] || { echo "ip not found" >&2; exit 2; }

CONF="${SUSANIN_CONF:-/opt/susanin/etc/susanin.conf}"
[ -f "$CONF" ] || CONF="/opt/etc/awg-manager/susanin/susanin.conf"
if [ -f "$CONF" ]; then
    while IFS='=' read -r key val || [ -n "$key" ]; do
        case "$key" in
            \#*|"") continue ;;
            egress_interface) [ -z "${SUSANIN_EGRESS:-}" ] && EGRESS="$val" ;;
            routing_table) [ -z "${SUSANIN_TABLE:-}" ] && TABLE="$val" ;;
            mark_ok) [ -z "${SUSANIN_MARK_OK:-}" ] && MARK_OK="$val" ;;
            mark_test) [ -z "${SUSANIN_MARK_TEST:-}" ] && MARK_TEST="$val" ;;
            mark_mask) [ -z "${SUSANIN_MARK_MASK:-}" ] && MASK="$val" ;;
            ip_rule_priority_start) [ -z "${SUSANIN_PRI_OK:-}" ] && PRI_OK="$val" && PRI_TEST=$((val + 1)) ;;
            lan_interfaces) [ -z "${SUSANIN_LAN:-}" ] && LAN="$val" ;;
            disk_mode) [ -z "${SUSANIN_DISK_MODE:-}" ] && DISK_MODE="$val" ;;
            policy_mark) [ -z "${SUSANIN_POLICY_MARK:-}" ] && POLICY_MARK="$val" ;;
            vpn_always_file) ALWAYS_FILE="$val" ;;
            vpn_never_file) NEVER_FILE="$val" ;;
        esac
    done < "$CONF"
fi

EGRESS=${EGRESS:-${SUSANIN_EGRESS:-awgsus0}}
case "$EGRESS" in
    Wireguard*)
        idx="${EGRESS#Wireguard}"
        if [ -e "/sys/class/net/nwg$idx" ] || ip link show "nwg$idx" >/dev/null 2>&1; then
            EGRESS="nwg$idx"
        fi
        ;;
    OpkgTun*)
        lower=$(printf '%s' "$EGRESS" | tr '[:upper:]' '[:lower:]')
        if [ -e "/sys/class/net/$lower" ] || ip link show "$lower" >/dev/null 2>&1; then
            EGRESS="$lower"
        fi
        ;;
esac
TABLE=${TABLE:-${SUSANIN_TABLE:-105}}
MARK_OK=${MARK_OK:-${SUSANIN_MARK_OK:-0x20000000}}
MARK_TEST=${MARK_TEST:-${SUSANIN_MARK_TEST:-0x10000000}}
MASK=${MASK:-${SUSANIN_MARK_MASK:-0x30000000}}
PRI_OK=${PRI_OK:-${SUSANIN_PRI_OK:-95}}
PRI_TEST=${PRI_TEST:-${SUSANIN_PRI_TEST:-96}}
LAN=${LAN:-${SUSANIN_LAN:-"br0"}}
LAN=$(printf '%s' "$LAN" | tr ',' ' ')
TTL_TEST=${SUSANIN_TTL_TEST:-60}
TTL_OK=${SUSANIN_TTL_OK:-21600}
DISK_MODE=${DISK_MODE:-${SUSANIN_DISK_MODE:-soft}}
POLICY_MARK=${POLICY_MARK:-${SUSANIN_POLICY_MARK:-}}
ALWAYS_FILE=${ALWAYS_FILE:-/opt/susanin/etc/vpn_always.txt}
NEVER_FILE=${NEVER_FILE:-/opt/susanin/etc/vpn_never.txt}

CHAIN=SUSANIN
SETS="susanin_ok_tcp susanin_ok_udp susanin_test_tcp susanin_test_udp"
NETSET=susanin_ok_net
NEVERSET=susanin_never

say() { echo "[susanin] $*"; }

mangle() { "$IPT" -w -t mangle -D "$@" >/dev/null 2>&1 || true; "$IPT" -w -t mangle -A "$@"; }
iprule() { "$IPCMD" rule del "$@" >/dev/null 2>&1 || true; "$IPCMD" rule add "$@"; }

set_exists() { "$IPSET" list "$1" >/dev/null 2>&1; }

backup() {
    if [ "$DISK_MODE" = "soft" ]; then
        say "disk_mode=soft: backup/archiving skipped"
        return 0
    fi
    mkdir -p "$PREFIX/susanin/var"
    bk="$PREFIX/susanin/var/datapath-$(date +%Y%m%d-%H%M%S)"
    mkdir -p "$bk"
    "$IPT" -w -t mangle -S > "$bk/mangle.txt" 2>/dev/null || true
    "$IPT" -w -t nat -S > "$bk/nat.txt" 2>/dev/null || true
    "$IPCMD" rule show > "$bk/ip-rule.txt" 2>/dev/null || true
    "$IPCMD" route show table all > "$bk/ip-route.txt" 2>/dev/null || true
    say "backup: $bk"
    arc="$PREFIX/susanin/var/archive"
    mkdir -p "$arc"
    ls -1dt "$PREFIX/susanin/var"/datapath-* 2>/dev/null | tail -n +4 | \
        while read -r old; do
            base=$(basename "$old")
            if tar -czf "$arc/$base.tar.gz" -C "$(dirname "$old")" "$base" 2>/dev/null; then
                rm -rf "$old"
            fi
        done
}

ensure_sets() {
    for s in $SETS; do
        set_exists "$s" || "$IPSET" create "$s" hash:ip timeout "$TTL_OK"
    done
    set_exists "$NETSET" || "$IPSET" create "$NETSET" hash:net timeout 0
    set_exists "$NEVERSET" || "$IPSET" create "$NEVERSET" hash:net timeout 0

    if [ -f "$ALWAYS_FILE" ]; then
        grep -vE '^(#|[[:space:]]*$)' "$ALWAYS_FILE" | while read -r net; do
            case "$net" in
                */*|*.*.*.*) "$IPSET" add "$NETSET" "$net" -exist 2>/dev/null || true ;;
            esac
        done
    fi
    say "ipsets ready"
}

ensure_chain() {
    "$IPT" -w -t mangle -S "$CHAIN" >/dev/null 2>&1 || "$IPT" -w -t mangle -N "$CHAIN"
}

rule_priv() {
    if [ -n "$POLICY_MARK" ]; then
        mangle "$CHAIN" -m mark ! --mark "$POLICY_MARK/0x0fffffff" -j RETURN
    fi
    for priv in 0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16 \
                172.16.0.0/12 192.168.0.0/16 224.0.0.0/4 240.0.0.0/4; do
        for i in $LAN; do
            mangle "$CHAIN" -i "$i" -d "$priv" -j RETURN
        done
    done
}

rule_mark() {
    for i in $LAN; do
        mangle "$CHAIN" -i "$i" -m set --match-set susanin_never dst -j RETURN
        for p in tcp udp; do
            mangle "$CHAIN" -i "$i" -p "$p" \
                -m set --match-set susanin_ok_${p} dst \
                -j MARK --set-xmark "$MARK_OK/$MASK"
            mangle "$CHAIN" -i "$i" -p "$p" \
                -m set --match-set susanin_ok_${p} dst \
                -j CONNMARK --save-mark --nfmask "$MASK" --ctmask "$MASK"

            mangle "$CHAIN" -i "$i" -p "$p" \
                -m set --match-set susanin_test_${p} dst \
                -j MARK --set-xmark "$MARK_TEST/$MASK"
            mangle "$CHAIN" -i "$i" -p "$p" \
                -m set --match-set susanin_test_${p} dst \
                -j CONNMARK --save-mark --nfmask "$MASK" --ctmask "$MASK"
        done

        mangle "$CHAIN" -i "$i" \
            -m set --match-set susanin_ok_net dst \
            -j MARK --set-xmark "$MARK_OK/$MASK"
        mangle "$CHAIN" -i "$i" \
            -m set --match-set susanin_ok_net dst \
            -j CONNMARK --save-mark --nfmask "$MASK" --ctmask "$MASK"

        mangle "$CHAIN" -i "$i" -j CONNMARK --restore-mark --nfmask "$MASK" --ctmask "$MASK"
        mangle "$CHAIN" -i "$i" -m mark --mark "$MARK_OK/$MASK" \
            -j MARK --set-xmark "$MARK_OK/$MASK"
        mangle "$CHAIN" -i "$i" -m mark --mark "$MARK_TEST/$MASK" \
            -j MARK --set-xmark "$MARK_TEST/$MASK"
    done
}

ensure_jump() {
    "$IPT" -w -t mangle -D PREROUTING -j "$CHAIN" >/dev/null 2>&1 || true
    for i in $LAN; do
        "$IPT" -w -t mangle -D PREROUTING -i "$i" -j "$CHAIN" >/dev/null 2>&1 || true
    done
    if [ -n "$POLICY_MARK" ]; then
        "$IPT" -w -t mangle -D PREROUTING -m mark --mark "$POLICY_MARK" -j "$CHAIN" >/dev/null 2>&1 || true
        "$IPT" -w -t mangle -D PREROUTING -m mark --mark "$POLICY_MARK/0x0fffffff" -j "$CHAIN" >/dev/null 2>&1 || true
        "$IPT" -w -t mangle -A PREROUTING -m mark --mark "$POLICY_MARK/0x0fffffff" -j "$CHAIN"
    else
        "$IPT" -w -t mangle -A PREROUTING -j "$CHAIN"
    fi
}

ensure_table() {
    "$IPCMD" route del default dev "$EGRESS" table "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" route add default dev "$EGRESS" table "$TABLE"
    "$IPCMD" rule del fwmark "$MARK_OK" priority "$PRI_OK" lookup "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" rule del fwmark "$MARK_TEST" priority "$PRI_TEST" lookup "$TABLE" >/dev/null 2>&1 || true
    iprule fwmark "$MARK_OK/$MASK" priority "$PRI_OK" lookup "$TABLE"
    iprule fwmark "$MARK_TEST/$MASK" priority "$PRI_TEST" lookup "$TABLE"

    # Forwarding and NAT for egress interface
    "$IPT" -w -D FORWARD -i "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -D FORWARD -o "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -I FORWARD 1 -i "$EGRESS" -j ACCEPT
    "$IPT" -w -I FORWARD 1 -o "$EGRESS" -j ACCEPT
    "$IPT" -w -D INPUT -i "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -I INPUT 1 -i "$EGRESS" -j ACCEPT
    "$IPT" -w -t mangle -C FORWARD -o "$EGRESS" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu >/dev/null 2>&1 || \
        "$IPT" -w -t mangle -I FORWARD 1 -o "$EGRESS" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu
    "$IPT" -w -t nat -C POSTROUTING -o "$EGRESS" -j MASQUERADE >/dev/null 2>&1 || \
        "$IPT" -w -t nat -I POSTROUTING 1 -o "$EGRESS" -j MASQUERADE
    # Reject direct QUIC (UDP 443) so browsers/apps immediately fall back to TCP HTTPS
    # without suffering 10-15s TSPU throttling on direct UDP streams.
    "$IPT" -w -D FORWARD -p udp --dport 443 -m mark --mark "0x0/$MASK" -j REJECT --reject-with icmp-port-unreachable >/dev/null 2>&1 || true
    "$IPT" -w -I FORWARD 1 -p udp --dport 443 -m mark --mark "0x0/$MASK" -j REJECT --reject-with icmp-port-unreachable

    say "table/ip-rule ready (table=$TABLE dev=$EGRESS mask=$MASK)"
}

command_up() {
    backup
    ensure_sets
    ensure_chain
    rule_priv
    rule_mark
    ensure_jump
    ensure_table
    say "data plane UP (table=$TABLE dev=$EGRESS)"
}

command_down() {
    "$IPT" -w -t mangle -D PREROUTING -j "$CHAIN" >/dev/null 2>&1 || true
    for i in $LAN; do
        "$IPT" -w -t mangle -D PREROUTING -i "$i" -j "$CHAIN" >/dev/null 2>&1 || true
    done
    if [ -n "$POLICY_MARK" ]; then
        "$IPT" -w -t mangle -D PREROUTING -m mark --mark "$POLICY_MARK" -j "$CHAIN" >/dev/null 2>&1 || true
        "$IPT" -w -t mangle -D PREROUTING -m mark --mark "$POLICY_MARK/0x0fffffff" -j "$CHAIN" >/dev/null 2>&1 || true
    fi
    if "$IPT" -w -t mangle -S "$CHAIN" >/dev/null 2>&1; then
        "$IPT" -w -t mangle -F "$CHAIN"; "$IPT" -w -t mangle -X "$CHAIN" || true
    fi
    for s in $SETS; do set_exists "$s" && "$IPSET" destroy "$s" || true; done
    set_exists "$NETSET" && "$IPSET" destroy "$NETSET" || true
    set_exists "$NEVERSET" && "$IPSET" destroy "$NEVERSET" || true
    "$IPCMD" rule del fwmark "$MARK_OK/$MASK" priority "$PRI_OK" lookup "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" rule del fwmark "$MARK_TEST/$MASK" priority "$PRI_TEST" lookup "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" rule del fwmark "$MARK_OK" priority "$PRI_OK" lookup "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" rule del fwmark "$MARK_TEST" priority "$PRI_TEST" lookup "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" route del default dev "$EGRESS" table "$TABLE" >/dev/null 2>&1 || true

    "$IPT" -w -D FORWARD -i "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -D FORWARD -o "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -D INPUT -i "$EGRESS" -j ACCEPT >/dev/null 2>&1 || true
    "$IPT" -w -t mangle -D FORWARD -o "$EGRESS" -p tcp --tcp-flags SYN,RST SYN -j TCPMSS --clamp-mss-to-pmtu >/dev/null 2>&1 || true
    "$IPT" -w -t nat -D POSTROUTING -o "$EGRESS" -j MASQUERADE >/dev/null 2>&1 || true
    "$IPT" -w -D FORWARD -p udp --dport 443 -m mark --mark "0x0/$MASK" -j REJECT --reject-with icmp-port-unreachable >/dev/null 2>&1 || true

    CT=$(find_bin conntrack)
    if [ -n "$CT" ]; then
        "$CT" -D -m "$MARK_OK" >/dev/null 2>&1 || true
        "$CT" -D -m "$MARK_TEST" >/dev/null 2>&1 || true
    fi

    say "data plane DOWN"
}

command_status() {
    if "$IPT" -w -t mangle -S PREROUTING >/dev/null 2>&1 && "$IPT" -w -t mangle -S PREROUTING | grep -q "$CHAIN"; then
        echo "jump: present"
    else
        echo "jump: MISSING"
    fi
    for s in $SETS; do
        if set_exists "$s"; then
            echo "$s = $("$IPSET" list "$s" 2>/dev/null | grep -cE '^[0-9]+\.' || true)"
        else
            echo "$s = (absent)"
        fi
    done
    if set_exists "$NETSET"; then
        echo "$NETSET = $("$IPSET" list "$NETSET" 2>/dev/null | grep -cE '^[0-9]+\.' || true)"
    else
        echo "$NETSET = (absent)"
    fi
    if set_exists "$NEVERSET"; then
        echo "$NEVERSET = $("$IPSET" list "$NEVERSET" 2>/dev/null | grep -cE '^[0-9]+\.' || true)"
    else
        echo "$NEVERSET = (absent)"
    fi
    "$IPCMD" rule show | grep -E "lookup $TABLE" || echo "no ip rule for table $TABLE"
}

command_egress() {
    iface="$1"
    [ -n "$iface" ] || { echo "usage: $0 egress <iface>" >&2; exit 2; }
    EGRESS="$iface"
    "$IPCMD" route del default table "$TABLE" >/dev/null 2>&1 || true
    "$IPCMD" route add default dev "$iface" table "$TABLE"
    say "egress -> $iface (table=$TABLE)"
}

command_flush() {
    for s in $SETS; do set_exists "$s" && "$IPSET" flush "$s" || true; done
    set_exists "$NETSET" && "$IPSET" flush "$NETSET" || true
    set_exists "$NEVERSET" && "$IPSET" flush "$NEVERSET" || true
    say "sets flushed (fail-open / DIRECT)"
}

command_add() {
    ip="$1"; p="$2"; ph="$3"
    case "$p" in tcp|udp) ;; *) echo "bad proto: $p" >&2; exit 2;; esac
    case "$ph" in test|ok) ;; *) echo "bad phase: $ph" >&2; exit 2;; esac
    ensure_sets
    ttl=$TTL_TEST; [ "$ph" = ok ] && ttl=$TTL_OK
    "$IPSET" -exist add "susanin_${ph}_${p}" "$ip" timeout "$ttl"
    say "added $ip -> susanin_${ph}_${p} (timeout ${ttl}s)"
}

command_del() {
    ip="$1"; p="$2"
    for s in "susanin_test_${p}" "susanin_ok_${p}"; do
        set_exists "$s" && "$IPSET" -exist del "$s" "$ip" >/dev/null 2>&1 || true
    done
    say "deleted $ip ($p)"
}

case "${1:-}" in
    up) command_up ;;
    down) command_down ;;
    status) command_status ;;
    flush) command_flush ;;
    egress) command_egress "${2:-}" ;;
    add) command_add "$2" "$3" "$4" ;;
    del) command_del "$2" "$3" ;;
    *)
        echo "usage: $0 {up|down|status|flush|egress <iface>|add <ip> <tcp|udp> <test|ok>|del <ip> <tcp|udp>}" >&2
        exit 2 ;;
esac
