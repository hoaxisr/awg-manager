#!/bin/sh
# Susanin.Keenetic installer.
# Online (default): downloads the per-arch archive from GitHub Releases.
#   curl -fsSL https://raw.githubusercontent.com/R17a/Susanin.Keenetic/main/install.sh \
#     | sh -s -- [--arch mipsel] [--version vX.Y.Z] [--yes]
# Offline/local: run from an extracted archive (susanin-agent is next to this script):
#   sh install.sh [--yes]
# Prompts accept short answers: "y" or "yes". Use --yes|-y to skip all prompts.
# POSIX sh (busybox ash compatible).
set -eu

REPO="R17a/Susanin.Keenetic"
PREFIX=/opt/susanin
INITD=/opt/etc/init.d
ARCH=""
VERSION="latest"
EGRESS=""
LAN=""
SUBNETS=""
YES=0
FORCE=0
NO_START=0
DISK_MODE=""
DEPS=0

say() { echo "[susanin] $*"; }
die() { echo "[susanin] ERROR: $*" >&2; exit 1; }

# Detect whether /opt lives on a removable flash (USB/SD) or on the router's
# internal memory (NAND/UBIFS/overlay). Internal -> soft disk mode (minimal writes).
detect_disk_mode() {
    dev=""; fst=""; mnt=""
    if command -v findmnt >/dev/null 2>&1; then
        mnt=$(findmnt -n -o TARGET --target /opt 2>/dev/null | head -n1)
        fst=$(findmnt -n -o FSTYPE --target /opt 2>/dev/null | head -n1)
        dev=$(findmnt -n -o SOURCE --target /opt 2>/dev/null | head -n1)
    fi
    if [ -z "$mnt" ] && [ -r /proc/mounts ]; then
        line=$(grep -E ' /opt ' /proc/mounts 2>/dev/null | head -n1)
        if [ -n "$line" ]; then
            dev=$(printf '%s' "$line" | awk '{print $1}')
            fst=$(printf '%s' "$line" | awk '{print $3}')
            mnt=/opt
        fi
    fi
    # /opt — не отдельная точка монтирования => часть rootfs (внутренняя память).
    if [ "$mnt" != "/opt" ]; then
        echo soft; return
    fi
    case "$fst" in
        ubifs|squashfs|jffs2|overlay|ramfs|tmpfs) echo soft; return ;;
    esac
    case "$dev" in
        *mtdblock*|*ubiblock*|*overlay*|*rootfs*|/storage*) echo soft; return ;;
        /dev/sd*|/dev/mmcblk*|/dev/nvme*|/dev/usb*) echo normal; return ;;
    esac
    # Не смогли определить носитель — безопаснее мягкий режим.
    echo soft
}

while [ $# -gt 0 ]; do
    case "$1" in
        --arch) ARCH="$2"; shift ;;
        --version) VERSION="$2"; shift ;;
        --egress) EGRESS="$2"; shift ;;
        --lan) LAN="$2"; shift ;;
        --subnets) SUBNETS="$2"; shift ;;
        --prefix) PREFIX="$2"; shift ;;
        --disk-mode) DISK_MODE="$2"; shift ;;
        --deps) DEPS=1 ;;
        --yes|-y) YES=1 ;;
        --force) FORCE=1 ;;
        --no-start) NO_START=1 ;;
        -h|--help)
            echo "usage: $0 [--arch mipsel|mips|aarch64|armv7|x86_64] [--version latest|vX.Y.Z]"
            echo "          [--egress IF] [--lan IF,IF] [--subnets CIDR,CIDR] [--prefix DIR]"
            echo "          [--disk-mode normal|soft] [--deps]"
            echo "          [--yes] [--force] [--no-start]"
            echo
            echo "  --disk-mode  normal (USB/SD) | soft (internal flash; no logs/state/backups)."
            echo "               Default: autodetect by /opt mount."
            echo "  --deps       доустановить недостающие пакеты через opkg без вопроса"
            echo
            echo "  Prompts: answer 'y' (or 'yes'); --yes|-y skips all prompts."
            exit 0 ;;
        *) die "unknown arg: $1" ;;
    esac
    shift
