#!/bin/bash
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

# Use VERSION env if set, otherwise read from VERSION file
VERSION="${VERSION:-$(cat "$PROJECT_ROOT/VERSION" 2>/dev/null || echo "dev")}"

# Architecture (default: mipsle for MT7621)
ARCH="${1:-mipsle}"

cd "$PROJECT_ROOT"

mkdir -p build/bin

# MIPS targets run on Keenetic routers with Linux kernel 3.4.
# Go 1.24+ requires kernel >= 3.17 (spinbit mutex uses futex ops unavailable on 3.4).
# Use Go 1.23.x for MIPS builds; system Go for everything else.
GO_CMD="go"
case "$ARCH" in
    mipsle|mipsel|mips)
        MIPS_GO="${MIPS_GO:-go1.23.12}"
        if command -v "$MIPS_GO" &>/dev/null; then
            GO_CMD="$MIPS_GO"
        elif [[ -x "$(go env GOPATH)/bin/$MIPS_GO" ]]; then
            GO_CMD="$(go env GOPATH)/bin/$MIPS_GO"
        else
            echo "WARNING: $MIPS_GO not found, falling back to system go ($(go version))"
            echo "  MIPS targets need Go 1.23.x — install with: go install golang.org/dl/${MIPS_GO}@latest && ${MIPS_GO} download"
        fi
        ;;
esac

# vendor/ contains pre-built binaries and kernel modules (not Go vendor).
# Use -mod=mod so Go ignores it and fetches deps from go.sum.
export GOFLAGS="${GOFLAGS:+$GOFLAGS }-mod=mod"

echo "Building awg-manager $VERSION for $ARCH ($($GO_CMD version))..."

case "$ARCH" in
    mipsle|mipsel)
        GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION}" \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    mips)
        GOOS=linux GOARCH=mips GOMIPS=softfloat CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION}" \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    arm64|aarch64)
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
            $GO_CMD build -ldflags="-s -w -X main.version=${VERSION}" \
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
