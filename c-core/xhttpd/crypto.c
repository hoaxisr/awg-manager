#include "xhttpd.h"
#include <errno.h>
#include <string.h>
#include <stdio.h>
#include <stdlib.h>
#include <ctype.h>
#ifndef _WIN32
#include <fcntl.h>
#include <unistd.h>
#endif

/* FIPS 180-4 SHA-256 */
static const uint32_t k256[64] = {
    0x428a2f98UL, 0x71374491UL, 0xb5c0fbcfUL, 0xe9b5dba5UL,
    0x3956c25bUL, 0x59f111f1UL, 0x923f82a4UL, 0xab1c5ed5UL,
    0xd807aa98UL, 0x12835b01UL, 0x243185beUL, 0x550c7dc3UL,
    0x72be5d74UL, 0x80deb1feUL, 0x9bdc06a7UL, 0xc19bf174UL,
    0xe49b69c1UL, 0xefbe4786UL, 0x0fc19dc6UL, 0x240ca1ccUL,
    0x2de92c6fUL, 0x4a7484aaUL, 0x5cb0a9dcUL, 0x76f988daUL,
    0x983e5152UL, 0xa831c66dUL, 0xb00327c8UL, 0xbf597fc7UL,
    0xc6e00bf3UL, 0xd5a79147UL, 0x06ca6351UL, 0x14292967UL,
    0x27b70a85UL, 0x2e1b2138UL, 0x4d2c6dfcUL, 0x53380d13UL,
    0x650a7354UL, 0x766a0abbUL, 0x81c2c92eUL, 0x92722c85UL,
    0xa2bfe8a1UL, 0xa81a664bUL, 0xc24b8b70UL, 0xc76c51a3UL,
    0xd192e819UL, 0xd6990624UL, 0xf40e3585UL, 0x106aa070UL,
    0x19a4c116UL, 0x1e376c08UL, 0x2748774cUL, 0x34b0bcb5UL,
    0x391c0cb3UL, 0x4ed8aa4aUL, 0x5b9cca4fUL, 0x682e6ff3UL,
    0x748f82eeUL, 0x78a5636fUL, 0x84c87814UL, 0x8cc70208UL,
    0x90befffaUL, 0xa4506cebUL, 0xbef9a3f7UL, 0xc67178f2UL
};

static inline uint32_t rotr32(uint32_t x, uint32_t n) {
    return (x >> n) | (x << (32 - n));
}

typedef struct {
    uint32_t state[8];
    uint64_t bitcount;
    uint8_t  buffer[64];
} sha256_ctx_t;

static void sha256_transform(sha256_ctx_t *ctx, const uint8_t data[64]) {
    uint32_t w[64];
    for (int i = 0; i < 16; i++) {
        w[i] = ((uint32_t)data[i * 4] << 24) |
               ((uint32_t)data[i * 4 + 1] << 16) |
               ((uint32_t)data[i * 4 + 2] << 8) |
               ((uint32_t)data[i * 4 + 3]);
    }
    for (int i = 16; i < 64; i++) {
        uint32_t s0 = rotr32(w[i - 15], 7) ^ rotr32(w[i - 15], 18) ^ (w[i - 15] >> 3);
        uint32_t s1 = rotr32(w[i - 2], 17) ^ rotr32(w[i - 2], 19) ^ (w[i - 2] >> 10);
        w[i] = w[i - 16] + s0 + w[i - 7] + s1;
    }

    uint32_t a = ctx->state[0];
    uint32_t b = ctx->state[1];
    uint32_t c = ctx->state[2];
    uint32_t d = ctx->state[3];
    uint32_t e = ctx->state[4];
    uint32_t f = ctx->state[5];
    uint32_t g = ctx->state[6];
    uint32_t h = ctx->state[7];

    for (int i = 0; i < 64; i++) {
        uint32_t S1 = rotr32(e, 6) ^ rotr32(e, 11) ^ rotr32(e, 25);
        uint32_t ch = (e & f) ^ ((~e) & g);
        uint32_t temp1 = h + S1 + ch + k256[i] + w[i];
        uint32_t S0 = rotr32(a, 2) ^ rotr32(a, 13) ^ rotr32(a, 22);
        uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
        uint32_t temp2 = S0 + maj;

        h = g;
        g = f;
        f = e;
        e = d + temp1;
        d = c;
        c = b;
        b = a;
        a = temp1 + temp2;
    }

    ctx->state[0] += a;
    ctx->state[1] += b;
    ctx->state[2] += c;
    ctx->state[3] += d;
    ctx->state[4] += e;
    ctx->state[5] += f;
    ctx->state[6] += g;
    ctx->state[7] += h;
}

