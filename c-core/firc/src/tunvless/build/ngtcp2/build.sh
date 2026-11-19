#!/bin/sh
set -eu
SRC="${1:?need the ngtcp2 sources}"
OUT="${2:?need the output archive path}"
WOLFSSL_LIB="${3:?need the wolfSSL archive}"
CC="${CC:-cc}"
HERE=$(cd "$(dirname "$0")" && pwd)
QUIC=$(cd "$HERE/../../src/proto/quic" && pwd)

DEFS="-DHAVE_ARPA_INET_H -DHAVE_NETINET_IN_H -DHAVE_ENDIAN_H -DHAVE_BYTESWAP_H -DHAVE_UNISTD_H -DHAVE_EXPLICIT_BZERO -DHAVE_DECL_BE64TOH=1 -DHAVE_DECL_BSWAP_64=1 -DNDEBUG"
[ "$($CC -dM -E -x c /dev/null | sed -n 's/^#define __BYTE_ORDER__ //p')" = __ORDER_BIG_ENDIAN__ ] && DEFS="$DEFS -DWORDS_BIGENDIAN"
DEFS="$DEFS -I$HERE/include $(cat "$WOLFSSL_LIB.cflags")"
export FIRC_CFLAGS="${FIRC_CFLAGS:-} -I$QUIC"

FILES=
for f in "$SRC"/lib/*.c; do FILES="$FILES lib/$(basename "$f")"; done
# shellcheck disable=SC2086
exec sh "$(dirname "$0")/../archive.sh" ngtcp2 "$SRC" "$OUT" \
    "lib lib/includes crypto crypto/includes" "$DEFS" \
    $FILES crypto/shared.c crypto/wolfssl/wolfssl.c "$QUIC/ngcc.c"
