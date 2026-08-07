#!/bin/bash
# Patched wdtt-server for Linux VPS (amd64).
# Default: qwdtt-monolith (SpaceNeuroX + listen-direct/raw). Legacy: ildarmaga.
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
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$OUT_DIR"

build_ildarmaga() {
  echo ">>> wdtt-server amd64 (ildarmaga ${ILDARMAGA_TAG} + awg-manager patches)"
  git clone --depth 1 --branch "$ILDARMAGA_TAG" https://github.com/ildarmaga/wdtt.git "$WORK/wdtt"
  cd "$WORK/wdtt"
  git apply "$PATCH_DIR/no-nat.patch"
  git apply "$PATCH_DIR/panel-db.patch"
  git apply "$PATCH_DIR/wg-iface.patch"
  git apply "$PATCH_DIR/no-wipe.patch"
  "$PYTHON" "$PATCH_DIR/apply-raw-listen.py" .
  cp "$PATCH_DIR/server_raw.go" server/server_raw.go
  export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/wdtt-server-linux-amd64" ./server/cmd
}

build_qwdtt_monolith() {
  echo ">>> wdtt-server amd64 (SpaceNeuroX monolith ${SNX_COMMIT} + qWDTT extensions)"
  git clone https://github.com/SpaceNeuroX/proxy-turn-vk-android.git "$WORK/snx"
  cd "$WORK/snx"
  git checkout "$SNX_COMMIT"
  "$PYTHON" "$PATCH_DIR/qwdtt-monolith/apply-keenetic.py" .
  export GOPROXY="${GOPROXY:-https://proxy.golang.org,direct}"
  go mod tidy
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" \
    -o "$OUT_DIR/wdtt-server-linux-amd64" .
}

case "$SOURCE" in
  ildarmaga) build_ildarmaga ;;
  qwdtt-monolith) build_qwdtt_monolith ;;
  *) echo "Unknown WDTT_SERVER_SOURCE=$SOURCE (ildarmaga|qwdtt-monolith)" >&2; exit 1 ;;
esac

sha256sum "$OUT_DIR/wdtt-server-linux-amd64"
ls -la "$OUT_DIR/wdtt-server-linux-amd64"
echo "Done: $OUT_DIR/wdtt-server-linux-amd64"
