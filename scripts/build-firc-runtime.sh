#!/usr/bin/env bash
# Build custom awg-firc-runtime IPK (fircd + tunvless + S99firc with .awgm-disabled)
# from FIRC_SOURCE_ROOT using the cached Entware SDK and package via cmd/firc-runtime-package.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

ARCH="${1:-mipsel-3.4}"
VERSION="${2:-$(bash "$SCRIPT_DIR/firc-runtime-version.sh")}"
FIRC_SOURCE_ROOT="${FIRC_SOURCE_ROOT:-$REPO_ROOT/c-core/firc}"
FIRC_DEPS_ROOT="${FIRC_DEPS_ROOT:-}"

if [ -z "$FIRC_SOURCE_ROOT" ] || [ ! -d "$FIRC_SOURCE_ROOT/src/tunvless" ] || [ ! -d "$FIRC_SOURCE_ROOT/src/backend-c" ]; then
  echo "FIRC_SOURCE_ROOT must point to the custom FIRC source tree (with src/tunvless and src/backend-c)" >&2
  exit 2
fi

case "$ARCH" in
  mipsel-3.4)
    SDK_PREFIX="entware-sdk-mipsel-3.4"
    TC_DIR_GLOB="toolchain-mipsel_mips32r2_gcc-*_glibc-*"
    CROSS_PREFIX="mipsel-openwrt-linux-gnu-"
    FIRC_BUILD="mipsel"
    FIRC_TARGET="mipsel-3.4_kn"
    DEPS_SUB="mipsle"
    ;;
  mips-3.4)
    SDK_PREFIX="entware-sdk-mips-3.4"
    TC_DIR_GLOB="toolchain-mips_mips32r2_gcc-*_glibc-*"
    CROSS_PREFIX="mips-openwrt-linux-gnu-"
    FIRC_BUILD="mips"
    FIRC_TARGET="mips-3.4_kn"
    DEPS_SUB="mips"
    ;;
  aarch64-3.10)
    SDK_PREFIX="entware-sdk-aarch64-3.10"
    TC_DIR_GLOB="toolchain-aarch64_cortex-a53_gcc-*_glibc-*"
    CROSS_PREFIX="aarch64-openwrt-linux-gnu-"
    FIRC_BUILD="aarch64"
    FIRC_TARGET="aarch64-3.10_kn"
    DEPS_SUB="arm64"
    ;;
  *)
    echo "Unsupported architecture: $ARCH (expected mipsel-3.4, mips-3.4, aarch64-3.10)" >&2
    exit 2
    ;;
esac

SDK=$(find "$REPO_ROOT/build/entware-sdk/$ARCH" -maxdepth 1 -type d -name "${SDK_PREFIX}*" | head -n1 || true)
if [ -z "$SDK" ]; then
  echo "Entware SDK not found under $REPO_ROOT/build/entware-sdk/$ARCH; run scripts/build-c-sidecars.sh $ARCH first." >&2
  exit 1
fi
TCBIN=$(find "$SDK/staging_dir" -maxdepth 2 -type d -name "bin" -path "*${TC_DIR_GLOB}*" | head -n1 || true)
if [ -z "$TCBIN" ] || [ ! -x "$TCBIN/${CROSS_PREFIX}gcc" ]; then
  echo "Toolchain binary ${CROSS_PREFIX}gcc not found in $SDK" >&2
  exit 1
fi
export STAGING_DIR="$SDK/staging_dir"

WORK_DIR="$REPO_ROOT/build/firc-runtime/$ARCH"
rm -rf "$WORK_DIR"
mkdir -p "$WORK_DIR/src" "$WORK_DIR/deps"
rsync -a --exclude '/.build' --exclude '/src/backend-c/build' --exclude 'out*' --exclude __pycache__ --exclude 'third_party/*/.git' \
  "$FIRC_SOURCE_ROOT/" "$WORK_DIR/src/"

find "$WORK_DIR/src" -type f \( -name '*.sh' -o -name 'S99firc' -o -name '100-firc' -o -name 'firc.conf' -o -name 'groups.yaml' -o -name 'Makefile' -o -name '*.c' -o -name '*.h' \) -exec sed -i 's/\r$//' {} +
python3 "$SCRIPT_DIR/firc-loopback-inbound.py" "$WORK_DIR/src"
for lib in zlib brotli zstd; do
  if [ -d "$WORK_DIR/src/src/tunvless/third_party/$lib" ]; then
    touch "$WORK_DIR/src/src/tunvless/third_party/$lib/.steer-$lib"
  fi