done

# --- зависимости (Entware) ---------------------------------------------------
need=""
for t in ipset conntrack iptables; do
    command -v "$t" >/dev/null 2>&1 || need="$need $t"
done
[ -f /opt/etc/ssl/certs/ca-certificates.crt ] || need="$need ca-certificates"
need=$(printf '%s' "$need" | sed 's/^ *//')
if [ -n "$need" ]; then
    say "не хватает пакетов:$need"
    install_them=0
    if [ "$DEPS" -eq 1 ]; then
        install_them=1
    elif [ "$YES" -ne 1 ] && [ -r /dev/tty ]; then
        printf "[susanin] Доустановить через opkg? [y/N]: " >&2
        read _ok < /dev/tty || _ok=n
        case "$_ok" in y|Y|yes|YES) install_them=1 ;; esac
    fi
    if [ "$install_them" -eq 1 ]; then
        say "opkg update && opkg install$need"
        opkg update || true
        opkg install $need || true
    else
        say "пропускаю. Установите вручную: opkg update && opkg install$need"
    fi
fi

if [ -z "$ARCH" ]; then
    _m=$(uname -m 2>/dev/null || echo unknown)
    case "$_m" in
        mips|mipsel) ARCH=mipsel ;;
        mips64) ARCH=mips64el ;;
        aarch64|arm64) ARCH=aarch64 ;;
        armv7l|armv7|armhf) ARCH=armv7 ;;
        x86_64|amd64) ARCH=x86_64 ;;
        *) die "cannot detect arch (uname -m=$_m); pass --arch" ;;
    esac
fi

DIR0=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
if [ -f "$DIR0/susanin-agent" ] || [ -n "$(ls "$DIR0"/susanin-agent.* 2>/dev/null)" ]; then
    DIR=$DIR0
    say "local package: $DIR"
else
    if command -v curl >/dev/null 2>&1; then
        fetch() { curl -fsSL "$1" -o "$2"; }
    elif command -v wget >/dev/null 2>&1; then
        fetch() { wget -qO "$2" "$1"; }
    else
        die "need curl or wget (Entware: opkg update && opkg install ca-certificates; optionally 'opkg install curl')"
    fi
    if [ "$VERSION" = latest ]; then
        BASE="https://github.com/$REPO/releases/latest/download"
    else
        BASE="https://github.com/$REPO/releases/download/$VERSION"
    fi
    ASSET="susanin-keenetic-deploy-$ARCH.tar.gz"
    say "arch=$ARCH version=$VERSION"
    TMP=$(mktemp -d /tmp/susanin-inst.XXXXXX)
    trap 'rm -rf "$TMP"' EXIT INT TERM
    say "downloading $BASE/$ASSET"
    fetch "$BASE/$ASSET" "$TMP/pkg.tar.gz" \
        || die "download failed (check --arch/--version or release assets): $BASE/$ASSET
     hint: opkg update && opkg install ca-certificates"
    tar -xzf "$TMP/pkg.tar.gz" -C "$TMP" || die "bad archive $ASSET"
    DIR=$TMP
fi

if [ -f "$DIR/susanin-agent" ]; then
    BINFILE=susanin-agent
else
    BINFILE=$(basename "$(ls "$DIR"/susanin-agent.* 2>/dev/null | head -1)")
fi
[ -n "${BINFILE:-}" ] && [ -f "$DIR/$BINFILE" ] || die "susanin-agent binary not found in $DIR"
say "binary: $BINFILE"

ifaces() { awk -F: '{print $1}' /proc/net/dev | tr -d ' ' | grep -v '^$'; }
addr_of() { ip addr show "$1" 2>/dev/null | awk '/inet /{print $2}' | head -1; }