static void sha256_init(sha256_ctx_t *ctx) {
    ctx->state[0] = 0x6a09e667UL;
    ctx->state[1] = 0xbb67ae85UL;
    ctx->state[2] = 0x3c6ef372UL;
    ctx->state[3] = 0xa54ff53aUL;
    ctx->state[4] = 0x510e527fUL;
    ctx->state[5] = 0x9b05688cUL;
    ctx->state[6] = 0x1f83d9abUL;
    ctx->state[7] = 0x5be0cd19UL;
    ctx->bitcount = 0;
}

static void sha256_update(sha256_ctx_t *ctx, const uint8_t *data, size_t len) {
    if (len == 0) return;
    size_t idx = (size_t)((ctx->bitcount >> 3) & 0x3F);
    ctx->bitcount += (uint64_t)len << 3;

    size_t part_len = 64 - idx;
    size_t i = 0;
    if (len >= part_len) {
        memcpy(&ctx->buffer[idx], data, part_len);
        sha256_transform(ctx, ctx->buffer);
        for (i = part_len; i + 63 < len; i += 64) {
            sha256_transform(ctx, &data[i]);
        }
        idx = 0;
    }
    memcpy(&ctx->buffer[idx], &data[i], len - i);
}

static void sha256_final(sha256_ctx_t *ctx, uint8_t out[32]) {
    size_t idx = (size_t)((ctx->bitcount >> 3) & 0x3F);
    ctx->buffer[idx++] = 0x80;
    if (idx > 56) {
        memset(&ctx->buffer[idx], 0, 64 - idx);
        sha256_transform(ctx, ctx->buffer);
        idx = 0;
    }
    memset(&ctx->buffer[idx], 0, 56 - idx);
    for (int i = 0; i < 8; i++) {
        ctx->buffer[56 + i] = (uint8_t)(ctx->bitcount >> (56 - i * 8));
    }
    sha256_transform(ctx, ctx->buffer);
    for (int i = 0; i < 8; i++) {
        out[i * 4]     = (uint8_t)(ctx->state[i] >> 24);
        out[i * 4 + 1] = (uint8_t)(ctx->state[i] >> 16);
        out[i * 4 + 2] = (uint8_t)(ctx->state[i] >> 8);
        out[i * 4 + 3] = (uint8_t)(ctx->state[i]);
    }
}

void sha256_hash(const uint8_t *data, size_t len, uint8_t out[32]) {
    sha256_ctx_t ctx;
    sha256_init(&ctx);
    sha256_update(&ctx, data, len);
    sha256_final(&ctx, out);
}

void hmac_sha256(const uint8_t *key, size_t key_len, const uint8_t *data, size_t data_len, uint8_t out[32]) {
    uint8_t k_ipad[64];
    uint8_t k_opad[64];
    uint8_t tk[32];

    if (key_len > 64) {
        sha256_hash(key, key_len, tk);
        key = tk;
        key_len = 32;
    }
    memset(k_ipad, 0x36, 64);
    memset(k_opad, 0x5c, 64);
    for (size_t i = 0; i < key_len; i++) {
        k_ipad[i] ^= key[i];
        k_opad[i] ^= key[i];
    }

    uint8_t inner[32];
    sha256_ctx_t ctx;
    sha256_init(&ctx);
    sha256_update(&ctx, k_ipad, 64);
    sha256_update(&ctx, data, data_len);
    sha256_final(&ctx, inner);

    sha256_init(&ctx);
    sha256_update(&ctx, k_opad, 64);
    sha256_update(&ctx, inner, 32);
    sha256_final(&ctx, out);
}

