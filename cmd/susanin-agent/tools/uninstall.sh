#!/bin/sh
# Susanin.Keenetic uninstall.
#   sh uninstall.sh            # stop, remove datapath + init + bin/tools (keeps etc/var)
#   sh uninstall.sh --purge    # also remove config, state, logs (whole /opt/susanin)
set -eu

PREFIX=/opt/susanin
INITD=/opt/etc/init.d/S94susanin
PURGE=0

say() { echo "[susanin] $*"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --purge) PURGE=1 ;;
        --prefix) PREFIX="$2"; shift ;;
        -h|--help) echo "usage: $0 [--purge] [--prefix DIR]"; exit 0 ;;
        *) echo "[susanin] unknown arg: $1" >&2; exit 1 ;;
    esac
    shift
done

if [ -x "$PREFIX/tools/susanin.sh" ]; then
    sh "$PREFIX/tools/susanin.sh" stop || true
fi
if [ -x "$PREFIX/tools/datapath.sh" ]; then
    sh "$PREFIX/tools/datapath.sh" down || true
fi
[ -f "$INITD" ] && rm -f "$INITD" && say "removed $INITD"

if [ "$PURGE" = 1 ]; then
    rm -rf "$PREFIX"
    say "purged $PREFIX (config/state/logs removed)"
else
    rm -rf "$PREFIX/bin" "$PREFIX/tools"
    say "removed bin/tools; kept $PREFIX/etc and $PREFIX/var"
fi
say "uninstall done"