pick() {
    _what="$1"; shift
    _n=0
    for _i in "$@"; do _n=$((_n + 1)); echo "  $_n) $_i" >&2; done
    if [ -r /dev/tty ]; then
        printf "%s [1-%d]: " "$_what" "$_n" >&2
        read _sel < /dev/tty || _sel=1
    else
        die "ambiguous $_what; pass flag explicitly (no tty)"
    fi
    _n=0
    for _i in "$@"; do _n=$((_n + 1)); [ "$_n" = "$_sel" ] && { echo "$_i"; return; }; done
    echo "$1"
}

default_devs() {
    ip route show table all 2>/dev/null \
      | awk '/default/ {for (i = 1; i <= NF; i++) if ($i == "dev") print $(i + 1)}' \
      | sort -u
}

if [ -z "$EGRESS" ]; then
    CAND=$(ifaces | grep -E '^(nwg|wg[0-9]*|amnezia|ovpn)' || true)
    if [ -z "$CAND" ]; then
        CAND=$(ifaces | grep -E '^(tun[0-9]+|tap[0-9]+)$' || true)
    fi
    CN=$(printf '%s\n' "$CAND" | grep -c . || true)
    if [ "$CN" = 1 ]; then
        EGRESS="$CAND"
    elif [ "$CN" -gt 1 ]; then
        DEF=$(default_devs)
        for c in $CAND; do
            if printf '%s\n' "$DEF" | grep -qx "$c"; then EGRESS="$c"; break; fi
        done
        [ -n "$EGRESS" ] || EGRESS=$(printf '%s\n' "$CAND" | grep -E '^(nwg|wg)' | head -1)
        [ -n "$EGRESS" ] || EGRESS=$(printf '%s\n' "$CAND" | head -1)
        say "selected egress=$EGRESS (candidates: $(printf '%s ' $CAND)); use --egress to override"
    elif ifaces | grep -qx nwg0; then
        EGRESS=nwg0
    else
        say "no VPN interface auto-detected; select manually"
        EGRESS=$(pick "select egress (VPN)" $(ifaces | grep -Ev '^(lo|ppp|tunl)' || true))
    fi
fi
say "egress=$EGRESS addr=$(addr_of "$EGRESS")"

lan_from_routes() {
    ip route show 2>/dev/null | awk '
        /^(10\.|192\.168\.|172\.(1[6-9]|2[0-9]|3[01])\.)/ && $0 !~ /default/ {
            for (i = 1; i <= NF; i++) if ($i == "dev") { print $(i + 1); break }
        }' | sort -u
}

if [ -z "$LAN" ]; then
    LANBR=$(lan_from_routes | grep '^br' || true)
    if [ -n "$LANBR" ]; then
        LAN=$(printf '%s\n' "$LANBR" | awk 'NR==1{s=$0;next}{s=s","$0}END{print s}')
    else
        LAN=$(lan_from_routes | grep -Ev '^(ppp|nwg|wg|tun|tap|eth)' \
              | awk 'NR==1{s=$0;next}{s=s","$0}END{print s}')
    fi
    [ -n "$LAN" ] || LAN="br0"
fi
if [ -z "$SUBNETS" ]; then
    for i in $(printf '%s' "$LAN" | tr ',' ' '); do
        p=$(ip route show 2>/dev/null | awk -v d="$i" '$0 ~ ("dev " d " ") && $1 ~ /\// {print $1; exit}')
        [ -n "$p" ] && SUBNETS="${SUBNETS:+$SUBNETS,}$p"
    done
fi

# OpenConnect (ocserv) server, if present, can be routed through Susanin too.
oc_if=""
for i in $(ifaces); do
    case "$i" in oc[0-9]*) oc_if=$i ;; esac
