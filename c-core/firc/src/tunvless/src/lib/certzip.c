#include <stdlib.h>
#include <string.h>
#include <zlib.h>
#include <brotli/decode.h>
#include <zstd.h>

#include "certzip.h"

static int inflate_zlib(const unsigned char *in, size_t in_n, unsigned char *out, size_t declared) {
    z_stream z;
    memset(&z, 0, sizeof z);
    if (inflateInit(&z) != Z_OK) return -1;
    z.next_in = (unsigned char *)in;
    z.avail_in = (uInt)in_n;
    z.next_out = out;
    z.avail_out = (uInt)declared;
    int rc = inflate(&z, Z_FINISH);
    int ok = rc == Z_STREAM_END && z.total_out == declared && z.avail_in == 0;
    inflateEnd(&z);
    return ok ? 0 : -1;
}

#define BROTLI_MEM_CAP ((size_t)512 << 10)
#define BROTLI_MEM_HDR 16

static void *brotli_alloc(void *opaque, size_t n) {
    size_t *live = opaque;
    if (n > BROTLI_MEM_CAP - *live) return NULL;
    unsigned char *p = malloc(n + BROTLI_MEM_HDR);
    if (!p) return NULL;
    memcpy(p, &n, sizeof n);
    *live += n;
    return p + BROTLI_MEM_HDR;
}

static void brotli_free(void *opaque, void *a) {
    if (!a) return;
    size_t *live = opaque, n;
    unsigned char *p = (unsigned char *)a - BROTLI_MEM_HDR;
    memcpy(&n, p, sizeof n);
    *live -= n;
    free(p);
}

static int inflate_brotli(const unsigned char *in, size_t in_n, unsigned char *out, size_t declared) {
    size_t live = 0;
    BrotliDecoderState *s = BrotliDecoderCreateInstance(brotli_alloc, brotli_free, &live);
    if (!s) return -1;
    size_t avail_in = in_n, avail_out = declared, total = 0;
    const uint8_t *next_in = in;
    uint8_t *next_out = out;
    BrotliDecoderResult rc = BrotliDecoderDecompressStream(s, &avail_in, &next_in, &avail_out,
                                                           &next_out, &total);
    BrotliDecoderDestroyInstance(s);
    return rc == BROTLI_DECODER_RESULT_SUCCESS && total == declared && avail_in == 0 ? 0 : -1;
}

static int inflate_zstd(const unsigned char *in, size_t in_n, unsigned char *out, size_t declared) {
    size_t n = ZSTD_decompress(out, declared, in, in_n);
    return !ZSTD_isError(n) && n == declared ? 0 : -1;
}

int certzip_inflate(int alg, const unsigned char *in, size_t in_n, size_t declared,
                    unsigned char *out, size_t out_cap) {
    if (declared > CERTZIP_MAX || declared > out_cap) return -1;
    switch (alg) {
    case CERTZIP_ZLIB:   return inflate_zlib(in, in_n, out, declared);
    case CERTZIP_BROTLI: return inflate_brotli(in, in_n, out, declared);
    case CERTZIP_ZSTD:   return inflate_zstd(in, in_n, out, declared);
    default:             return -1;
    }
}
