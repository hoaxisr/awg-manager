#!/bin/bash
# Сборка wt-client (qWDTT / SpaceNeuroX go_client) для Keenetic.
# Raw-клиент (-mode rawtun): contrib/wdtt-client-patch (см. BUILD.md).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PATCH_DIR="$PROJECT_ROOT/contrib/wdtt-client-patch"
OUT_DIR="$PROJECT_ROOT/build/wdtt"
TAG="${WDTT_CLIENT_TAG:-v1.4.0}"
REPO="${WDTT_CLIENT_REPO:-https://github.com/SpaceNeuroX/proxy-turn-vk-android.git}"
VERSION="$(grep 'const PinnedClientVersion' "$PROJECT_ROOT/internal/wdtt/install.go" | sed -n 's/.*"\([^"]*\)".*/\1/p')"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$OUT_DIR"

echo "Building wt-client ${VERSION} from ${REPO}@${TAG} + awg-manager rawtun patch..."

git clone --depth 1 --branch "$TAG" "$REPO" "$WORK/src"
cd "$WORK/src/go_client"

python3 "$PATCH_DIR/apply-main-patch.py" main.go
for f in protocol_raw.go raw_session.go raw_tun.go tun_fd_linux.go tun_fd_stub.go group_raw.go main_rawtun.go flags_compat.go dispatcher.go; do
  cp "$PATCH_DIR/$f" .
done
# dispatcher.go — flow-hash uplink для rawtun (NewRawTunDispatcher); downlink chunk RR=8 как upstream.

echo "  go mod tidy (go.sum отсутствует в upstream tag)..."
export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
go mod tidy

build_one() {
  local goarch="$1" out="$2"
  echo "  GOARCH=${goarch} -> ${out}"
  CGO_ENABLED=0 GOOS=linux GOARCH="$goarch" go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/$out" .
}

build_one arm64 wt-client-linux-arm64
# softfloat ABI for Entware mipsel/mips routers
build_one mipsle wt-client-linux-mipsle-softfloat
build_one mips wt-client-linux-mips-softfloat

echo
echo "SHA256 / size (вписать в internal/wdtt/install.go):"
for f in wt-client-linux-arm64 wt-client-linux-mipsle-softfloat wt-client-linux-mips-softfloat; do
  sha256sum "$OUT_DIR/$f"
  stat -c '%n %s bytes' "$OUT_DIR/$f" 2>/dev/null || stat -f '%N %z bytes' "$OUT_DIR/$f"
done

echo
echo "Upload:"
echo "  scp $OUT_DIR/wt-client-linux-* awgm-server:/var/www/entware-repo/wt/${VERSION}/"
