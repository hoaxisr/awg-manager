#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
PATCH_DIR="$PROJECT_ROOT/contrib/wdtt-server-patch"
OUT_DIR="$PROJECT_ROOT/build/wdtt"
TAG="v1.4.62"
VERSION="$(grep 'const PinnedServerVersion' "$PROJECT_ROOT/internal/wdtt/install.go" | sed -n 's/.*"\([^"]*\)".*/\1/p')"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p "$OUT_DIR"

echo "Building wdtt-server ${VERSION} from ildarmaga/wdtt@${TAG}..."

git clone --depth 1 --branch "$TAG" https://github.com/ildarmaga/wdtt.git "$WORK/wdtt"
cd "$WORK/wdtt"
git apply "$PATCH_DIR/no-nat.patch"
git apply "$PATCH_DIR/panel-db.patch"
git apply "$PATCH_DIR/wg-iface.patch"

CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" \
  -o "$OUT_DIR/wdtt-server-linux-arm64" ./server/cmd

sha256sum "$OUT_DIR/wdtt-server-linux-arm64"
stat -c '%s bytes' "$OUT_DIR/wdtt-server-linux-arm64" 2>/dev/null || stat -f '%z bytes' "$OUT_DIR/wdtt-server-linux-arm64"

echo "Output: $OUT_DIR/wdtt-server-linux-arm64"
echo
echo "Бинарь в IPK не кладётся — доставка только с зеркала. Выложить:"
echo "  scp \$OUT_DIR/wdtt-server-linux-arm64 awgm-server:/var/www/entware-repo/wt/server/${VERSION}/"
echo "и вписать SHA256/размер в internal/wdtt/install.go (EmbeddedBinaries)."
