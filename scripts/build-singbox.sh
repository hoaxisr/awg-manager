#!/bin/bash
set -euo pipefail

usage() {
    echo "Usage: $0 <mipsel-3.4|mips-3.4|aarch64-3.10> [sing-box-version]" >&2
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"

ENTWARE_ARCH="${1:-}"
SINGBOX_VERSION_ARG="${2:-}"
if [[ -z "$ENTWARE_ARCH" || $# -lt 1 || $# -gt 2 ]]; then
    usage
    exit 1
fi

REQUIRED_VERSION_FILE="$PROJECT_ROOT/internal/singbox/installer/embedded.go"
DEFAULT_SINGBOX_VERSION="$(sed -n 's/^const RequiredVersion = "\(.*\)"/\1/p' "$REQUIRED_VERSION_FILE")"
SINGBOX_VERSION="${SINGBOX_VERSION_ARG:-${SINGBOX_VERSION:-$DEFAULT_SINGBOX_VERSION}}"
SINGBOX_REF="${SINGBOX_REF:-v$SINGBOX_VERSION}"
SINGBOX_GO="${SINGBOX_GO:-go}"
SINGBOX_LOW_MEMORY="${SINGBOX_LOW_MEMORY:-1}"
CRONET_GO_DIR="${CRONET_GO_DIR:-$HOME/cronet-go}"
RELEASE_REPO="${RELEASE_REPO:-hoaxisr/awg-manager}"
RELEASE_TAG="${RELEASE_TAG:-latest}"
RELEASE_BASE_URL="${RELEASE_BASE_URL:-https://github.com/$RELEASE_REPO/releases/download/$RELEASE_TAG}"
export GOTOOLCHAIN="${GOTOOLCHAIN:-local}"

case "$ENTWARE_ARCH" in
    mipsel-3.4)
        GOARCH="mipsle"
        GOMIPS_VALUE="softfloat"
        NAIVE=true
        ;;
    mips-3.4)
        GOARCH="mips"
        GOMIPS_VALUE="softfloat"
        NAIVE=false
        ;;
    aarch64-3.10)
        GOARCH="arm64"
        GOMIPS_VALUE=""
        NAIVE=true
        ;;
    *)
        echo "Unknown architecture: $ENTWARE_ARCH" >&2
        usage
        exit 1
        ;;
esac

if [[ -z "$SINGBOX_VERSION" ]]; then
    echo "ERROR: unable to determine sing-box version from $REQUIRED_VERSION_FILE" >&2
    exit 1
fi

require_command() {
    local name="$1"
    if ! command -v "$name" >/dev/null 2>&1; then
        echo "ERROR: missing required command: $name" >&2
        exit 1
    fi
}

require_command git
require_command file
require_command python3
require_command gofmt
require_command "$SINGBOX_GO"

cd "$PROJECT_ROOT"
mkdir -p dist build

echo "Building sing-box $SINGBOX_VERSION for $ENTWARE_ARCH using $($SINGBOX_GO version)"
echo "Using sing-box git ref: $SINGBOX_REF"

checkout_singbox_ref() {
    local dir="$1"
    local ref="$2"

    if [[ ! -d "$dir/.git" ]]; then
        rm -rf "$dir"
        mkdir -p "$dir"
        git init "$dir"
        git -C "$dir" remote add origin https://github.com/sagernet/sing-box.git
    elif git -C "$dir" remote get-url origin >/dev/null 2>&1; then
        git -C "$dir" remote set-url origin https://github.com/sagernet/sing-box.git
    else
        git -C "$dir" remote add origin https://github.com/sagernet/sing-box.git
    fi

    git -C "$dir" fetch --depth=1 origin "$ref"
    git -C "$dir" checkout --force FETCH_HEAD
}

if [[ -n "${SINGBOX_SRC:-}" ]]; then
    SINGBOX_DIR="$SINGBOX_SRC"
    if [[ ! -f "$SINGBOX_DIR/go.mod" ]]; then
        echo "ERROR: SINGBOX_SRC does not look like a sing-box checkout: $SINGBOX_DIR" >&2
        exit 1
    fi
else
    SINGBOX_DIR="${RUNNER_TEMP:-$PROJECT_ROOT/build}/sing-box-src"
    checkout_singbox_ref "$SINGBOX_DIR" "$SINGBOX_REF"
fi

cd "$SINGBOX_DIR"

apply_mieru_patch() {
    local patch_file="$PROJECT_ROOT/scripts/patches/mieru.patch"
    if [[ ! -f "$patch_file" ]]; then
        echo "ERROR: missing Mieru sing-box patch: $patch_file" >&2
        exit 1
    fi
    if git apply --reverse --check "$patch_file" >/dev/null 2>&1; then
        echo "Mieru patch already applied"
        return
    fi
    echo "Applying Mieru patch: $patch_file"
    if ! git apply --3way --whitespace=fix "$patch_file"; then
        echo "ERROR: failed to apply Mieru patch to sing-box source at $SINGBOX_DIR" >&2
        echo "       Check that SINGBOX_REF=$SINGBOX_REF is compatible with scripts/patches/mieru.patch" >&2
        exit 1
    fi
}

