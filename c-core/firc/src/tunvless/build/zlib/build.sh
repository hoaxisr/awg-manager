#!/bin/sh
set -eu
exec sh "$(dirname "$0")/../archive.sh" zlib "${1:?need the zlib sources}" "${2:?need the output archive path}" \
    . "" inflate.c inftrees.c inffast.c adler32.c crc32.c zutil.c