done
if [ -z "$oc_if" ]; then
    dev=$(sed -n 's/^device=//p' /var/run/ocserv/ocserv.conf 2>/dev/null | head -1)
    if [ -n "$dev" ] && ifaces | grep -qx "${dev}0"; then
        oc_if="${dev}0"
    elif ps 2>/dev/null | grep -q '[o]cserv'; then
        [ -n "$dev" ] && oc_if="${dev}0" || oc_if=oc0
    fi
fi
if [ -n "$oc_if" ]; then
    case ",$LAN," in
        *",$oc_if,"*) : ;;
        *)
            add_oc=0
            if [ "$YES" -eq 1 ]; then
                add_oc=1
            elif [ -r /dev/tty ]; then
                printf "[susanin] OpenConnect server detected (%s). Add it to Susanin routing? [y/N]: " "$oc_if" >&2
                read _oc < /dev/tty || _oc=n
                case "$_oc" in y|Y|yes|YES) add_oc=1 ;; esac
            fi
            if [ "$add_oc" -eq 1 ]; then
                oc_addr=$(sed -n 's/^ipv4-network=//p' /var/run/ocserv/ocserv.conf 2>/dev/null | head -1)
                [ -n "$oc_addr" ] || oc_addr=$(addr_of "$oc_if")
                oc_net=$(printf '%s' "$oc_addr" | awk -F'[./]' '{print $1"."$2"."$3".0/24"}')
                LAN="${LAN:+$LAN,}$oc_if"
                SUBNETS="${SUBNETS:+$SUBNETS,}$oc_net"
                say "OpenConnect added: iface=$oc_if subnet=$oc_net"
            fi
            ;;
    esac
fi
say "lan=$LAN subnets=${SUBNETS:-n/a}"

if [ -z "$DISK_MODE" ]; then
    DISK_MODE=$(detect_disk_mode)
fi
case "$DISK_MODE" in
    normal) say "disk mode: normal (flash/USB)" ;;
    soft)   say "disk mode: soft (внутренняя память: логи/state/бэкапы отключены)" ;;
    *)      die "bad --disk-mode: $DISK_MODE (normal|soft)" ;;
esac

if [ "$YES" -ne 1 ] && [ -r /dev/tty ]; then
    printf "[susanin] Install to %s ?\n  egress:  %s\n  lan:     %s\n  subnets: %s\n  disk:    %s\nProceed? [y/N]: " \
        "$PREFIX" "$EGRESS" "$LAN" "${SUBNETS:-n/a}" "$DISK_MODE" >&2
    read _ok < /dev/tty || _ok=n
    case "$_ok" in y|Y|yes|YES) ;; *) die "aborted" ;; esac
fi

mkdir -p "$PREFIX/bin" "$PREFIX/tools" "$PREFIX/etc" "$PREFIX/var" "$INITD"
cp "$DIR/$BINFILE" "$PREFIX/bin/susanin-agent"
for f in datapath.sh susanin.sh update.sh uninstall.sh install.sh report.sh diagnose.sh; do
    [ -f "$DIR/$f" ] && cp "$DIR/$f" "$PREFIX/tools/$f"
done
chmod +x "$PREFIX/bin/susanin-agent" "$PREFIX/tools/"*.sh 2>/dev/null || true

if [ ! -f "$PREFIX/etc/susanin.conf" ] || [ "$FORCE" = 1 ]; then
    [ -f "$DIR/config.example.conf" ] && cp "$DIR/config.example.conf" "$PREFIX/etc/susanin.conf"
    sed -i "s|^egress_interface=.*|egress_interface=$EGRESS|" "$PREFIX/etc/susanin.conf" 2>/dev/null || true
    sed -i "s|^lan_interfaces=.*|lan_interfaces=$LAN|" "$PREFIX/etc/susanin.conf" 2>/dev/null || true
    [ -n "$SUBNETS" ] && sed -i "s|^lan_subnets=.*|lan_subnets=$SUBNETS|" "$PREFIX/etc/susanin.conf" 2>/dev/null || true
    EA=$(addr_of "$EGRESS")
    [ -n "$EA" ] && sed -i "s|^egress_address=.*|egress_address=${EA%%/*}|" "$PREFIX/etc/susanin.conf" 2>/dev/null || true
    say "config written: $PREFIX/etc/susanin.conf"
