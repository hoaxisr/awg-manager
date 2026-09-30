#!/bin/sh
# build-mips.sh — cross-compile susanin-agent to a static MIPS (mipsel) binary.
#
# Run inside a Linux (Debian/Ubuntu et al.): WSL, a container, or a VM.
# It uses the distro cross-compiler `gcc-mipsel-linux-gnu` and links a fully
# static binary (-static) so it runs on Entware (Keenetic) without needing any
# matching libc on the router.
#
# Requirements on the build host:
#   sudo apt-get install -y gcc-mipsel-linux-gnu make file
#
# Result: ./susanin-agent.mipsel
set -eu

CC=${CC:-mipsel-linux-gnu-gcc}
OUT=susanin-agent.mipsel

echo "== cleaning =="
make clean >/dev/null 2>&1 || true

echo "== building (CC=$CC, static) =="
make CC="$CC" \
     CFLAGS="-O2 -std=c11 -Wall -Wextra -Wpedantic -static" \
     LDFLAGS="-static"

if command -v file >/dev/null 2>&1; then
    file susanin-agent || true
fi

mv susanin-agent "$OUT"
echo "== done: ./$OUT =="
echo "   copy to router:  scp ./$OUT root@<router>:/opt/susanin/bin/susanin-agent"
