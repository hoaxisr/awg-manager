#!/bin/bash
# Извлекает assets/server и assets/deploy.sh из APK qWDTT (VPS deploy).
# Бинарник: linux amd64, статически слинкованный Go, с -listen-raw / -listen-direct / -dns.
# В APK нет linux/arm64 — бинари для Keenetic выпускает CI форка
# hoaxisr/proxy-turn-vk-android (релиз awgm-server-*), см. internal/wdtt/install.go.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
OUT_DIR="${1:-$PROJECT_ROOT/build/wdtt}"
APK_URL="${QWDTT_APK_URL:-https://github.com/SpaceNeuroX/proxy-turn-vk-android/releases/download/v1.4.0/app-universal-release.apk}"
APK="${QWDTT_APK:-$PROJECT_ROOT/build/wdtt-apk/qwdtt-1.4.0-universal.apk}"
TMP="$PROJECT_ROOT/build/wdtt-apk/extract-tmp"

mkdir -p "$(dirname "$APK")" "$OUT_DIR" "$TMP"

if [[ ! -f "$APK" ]]; then
  echo "Downloading $APK_URL ..."
  curl -fsSL -o "$APK" "$APK_URL"
fi

echo "Extracting assets/server from $(basename "$APK") ..."
unzip -p "$APK" assets/server > "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0"
unzip -p "$APK" assets/deploy.sh > "$OUT_DIR/deploy-qwdtt-1.4.0.sh"
chmod +x "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0" "$OUT_DIR/deploy-qwdtt-1.4.0.sh"

echo
echo "Output:"
echo "  $OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0"
echo "  $OUT_DIR/deploy-qwdtt-1.4.0.sh"
echo
sha256sum "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0"
stat -c '%s bytes' "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0" 2>/dev/null || stat -f '%z bytes' "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0"
echo
echo "Build info (go version -m):"
go version -m "$OUT_DIR/wdtt-server-linux-amd64-qwdtt-1.4.0" 2>/dev/null || echo "(go not installed)"