else
    say "config kept: $PREFIX/etc/susanin.conf"
fi

# disk_mode применяем всегда (в т.ч. когда конфиг уже существовал).
if [ -f "$PREFIX/etc/susanin.conf" ]; then
    if grep -q '^disk_mode=' "$PREFIX/etc/susanin.conf" 2>/dev/null; then
        sed -i "s|^disk_mode=.*|disk_mode=$DISK_MODE|" "$PREFIX/etc/susanin.conf" 2>/dev/null || true
    else
        echo "disk_mode=$DISK_MODE" >> "$PREFIX/etc/susanin.conf"
    fi
    say "disk_mode=$DISK_MODE -> $PREFIX/etc/susanin.conf"
fi
# Дополнить существующий список новыми строками из пакета. Ничего не удаляем и
# не перезаписываем: добавляем только те «чистые» строки (домены/IP/CIDR), которых
# ещё нет. Идемпотентно: повторный запуск ничего не меняет.
merge_list() { # merge_list <user_file> <package_file> <label>
    _uf="$1"; _pf="$2"; _label="$3"
    [ -f "$_uf" ] && [ -f "$_pf" ] || return 0
    _tmp=$(mktemp 2>/dev/null) || _tmp="/tmp/susanin-merge.$$"
    sed 's/#.*//' "$_uf" | tr -d ' \t\r' | grep -v '^$' | sort -u > "$_tmp" || true
    _added=0
    while IFS= read -r _line; do
        _e=$(printf '%s' "$_line" | sed 's/#.*//' | tr -d ' \t\r')
        if [ -z "$_e" ]; then
            continue
        fi
        if grep -qxF "$_e" "$_tmp"; then
            continue
        fi
        if [ "$_added" -eq 0 ]; then
            if ! grep -qF 'susanin-update:' "$_uf"; then
                printf '\n# susanin-update: added missing default entries\n' >> "$_uf"
            fi
        fi
        printf '%s\n' "$_e" >> "$_uf"
        printf '%s\n' "$_e" >> "$_tmp"
        _added=$((_added + 1))
    done < "$_pf"
    rm -f "$_tmp"
    if [ "$_added" -gt 0 ]; then
        say "$_label: добавлено новых строк: $_added"
    fi
    return 0
}

if [ ! -f "$PREFIX/etc/vpn_always.txt" ] && [ -f "$DIR/vpn_always.txt" ]; then
    cp "$DIR/vpn_always.txt" "$PREFIX/etc/vpn_always.txt"
    say "vpn_always list installed: $PREFIX/etc/vpn_always.txt"
else
    say "vpn_always list kept (not overwritten)"
fi
if [ ! -f "$PREFIX/etc/vpn_never.txt" ] && [ -f "$DIR/vpn_never.txt" ]; then
    cp "$DIR/vpn_never.txt" "$PREFIX/etc/vpn_never.txt"
    say "vpn_never list installed: $PREFIX/etc/vpn_never.txt"
else
    say "vpn_never list kept (not overwritten)"
    merge_list "$PREFIX/etc/vpn_never.txt" "$DIR/vpn_never.txt" "vpn_never"
fi

if [ -f "$DIR/S94susanin" ]; then
    cp "$DIR/S94susanin" "$INITD/S94susanin"
    chmod +x "$INITD/S94susanin"
fi

if [ "$NO_START" -ne 1 ]; then
    if ps 2>/dev/null | grep '[s]usanin-agent' >/dev/null 2>&1; then
        sh "$PREFIX/tools/susanin.sh" restart || true
    else
        sh "$PREFIX/tools/susanin.sh" start || true
    fi
fi
say "installed to $PREFIX (run: sh $PREFIX/tools/susanin.sh status)"
