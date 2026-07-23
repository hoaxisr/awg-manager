#!/bin/bash
# Cross-build WDTT headless client (third_party/wdtt-client) for Keenetic arches.
#
# Usage:
#   ./scripts/build-wdtt-client.sh           # all arches
#   ./scripts/build-wdtt-client.sh arm64     # one arch
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(dirname "$SCRIPT_DIR")"
SRC="$ROOT/third_party/wdtt-client"
OUT="$ROOT/prebuilt/wdtt"
VERSION="1.0.0-awg.1"
LDFLAGS="-X main.version=${VERSION} -s -w"

if [[ ! -f "$SRC/main.go" ]]; then
	echo "ERROR: $SRC/main.go not found."
	echo "Populate third_party/wdtt-client — see third_party/wdtt-client/README.md"
	exit 1
fi

mkdir -p "$OUT"
cd "$SRC"

build_arch() {
	local goarch="$1"
	local suffix="$2"
	local extra_env="${3:-}"
	echo "==> building linux/${goarch} -> ${suffix}"
	# proxy.golang.org may fail TLS on some Windows/WSL setups — goproxy.io fallback.
	export GOPROXY="${GOPROXY:-https://goproxy.io,https://proxy.golang.org,direct}"
	# shellcheck disable=SC2086
	GOTOOLCHAIN=local GOOS=linux GOARCH="$goarch" CGO_ENABLED=0 env $extra_env \
		go build -ldflags "$LDFLAGS" -o "$OUT/client-linux-${suffix}" .
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
echo "Built wdtt-client ${VERSION} into ${OUT}/"
if command -v sha256sum >/dev/null 2>&1; then
	sha256sum "$OUT"/*
elif command -v shasum >/dev/null 2>&1; then
	shasum -a 256 "$OUT"/*
fi