bool ct_memeq(const void *a, const void *b, size_t n) {
    const volatile uint8_t *pa = (const volatile uint8_t *)a;
    const volatile uint8_t *pb = (const volatile uint8_t *)b;
    uint8_t diff = 0;
    for (size_t i = 0; i < n; i++) {
        diff |= pa[i] ^ pb[i];
    }
    return diff == 0;
}

static int hex_val(char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

int hex_to_bytes(const char *hex, uint8_t *out, size_t max_out) {
    size_t slen = strlen(hex);
    if (slen % 2 != 0 || (slen / 2) > max_out) return -1;
    size_t n = slen / 2;
    for (size_t i = 0; i < n; i++) {
        int hi = hex_val(hex[i * 2]);
        int lo = hex_val(hex[i * 2 + 1]);
        if (hi < 0 || lo < 0) return -1;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return (int)n;
}

void bytes_to_hex(const uint8_t *in, size_t len, char *out) {
    static const char hd[] = "0123456789abcdef";
    for (size_t i = 0; i < len; i++) {
        out[i * 2]     = hd[(in[i] >> 4) & 0x0F];
        out[i * 2 + 1] = hd[in[i] & 0x0F];
    }
    out[len * 2] = '\0';
}

int base64url_encode(const uint8_t *in, size_t in_len, char *out, size_t max_out) {
    static const char b64u[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    if (!out || max_out == 0) return -1;
    if (in_len == 0) {
        out[0] = '\0';
        return 0;
    }
    if (!in) return -1;
    size_t needed = (in_len / 3) * 4 + ((in_len % 3 == 0) ? 0 : (in_len % 3 + 1));
    if (needed + 1 > max_out) return -1;

    size_t i = 0, o = 0;
    while (i + 3 <= in_len) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8) | (uint32_t)in[i + 2];
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
        out[o++] = b64u[(v >> 6) & 0x3F];
        out[o++] = b64u[v & 0x3F];
        i += 3;
    }
    size_t rem = in_len - i;
    if (rem == 1) {
        uint32_t v = (uint32_t)in[i] << 16;
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
    } else if (rem == 2) {
        uint32_t v = ((uint32_t)in[i] << 16) | ((uint32_t)in[i + 1] << 8);
        out[o++] = b64u[(v >> 18) & 0x3F];
        out[o++] = b64u[(v >> 12) & 0x3F];
        out[o++] = b64u[(v >> 6) & 0x3F];
    }
    out[o] = '\0';
    return (int)o;
}

static int b64url_val(char c) {
    if (c >= 'A' && c <= 'Z') return c - 'A';
    if (c >= 'a' && c <= 'z') return c - 'a' + 26;
    if (c >= '0' && c <= '9') return c - '0' + 52;
    if (c == '-' || c == '+') return 62;
    if (c == '_' || c == '/') return 63;
    return -1;
}

int base64url_decode(const char *in, size_t in_len, uint8_t *out, size_t max_out) {
    if (!in || !out) return -1;
    uint8_t q[4];
    int qlen = 0;
    size_t o = 0;

    for (size_t i = 0; i < in_len; i++) {
        char c = in[i];
        if (c == '=' || c == ' ' || c == '\t' || c == '\r' || c == '\n' || c == ',') {
            continue;
        }
        int v = b64url_val(c);
        if (v < 0) return -1;
        q[qlen++] = (uint8_t)v;
        if (qlen == 4) {
            if (o + 3 > max_out) return -1;
            out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
            out[o++] = (uint8_t)(((q[1] & 0x0F) << 4) | (q[2] >> 2));
            out[o++] = (uint8_t)(((q[2] & 0x03) << 6) | q[3]);
            qlen = 0;
        }
    }
    if (qlen == 1) {
        return -1;
    } else if (qlen == 2) {
        if (o + 1 > max_out) return -1;
        out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
    } else if (qlen == 3) {
        if (o + 2 > max_out) return -1;
        out[o++] = (uint8_t)((q[0] << 2) | (q[1] >> 4));
        out[o++] = (uint8_t)(((q[1] & 0x0F) << 4) | (q[2] >> 2));
    }
    return (int)o;
}

bool parse_uuid_str(const char *str, uint8_t out[16]) {
    if (!str) return false;
    while (*str == ' ' || *str == '\t') str++;
    char clean[33];
    int idx = 0;
    for (const char *p = str; *p && *p != ' ' && *p != '\t' && *p != '"'; p++) {
        if (*p == '-') continue;
        if (idx >= 32 || hex_val(*p) < 0) return false;
        clean[idx++] = *p;
    }
    if (idx != 32) return false;
    clean[32] = '\0';
    return hex_to_bytes(clean, out, 16) == 16;
}

void format_uuid_canonical(const uint8_t u[16], char out[37]) {
    snprintf(out, 37,
             "%02x%02x%02x%02x-%02x%02x-%02x%02x-%02x%02x-%02x%02x%02x%02x%02x%02x",
             u[0], u[1], u[2], u[3], u[4], u[5], u[6], u[7],
             u[8], u[9], u[10], u[11], u[12], u[13], u[14], u[15]);
}

bool is_uuid_derived_fp_secret(const uint8_t secret[32], const uint8_t u[16]) {
    if (!secret || !u) return false;
    char canonical[37];
    format_uuid_canonical(u, canonical);
    char buf[64];
    int n = snprintf(buf, sizeof(buf), "awgm-fp-v1:%s", canonical);
    uint8_t derived[32];
    sha256_hash((const uint8_t *)buf, (size_t)n, derived);
    if (ct_memeq(secret, derived, 32)) return true;

    uint8_t raw_hash[32];
    sha256_hash(u, 16, raw_hash);
    if (ct_memeq(secret, raw_hash, 32)) return true;

    char hex_no_dash[33];
    bytes_to_hex(u, 16, hex_no_dash);
    if (ct_memeq(secret, (const uint8_t *)hex_no_dash, 32)) return true;

    return false;
}

static bool is_valid_v2_fp_secret(const xhttpd_config_t *cfg, const char *client_id, const uint8_t *secret, size_t secret_len) {
    if (!client_id || !client_id[0] || !secret || secret_len != 32) return false;
    uint8_t nz = 0;
    for (int i = 0; i < 32; i++) nz |= secret[i];
    if (!nz) return false;

    uint8_t cid_uuid[16];
    if (parse_uuid_str(client_id, cid_uuid)) {
        if (is_uuid_derived_fp_secret(secret, cid_uuid)) return false;
    }
    if (cfg) {
        for (int i = 0; i < cfg->num_clients; i++) {
            if (is_uuid_derived_fp_secret(secret, cfg->clients[i].uuid)) return false;
        }
    }
    return true;
}

int csprng_bytes(uint8_t *buf, size_t len) {
#ifdef _WIN32
    typedef BOOLEAN (WINAPI *RtlGenRandFn)(PVOID, ULONG);
    HMODULE hAdv = GetModuleHandleA("advapi32.dll");
    if (!hAdv) hAdv = LoadLibraryA("advapi32.dll");
    if (hAdv) {
        RtlGenRandFn fn = (RtlGenRandFn)GetProcAddress(hAdv, "SystemFunction036");
        if (fn && fn(buf, (ULONG)len)) return 0;
    }
    return -1;
#else
    int fd = open("/dev/urandom", O_RDONLY | O_CLOEXEC);
    if (fd < 0) return -1;
    size_t off = 0;
    while (off < len) {
        ssize_t r = read(fd, buf + off, len - off);
        if (r < 0 && errno == EINTR) continue;
        if (r <= 0) {
            close(fd);
            return -1;
        }
        off += (size_t)r;
    }
    close(fd);
    return 0;
#endif
}

int add_explicit_fp_cred(xhttpd_config_t *cfg, const char *client_id, const uint8_t secret[32]) {
    if (!cfg || !is_valid_v2_fp_secret(cfg, client_id, secret, 32)) return -1;
    for (int i = 0; i < cfg->num_cfg_fp_creds; i++) {
        if (strcasecmp(cfg->cfg_fp_creds[i].client_id, client_id) == 0) {
            snprintf(cfg->cfg_fp_creds[i].client_id, sizeof(cfg->cfg_fp_creds[i].client_id), "%s", client_id);
            memcpy(cfg->cfg_fp_creds[i].secret, secret, 32);
            cfg->cfg_fp_creds[i].secret_len = 32;
            return 0;
        }
    }
    if (cfg->num_cfg_fp_creds >= MAX_FP_CREDS) return -1;
    fp_cred_t *c = &cfg->cfg_fp_creds[cfg->num_cfg_fp_creds++];
    snprintf(c->client_id, sizeof(c->client_id), "%s", client_id);
    memcpy(c->secret, secret, 32);
    c->secret_len = 32;
    return 0;
}

static int add_fp_cred(xhttpd_config_t *cfg, const char *client_id, const uint8_t *secret, size_t secret_len) {
    if (!is_valid_v2_fp_secret(cfg, client_id, secret, secret_len)) return -1;
    for (int i = 0; i < cfg->num_fp_creds; i++) {
        if (strcasecmp(cfg->fp_creds[i].client_id, client_id) == 0) {
            snprintf(cfg->fp_creds[i].client_id, sizeof(cfg->fp_creds[i].client_id), "%s", client_id);
            memcpy(cfg->fp_creds[i].secret, secret, 32);
            cfg->fp_creds[i].secret_len = 32;
            return 0;
        }
    }
    if (cfg->num_fp_creds >= MAX_FP_CREDS) return -1;
    fp_cred_t *c = &cfg->fp_creds[cfg->num_fp_creds++];
    snprintf(c->client_id, sizeof(c->client_id), "%s", client_id);
    memcpy(c->secret, secret, 32);
    c->secret_len = 32;
    return 0;
}

int rebuild_fp_credentials(xhttpd_config_t *cfg) {
    if (!cfg) return -1;
    cfg->num_fp_creds = 0;
    if (cfg->num_clients < 0 || cfg->num_clients > MAX_CLIENTS) return -1;
    if (cfg->num_cfg_fp_creds < 0 || cfg->num_cfg_fp_creds > MAX_FP_CREDS) return -1;

    for (int i = 0; i < cfg->num_clients; i++) {
        vless_client_t *cl = &cfg->clients[i];
        format_uuid_canonical(cl->uuid, cl->uuid_str);
        bytes_to_hex(cl->uuid, 16, cl->hex_no_dash);
    }

    for (int i = 0; i < cfg->num_cfg_fp_creds; i++) {
        const fp_cred_t *src = &cfg->cfg_fp_creds[i];
        if (!is_valid_v2_fp_secret(cfg, src->client_id, src->secret, src->secret_len)) {
            continue;
        }
        if (add_fp_cred(cfg, src->client_id, src->secret, 32) < 0) return -1;
    }

    if (cfg->num_clients > 0 || cfg->num_cfg_fp_creds > 0) {
        const char *enable_diag = getenv("FP_ENABLE_DIAG_CRED");
        const char *env_sec = getenv("FP_SECRET_HEX");
        if (enable_diag && strcmp(enable_diag, "1") == 0 && env_sec && strlen(env_sec) == 64) {
            uint8_t sec[32];
            int n = hex_to_bytes(env_sec, sec, sizeof(sec));
            if (n == 32 && is_valid_v2_fp_secret(cfg, DEFAULT_STAGE2_ID, sec, 32)) {
                if (add_fp_cred(cfg, DEFAULT_STAGE2_ID, sec, 32) < 0) return -1;
            }
        }
    }
    return 0;
}
