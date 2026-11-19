#!/bin/sh
set -eu
exec sh "$(dirname "$0")/../archive.sh" brotli "${1:?need the brotli sources}" "${2:?need the output archive path}" \
    c/include -DBROTLI_BUILD_PORTABLE \
    c/common/constants.c c/common/context.c c/common/dictionary.c c/common/platform.c \
    c/common/shared_dictionary.c c/common/transform.c \
    c/dec/bit_reader.c c/dec/decode.c c/dec/huffman.c c/dec/prefix.c c/dec/state.c c/dec/static_init.c