done

python3 - <<PY
p = "$WORK_DIR/src/files/entware_kn/etc/init.d/S99firc"
text = open(p, encoding="utf-8").read()
if ".awgm-disabled" not in text:
    anchor = '  [ "\$ENABLED" != "yes" ] && return 8\n'
    guard = anchor + (
        '  if [ -e /opt/etc/firc/.awgm-disabled ]; then\n'
        '    echo -e "\$ansi_yellow \$DESC is disabled by awg-manager (.awgm-disabled). \$ansi_std"\n'
        '    logger "\$DESC: disabled by awg-manager (.awgm-disabled); refusing start."\n'
        '    return 8\n'
        '  fi\n'
    )
    assert anchor in text, "missing S99firc start() anchor"
    open(p, "w", encoding="utf-8", newline="\n").write(text.replace(anchor, guard, 1))
PY

if [ -n "$FIRC_DEPS_ROOT" ]; then
  cp -a "$FIRC_DEPS_ROOT/sysroot/usr/include" "$WORK_DIR/deps/include"
  cp -a "$FIRC_DEPS_ROOT/cross-lib/$DEPS_SUB" "$WORK_DIR/deps/lib"
  CFLAGS_EXTRA="-I$WORK_DIR/deps/include -I$WORK_DIR/deps/include/x86_64-linux-gnu"
  LDFLAGS_EXTRA="-L$WORK_DIR/deps/lib -Wl,--dynamic-linker=/opt/lib/ld.so.1 -Wl,-rpath,/opt/lib -Wl,--allow-shlib-undefined -s"
else
  CFLAGS_EXTRA=""
  LDFLAGS_EXTRA="-Wl,--dynamic-linker=/opt/lib/ld.so.1 -Wl,-rpath,/opt/lib -s"
fi

if [ "$ARCH" = "aarch64-3.10" ]; then
  DYN_LINKER="/opt/lib/ld-linux-aarch64.so.1"
  LDFLAGS_EXTRA="${LDFLAGS_EXTRA/\/opt\/lib\/ld.so.1/$DYN_LINKER}"
else
  DYN_LINKER="/opt/lib/ld.so.1"
fi

make -j"$(nproc)" -C "$WORK_DIR/src/src/tunvless" \
  CC="${TCBIN}/${CROSS_PREFIX}gcc" \
  AR="${TCBIN}/${CROSS_PREFIX}ar" \
  CFLAGS="-O2" \
  LDFLAGS="-Wl,--dynamic-linker=${DYN_LINKER} -Wl,-rpath,/opt/lib -Wl,-z,relro,-z,now -s" \
  VERSION="$VERSION" \
  O="$WORK_DIR/out-tunvless"

make -j"$(nproc)" -C "$WORK_DIR/src/src/backend-c" \
  BUILD="$FIRC_BUILD" WITH_DEPS=1 PLATFORM=entware ENTWARE_KN=1 \
  FIRC_VERSION="$VERSION" FIRC_TARGET="$FIRC_TARGET" FIRC_REVISION=1 \
  CROSS_COMPILE="${TCBIN}/${CROSS_PREFIX}" \
  CFLAGS_EXTRA="$CFLAGS_EXTRA" \
  LDFLAGS_EXTRA="$LDFLAGS_EXTRA" \
  LDLIBS="-lpthread -Wl,-Bstatic -latomic -Wl,-Bdynamic" \
  "build/${FIRC_BUILD}/fircd"

mkdir -p "$REPO_ROOT/dist"
OUT_IPK="$REPO_ROOT/dist/awg-firc-runtime_${VERSION}_${ARCH}.ipk"
cd "$REPO_ROOT"
FIRC_SOURCE_ROOT="$WORK_DIR/src" go run -mod=vendor ./cmd/firc-runtime-package \
  -source "$REPO_ROOT" \
  -fircd "$WORK_DIR/src/src/backend-c/build/${FIRC_BUILD}/fircd" \
  -tunvless "$WORK_DIR/out-tunvless/tunvless" \
  -arch "$ARCH" \
  -version "$VERSION" \
  -out "$OUT_IPK"
ls -lh "$OUT_IPK"
