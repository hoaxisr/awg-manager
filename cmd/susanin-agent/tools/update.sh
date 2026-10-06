#!/bin/sh
# Susanin.Keenetic update: replace binary/tools, keep config and state.
# Never touches /opt/susanin/etc/vpn_always.txt (user list is preserved).
#   sh update.sh [--arch mipsel] [--version latest|vX.Y.Z] [--prefix /opt/susanin]
set -eu

REPO="R17a/Susanin.Keenetic"
PREFIX=/opt/susanin
ARCH=""
VERSION="latest"

say() { echo "[susanin] $*"; }
die() { echo "[susanin] ERROR: $*" >&2; exit 1; }

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

while [ $# -gt 0 ]; do
    case "$1" in
        --arch) ARCH="$2"; shift ;;
        --version) VERSION="$2"; shift ;;
        v[0-9]*) VERSION="$1" ;;
        [0-9]*) VERSION="v$1" ;;
        --prefix) PREFIX="$2"; shift ;;
        -h|--help) echo "usage: $0 [--arch ARCH] [--version latest|vX.Y.Z] [--prefix DIR]"; exit 0 ;;
        *) die "unknown arg: $1" ;;
    esac
    shift
done

if [ -z "$ARCH" ]; then
    _m=$(uname -m 2>/dev/null || echo unknown)
    case "$_m" in
        mips|mipsel) ARCH=mipsel ;;
        mips64) ARCH=mips64el ;;
        aarch64|arm64) ARCH=aarch64 ;;
        armv7l|armv7|armhf) ARCH=armv7 ;;
        x86_64|amd64) ARCH=x86_64 ;;
        *) die "cannot detect arch; pass --arch" ;;
    esac
fi

if command -v curl >/dev/null 2>&1; then
    fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
    fetch() { wget -qO "$2" "$1"; }
else
    die "need curl or wget (Entware: opkg update && opkg install ca-certificates)"
fi

OLD="unknown"
[ -x "$PREFIX/bin/susanin-agent" ] && OLD=$("$PREFIX/bin/susanin-agent" version 2>/dev/null || echo unknown)
say "installed version: $OLD -> target: $VERSION ($ARCH)"

if [ -x "$PREFIX/tools/susanin.sh" ]; then
    sh "$PREFIX/tools/susanin.sh" stop || true
fi

if [ "$VERSION" = latest ]; then
    BASE="https://github.com/$REPO/releases/latest/download"
else
    BASE="https://github.com/$REPO/releases/download/$VERSION"
fi
ASSET="susanin-keenetic-deploy-$ARCH.tar.gz"
TMP=$(mktemp -d /tmp/susanin-upd.XXXXXX)
trap 'rm -rf "$TMP"' EXIT INT TERM
say "downloading $BASE/$ASSET"
fetch "$BASE/$ASSET" "$TMP/pkg.tar.gz" || die "download failed: $BASE/$ASSET
     hint: opkg update && opkg install ca-certificates"
tar -xzf "$TMP/pkg.tar.gz" -C "$TMP" || die "bad archive"
PKG=$(find "$TMP" -maxdepth 2 -name 'susanin-agent' -type f 2>/dev/null | head -1)
[ -n "$PKG" ] || PKG=$(find "$TMP" -maxdepth 2 -name 'susanin-agent.*' -type f 2>/dev/null | head -1)
[ -n "$PKG" ] || die "binary not found in package"
DIR=$(dirname "$PKG")
if [ -f "$DIR/susanin-agent" ]; then
    BINFILE=susanin-agent
else
    BINFILE=$(basename "$PKG")
fi

mkdir -p "$PREFIX/bin" "$PREFIX/tools" "$PREFIX/var/backup"
STAMP=$(date +%Y%m%d-%H%M%S)
[ -f "$PREFIX/etc/susanin.conf" ] && cp "$PREFIX/etc/susanin.conf" "$PREFIX/var/backup/susanin.conf.$STAMP"
[ -f "$PREFIX/var/susanin.state" ] && cp "$PREFIX/var/susanin.state" "$PREFIX/var/backup/susanin.state.$STAMP"

cp "$DIR/$BINFILE" "$PREFIX/bin/susanin-agent.new"
mv "$PREFIX/bin/susanin-agent.new" "$PREFIX/bin/susanin-agent"
chmod +x "$PREFIX/bin/susanin-agent"
# Копируем все скрипты из пакета (не жёстким списком): так новые файлы,
# добавленные в релизе, не теряются при обновлении старым update.sh.
for f in "$DIR"/*.sh; do
    [ -f "$f" ] || continue
    cp "$f" "$PREFIX/tools/"
done
chmod +x "$PREFIX/tools/"*.sh 2>/dev/null || true

# Дополнить существующие списки новыми строками из пакета (идемпотентно).
if [ -f "$DIR/vpn_never.txt" ]; then
    merge_list "$PREFIX/etc/vpn_never.txt" "$DIR/vpn_never.txt" "vpn_never"
fi

if [ -x "$PREFIX/tools/susanin.sh" ]; then
    sh "$PREFIX/tools/susanin.sh" start || true
fi
NEW=$("$PREFIX/bin/susanin-agent" version 2>/dev/null || echo unknown)
say "updated: $OLD -> $NEW (config/state preserved)"
