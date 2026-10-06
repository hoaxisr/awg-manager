#!/bin/sh
# wsl-build.sh — run inside WSL (as root) to cross-compile susanin-agent.
# The project folder is taken from the location of this script, so the script is
# portable (no absolute local paths).
#
# Usage:
#   sh wsl-build.sh                  # build mipsel (canonical default)
#   sh wsl-build.sh mipsel mips aarch64 armv7 mips64el x86_64
#
# Required toolchains (Ubuntu/Debian): gcc-mipsel-linux-gnu, gcc-mips-linux-gnu,
#   gcc-aarch64-linux-gnu, gcc-arm-linux-gnueabihf, gcc-mips64el-linux-gnuabi64,
#   gcc-multilib (for static x86_64). Missing compilers are skipped with a hint.

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
SRC=$(CDPATH= cd -- "$SCRIPT_DIR/.." && pwd)
DEST="/root/Susanin.Keenetic"

[ -d "$SRC" ] || { echo "source not found: $SRC" >&2; exit 1; }

if [ "$#" -gt 0 ]; then
    TARGETS="$@"
else
    TARGETS="mipsel"
fi

pick_cc() {
    case "$1" in
        mipsel)   cc=mipsel-linux-gnu-gcc ;;
        mips)     cc=mips-linux-gnu-gcc ;;
        mips64el) cc=mips64el-linux-gnuabi64-gcc ;;
        aarch64)  cc=aarch64-linux-gnu-gcc ;;
        armv7)    cc=arm-linux-gnueabihf-gcc ;;
        x86_64)   cc=gcc ;;
        *) echo "unknown target: $1" >&2; return 1 ;;
    esac
}

pick_pkg() {
    case "$1" in
        mipsel)   echo gcc-mipsel-linux-gnu ;;
        mips)     echo gcc-mips-linux-gnu ;;
        mips64el) echo gcc-mips64el-linux-gnuabi64 ;;
        aarch64)  echo gcc-aarch64-linux-gnu ;;
        armv7)    echo gcc-arm-linux-gnueabihf ;;
        x86_64)   echo gcc-multilib ;;
    esac
}

rm -rf "$DEST"
cp -r "$SRC" "$DEST"
mkdir -p "$SRC/build"
ok=0

for T in $TARGETS; do
    if ! pick_cc "$T"; then
        continue
    fi
    if ! command -v "$cc" >/dev/null 2>&1; then
        echo "== $T: compiler '$cc' not found, skipping =="
        echo "   install: apt-get install -y $(pick_pkg "$T")"
        continue
    fi
    echo "== building $T ($cc) =="
    ( cd "$DEST" && make clean >/dev/null 2>&1 || true
      make CC="$cc" \
           CFLAGS="-O2 -std=c11 -Wall -Wextra -Wpedantic -static" \
           LDFLAGS="-static" )
    rc=$?
    if [ "$rc" -ne 0 ]; then
        echo "== $T: build failed ($rc) =="
        continue
    fi
    cp "$DEST/susanin-agent" "$SRC/build/susanin-agent.$T"
    file "$SRC/build/susanin-agent.$T"
    ok=1
done

cp -f "$SRC/config.example.conf" "$SRC/build/susanin.conf.example"

echo "=== delivered ==="
ls -l "$SRC/build/" | grep -E 'susanin-agent\.|susanin.conf.example'

[ "$ok" -eq 1 ] || { echo "no artifacts built" >&2; exit 1; }
