#!/bin/bash
# Cross-build patched free-turn-proxy (third_party/) for Keenetic arches.
# Outputs: prebuilt/freeturn/client-linux-* and server-linux-*
#
# Usage:
#   ./scripts/build-freeturn-client.sh           # all arches
#   ./scripts/build-freeturn-client.sh arm64     # one arch
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
FTP="$ROOT/third_party/free-turn-proxy"
OUT="$ROOT/prebuilt/freeturn"
VERSION="1.8.0-awg.4"
LDFLAGS="-X main.version=${VERSION} -s -w"

mkdir -p "$OUT"
cd "$FTP"

build_arch() {
	local goarch="$1"
	local suffix="$2"
	echo "==> building linux/${goarch} -> ${suffix}"
	GOTOOLCHAIN=local GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags "$LDFLAGS" \
		-o "$OUT/client-linux-${suffix}" ./cmd/client
	GOTOOLCHAIN=local GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 go build -ldflags "$LDFLAGS" \
		-o "$OUT/server-linux-${suffix}" ./cmd/server
}

TARGET="${1:-all}"
case "$TARGET" in
	all)
		build_arch arm64 arm64
		build_arch mipsle mipsle-softfloat
		build_arch mips mips-softfloat
		;;
	arm64|aarch64)
		build_arch arm64 arm64
		;;
	mipsel|mipsle)
		build_arch mipsle mipsle-softfloat
		;;
	mips)
		build_arch mips mips-softfloat
		;;
	*)
		echo "Unknown arch: $TARGET (use all|arm64|mipsel|mips)"
		exit 1
		;;
esac

echo ""
echo "Built freeturn ${VERSION} into ${OUT}/"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$OUT"/*
elif command -v shasum >/dev/null 2>&1; then
	shasum -a 256 "$OUT"/*
fi
