#!/bin/bash
set -e
export COPYFILE_DISABLE=1

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

# bundle_kmods NAME OUTDIR DEST — модули ядра NAME в IPK: arch-default как
# NAME.ko + SoC-файлы NAME-<soc>.ko той же разрядности/порядка байт.
# Общая раскладка awg_proxy и awgm_relay.
bundle_kmods() {
    local name="$1" outdir="$2" dest="$3"
    local count=0 default

    case "$ENTWARE_ARCH" in
        mipsel-3.4) default="$outdir/${name}-mt7621.ko" ;;
        mips-3.4)   default="$outdir/${name}-mips.ko" ;;
        aarch64-3.10) default="$outdir/${name}-arm64.ko" ;;
    esac
    if [[ -f "$default" ]]; then
        mkdir -p "$dest"
        cp "$default" "$dest/${name}.ko"
        count=$((count + 1))
        echo "Bundled ${name}.ko default ($(basename "$default"))"
    else
        echo "WARNING: $default not found, IPK will have no ${name} module"
    fi

    local ko filetype match koname
    for ko in "$outdir/${name}"-*.ko; do
        [[ -f "$ko" ]] || continue
        case "$(basename "$ko")" in
            "${name}-mips.ko"|"${name}-arm64.ko") continue ;;
        esac
        filetype=$(file -b "$ko")
        match=false
        case "$ENTWARE_ARCH" in
            mipsel-3.4)   [[ "$filetype" == *"LSB"*"MIPS"* ]] && match=true ;;
            mips-3.4)     [[ "$filetype" == *"MSB"*"MIPS"* ]] && match=true ;;
            aarch64-3.10) [[ "$filetype" == *"aarch64"* ]]     && match=true ;;
        esac
        if $match; then
            koname=$(basename "$ko" .ko | sed "s/${name}-//")
            mkdir -p "$dest"
            cp "$ko" "$dest/${name}-${koname}.ko"
            count=$((count + 1))
            echo "Bundled ${name} override: ${koname}"
        fi
    done

    echo "Total ${name} modules bundled: $count"
}

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

    local WARPSCOUT_BIN="c-core/warpscout/out/awgm-warpscout-${AWG_ARCH}"
    if [[ ! -f "$WARPSCOUT_BIN" ]]; then
        echo "ERROR: Missing $WARPSCOUT_BIN"
        echo "Run 'make -C c-core/warpscout cross' before building IPK."
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
    if [[ -n "${AWG_MANAGER_BIN_OVERRIDE:-}" ]]; then
        echo "Using prebuilt backend override: $AWG_MANAGER_BIN_OVERRIDE"
        cp "$AWG_MANAGER_BIN_OVERRIDE" build/bin/awg-manager
        chmod 755 build/bin/awg-manager
    else
        echo "Building backend..."
        VERSION="$VERSION" ENTWARE_ARCH="$ENTWARE_ARCH" "$SCRIPT_DIR/build-backend.sh" "$GO_ARCH"
    fi

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
    cp "$WARPSCOUT_BIN" "$IPK_ROOT/opt/sbin/awgm-warpscout"
    chmod 755 "$IPK_ROOT/opt/sbin/awgm-warpscout"

    # wdtt/freeturn в IPK не кладём: бинари приезжают только с зеркала по пину
    # install.go, с проверкой SHA256. Бандл давал молчаливое расхождение —
    # старый бинарь считался установленным пином и падал на неизвестном флаге.

    local KMOD_VERSION
    KMOD_VERSION=$(sed -n 's/.*ExpectedKmodVersion[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' internal/sys/kmod/download.go | head -n 1)
    if [[ -z "$KMOD_VERSION" ]]; then
        echo "ERROR: ExpectedKmodVersion not found in internal/sys/kmod/download.go"
        exit 1
    fi
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

    local MODULES_DIR="$IPK_ROOT/opt/etc/awg-manager/modules"
    bundle_kmods awg_proxy kmod/awg-proxy/out "$MODULES_DIR"
    bundle_kmods awgm_relay kmod/awgm-relay/out "$MODULES_DIR"

    cp entware/files/etc/init.d/* "$IPK_ROOT/opt/etc/init.d/"

    # FIRC runtime is separately installed and removed by opkg.
    verify_elf_arch() {
        local bin_path="$1"
        local target_arch="$2"
        local magic class endian machine_hex
        magic="$(od -An -tx1 -N4 "$bin_path" | tr -d ' \n')"
        if [[ "$magic" != "7f454c46" ]]; then
            echo "Error: $bin_path is not a valid ELF binary (magic=$magic)" >&2
            return 1
        fi
        class="$(od -An -tu1 -j4 -N1 "$bin_path" | tr -d ' \n')"
        endian="$(od -An -tu1 -j5 -N1 "$bin_path" | tr -d ' \n')"
        machine_hex="$(od -An -tx1 -j18 -N2 "$bin_path" | tr -d ' \n')"
        case "$target_arch" in
            arm64|aarch64|aarch64-*)
                # ELF64 (class=2), LSB (endian=1), EM_AARCH64=183 (0xb7 0x00 in LSB)
                if [[ "$class" != "2" || "$endian" != "1" || "$machine_hex" != "b700" ]]; then
                    echo "Error: $bin_path ELF architecture mismatch for $target_arch (class=$class endian=$endian machine_hex=$machine_hex)" >&2
                    return 1
                fi
                ;;
            mipsle|mipsel|mipsel-*)
                # ELF32 (class=1), LSB (endian=1), EM_MIPS=8 (0x08 0x00 in LSB)
                if [[ "$class" != "1" || "$endian" != "1" || "$machine_hex" != "0800" ]]; then
                    echo "Error: $bin_path ELF architecture mismatch for $target_arch (class=$class endian=$endian machine_hex=$machine_hex)" >&2
                    return 1
                fi
                ;;
            mips|mips-*)
                # ELF32 (class=1), MSB (endian=2), EM_MIPS=8 (0x00 0x08 in MSB)
                if [[ "$class" != "1" || "$endian" != "2" || "$machine_hex" != "0008" ]]; then
                    echo "Error: $bin_path ELF architecture mismatch for $target_arch (class=$class endian=$endian machine_hex=$machine_hex)" >&2
                    return 1
                fi
                ;;
            *)
                echo "Error: unsupported target architecture $target_arch for ELF verification" >&2
                return 1
                ;;
        esac
        return 0
    }
    verify_entware_interp() {
        local bin_path="$1"
        local target_arch="$2"
        local want_interp="/opt/lib/ld.so.1"
        case "$target_arch" in
            aarch64|aarch64-*|arm64)
                want_interp="/opt/lib/ld-linux-aarch64.so.1"
                ;;
        esac
        if command -v readelf >/dev/null 2>&1; then
            local interp
            interp=$(readelf -l "$bin_path" 2>/dev/null | sed -n 's/.*Requesting program interpreter: \(.*\)\].*/\1/p' | head -n 1 | tr -d '[:space:]')
            if [[ -n "$interp" && "$interp" != "$want_interp" ]]; then
                echo "Error: $bin_path has non-Entware PT_INTERP '$interp' (expected '$want_interp')" >&2
                return 1
            fi
        fi
        return 0
    }
    verify_elf_arch "$IPK_ROOT/opt/bin/awg-manager" "$AWG_ARCH" || exit 1
    verify_entware_interp "$IPK_ROOT/opt/bin/awg-manager" "$AWG_ARCH" || exit 1

    local XHTTPD_BIN="${XHTTPD_BIN_OVERRIDE:-}"
    if [[ -z "$XHTTPD_BIN" ]]; then
        if [[ -f "build/cross/awgm-xhttpd-${GO_ARCH}" ]]; then
            XHTTPD_BIN="build/cross/awgm-xhttpd-${GO_ARCH}"
        else
            XHTTPD_BIN="c-core/xhttpd/awgm-xhttpd"
        fi
    fi
    if [[ -f "$XHTTPD_BIN" ]] && verify_elf_arch "$XHTTPD_BIN" "$AWG_ARCH" 2>/dev/null && verify_entware_interp "$XHTTPD_BIN" "$AWG_ARCH"; then
        cp "$XHTTPD_BIN" "$IPK_ROOT/opt/bin/awgm-xhttpd"
        chmod 755 "$IPK_ROOT/opt/bin/awgm-xhttpd"
        ln -sf ../bin/awgm-xhttpd "$IPK_ROOT/opt/sbin/awgm-xhttpd"
        echo "Bundled target-verified awgm-xhttpd (${AWG_ARCH}) from $XHTTPD_BIN"
    else
        echo "WARNING: awgm-xhttpd NOT included in IPK (binary not found at $XHTTPD_BIN or ELF/PT_INTERP mismatch)" >&2
        if [[ "${AWGM_SIDECARS_REQUIRED:-1}" == "1" ]]; then
            echo "Error: AWGM_SIDECARS_REQUIRED=1 but awgm-xhttpd is missing or has wrong architecture/loader" >&2
            exit 1
        fi
    fi

    local FPC_BIN="${FPC_BIN_OVERRIDE:-}"
    if [[ -z "$FPC_BIN" ]]; then
        if [[ -f "build/cross/awgm-fpc-${GO_ARCH}" ]]; then
            FPC_BIN="build/cross/awgm-fpc-${GO_ARCH}"
        else
            FPC_BIN="c-core/fpc/awgm-fpc"
        fi
    fi
    if [[ -f "$FPC_BIN" ]] && verify_elf_arch "$FPC_BIN" "$AWG_ARCH" 2>/dev/null && verify_entware_interp "$FPC_BIN" "$AWG_ARCH"; then
        cp "$FPC_BIN" "$IPK_ROOT/opt/bin/awgm-fpc"
        chmod 755 "$IPK_ROOT/opt/bin/awgm-fpc"
        ln -sf ../bin/awgm-fpc "$IPK_ROOT/opt/sbin/awgm-fpc"
        echo "Bundled target-verified awgm-fpc (${AWG_ARCH}) from $FPC_BIN"
    else
        echo "WARNING: awgm-fpc NOT included in IPK (binary not found at $FPC_BIN or ELF/PT_INTERP mismatch)" >&2
        if [[ "${AWGM_SIDECARS_REQUIRED:-1}" == "1" ]]; then
            echo "Error: AWGM_SIDECARS_REQUIRED=1 but awgm-fpc is missing or has wrong architecture/loader" >&2
            exit 1
        fi
    fi

    for hook in iflayerchanged ifcreated ifdestroyed ifipchanged; do
        cp internal/ndms/events/hook-script.sh \
            "$IPK_ROOT/opt/etc/ndm/${hook}.d/50-awg-manager.sh"
    done

    local CRYPTO_DEPENDENCY=""
    if [[ "$GO_ARCH" == mips || "$GO_ARCH" == mipsle ]]; then
        CRYPTO_DEPENDENCY=", libopenssl"
    fi

    cat > "$IPK_ROOT/CONTROL/control" << EOF
