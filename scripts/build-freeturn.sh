#!/usr/bin/env bash
# Build freeturn 2.0.1-1 for awg-manager prebuilt/ and optional repo upload.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
FTP="${FREETURN_SRC:-$ROOT/../free-turn-proxy-vkcalls}"
OUT="$ROOT/prebuilt/freeturn"
VERSION="${FREETURN_VERSION:-2.0.1-1}"

if [[ ! -d "$FTP/cmd/client" ]]; then
  echo "ERROR: free-turn-proxy source not found at $FTP" >&2
  exit 1
fi

mkdir -p "$OUT"
export CGO_ENABLED=0
LDFLAGS="-s -w -X main.version=${VERSION} -checklinkname=0"

build_one() {
  local goos=$1 goarch=$2 goarm=$3 out=$4
  echo "==> $goos/$goarch -> $(basename "$out")"
  if [[ -n "$goarm" ]]; then
    GOOS=$goos GOARCH=$goarch GOARM=$goarm go build -ldflags "$LDFLAGS" -trimpath -o "$out" -C "$FTP" ./cmd/client
  else
    GOOS=$goos GOARCH=$goarch go build -ldflags "$LDFLAGS" -trimpath -o "$out" -C "$FTP" ./cmd/client
  fi
}

build_server() {
  local goos=$1 goarch=$2 out=$3
  echo "==> $goos/$goarch server -> $(basename "$out")"
  GOOS=$goos GOARCH=$goarch go build -ldflags "$LDFLAGS" -trimpath -o "$out" -C "$FTP" ./cmd/server
}

# aarch64 router
build_one linux arm64 "" "$OUT/ft-client-linux-arm64"
build_server linux arm64 "$OUT/ft-server-linux-arm64"

# mipsel / mips — build if cross-compiler available (best-effort)
if GOOS=linux GOARCH=mipsle GOMIPS=softfloat go build -ldflags "$LDFLAGS" -trimpath -o /dev/null -C "$FTP" ./cmd/client 2>/dev/null; then
  build_one linux mipsle softfloat "$OUT/ft-client-linux-mipsle-softfloat"
  build_server linux mipsle "$OUT/ft-server-linux-mipsle-softfloat"
else
  echo "WARN: skip mipsle (toolchain unavailable)"
fi

if GOOS=linux GOARCH=mips GOMIPS=softfloat go build -ldflags "$LDFLAGS" -trimpath -o /dev/null -C "$FTP" ./cmd/client 2>/dev/null; then
  build_one linux mips softfloat "$OUT/ft-client-linux-mips-softfloat"
  build_server linux mips "$OUT/ft-server-linux-mips-softfloat"
else
  echo "WARN: skip mips (toolchain unavailable)"
fi

ls -la "$OUT"
