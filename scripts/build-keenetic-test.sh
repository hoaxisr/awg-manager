#!/bin/bash
# Полная develop-сборка IPK для Keenetic (+rN). Бинари wdtt сюда не входят.
#
# Windows: wsl -d Ubuntu bash -lc "cd /mnt/e/AWGM/awg-manager && ./scripts/build-keenetic-test.sh r19"
# GOPROXY=https://goproxy.io,direct — если proxy.golang.org падает по TLS
#
# Usage:
#   ./scripts/build-keenetic-test.sh              # IPK 2.16.5+r19, aarch64
#   ./scripts/build-keenetic-test.sh r20          # другая ревизия
#   ./scripts/build-keenetic-test.sh r19 all      # все архы IPK
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
cd "$PROJECT_ROOT"

REV="${1:-r21}"
ARCH="${2:-aarch64-3.10}"
BASE_VERSION="$(tr -d '[:space:]' < VERSION)"
IPK_VERSION="${BASE_VERSION}+${REV}"

echo "=== Keenetic test build: awg-manager ${IPK_VERSION} ==="

echo ">>> wdtt: бинари не собираются здесь — их выпускает CI форка по тегу,"
echo "    релиз уезжает на repo.hoaxisr.ru сам, awg-manager качает по пину install.go."
echo "    Пины после нового релиза: scripts/update-wdtt-pins.py --client-tag ... --server-tag ..."

echo ">>> awg-manager IPK"
if [[ "$ARCH" == "all" ]]; then
  "$SCRIPT_DIR/build-ipk.sh" "$IPK_VERSION" all
else
  "$SCRIPT_DIR/build-ipk.sh" "$IPK_VERSION" "$ARCH"
fi

echo ""
echo "Done."
echo "  IPK: dist/awg-manager_${IPK_VERSION}_*-kn.ipk"
echo ""
echo "Keenetic (aarch64):"
echo "  scp dist/awg-manager_${IPK_VERSION}_aarch64-3.10-kn.ipk router:/opt/tmp/"
echo "  ssh router 'opkg install /opt/tmp/awg-manager_${IPK_VERSION}_aarch64-3.10-kn.ipk'"
echo ""
echo "Raw test:"
echo "  Server on router: RelayMode=raw, -listen-raw auto (DTLS+1)"
echo "  Client to VPS:      ConnMode=raw, Peer=VPS:56013 (не трогаем VPS unit)"
echo "  Client to router:   Peer=<WAN-IP>:<raw-port>"
