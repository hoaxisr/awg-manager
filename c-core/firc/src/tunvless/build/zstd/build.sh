#!/bin/sh
set -eu
exec sh "$(dirname "$0")/../archive.sh" zstd "${1:?need the zstd sources}" "${2:?need the output archive path}" \
    lib -DZSTD_DISABLE_ASM \
    lib/common/debug.c lib/common/entropy_common.c lib/common/error_private.c \
    lib/common/fse_decompress.c lib/common/pool.c lib/common/threading.c lib/common/xxhash.c \
    lib/common/zstd_common.c \
    lib/decompress/huf_decompress.c lib/decompress/zstd_ddict.c lib/decompress/zstd_decompress.c \
    lib/decompress/zstd_decompress_block.c
