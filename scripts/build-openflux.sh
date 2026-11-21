#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
CORE_DIR="$REPO_ROOT/openflux-core"
OUT_DIR="${OPENFLUX_OUT_DIR:-$REPO_ROOT/dist/openflux}"
VERSION="${1:-$(bash "$SCRIPT_DIR/openflux-runtime-version.sh" --base)}"

if [[ ! -f "$CORE_DIR/go.mod" ]]; then
  echo "openflux-core/go.mod not found at $CORE_DIR" >&2
  exit 1
fi

rm -rf "$OUT_DIR"
mkdir -p "$OUT_DIR"

TARGETS=(
  "aarch64-3.10:arm64::"
  "mipsel-3.4:mipsle::softfloat"
  "mips-3.4:mips::softfloat"
  "arm:arm:7:"
  "x86_64:amd64::"
)

echo "Building OpenFlux core version=$VERSION -> $OUT_DIR"
pushd "$CORE_DIR" >/dev/null
for spec in "${TARGETS[@]}"; do
  IFS=':' read -r name goarch goarm gomips <<< "$spec"
  out="$OUT_DIR/openflux-linux-$name"
  echo "  -> openflux-linux-$name (GOARCH=$goarch${goarm:+ GOARM=$goarm}${gomips:+ GOMIPS=$gomips})"
  env -u GOARM -u GOMIPS \
    CGO_ENABLED=0 \
    GOOS=linux \
    GOARCH="$goarch" \
    ${goarm:+GOARM="$goarm"} \
    ${gomips:+GOMIPS="$gomips"} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o "$out" .
  chmod 0755 "$out"
done
popd >/dev/null

(
  cd "$OUT_DIR"
  sha256sum openflux-linux-* > SHA256SUMS.txt
)
cat "$OUT_DIR/SHA256SUMS.txt"
ls -lh "$OUT_DIR"/openflux-linux-*
