#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ARCH="${1:?expected an Entware MIPS architecture}"
case "$ARCH" in
    mipsel-3.4|mips-3.4) ;;
    *) echo "Unsupported Entware MIPS architecture: $ARCH" >&2; exit 2 ;;
esac

if [[ "$(uname -s)" != Linux || "$(uname -m)" != x86_64 ]]; then
    echo "The Entware MIPS SDK requires a Linux x86_64 build host." >&2
    exit 1
fi

SDK_TAG=v2026.10.19
ASSET="entware-sdk-${ARCH}_gcc-8.4.0_glibc-2.27.Linux-x86_64.tar.zst"
SHA="$(awk -v name="$ASSET" '$2 == name {print $1}' "$ROOT/scripts/entware-sdk.SHA256SUMS")"
[[ "$SHA" =~ ^[0-9a-f]{64}$ ]] || { echo "Missing SDK SHA-256 pin for $ASSET" >&2; exit 1; }

SDK_DIR="$ROOT/build/entware-sdk/$ARCH"
ARCHIVE="$SDK_DIR/$ASSET"
mkdir -p "$SDK_DIR"
printf 'module awgm.local/entware-sdk-cache\n\ngo 1.24\n' > "$ROOT/build/entware-sdk/go.mod"

if [[ ! -f "$ARCHIVE" ]]; then
    curl --fail --location --proto '=https' --tlsv1.2 \
        "https://github.com/ownik/entware-sdk/releases/download/$SDK_TAG/$ASSET" \
        --output "$ARCHIVE.download"
    printf '%s  %s\n' "$SHA" "$ARCHIVE.download" | sha256sum -c - >&2
    mv "$ARCHIVE.download" "$ARCHIVE"
fi
printf '%s  %s\n' "$SHA" "$ARCHIVE" | sha256sum -c - >&2
if [[ ! -f "$SDK_DIR/.extracted-$SHA" ]]; then
    tar --zstd --no-same-owner -xf "$ARCHIVE" -C "$SDK_DIR"
    touch "$SDK_DIR/.extracted-$SHA"
fi

mapfile -t compilers < <(find "$SDK_DIR" -path '*/staging_dir/toolchain-*/bin/*-openwrt-linux-gnu-gcc' | sort)
if [[ "${#compilers[@]}" != 1 || ! -x "${compilers[0]}" ]]; then
    echo "Expected one Entware compiler under $SDK_DIR." >&2
    exit 1
fi
printf '%s\n' "${compilers[0]}"