apply_xhttp_patch() {
    # Ported xhttp transport imports quic-go/http3 directly and is NOT build-tag
    # guarded; it requires `with_quic` (present in both DEFAULT_BUILD_TAGS sets).
    # If a future tag refactor drops with_quic, these files will fail to build.
    local patch_file="$PROJECT_ROOT/scripts/patches/xhttp.patch"
    if [[ ! -f "$patch_file" ]]; then
        echo "ERROR: missing XHTTP sing-box patch: $patch_file" >&2
        exit 1
    fi
    if git apply --reverse --check "$patch_file" >/dev/null 2>&1; then
        echo "XHTTP patch already applied"
        return
    fi
    echo "Applying XHTTP patch: $patch_file"
    if ! git apply --3way --whitespace=fix "$patch_file"; then
        echo "ERROR: failed to apply XHTTP patch to sing-box source at $SINGBOX_DIR" >&2
        echo "       Check that SINGBOX_REF=$SINGBOX_REF is compatible with scripts/patches/xhttp.patch" >&2
        exit 1
    fi
}

apply_mieru_patch
apply_xhttp_patch

append_tag() {
    local tag="$1"
    case ",$TAGS," in
        *,"$tag",*) ;;
        *) TAGS="$TAGS,$tag" ;;
    esac
}

if [[ "$NAIVE" == true ]]; then
    CRONET_REF="$(cat .github/CRONET_GO_VERSION)"
    if [[ ! -d "$CRONET_GO_DIR/.git" ]]; then
        mkdir -p "$CRONET_GO_DIR"
        git init "$CRONET_GO_DIR"
    fi
    if git -C "$CRONET_GO_DIR" remote get-url origin >/dev/null 2>&1; then
        git -C "$CRONET_GO_DIR" remote set-url origin https://github.com/sagernet/cronet-go.git
    else
        git -C "$CRONET_GO_DIR" remote add origin https://github.com/sagernet/cronet-go.git
    fi
    git -C "$CRONET_GO_DIR" fetch --depth=1 origin "$CRONET_REF"
    git -C "$CRONET_GO_DIR" checkout --force FETCH_HEAD
    git -C "$CRONET_GO_DIR" submodule update --init --recursive --depth=1

    rm -f "$CRONET_GO_DIR/naiveproxy/src/build/linux/sysroot_scripts/keyring.gpg"
    (
        cd "$CRONET_GO_DIR"
        GPG_TTY=/dev/null ./naiveproxy/src/build/linux/sysroot_scripts/generate_keyring.sh
    )

    (
        cd "$CRONET_GO_DIR"
        "$SINGBOX_GO" run ./cmd/build-naive --target="linux/$GOARCH" --libc=musl download-toolchain
    )
    eval "$(
        cd "$CRONET_GO_DIR"
        "$SINGBOX_GO" run ./cmd/build-naive --target="linux/$GOARCH" --libc=musl env --export
    )"
    TAGS="$(cat release/DEFAULT_BUILD_TAGS),with_musl"
    CGO_ENABLED_VALUE="1"
else
    TAGS="$(cat release/DEFAULT_BUILD_TAGS_OTHERS)"
    CGO_ENABLED_VALUE="0"
fi

if [[ "$SINGBOX_LOW_MEMORY" == "1" || "$SINGBOX_LOW_MEMORY" == "true" ]]; then
    append_tag with_low_memory
fi

echo "Build tags: $TAGS"

LDFLAGS_SHARED="$(cat release/LDFLAGS)"
OUTPUT_TMP="$PROJECT_ROOT/build/sing-box-$SINGBOX_VERSION-$ENTWARE_ARCH"
OUTPUT="$PROJECT_ROOT/dist/sing-box-$SINGBOX_VERSION-$ENTWARE_ARCH"

ENV_VARS=(
    "CGO_ENABLED=$CGO_ENABLED_VALUE"
    "GOOS=linux"
    "GOARCH=$GOARCH"
)
if [[ -n "$GOMIPS_VALUE" ]]; then
    ENV_VARS+=("GOMIPS=$GOMIPS_VALUE")
fi

env "${ENV_VARS[@]}" "$SINGBOX_GO" build -v -trimpath \
    -o "$OUTPUT_TMP" \
    -tags "$TAGS" \
    -ldflags "-X 'github.com/sagernet/sing-box/constant.Version=$SINGBOX_VERSION' $LDFLAGS_SHARED -s -w -buildid=" \
    ./cmd/sing-box

if [[ -n "${STRIP:-}" ]] && command -v "$STRIP" >/dev/null 2>&1; then
    "$STRIP" --strip-all "$OUTPUT_TMP" || echo "WARNING: strip failed, keeping unstripped binary" >&2
fi

mv "$OUTPUT_TMP" "$OUTPUT"
chmod +x "$OUTPUT"
file "$OUTPUT"
ls -lh "$OUTPUT"

# Sidecar (.sha256) + embedded.go (URL/SHA256/Size) are written by a dedicated
# script so the same logic is reusable from CI on a sing-box cache hit, where
# this build is skipped. Mirrors upstream's build / embed separation.
RELEASE_BASE_URL="$RELEASE_BASE_URL" "$SCRIPT_DIR/update-singbox-embedded.sh" "$ENTWARE_ARCH" "$SINGBOX_VERSION"
