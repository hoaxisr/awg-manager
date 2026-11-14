#!/bin/bash
# Build clean opkg packages for the supported Keenetic Entware architectures.
set -euo pipefail
export COPYFILE_DISABLE=1

MODULE_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$MODULE_DIR/.." && pwd)"
OUTDIR="${OUTDIR:-$PROJECT_ROOT/dist}"
# Satellite ships with the panel version: opkg upgrades only to a higher
# version, and the satellite/panel API pair is released together.
VERSION="${VERSION:-$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null)}"
TARGET="${1:-all}"
if [[ ! "$VERSION" =~ ^[A-Za-z0-9.+_:-]+$ ]]; then
    echo "ERROR: invalid or missing version" >&2
    exit 1
fi
case "$TARGET" in
    all) ARCHES=(mipsel-3.4 mips-3.4 aarch64-3.10) ;;
    mipsel-3.4|mips-3.4|aarch64-3.10) ARCHES=("$TARGET") ;;
    *) echo "Usage: $0 [all|aarch64-3.10|mipsel-3.4|mips-3.4]" >&2; exit 1 ;;
esac
for cmd in go tar sed; do
    command -v "$cmd" >/dev/null 2>&1 || { echo "ERROR: $cmd not found" >&2; exit 1; }
done
if [[ ! -f "$MODULE_DIR/vendor/modules.txt" ]]; then
    echo "ERROR: vendor/modules.txt is missing; run go mod vendor" >&2
    exit 1
fi
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
    if [[ "$GOARCH" == arm64 ]]; then
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -C "$MODULE_DIR" -mod=vendor -trimpath \
            -ldflags="-s -w -X github.com/hoaxisr/awg-manager/awgmautolist/internal/buildinfo.Version=$VERSION" -o "$PKG_ROOT/opt/bin/awgmautolist" ./cmd/awgmautolist
    else
        GOOS=linux GOARCH="$GOARCH" GOMIPS=softfloat CGO_ENABLED=0 go build -C "$MODULE_DIR" -mod=vendor -trimpath \
            -ldflags="-s -w -X github.com/hoaxisr/awg-manager/awgmautolist/internal/buildinfo.Version=$VERSION" -o "$PKG_ROOT/opt/bin/awgmautolist" ./cmd/awgmautolist
    fi
    cp "$MODULE_DIR/entware/S99awgmautolist" "$PKG_ROOT/opt/etc/init.d/S99awgmautolist"
    cp "$MODULE_DIR/ipk/CONTROL/postinst" "$MODULE_DIR/ipk/CONTROL/prerm" "$MODULE_DIR/ipk/CONTROL/postrm" "$PKG_ROOT/CONTROL/"
    sed -e "s/^Version:.*/Version: $VERSION/" -e "s/^Architecture:.*/Architecture: $PKG_ARCH/" \
        "$MODULE_DIR/ipk/CONTROL/control" > "$PKG_ROOT/CONTROL/control"
    chmod 755 "$PKG_ROOT/opt/bin/awgmautolist" "$PKG_ROOT/opt/etc/init.d/S99awgmautolist" \
        "$PKG_ROOT/CONTROL/postinst" "$PKG_ROOT/CONTROL/prerm" "$PKG_ROOT/CONTROL/postrm"
    chmod 644 "$PKG_ROOT/CONTROL/control"
    printf '2.0\n' > "$BUILD_DIR/debian-binary"
    tar --format=ustar -C "$PKG_ROOT/CONTROL" --numeric-owner --owner=0 --group=0 \
        -czf "$BUILD_DIR/control.tar.gz" control postinst prerm postrm
    tar --format=ustar -C "$PKG_ROOT" --numeric-owner --owner=0 --group=0 \
        -czf "$BUILD_DIR/data.tar.gz" ./opt
    OUTPUT="$OUTDIR/awgmautolist_${VERSION}_${PKG_ARCH}-kn.ipk"
    TEMP_OUTPUT="$OUTDIR/.awgmautolist_${VERSION}_${PKG_ARCH}-kn.ipk.tmp"
    rm -f "$TEMP_OUTPUT"
    tar --format=ustar -C "$BUILD_DIR" --numeric-owner --owner=0 --group=0 \
        -czf "$TEMP_OUTPUT" ./debian-binary ./control.tar.gz ./data.tar.gz
    mv "$TEMP_OUTPUT" "$OUTPUT"
    tar -tzf "$OUTPUT" > "$BUILD_DIR/package-contents"
    grep -q '^\./data\.tar\.gz$' "$BUILD_DIR/package-contents"
    tar -xOzf "$OUTPUT" ./control.tar.gz > "$BUILD_DIR/verify-control.tar.gz"
    tar -tzf "$BUILD_DIR/verify-control.tar.gz" > "$BUILD_DIR/control-contents"
    tar -xOzf "$OUTPUT" ./data.tar.gz > "$BUILD_DIR/verify-data.tar.gz"
    tar -tzf "$BUILD_DIR/verify-data.tar.gz" > "$BUILD_DIR/data-contents"
    grep -q '^\./opt/bin/awgmautolist$' "$BUILD_DIR/data-contents"
    grep -q '^\./opt/etc/init.d/S99awgmautolist$' "$BUILD_DIR/data-contents"
    if grep -Eq '(^|/)\._' "$BUILD_DIR/package-contents" "$BUILD_DIR/control-contents" "$BUILD_DIR/data-contents"; then
        echo "ERROR: AppleDouble entry in $OUTPUT" >&2
        exit 1
    fi
    echo "Created: $OUTPUT"
    sha256sum "$OUTPUT"
done
