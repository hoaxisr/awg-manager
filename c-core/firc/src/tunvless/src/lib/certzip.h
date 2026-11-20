#ifndef TUNVLESS_CERTZIP_H
#define TUNVLESS_CERTZIP_H
#include <stddef.h>

#define CERTZIP_ZLIB   1
#define CERTZIP_BROTLI 2
#define CERTZIP_ZSTD   3
#define CERTZIP_MAX    40960

int certzip_inflate(int alg, const unsigned char *in, size_t in_n, size_t declared,
                    unsigned char *out, size_t out_cap);

#endif
