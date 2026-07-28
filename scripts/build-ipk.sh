#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

ALL_ARCHES=(mipsel-3.4 mips-3.4 aarch64-3.10)

# Read version from VERSION file
VERSION=$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null || echo "0.1.0")

# Usage:
#   ./scripts/build-ipk.sh                         → all arches, VERSION from file
#   ./scripts/build-ipk.sh <arch>                  → one arch
#   ./scripts/build-ipk.sh <version>               → all arches
#   ./scripts/build-ipk.sh <version> <arch|all>    → one arch or all
#   ./scripts/build-ipk.sh all                     → all arches
BUILD_ALL=0
ENTWARE_ARCH=""

if [[ $# -eq 0 ]]; then
    BUILD_ALL=1
elif [[ "$1" =~ ^[0-9]+\.[0-9]+\.[0-9]+ ]]; then
    VERSION="$1"
    if [[ -z "${2:-}" || "$2" == "all" ]]; then
        BUILD_ALL=1
    else
        ENTWARE_ARCH="$2"
    fi
elif [[ "$1" == "all" ]]; then
    BUILD_ALL=1
else
    ENTWARE_ARCH="$1"
fi

build_ipk_one() {
    local ENTWARE_ARCH="$1"

    echo ""
    echo "========================================"
    echo "Building awg-manager IPK package"
    echo "Version: $VERSION"
    echo "Architecture: $ENTWARE_ARCH"
    echo "========================================"

    local GO_ARCH PKG_ARCH AWG_ARCH KMOD_ARCH
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

    local AWG_CLI_BIN="prebuilt/bin/awg-${AWG_ARCH}"
    if [[ ! -f "$AWG_CLI_BIN" ]]; then
        echo "ERROR: Missing $AWG_CLI_BIN"
        echo "Please place awg CLI binary for ${AWG_ARCH} architecture in prebuilt/bin/"
        exit 1
    fi

    # Kernel modules are bundled per-model from prebuilt/kmod/.
    # At runtime, the daemon selects the correct .ko for the detected router model.

    rm -rf build/ipk build/bin
    mkdir -p build/ipk build/bin dist

    if [[ "${SKIP_FRONTEND_BUILD:-0}" == "1" ]]; then
        if [[ ! -f "$PROJECT_ROOT/frontend/build/index.html.gz" && ! -f "$PROJECT_ROOT/frontend/build/index.html" ]]; then
            echo "ERROR: SKIP_FRONTEND_BUILD=1 but frontend/build/index.html(.gz) is missing"
            exit 1
        fi
        echo "Using existing frontend build: frontend/build/"
    else
        echo "Building frontend..."
        "$SCRIPT_DIR/build-frontend.sh"
    fi

    echo ""
    echo "Building backend..."
    VERSION="$VERSION" ENTWARE_ARCH="$ENTWARE_ARCH" "$SCRIPT_DIR/build-backend.sh" "$GO_ARCH"

    local IPK_ROOT="build/ipk"
    mkdir -p "$IPK_ROOT/CONTROL"
    mkdir -p "$IPK_ROOT/opt/bin"
    mkdir -p "$IPK_ROOT/opt/sbin"
    mkdir -p "$IPK_ROOT/opt/etc/init.d"
    mkdir -p "$IPK_ROOT/opt/etc/awg-manager"
    for hook in iflayerchanged ifcreated ifdestroyed ifipchanged; do
        mkdir -p "$IPK_ROOT/opt/etc/ndm/${hook}.d"
    done

    cp build/bin/awg-manager "$IPK_ROOT/opt/bin/"
    cp "$AWG_CLI_BIN" "$IPK_ROOT/opt/sbin/awg"
    chmod +x "$IPK_ROOT/opt/sbin/awg"

    local KMOD_VERSION
    KMOD_VERSION=$(grep 'ExpectedKmodVersion' internal/sys/kmod/download.go | grep -oP '"[^"]+"' | tr -d '"')
    local BUNDLED_DIR="$IPK_ROOT/opt/etc/awg-manager/modules/bundled"
    local KMOD_COUNT=0

    if ls "$PROJECT_ROOT/prebuilt/kmod"/amneziawg-*.ko &>/dev/null; then
        mkdir -p "$BUNDLED_DIR"
        for ko in "$PROJECT_ROOT/prebuilt/kmod"/amneziawg-*.ko; do
            local filetype
            filetype=$(file -b "$ko")
            local match=false
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

    local AWG_PROXY_DIR="$IPK_ROOT/opt/etc/awg-manager/modules"
    local AWG_PROXY_COUNT=0
    local AWG_PROXY_DEFAULT

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

    for EXTRA_KO in kmod/awg-proxy/out/awg_proxy-*.ko; do
        [[ -f "$EXTRA_KO" ]] || continue
        case "$(basename "$EXTRA_KO")" in
            awg_proxy-mips.ko|awg_proxy-arm64.ko) continue ;;
        esac
        local filetype
        filetype=$(file -b "$EXTRA_KO")
        local match=false
        case "$ENTWARE_ARCH" in
            mipsel-3.4)   [[ "$filetype" == *"LSB"*"MIPS"* ]] && match=true ;;
            mips-3.4)     [[ "$filetype" == *"MSB"*"MIPS"* ]] && match=true ;;
            aarch64-3.10) [[ "$filetype" == *"aarch64"* ]]     && match=true ;;
        esac
        if $match; then
            local KONAME
            KONAME=$(basename "$EXTRA_KO" .ko | sed 's/awg_proxy-//')
            mkdir -p "$AWG_PROXY_DIR"
            cp "$EXTRA_KO" "$AWG_PROXY_DIR/awg_proxy-${KONAME}.ko"
            AWG_PROXY_COUNT=$((AWG_PROXY_COUNT + 1))
            echo "Bundled awg_proxy override: ${KONAME}"
        fi
    done

    echo "Total awg_proxy modules bundled: $AWG_PROXY_COUNT"

    cp entware/files/etc/init.d/* "$IPK_ROOT/opt/etc/init.d/"

    for hook in iflayerchanged ifcreated ifdestroyed ifipchanged; do
        cp internal/ndms/events/hook-script.sh \
            "$IPK_ROOT/opt/etc/ndm/${hook}.d/50-awg-manager.sh"
    done

    cat > "$IPK_ROOT/CONTROL/control" << EOF
Package: awg-manager
Version: ${VERSION}
Depends: iptables, ip-full, wireguard-tools
Section: net
Architecture: ${PKG_ARCH}
Maintainer: hoaxisr
Description: AmneziaWG tunnel manager with web interface
 Simple web interface for managing AmneziaWG VPN tunnels on Keenetic routers.
 Supports creating, configuring, and testing tunnels.
 Includes bundled kernel modules.
EOF

    cp entware/control/postinst "$IPK_ROOT/CONTROL/"
    cp entware/control/prerm "$IPK_ROOT/CONTROL/"
    chmod 755 "$IPK_ROOT/CONTROL/postinst"
    chmod 755 "$IPK_ROOT/CONTROL/prerm"

    echo ""
    echo "Creating IPK package..."

    local IPK_DIR="$PROJECT_ROOT/build/ipk"

    echo "2.0" > "$IPK_DIR/debian-binary"

    cd "$IPK_DIR/CONTROL"
    tar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/control.tar.gz" \
        control postinst prerm

    cd "$IPK_DIR"
    tar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/data.tar.gz" \
        ./opt

    cd "$IPK_DIR"
    rm -f "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk"
    tar --numeric-owner --owner=0 --group=0 -czf "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk" \
        ./debian-binary ./data.tar.gz ./control.tar.gz

    rm -f "$IPK_DIR/debian-binary" "$IPK_DIR/control.tar.gz" "$IPK_DIR/data.tar.gz"

    echo ""
    echo "IPK package created: dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk"
}

if [[ "$BUILD_ALL" -eq 1 ]]; then
    echo "Building awg-manager IPK for all architectures"
    echo "Version: $VERSION"
    echo "Architectures: ${ALL_ARCHES[*]}"
    first=1
    for arch in "${ALL_ARCHES[@]}"; do
        if [[ $first -eq 1 ]]; then
            first=0
            build_ipk_one "$arch"
        else
            SKIP_FRONTEND_BUILD=1 build_ipk_one "$arch"
        fi
    done
    echo ""
    echo "All IPK packages:"
    ls -la "$PROJECT_ROOT/dist/"*.ipk
else
    build_ipk_one "$ENTWARE_ARCH"
    ls -la "$PROJECT_ROOT/dist/"*.ipk
fi
