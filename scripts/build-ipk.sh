#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Read version from VERSION file
VERSION=$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null || echo "0.1.0")

# Parse arguments
if [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    VERSION="$1"
    ENTWARE_ARCH="${2:-mipsel-3.4}"
else
    ENTWARE_ARCH="${1:-mipsel-3.4}"
fi

echo "Building awg-manager IPK package"
echo "Version: $VERSION"
echo "Architecture: $ENTWARE_ARCH"

# Extract Go arch from entware arch
case "$ENTWARE_ARCH" in
    mipsel-*)
        GO_ARCH="mipsle"
        PKG_ARCH="$ENTWARE_ARCH"
        AWG_ARCH="mipsle"
        KMOD_ARCH="mipsel"
        ;;
    mips-*)
        GO_ARCH="mips"
        PKG_ARCH="$ENTWARE_ARCH"
        AWG_ARCH="mips"
        KMOD_ARCH="mips"
        ;;
    aarch64-*)
        GO_ARCH="arm64"
        PKG_ARCH="$ENTWARE_ARCH"
        AWG_ARCH="arm64"
        KMOD_ARCH="arm64"
        ;;
    *)
        echo "Unknown architecture: $ENTWARE_ARCH"
        exit 1
        ;;
esac

cd "$PROJECT_ROOT"

# Check for amneziawg binaries
AWG_CLI_BIN="prebuilt/bin/awg-${AWG_ARCH}"

if [[ ! -f "$AWG_CLI_BIN" ]]; then
    echo "ERROR: Missing $AWG_CLI_BIN"
    echo "Please place awg CLI binary for ${AWG_ARCH} architecture in prebuilt/bin/"
    exit 1
fi

# Kernel modules are bundled per-model from prebuilt/kmod/.
# At runtime, the daemon selects the correct .ko for the detected router model.

# Clean previous builds
rm -rf build/ipk build/www build/bin
mkdir -p build/ipk build/www build/bin dist

# Build backend (export VERSION so build-backend.sh uses it)
echo ""
echo "Building backend..."
VERSION="$VERSION" "$SCRIPT_DIR/build-backend.sh" "$GO_ARCH"

# Build frontend
echo "Building frontend..."
"$SCRIPT_DIR/build-frontend.sh"