Package: awg-manager
Version: ${VERSION}
Depends: iptables, ipset, ip-full, wireguard-tools, conntrack, curl${CRYPTO_DEPENDENCY}
Section: net
Architecture: ${PKG_ARCH}
Maintainer: hoaxisr
Description: AmneziaWG tunnel manager with web interface
 Simple web interface for managing AmneziaWG VPN tunnels on Keenetic routers.
 Supports creating, configuring, and testing tunnels.
 Includes bundled kernel modules.
EOF

    # Runtime configuration belongs exclusively to awgm-route.
    : > "$IPK_ROOT/CONTROL/conffiles"

    cp entware/control/postinst "$IPK_ROOT/CONTROL/"
    cp entware/control/prerm "$IPK_ROOT/CONTROL/"
    chmod 755 "$IPK_ROOT/CONTROL/postinst"
    chmod 755 "$IPK_ROOT/CONTROL/prerm"

    echo ""
    echo "Creating IPK package..."

    local IPK_DIR="$PROJECT_ROOT/build/ipk"

    echo "2.0" > "$IPK_DIR/debian-binary"

    cd "$IPK_DIR/CONTROL"
    local CONTROL_FILES="control conffiles postinst prerm"
    tar --format=ustar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/control.tar.gz" \
        $CONTROL_FILES

    cd "$IPK_DIR"
    tar --format=ustar --numeric-owner --owner=0 --group=0 -czf "$IPK_DIR/data.tar.gz" \
        ./opt

    cd "$IPK_DIR"
    rm -f "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk"
    tar --format=ustar --numeric-owner --owner=0 --group=0 -czf "$PROJECT_ROOT/dist/awg-manager_${VERSION}_${PKG_ARCH}-kn.ipk" \
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
