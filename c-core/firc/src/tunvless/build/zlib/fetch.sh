#!/bin/sh
set -eu
ZLIB_VERSION=1.3.2
ZLIB_SHA256=bb329a0a2cd0274d05519d61c667c062e06990d72e125ee2dfa8de64f0119d16
URL="https://github.com/madler/zlib/releases/download/v${ZLIB_VERSION}/zlib-${ZLIB_VERSION}.tar.gz"

[ "${1:-}" = version ] && { echo "$ZLIB_VERSION"; exit 0; }
exec sh "$(dirname "$0")/../release.sh" zlib "$ZLIB_SHA256" "$URL" inflate.c \
    "${1:?need a directory to unpack the sources into}" "${ZLIB_TARBALL:-}"
