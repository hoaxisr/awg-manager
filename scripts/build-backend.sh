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

echo "Building awg-manager $VERSION for $ARCH..."

case "$ARCH" in
    mipsle|mipsel)
        GOOS=linux GOARCH=mipsle GOMIPS=softfloat CGO_ENABLED=0 \
            go build -ldflags="-s -w -X main.version=${VERSION}" \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    mips)
        GOOS=linux GOARCH=mips GOMIPS=softfloat CGO_ENABLED=0 \
            go build -ldflags="-s -w -X main.version=${VERSION}" \
            -o build/bin/awg-manager ./cmd/awg-manager
        ;;
    arm64|aarch64)
        GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
            go build -ldflags="-s -w -X main.version=${VERSION}" \
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