# Copy frontend to build/www
cp -r frontend/build/* build/www/

# Create IPK structure
IPK_ROOT="build/ipk"
mkdir -p "$IPK_ROOT/CONTROL"
mkdir -p "$IPK_ROOT/opt/bin"
mkdir -p "$IPK_ROOT/opt/sbin"
mkdir -p "$IPK_ROOT/opt/share/www/awg-manager"
mkdir -p "$IPK_ROOT/opt/etc/init.d"
mkdir -p "$IPK_ROOT/opt/etc/awg-manager"
for hook in iflayerchanged ifcreated ifdestroyed ifipchanged; do
    mkdir -p "$IPK_ROOT/opt/etc/ndm/${hook}.d"
done

# Copy binaries
cp build/bin/awg-manager "$IPK_ROOT/opt/bin/"
cp "$AWG_CLI_BIN" "$IPK_ROOT/opt/sbin/awg"
chmod +x "$IPK_ROOT/opt/sbin/awg"

# Bundle kernel modules (filtered by architecture)
KMOD_VERSION=$(grep 'ExpectedKmodVersion' internal/sys/kmod/download.go | grep -oP '"[^"]+"' | tr -d '"')
BUNDLED_DIR="$IPK_ROOT/opt/etc/awg-manager/modules/bundled"
KMOD_COUNT=0

if ls "$PROJECT_ROOT/prebuilt/kmod"/amneziawg-*.ko &>/dev/null; then
    mkdir -p "$BUNDLED_DIR"
    for ko in "$PROJECT_ROOT/prebuilt/kmod"/amneziawg-*.ko; do
        filetype=$(file -b "$ko")
        match=false
        case "$ENTWARE_ARCH" in
            mipsel-3.4)   [[ "$filetype" == *"LSB"*"MIPS"* ]] && match=true ;;
            mips-3.4)     [[ "$filetype" == *"MSB"*"MIPS"* ]] && match=true ;;
            aarch64-3.10) [[ "$filetype" == *"aarch64"* ]]     && match=true ;;
        esac
        if $match; then
            cp "$ko" "$BUNDLED_DIR/"
            KMOD_COUNT=$((KMOD_COUNT + 1))
        fi
    done
    if [[ $KMOD_COUNT -gt 0 ]]; then
        echo "$KMOD_VERSION" > "$BUNDLED_DIR/version"
        echo "Bundled $KMOD_COUNT kernel modules (kmod $KMOD_VERSION) for $ENTWARE_ARCH"
    else
        echo "WARNING: No kernel modules matched architecture $ENTWARE_ARCH"
        rmdir "$BUNDLED_DIR" 2>/dev/null || true
    fi
else
    echo "WARNING: No prebuilt/kmod/*.ko files found, IPK will have no bundled modules"
fi

# Bundle awg_proxy.ko kernel modules (NativeWG obfuscation proxy)
# Modules are installed to /opt/etc/awg-manager/modules/ alongside amneziawg.ko.
# For mipsel: SoC-specific (mt7621, mt7628) + per-model (KN-1011 HIGHMEM).
# For mips/arm64: single arch default copied as awg_proxy.ko.
AWG_PROXY_DIR="$IPK_ROOT/opt/etc/awg-manager/modules"
AWG_PROXY_COUNT=0

# 1. Arch default (awg_proxy.ko) — for mips/arm64 fallback, for mipsel = copy of mt7621
case "$ENTWARE_ARCH" in
    mipsel-3.4) AWG_PROXY_DEFAULT="kmod/awg-proxy/out/awg_proxy-mt7621.ko" ;;
    mips-3.4)   AWG_PROXY_DEFAULT="kmod/awg-proxy/out/awg_proxy-mips.ko" ;;
    aarch64-3.10) AWG_PROXY_DEFAULT="kmod/awg-proxy/out/awg_proxy-arm64.ko" ;;
esac
if [[ -f "$AWG_PROXY_DEFAULT" ]]; then
    mkdir -p "$AWG_PROXY_DIR"
    cp "$AWG_PROXY_DEFAULT" "$AWG_PROXY_DIR/awg_proxy.ko"
    AWG_PROXY_COUNT=$((AWG_PROXY_COUNT + 1))
    echo "Bundled awg_proxy.ko default ($(basename "$AWG_PROXY_DEFAULT"))"
else
    echo "WARNING: $AWG_PROXY_DEFAULT not found, IPK will have no awg_proxy module"
fi

# 2. SoC-specific and per-model overrides (filtered by ELF architecture)
for EXTRA_KO in kmod/awg-proxy/out/awg_proxy-*.ko; do
    [[ -f "$EXTRA_KO" ]] || continue
    # Skip arch defaults (already handled above)
    case "$(basename "$EXTRA_KO")" in
        awg_proxy-mips.ko|awg_proxy-arm64.ko) continue ;;
    esac
    filetype=$(file -b "$EXTRA_KO")
    match=false
    case "$ENTWARE_ARCH" in
        mipsel-3.4)   [[ "$filetype" == *"LSB"*"MIPS"* ]] && match=true ;;
        mips-3.4)     [[ "$filetype" == *"MSB"*"MIPS"* ]] && match=true ;;
        aarch64-3.10) [[ "$filetype" == *"aarch64"* ]]     && match=true ;;
    esac
    if $match; then
        KONAME=$(basename "$EXTRA_KO" .ko | sed 's/awg_proxy-//')
        mkdir -p "$AWG_PROXY_DIR"
        cp "$EXTRA_KO" "$AWG_PROXY_DIR/awg_proxy-${KONAME}.ko"
        AWG_PROXY_COUNT=$((AWG_PROXY_COUNT + 1))
        echo "Bundled awg_proxy override: ${KONAME}"
    fi
done

echo "Total awg_proxy modules bundled: $AWG_PROXY_COUNT"

# Copy web files
cp -r build/www/* "$IPK_ROOT/opt/share/www/awg-manager/"

# Copy init script (lighttpd config is generated dynamically)
cp entware/files/etc/init.d/* "$IPK_ROOT/opt/etc/init.d/"

# Copy unified hook script into every NDMS hook directory we use.
# Source file is internal/ndms/events/hook-script.sh (also used as
# //go:embed for the Installer component at runtime).
for hook in iflayerchanged ifcreated ifdestroyed ifipchanged; do
    cp internal/ndms/events/hook-script.sh \
        "$IPK_ROOT/opt/etc/ndm/${hook}.d/50-awg-manager.sh"
done

# Generate control file
cat > "$IPK_ROOT/CONTROL/control" << EOF
Package: awg-manager
Version: ${VERSION}
Depends: curl, iptables, ip-full
Section: net
Architecture: ${PKG_ARCH}
Maintainer: hoaxisr
Description: AmneziaWG tunnel manager with web interface
 Simple web interface for managing AmneziaWG VPN tunnels on Keenetic routers.
 Supports creating, configuring, and testing tunnels.
 Includes bundled kernel modules.
EOF

# Copy control scripts
cp entware/control/postinst "$IPK_ROOT/CONTROL/"
cp entware/control/prerm "$IPK_ROOT/CONTROL/"
chmod 755 "$IPK_ROOT/CONTROL/postinst"
chmod 755 "$IPK_ROOT/CONTROL/prerm"

# Build IPK
echo ""
echo "Creating IPK package..."

IPK_DIR="$PROJECT_ROOT/build/ipk"

# debian-binary
echo "2.0" > "$IPK_DIR/debian-binary"

# control.tar.gz - without ./ prefix
cd "$IPK_DIR/CONTROL"
tar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/control.tar.gz" \
    control postinst prerm

# data.tar.gz - with ./opt prefix
cd "$IPK_DIR"
tar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/data.tar.gz" \
    ./opt

# IPK as gzip tar archive (Entware format)
cd "$IPK_DIR"
rm -f "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk"
tar --numeric-owner --owner=0 --group=0 -czf "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk" \
    ./debian-binary ./data.tar.gz ./control.tar.gz

# Cleanup
rm -f "$IPK_DIR/debian-binary" "$IPK_DIR/control.tar.gz" "$IPK_DIR/data.tar.gz"

echo ""
echo "IPK package created: dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk"
ls -la "$PROJECT_ROOT/dist/"*.ipk
