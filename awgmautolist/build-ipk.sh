#!/bin/bash
# Build awgmautolist IPK packages for supported Keenetic Entware architectures.
# Usage: ./awgmautolist/build-ipk.sh [all|aarch64-3.10|mipsel-3.4|mips-3.4]
# VERSION overrides the package version; default is <satellite Version>-1.
set -euo pipefail

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$MODULE_DIR/.." && pwd)"
OUTDIR="${OUTDIR:-$PROJECT_ROOT/dist}"
BASE_VERSION="$(sed -n 's/^[[:space:]]*const Version = "\([^"]*\)".*/\1/p' "$MODULE_DIR/internal/autolist/doc.go" | head -n 1)"
VERSION="${VERSION:-${BASE_VERSION:-0.1.0}-1}"
TARGET="${1:-all}"

case "$VERSION" in
    ''|*[!A-Za-z0-9.+_:-]*)
        echo "ERROR: invalid package version: $VERSION" >&2
        exit 1
        ;;
esac
case "$TARGET" in
    all) ARCHES=(mipsel-3.4 mips-3.4 aarch64-3.10) ;;
    mipsel-3.4|mips-3.4|aarch64-3.10) ARCHES=("$TARGET") ;;
    *)
        echo "ERROR: unknown architecture: $TARGET" >&2
        echo "Usage: $0 [all|aarch64-3.10|mipsel-3.4|mips-3.4]" >&2
        exit 1
        ;;
esac

for cmd in go tar sed; do
    command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd not found" >&2; exit 1; }
done
mkdir -p "$OUTDIR"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/awgmautolist-ipk.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

for PKG_ARCH in "${ARCHES[@]}"; do
    case "$PKG_ARCH" in
        mipsel-3.4) GOARCH=mipsle ;;
        mips-3.4) GOARCH=mips ;;
        aarch64-3.10) GOARCH=arm64 ;;
    esac

    echo "=== awgmautolist $VERSION: $PKG_ARCH (linux/$GOARCH) ==="
    BUILD_DIR="$WORK/$PKG_ARCH"
    PKG_ROOT="$BUILD_DIR/root"
    mkdir -p "$PKG_ROOT/CONTROL" "$PKG_ROOT/opt/bin" "$PKG_ROOT/opt/etc/init.d"

    if [ "$GOARCH" = arm64 ]; then
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -C "$MODULE_DIR" -trimpath \
            -ldflags="-s -w" -o "$PKG_ROOT/opt/bin/awgmautolist" ./cmd/awgmautolist
    else
        GOOS=linux GOARCH="$GOARCH" GOMIPS=softfloat CGO_ENABLED=0 go build -C "$MODULE_DIR" -trimpath \
            -ldflags="-s -w" -o "$PKG_ROOT/opt/bin/awgmautolist" ./cmd/awgmautolist
    fi

    cp "$MODULE_DIR/entware/S99awgmautolist" "$PKG_ROOT/opt/etc/init.d/S99awgmautolist"
    cp "$MODULE_DIR/ipk/CONTROL/postinst" "$MODULE_DIR/ipk/CONTROL/prerm" "$PKG_ROOT/CONTROL/"
    sed -e "s/^Version:.*/Version: $VERSION/" \
        -e "s/^Architecture:.*/Architecture: $PKG_ARCH/" \
        "$MODULE_DIR/ipk/CONTROL/control" > "$PKG_ROOT/CONTROL/control"
    chmod 755 "$PKG_ROOT/opt/bin/awgmautolist" "$PKG_ROOT/opt/etc/init.d/S99awgmautolist" \
        "$PKG_ROOT/CONTROL/postinst" "$PKG_ROOT/CONTROL/prerm"
    chmod 644 "$PKG_ROOT/CONTROL/control"
    printf '2.0\n' > "$BUILD_DIR/debian-binary"

    tar -C "$PKG_ROOT/CONTROL" --numeric-owner --owner=0 --group=0 \
        -czf "$BUILD_DIR/control.tar.gz" control postinst prerm
    tar -C "$PKG_ROOT" --numeric-owner --owner=0 --group=0 \
        -czf "$BUILD_DIR/data.tar.gz" ./opt

    OUTPUT="$OUTDIR/awgmautolist_${VERSION}_${PKG_ARCH}-kn.ipk"
    TEMP_OUTPUT="$OUTDIR/.awgmautolist_${VERSION}_${PKG_ARCH}-kn.ipk.tmp"
    rm -f "$TEMP_OUTPUT"
    tar -C "$BUILD_DIR" --numeric-owner --owner=0 --group=0 \
        -czf "$TEMP_OUTPUT" ./debian-binary ./control.tar.gz ./data.tar.gz
    mv "$TEMP_OUTPUT" "$OUTPUT"

    # Fail before publishing if the outer package or payload is incomplete.
    tar -tzf "$OUTPUT" > "$BUILD_DIR/package-contents"
    grep -q '^\./data\.tar\.gz$' "$BUILD_DIR/package-contents"
    tar -xOzf "$OUTPUT" ./data.tar.gz > "$BUILD_DIR/verify-data.tar.gz"
    tar -tzf "$BUILD_DIR/verify-data.tar.gz" > "$BUILD_DIR/data-contents"
    grep -q '^\./opt/bin/awgmautolist$' "$BUILD_DIR/data-contents"
    grep -q '^\./opt/etc/init.d/S99awgmautolist$' "$BUILD_DIR/data-contents"
    echo "Created: $OUTPUT"
    sha256sum "$OUTPUT"
done
