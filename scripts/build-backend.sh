#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Use VERSION env if set, otherwise read from VERSION file
VERSION="${VERSION:-$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null || echo "dev")}"

# ENTWARE_ARCH carries the awg-manager arch key (e.g. "mipsel-3.4").
# Set by build-ipk.sh; defaults to empty for direct invocations.
ENTWARE_ARCH="${ENTWARE_ARCH:-}"

# Architecture (default: mipsle for MT7621)
ARCH="${1:-mipsle}"

cd "$PROJECT_ROOT"

if [[ ! -f "$PROJECT_ROOT/frontend/build/index.html.gz" && ! -f "$PROJECT_ROOT/frontend/build/index.html" ]]; then
    echo "ERROR: Missing frontend/build/index.html(.gz)"
    echo "Run scripts/build-frontend.sh first."
    exit 1
fi

mkdir -p build/bin

# All targets build with the system Go toolchain. The old Go 1.23 pin for
# MIPS assumed Keenetic routers run kernel 3.4 and Go 1.24+ needs >= 3.17;
# both premises are false — the fleet runs kernel 4.9-ndm (all five Keenetic
# kernel-ABI groups), and Go 1.24+ officially requires only kernel 3.2+.
# sing-box for the same routers is already built with the system Go.
GO_CMD="go"

# vendor/ contains Go module sources for offline builds (see vendor/modules.txt).
# Use GO_MOD=vendor for air-gapped or flaky-network builds; default is mod.
GO_MOD="${GO_MOD:-mod}"
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=${GO_MOD}"

echo "Building awg-manager $VERSION for $ARCH ($($GO_CMD version))..."

case "$ARCH" in
    mipsle|mipsel)
        GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION} -X main.buildArch=${ENTWARE_ARCH}" \
            -tags embed_frontend \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    mips)
        GOOS=linux GOARCH=mips GOMIPS=softfloat CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION} -X main.buildArch=${ENTWARE_ARCH}" \
            -tags embed_frontend \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    arm64|aarch64)
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION} -X main.buildArch=${ENTWARE_ARCH}" \
            -tags embed_frontend \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    *)
        echo "Unknown architecture: $ARCH"
        echo "Supported: mipsle, mips, arm64"
        exit 1
        ;;
esac

echo "Backend build complete: build/bin/awg-manager"
ls -la build/bin/awg-manager
