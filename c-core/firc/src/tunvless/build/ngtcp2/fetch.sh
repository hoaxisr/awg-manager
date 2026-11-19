#!/bin/sh
set -eu
NGTCP2_VERSION=1.26.0
NGTCP2_SHA256=85174d674e7f8fd911a827c6e483fd417b21b1f54d7853dac464cee446c8d35f
URL="https://github.com/ngtcp2/ngtcp2/releases/download/v${NGTCP2_VERSION}/ngtcp2-${NGTCP2_VERSION}.tar.gz"

[ "${1:-}" = version ] && { echo "$NGTCP2_VERSION"; exit 0; }
exec sh "$(dirname "$0")/../release.sh" ngtcp2 "$NGTCP2_SHA256" "$URL" lib/includes/ngtcp2/version.h \
    "${1:?need a directory to unpack the sources into}" "${NGTCP2_TARBALL:-}"
