#!/bin/sh
set -eu
ZSTD_VERSION=1.5.7
ZSTD_SHA256=eb33e51f49a15e023950cd7825ca74a4a2b43db8354825ac24fc1b7ee09e6fa3
URL="https://github.com/facebook/zstd/releases/download/v${ZSTD_VERSION}/zstd-${ZSTD_VERSION}.tar.gz"

[ "${1:-}" = version ] && { echo "$ZSTD_VERSION"; exit 0; }
exec sh "$(dirname "$0")/../release.sh" zstd "$ZSTD_SHA256" "$URL" lib/zstd.h \
    "${1:?need a directory to unpack the sources into}" "${ZSTD_TARBALL:-}"
