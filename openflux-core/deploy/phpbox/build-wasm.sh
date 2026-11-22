#!/usr/bin/env bash
# Builds the browser parser for the phpbox page: the core's own share package
# (openflux:// links + QR) compiled to WebAssembly, gzipped, plus Go's loader.
# Output: deploy/phpbox/assets/{share.wasm.gz,wasm_exec.js} (tracked and verified in CI)
set -euo pipefail
cd "$(dirname "$0")/../.."
GO_VER="$(awk '$1 == "go" { print $2; exit }' go.mod)"
if [[ -z "$GO_VER" ]]; then
  echo "failed to read Go version from openflux-core/go.mod" >&2
  exit 1
fi
export GOTOOLCHAIN="go${GO_VER}"
out=deploy/phpbox/assets
mkdir -p "$out"
GOOS=js GOARCH=wasm CGO_ENABLED=0 go build -trimpath -buildvcs=false -ldflags "-s -w -buildid=" -o "$out/share.wasm" ./cmd/sharewasm
gzip -n -9 -f -c "$out/share.wasm" > "$out/share.wasm.gz"
rm "$out/share.wasm"
cp -f "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$out/wasm_exec.js"
chmod 0644 "$out/share.wasm.gz" "$out/wasm_exec.js"
ls -la "$out"
