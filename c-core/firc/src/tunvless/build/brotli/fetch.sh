#!/bin/sh
set -eu
BROTLI_VERSION=1.2.1
BROTLI_SHA256=98d35d11a78dc453aebb7d10c3748f3f73ff78c8267c0dc697b8631f67bf7e8f
URL="https://github.com/google/brotli/archive/refs/tags/v${BROTLI_VERSION}.tar.gz"

[ "${1:-}" = version ] && { echo "$BROTLI_VERSION"; exit 0; }
exec sh "$(dirname "$0")/../release.sh" brotli "$BROTLI_SHA256" "$URL" c/dec/decode.c \
    "${1:?need a directory to unpack the sources into}" "${BROTLI_TARBALL:-}"
