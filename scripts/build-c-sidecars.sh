#!/usr/bin/env bash
set -euo pipefail
PROJECT_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:?usage: build-c-sidecars.sh <Entware arch>}"
case "$ARCH" in
    mipsel-3.4) GO_ARCH=mipsle; LOADER=ld.so.1 ;;
    mips-3.4) GO_ARCH=mips; LOADER=ld.so.1 ;;
    aarch64-3.10) GO_ARCH=arm64; LOADER=ld-linux-aarch64.so.1 ;;
    *) echo "unsupported Entware architecture: $ARCH" >&2; exit 2 ;;
esac
[[ "$(uname -s)" == Linux && "$(uname -m)" == x86_64 ]] || { echo 'Linux x86_64 build host required' >&2; exit 1; }
# Pins mirror hoaxisr/firc tools/ci/entware-sdk/SHA256SUMS at upstream
# 2dabae38f3af06c9bdaf0a837a4a32e9fdfd2179. No mutable SDK selection.
SDK_TAG=v2026.10.19
ASSET="entware-sdk-${ARCH}_gcc-8.4.0_glibc-2.27.Linux-x86_64.tar.zst"
SDK_DIR="$PROJECT_ROOT/build/entware-sdk/$ARCH"
mkdir -p "$SDK_DIR"
printf 'module awgm.local/entware-sdk-cache\n\ngo 1.24\n' > "$PROJECT_ROOT/build/entware-sdk/go.mod"
SDK_SHA="$(awk -v name="$ASSET" '$2 == name {print $1}' "$PROJECT_ROOT/scripts/entware-sdk.SHA256SUMS")"
[[ "$SDK_SHA" =~ ^[0-9a-f]{64}$ ]] || { echo 'missing SDK hash pin' >&2; exit 1; }
ARCHIVE="$SDK_DIR/$ASSET"
if [[ ! -f "$ARCHIVE" ]]; then
    curl --fail --location --proto '=https' --tlsv1.2 \
        "https://github.com/ownik/entware-sdk/releases/download/$SDK_TAG/$ASSET" --output "$ARCHIVE.download"
    printf '%s  %s\n' "$SDK_SHA" "$ARCHIVE.download" | sha256sum -c -
    mv "$ARCHIVE.download" "$ARCHIVE"
fi
printf '%s  %s\n' "$SDK_SHA" "$ARCHIVE" | sha256sum -c -
if [[ ! -f "$SDK_DIR/.extracted-$SDK_SHA" ]]; then
    tar --zstd --no-same-owner -xf "$ARCHIVE" -C "$SDK_DIR"
    touch "$SDK_DIR/.extracted-$SDK_SHA"
fi
mapfile -t COMPILERS < <(find "$SDK_DIR" -path '*/staging_dir/toolchain-*/bin/*-openwrt-linux-gnu-gcc' | sort)
[[ "${#COMPILERS[@]}" == 1 && -x "${COMPILERS[0]}" ]] || { echo 'exactly one target compiler is required' >&2; exit 1; }
TARGET_CC="${COMPILERS[0]}"
export STAGING_DIR="$(dirname "$(dirname "$(dirname "$TARGET_CC")")")"
TARGET_CFLAGS='-O2 -Wall -Wextra -std=gnu11 -pthread -fstack-protector-strong'
TARGET_LDFLAGS="-lpthread -Wl,-Bstatic -latomic -Wl,-Bdynamic -Wl,--dynamic-linker=/opt/lib/$LOADER -Wl,-rpath,/opt/lib -Wl,-z,relro,-z,now"
mkdir -p "$PROJECT_ROOT/build/cross"
for sidecar in xhttpd fpc; do
    make -B -C "$PROJECT_ROOT/c-core/$sidecar" CC="$TARGET_CC" CFLAGS="$TARGET_CFLAGS" LDFLAGS="$TARGET_LDFLAGS"
    cp "$PROJECT_ROOT/c-core/$sidecar/awgm-$sidecar" "$PROJECT_ROOT/build/cross/awgm-$sidecar-$GO_ARCH"
done
echo "Entware sidecars built for $ARCH; build-ipk.sh will verify ELF headers before packaging."
