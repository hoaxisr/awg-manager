#!/bin/bash
# Сборка wdtt-server для Keenetic (arm64) с патчами awg-manager.
#
# Два источника (WDTT_SERVER_SOURCE):
#   ildarmaga  — модульный server/cmd (WG + Raw через server_raw.go; патчи awg-manager)
#   spaceneurox — монолитный server.go из qWDTT ( -dns ; Raw в APK новее GitHub — см. BUILD.md)
#
#   qwdtt-monolith — SpaceNeuroX monolith @2dd5d37 + listen-direct/raw (APK qWDTT 1.4)
#
# По умолчанию: qwdtt-monolith (official stack + Keenetic flags).
# ildarmaga — legacy modular server (медленный raw vs official).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PATCH_DIR="$PROJECT_ROOT/contrib/wdtt-server-patch"
PYTHON="${PYTHON:-}"
if [ -z "$PYTHON" ]; then
  if command -v python >/dev/null 2>&1 && python -c 'import sys; sys.exit(0)' >/dev/null 2>&1; then
    PYTHON=python
  elif command -v py >/dev/null 2>&1; then
    PYTHON="py -3"
  else
    PYTHON=python3
  fi
fi
OUT_DIR="$PROJECT_ROOT/build/wdtt"
SOURCE="${WDTT_SERVER_SOURCE:-qwdtt-monolith}"
ILDARMAGA_TAG="${ILDARMAGA_TAG:-v1.4.62}"
SNX_COMMIT="${SNX_COMMIT:-2dd5d37f18a0}"
VERSION="$(grep 'const PinnedServerVersion' "$PROJECT_ROOT/internal/wdtt/install.go" | sed -n 's/.*"\([^"]*\)".*/\1/p')"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$OUT_DIR"

build_ildarmaga() {
  echo "Building wdtt-server ${VERSION} from ildarmaga/wdtt@${ILDARMAGA_TAG}..."
  git clone --depth 1 --branch "$ILDARMAGA_TAG" https://github.com/ildarmaga/wdtt.git "$WORK/wdtt"
  cd "$WORK/wdtt"
  git apply "$PATCH_DIR/no-nat.patch"
  git apply "$PATCH_DIR/panel-db.patch"
  git apply "$PATCH_DIR/wg-iface.patch"
  git apply "$PATCH_DIR/no-wipe.patch"
  "$PYTHON" "$PATCH_DIR/apply-raw-listen.py" .
  cp "$PATCH_DIR/server_raw.go" server/server_raw.go
  export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/wdtt-server-linux-arm64" ./server/cmd
}

build_qwdtt_monolith() {
  echo "Building wdtt-server ${VERSION} from SpaceNeuroX monolith@${SNX_COMMIT} + qWDTT extensions..."
  git clone https://github.com/SpaceNeuroX/proxy-turn-vk-android.git "$WORK/snx"
  cd "$WORK/snx"
  git checkout "$SNX_COMMIT"
  "$PYTHON" "$PATCH_DIR/qwdtt-monolith/apply-keenetic.py" .
  export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
  go mod tidy
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/wdtt-server-linux-arm64" .
}

build_spaceneurox() {
  echo "Building wdtt-server ${VERSION} from SpaceNeuroX/proxy-turn-vk-android@${SNX_COMMIT} (monolith, no extensions)..."
  git clone https://github.com/SpaceNeuroX/proxy-turn-vk-android.git "$WORK/snx"
  cd "$WORK/snx"
  git checkout "$SNX_COMMIT"
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/wdtt-server-linux-arm64" .
}

case "$SOURCE" in
  ildarmaga) build_ildarmaga ;;
  qwdtt-monolith) build_qwdtt_monolith ;;
  spaceneurox) build_spaceneurox ;;
  *) echo "Unknown WDTT_SERVER_SOURCE=$SOURCE (ildarmaga|qwdtt-monolith|spaceneurox)" >&2; exit 1 ;;
esac

sha256sum "$OUT_DIR/wdtt-server-linux-arm64"
stat -c '%s bytes' "$OUT_DIR/wdtt-server-linux-arm64" 2>/dev/null || stat -f '%z bytes' "$OUT_DIR/wdtt-server-linux-arm64"
echo "Output: $OUT_DIR/wdtt-server-linux-arm64"
echo "Upload: scp ... awgm-server:/var/www/entware-repo/wt/server/${VERSION}/"
