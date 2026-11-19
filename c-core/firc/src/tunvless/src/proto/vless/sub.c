/* Subscription parsing: the VLESS nodes of a subscription file.
 *
 * A subscription is a list of links (plain or base64, one per line), an Xray or sing-box JSON
 * config, or Clash YAML. Parsing needs no network and no cryptography, so the subscription tests
 * build this file without libraries.
 *
 * Links and Clash proxies of other protocols are skipped but counted (foreign): a subscription is
 * usually shared, and "26 nodes in it, 17 used" must be explained by numbers, not guessed.
 */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <arpa/inet.h>
#include "vless.h"
#include "sublink.h"
#include "vlink.h"
/* vless_uuid_form: the rule that turns the id into 16 bytes lives in one place, vless_proto.c
 * (no libraries). */
#include "vless_proto.h"
/* vencp_parse: a VLESS encryption string is checked here, before connecting. Strings only, no
 * cryptography. */
#include "vencp.h"
#include "hy2link.h"

/* Link strings, transport and security parameters, long values and certificate checks are in
 * sublink.c. Here: whole subscriptions and the VLESS fields (flow, encryption, the id). */

/* Marker of an encryption value that did not parse; node_usable names the reason. */
static const char SUB_BAD_ENC[] = "!encryption";

/* Empty and "none" mean no encryption. Anything else must parse by Xray's rule (vencp.h), or the
 * node is marked and node_usable rejects it. v is already percent-decoded. */
static void set_encryption(struct vless_node *n, const char *v) {
    n->encryption = NULL;
    if (!v[0] || !strcmp(v, "none")) return;
    struct venc_cfg c;
    if (vencp_parse(v, &c, NULL) != 0) { n->encryption = SUB_BAD_ENC; return; }
    n->encryption = sl_intern(v, strlen(v));
}

static int node_usable(struct vless_node *n);

/* ---- TrustTunnel (tt://, https://...?d=) and SOCKS5 (socks5://, socks://) ------------------- */

static void tt_sha256_hex(const unsigned char *data, size_t len, char hex[65]) {
    static const uint32_t K[64] = {
        0x428a2f98u,0x71374491u,0xb5c0fbcfu,0xe9b5dba5u,0x3956c25bu,0x59f111f1u,0x923f82a4u,0xab1c5ed5u,
        0xd807aa98u,0x12835b01u,0x243185beu,0x550c7dc3u,0x72be5d74u,0x80deb1feu,0x9bdc06a7u,0xc19bf174u,
        0xe49b69c1u,0xefbe4786u,0x0fc19dc6u,0x240ca1ccu,0x2de92c6fu,0x4a7484aau,0x5cb0a9dcu,0x76f988dau,
        0x983e5152u,0xa831c66du,0xb00327c8u,0xbf597fc7u,0xc6e00bf3u,0xd5a79147u,0x06ca6351u,0x14292967u,
        0x27b70a85u,0x2e1b2138u,0x4d2c6dfcu,0x53380d13u,0x650a7354u,0x766a0abbu,0x81c2c92eu,0x92722c85u,
        0xa2bfe8a1u,0xa81a664bu,0xc24b8b70u,0xc76c51a3u,0xd192e819u,0xd6990624u,0xf40e3585u,0x106aa070u,
        0x19a4c116u,0x1e376c08u,0x2748774cu,0x34b0bcb5u,0x391c0cb3u,0x4ed8aa4au,0x5b9cca4fu,0x682e6ff3u,
        0x748f82eeu,0x78a5636fu,0x84c87814u,0x8cc70208u,0x90befffau,0xa4506cebu,0xbef9a3f7u,0xc67178f2u
    };
    uint32_t h[8] = {
        0x6a09e667u,0xbb67ae85u,0x3c6ef372u,0xa54ff53au,
        0x510e527fu,0x9b05688cu,0x1f83d9abu,0x5be0cd19u
    };
    unsigned char blk[64];
    size_t off = 0;
    uint64_t bits = (uint64_t)len * 8;
    int padded = 0;
    while (!padded) {
        size_t rem = len - off;
        if (rem >= 64) {
            memcpy(blk, data + off, 64);
            off += 64;
        } else {
            memset(blk, 0, 64);
            if (rem > 0) memcpy(blk, data + off, rem);
            if (off <= len) {
                blk[rem] = 0x80;
                off = len + 1;
            }
            if (rem < 56) {
                for (int i = 0; i < 8; i++) blk[56 + i] = (unsigned char)(bits >> (56 - 8 * i));
                padded = 1;
            }
        }
        uint32_t w[64];
        for (int i = 0; i < 16; i++)
            w[i] = ((uint32_t)blk[4*i] << 24) | ((uint32_t)blk[4*i+1] << 16) |
                   ((uint32_t)blk[4*i+2] << 8) | (uint32_t)blk[4*i+3];
        for (int i = 16; i < 64; i++) {
            uint32_t x = w[i-15], y = w[i-2];
            uint32_t s0 = ((x >> 7) | (x << 25)) ^ ((x >> 18) | (x << 14)) ^ (x >> 3);
            uint32_t s1 = ((y >> 17) | (y << 15)) ^ ((y >> 19) | (y << 13)) ^ (y >> 10);
            w[i] = w[i-16] + s0 + w[i-7] + s1;
        }
        uint32_t a=h[0], b=h[1], c=h[2], d=h[3], e=h[4], f=h[5], g=h[6], hh=h[7];
        for (int i = 0; i < 64; i++) {
            uint32_t S1 = ((e >> 6) | (e << 26)) ^ ((e >> 11) | (e << 21)) ^ ((e >> 25) | (e << 7));
            uint32_t ch = (e & f) ^ (~e & g);
            uint32_t t1 = hh + S1 + ch + K[i] + w[i];
            uint32_t S0 = ((a >> 2) | (a << 30)) ^ ((a >> 13) | (a << 19)) ^ ((a >> 22) | (a << 10));
            uint32_t maj = (a & b) ^ (a & c) ^ (b & c);
            uint32_t t2 = S0 + maj;
            hh = g; g = f; f = e; e = d + t1; d = c; c = b; b = a; a = t1 + t2;
        }
        h[0]+=a; h[1]+=b; h[2]+=c; h[3]+=d; h[4]+=e; h[5]+=f; h[6]+=g; h[7]+=hh;
    }
    static const char HX[] = "0123456789abcdef";
    for (int i = 0; i < 8; i++) {
        for (int j = 0; j < 4; j++) {
            unsigned char byte = (unsigned char)(h[i] >> (24 - 8 * j));
            hex[i * 8 + j * 2] = HX[byte >> 4];
            hex[i * 8 + j * 2 + 1] = HX[byte & 15];
        }
    }
    hex[64] = '\0';
}

/* Walk a DER certificate chain (one or more concatenated ASN.1 SEQUENCEs) and format their
 * SHA-256 fingerprints into comma-separated hex for sl_add_pins(n, pins, 0). Returns 0 on valid
 * DER, -1 on malformed DER. */
static int tt_der_pins(const unsigned char *der, size_t n, char *out, size_t cap) {
    if (!n) return -1;
    size_t p = 0, o = 0;
    out[0] = '\0';
    while (p < n) {
        if (p + 2 > n || der[p] != 0x30) return -1;
        size_t hdr = 2, len = der[p + 1];
        if (len & 0x80) {
            size_t k = len & 0x7f;
            if (k == 0 || k > 4 || p + 2 + k > n) return -1;
            len = 0;
            for (size_t i = 0; i < k; i++) len = (len << 8) | der[p + 2 + i];
            hdr += k;
        }
        if (len == 0 || p + hdr + len > n) return -1;
        char hex[65];
        tt_sha256_hex(der + p, hdr + len, hex);
        if (o + 66 >= cap) return -1;
        if (o) out[o++] = ',';
        memcpy(out + o, hex, 64);
        o += 64;
        out[o] = '\0';
        p += hdr + len;
    }
    return o ? 0 : -1;
}

static int tt_varint(const unsigned char *b, size_t n, uint64_t *val, size_t *used) {
    if (!n) return -1;
    size_t len = (size_t)1u << (b[0] >> 6);
    if (n < len) return -1;
    uint64_t v = b[0] & 0x3fu;
    for (size_t i = 1; i < len; i++) v = (v << 8) | b[i];
    *val = v;
    *used = len;
    return 0;
}

static int is_tt_connect_url(const char *url) {
    if (strncmp(url, "https://", 8) != 0 && strncmp(url, "http://", 7) != 0) return 0;
    const char *hash = strchr(url, '#');
    const char *q = strchr(url, '?');
    if (!q || (hash && q > hash)) return 0;
    const char *end = hash ? hash : url + strlen(url);
    const char *k = q + 1;
    while (k < end) {
        const char *amp = memchr(k, '&', (size_t)(end - k));
        const char *stop = amp ? amp : end;
        if (stop - k > 2 && k[0] == 'd' && k[1] == '=') return 1;
        if (!amp) break;
        k = amp + 1;
    }
    return 0;
}

static int is_tt_tlv_url(const char *url) {
    if (!strncmp(url, "tt://", 5)) {
        const char *p = url + 5;
        const char *hash = strchr(p, '#');
        const char *q = strchr(p, '?');
        const char *end = q ? q : hash ? hash : p + strlen(p);
        /* Canonical tt://user:pass@host:port has '@' before '?' and '#'. */
        if (memchr(p, '@', (size_t)(end - p))) return 0;
        return 1;
    }
    return is_tt_connect_url(url);
}

/* Strict base64/base64url decode for TrustTunnel TLV payloads: rejects non-base64 characters. */
static int tt_b64_decode(const char *in, size_t n, unsigned char *out, size_t cap, size_t *out_n) {
    *out_n = 0;
    if (!n) return -1;
    unsigned acc = 0;
    int bits = 0, pad = 0;
    size_t o = 0;
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)in[i];
        if (c == '=') { pad++; continue; }
        if (pad) return -1;
        int v = -1;
        if (c >= 'A' && c <= 'Z') v = c - 'A';
        else if (c >= 'a' && c <= 'z') v = c - 'a' + 26;
        else if (c >= '0' && c <= '9') v = c - '0' + 52;
        else if (c == '+' || c == '-') v = 62;
        else if (c == '/' || c == '_') v = 63;
        if (v < 0) return -1;
        acc = (acc << 6) | (unsigned)v;
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (o >= cap) return -1;
            out[o++] = (unsigned char)((acc >> bits) & 0xFFu);
        }
    }
    if (bits >= 6 || (acc & ((1u << bits) - 1u)) != 0 || o == 0) return -1;
    *out_n = o;
    return 0;
}

/* Parse a TrustTunnel TLV deep-link (tt://?<TLV> or http(s)://...?d=<TLV>&name=...) into 1..max
 * nodes (one per address in the TLV). Returns 0 if at least one node is usable, 1 if skipped
 * (reason in out[0].skip_reason), -1 if not a TrustTunnel TLV link. */
static int tt_parse_tlv_multi(const char *url, struct vless_node *out, size_t max, size_t *out_n) {
    *out_n = 0;
    if (!max || !is_tt_tlv_url(url)) return -1;
    memset(&out[0], 0, sizeof(out[0]));
    snprintf(out[0].proto_str, sizeof(out[0].proto_str), "tt");

    const char *b64 = NULL;
    size_t b64_n = 0;
    char label[128] = "";
    const char *hash = strchr(url, '#');
    if (hash) sl_set_name(label, sizeof(label), hash + 1);

    if (!strncmp(url, "tt://", 5)) {
        const char *p = url + 5;
        if (*p == '?') p++;
        const char *end = hash ? hash : p + strlen(p);
        b64 = p;
        b64_n = (size_t)(end - p);
    } else {
        const char *q = strchr(url, '?');
        const char *end = hash ? hash : url + strlen(url);
        const char *k = q + 1;
        while (k < end) {
            const char *amp = memchr(k, '&', (size_t)(end - k));
            const char *stop = amp ? amp : end;
            const char *eq = memchr(k, '=', (size_t)(stop - k));
            if (eq) {
                size_t kl = (size_t)(eq - k), vl = (size_t)(stop - eq - 1);
                const char *v = eq + 1;
                if (kl == 1 && k[0] == 'd') { b64 = v; b64_n = vl; }
                else if (kl == 4 && !strncmp(k, "name", 4)) {
                    sl_set_pct(label, sizeof(label), v, vl);
                    sl_utf8_trim_tail(label);
                }
            }
            if (!amp) break;
            k = amp + 1;
        }
    }

    if (!b64 || !b64_n) {
        snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: empty payload");
        *out_n = 1;
        return 1;
    }

    unsigned char raw[4096];
    size_t raw_n = 0;
    if (tt_b64_decode(b64, b64_n, raw, sizeof(raw), &raw_n) != 0) {
        snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: invalid base64url payload");
        *out_n = 1;
        return 1;
    }

    char hostname[128] = "", custom_sni[128] = "", username[64] = "", password[64] = "", tlv_name[128] = "";
    char addrs[16][144];
    size_t n_addrs = 0;
    uint8_t skip_verify = 0, anti_dpi = 0;
    char pins_hex[520] = "";

    size_t pos = 0;
    while (pos < raw_n) {
        uint64_t tag = 0, flen = 0;
        size_t u1 = 0, u2 = 0;
        if (tt_varint(raw + pos, raw_n - pos, &tag, &u1) != 0) {
            snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: truncated TLV tag");
            *out_n = 1;
            return 1;
        }
        pos += u1;
        if (tt_varint(raw + pos, raw_n - pos, &flen, &u2) != 0 || flen > raw_n - pos - u2) {
            snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: truncated TLV value");
            *out_n = 1;
            return 1;
        }
        pos += u2;
        const unsigned char *val = raw + pos;
        size_t vlen = (size_t)flen;
        pos += vlen;

        switch (tag) {
        case 0x00: {
            uint64_t ver = 0;
            size_t vu = 0;
            if (tt_varint(val, vlen, &ver, &vu) != 0 || ver > 1) {
                snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: unsupported TLV version");
                *out_n = 1;
                return 1;
            }
            break;
        }
        case 0x01: sl_set_field(hostname, sizeof(hostname), (const char *)val, vlen); break;
        case 0x02:
            if (n_addrs < 16) sl_set_field(addrs[n_addrs++], sizeof(addrs[0]), (const char *)val, vlen);
            break;
        case 0x03: sl_set_field(custom_sni, sizeof(custom_sni), (const char *)val, vlen); break;
        case 0x05: sl_set_field(username, sizeof(username), (const char *)val, vlen); break;
        case 0x06: sl_set_field(password, sizeof(password), (const char *)val, vlen); break;
        case 0x07: skip_verify = (vlen == 1 && val[0] == 1) ? 1 : 0; break;
        case 0x08:
            if (tt_der_pins(val, vlen, pins_hex, sizeof(pins_hex)) != 0) {
                snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: invalid certificate DER");
                *out_n = 1;
                return 1;
            }
            break;
        case 0x09: {
            uint64_t up = 0;
            size_t vu = 0;
            if (tt_varint(val, vlen, &up, &vu) != 0) {
                snprintf(out[0].skip_reason, sizeof(out[0].skip_reason), "tt: invalid upstream_protocol");
                *out_n = 1;
                return 1;
            }
            break;
        }
        case 0x0A: anti_dpi = (vlen == 1 && val[0] == 1) ? 1 : 0; break;
        case 0x0C:
            sl_set_field(tlv_name, sizeof(tlv_name), (const char *)val, vlen);
            sl_utf8_trim_tail(tlv_name);
            break;
        default: break;
        }
    }

    const char *base_name = label[0] ? label : tlv_name[0] ? tlv_name : hostname;
    if (base_name[0]) snprintf(out[0].name, sizeof(out[0].name), "%s", base_name);

    if (!hostname[0] || !n_addrs || !username[0] || !password[0]) {
        snprintf(out[0].skip_reason, sizeof(out[0].skip_reason),
                 !hostname[0] ? "tt: missing hostname" :
                 !n_addrs ? "tt: missing address" : "tt: missing username or password");
        *out_n = 1;
        return 1;
    }

    const char *sni = custom_sni[0] ? custom_sni : hostname;
    size_t count = n_addrs < max ? n_addrs : max;
    int any_ok = 0;
    for (size_t i = 0; i < count; i++) {
        struct vless_node *nd = &out[i];
        memset(nd, 0, sizeof(*nd));
        snprintf(nd->proto_str, sizeof(nd->proto_str), "tt");
        snprintf(nd->type, sizeof(nd->type), "tcp");
        snprintf(nd->security, sizeof(nd->security), "tls");
        if (n_addrs > 1)
            snprintf(nd->name, sizeof(nd->name), "%.*s-%u", (int)(sizeof(nd->name) - 16), base_name, (unsigned)(i + 1));
        else
            snprintf(nd->name, sizeof(nd->name), "%s", base_name);
        sl_utf8_trim_tail(nd->name);

        const char *a = addrs[i];
        const char *colon = strrchr(a, ':');
        if (!colon || colon == a) {
            snprintf(nd->skip_reason, sizeof(nd->skip_reason), "tt: invalid address");
            continue;
        }
        size_t hl = (size_t)(colon - a);
        const char *hs = a;
        if (hl >= 2 && hs[0] == '[' && hs[hl - 1] == ']') {
            /* IPv6 literal in brackets: keep brackets so sl_host_is_name / tunnodes recognizes it. */
            sl_set_field(nd->host, sizeof(nd->host), hs, hl);
        } else {
            sl_set_field(nd->host, sizeof(nd->host), hs, hl);
        }
        nd->port = sl_port_of(colon + 1);
        if (!nd->port) {
            snprintf(nd->skip_reason, sizeof(nd->skip_reason), "tt: invalid port");
            continue;
        }
        snprintf(nd->uuid, sizeof(nd->uuid), "%s:%s", username, password);
        snprintf(nd->sni, sizeof(nd->sni), "%s", sni);
        if (custom_sni[0] && strcmp(custom_sni, hostname) != 0) sl_set_vcn(nd, hostname);
        if (pins_hex[0]) sl_add_pins(nd, pins_hex, 0);
        nd->allow_insecure = skip_verify;
        nd->record_fragment = anti_dpi;
        if (node_usable(nd) == 0) any_ok = 1;
    }
    *out_n = count;
    return any_ok ? 0 : 1;
}

static int socks_parse_url(const char *url, struct vless_node *n) {
    size_t scl = !strncmp(url, "socks5://", 9) ? 9 : !strncmp(url, "socks://", 8) ? 8 : 0;
    if (!scl) return -1;
    memset(n, 0, sizeof(*n));
    snprintf(n->proto_str, sizeof(n->proto_str), "socks5");
    snprintf(n->type, sizeof(n->type), "tcp");
    snprintf(n->security, sizeof(n->security), "none");

    const char *p = url + scl;
    const char *hash = strchr(p, '#');
    const char *qmark = memchr(p, '?', (size_t)((hash ? hash : p + strlen(p)) - p));
    const char *hp_end = qmark ? qmark : hash ? hash : p + strlen(p);
    const char *at = memchr(p, '@', (size_t)(hp_end - p));
    if (at) {
        char cred[192];
        sl_set_pct(cred, sizeof(cred), p, (size_t)(at - p));
        if (strchr(cred, ':')) {
            sl_set_field(n->uuid, sizeof(n->uuid), cred, strlen(cred));
        } else if (cred[0]) {
            char dec[192];
            size_t dn = b64_decode(cred, strlen(cred), dec, sizeof(dec));
            if (dn > 0 && strchr(dec, ':')) sl_set_field(n->uuid, sizeof(n->uuid), dec, dn);
            else sl_set_field(n->uuid, sizeof(n->uuid), cred, strlen(cred));
        }
        p = at + 1;
    }
    const char *colon = memchr(p, ':', (size_t)(hp_end - p));
    if (!colon || colon == p) return -1;
    sl_set_field(n->host, sizeof(n->host), p, (size_t)(colon - p));
    char pnum[8];
    const char *pe = colon + 1;
    size_t pl = 0;
    while (pe < hp_end && *pe >= '0' && *pe <= '9' && pl + 1 < sizeof(pnum)) pnum[pl++] = *pe++;
    pnum[pl] = '\0';
    if (!pl || pe != hp_end) return -1;
    n->port = sl_port_of(pnum);
    if (!n->port) return -1;
    if (hash) sl_set_name(n->name, sizeof(n->name), hash + 1);
    return node_usable(n);
}

static int ss_cipher_ok(const char *uuid) {
    const char *col = strchr(uuid, ':');
    if (!col || col == uuid || !col[1]) return 0;
    size_t ml = (size_t)(col - uuid);
    return (ml == 11 && !strncmp(uuid, "aes-128-gcm", 11)) ||
           (ml == 11 && !strncmp(uuid, "aes-256-gcm", 11)) ||
           (ml == 22 && !strncmp(uuid, "chacha20-ietf-poly1305", 22)) ||
           (ml == 17 && !strncmp(uuid, "chacha20-poly1305", 17)) ||
           (ml == 23 && !strncmp(uuid, "2022-blake3-aes-128-gcm", 23)) ||
           (ml == 23 && !strncmp(uuid, "2022-blake3-aes-256-gcm", 23)) ||
           (ml == 29 && !strncmp(uuid, "2022-blake3-chacha20-poly1305", 29));
}

static int ss_parse_url(const char *url, struct vless_node *n) {
    if (strncmp(url, "ss://", 5) != 0) return -1;
    const char *p = url + 5;
    const char *hash = strchr(p, '#');
    const char *qmark = memchr(p, '?', (size_t)((hash ? hash : p + strlen(p)) - p));
    const char *hp_end = qmark ? qmark : hash ? hash : p + strlen(p);
    const char *at = memchr(p, '@', (size_t)(hp_end - p));

    char full_buf[512];
    if (!at) {
        /* Legacy ss://base64(method:password@host:port)#name */
        char dec[384];
        size_t dn = b64_decode(p, (size_t)(hp_end - p), dec, sizeof(dec));
        if (!dn || !strchr(dec, '@') || !strchr(dec, ':')) return -1;
        snprintf(full_buf, sizeof(full_buf), "ss://%s%s", dec, qmark ? qmark : (hash ? hash : ""));
        url = full_buf;
    }

    const char *sec = NULL;
    size_t sec_n = 0;
    int rc = sl_link_parse(url, "ss://", n, NULL, &sec, &sec_n, 0);
    if (rc) return rc;
    snprintf(n->proto_str, sizeof(n->proto_str), "ss");
    char cred[256];
    sl_set_pct(cred, sizeof(cred), sec, sec_n);
    if (strchr(cred, ':')) {
        sl_set_field(n->uuid, sizeof(n->uuid), cred, strlen(cred));
    } else {
        char dec[256];
        size_t dn = b64_decode(cred, strlen(cred), dec, sizeof(dec));
        if (!dn || !strchr(dec, ':')) return -1;
        sl_set_field(n->uuid, sizeof(n->uuid), dec, dn);
    }
    return node_usable(n);
}

static int vmess_parse_url(const char *url, struct vless_node *n);
static int mieru_parse_url(const char *url, struct vless_node *n);
static int anytls_parse_url(const char *url, struct vless_node *n);

/* The VLESS parameters of a link: flow and encryption (Xray-core's post-quantum encryption, a
 * long value, see sl_intern). The rest go to sl_link_param. */
static int vless_own(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if (klen == 4 && !strncmp(k, "flow", 4)) sl_set_pct(n->flow, sizeof(n->flow), v, vlen);
    else if (klen == 10 && !strncmp(k, "encryption", 10)) {
        char *d = sl_param_dup(v, vlen);
        if (d) { set_encryption(n, d); free(d); } else n->encryption = SUB_BAD_ENC;
    }
    else return 0;
    return 1;
}

size_t vless_link_scheme(const char *s) {
    if (!s) return 0;
    if (!strncmp(s, "vless://", 8)) return 8;
    if (!strncmp(s, "hysteria2://", 12)) return 12;
    if (!strncmp(s, "hy2://", 6)) return 6;
    if (!strncmp(s, "finiteproto://", 14)) return 14;
    if (!strncmp(s, "fp://", 5)) return 5;
    if (!strncmp(s, "tt://", 5)) return 5;
    if (!strncmp(s, "trojan://", 9)) return 9;
    if (!strncmp(s, "ss://", 5)) return 5;
    if (!strncmp(s, "vmess://", 8)) return 8;
    if (!strncmp(s, "mieru://", 8)) return 8;
    if (!strncmp(s, "mierus://", 9)) return 9;
    if (!strncmp(s, "anytls://", 9)) return 9;
    if (!strncmp(s, "naive+https://", 14)) return 14;
    if (!strncmp(s, "naive://", 8)) return 8;
    if (!strncmp(s, "socks5://", 9)) return 9;
    if (!strncmp(s, "socks://", 8)) return 8;
    if (is_tt_connect_url(s)) return !strncmp(s, "https://", 8) ? 8 : 7;
    return 0;
}

/* vless://UUID@host:port?params#name, hysteria2://, hy2://, tt://, https://...?d=<TLV>,
 * socks5://, trojan://, ss://, vmess://, mieru://, mierus://, anytls://, finiteproto://,
 * naive+https://. An unusable node is not an error of the subscription: only that node is
 * skipped. */
int vless_parse_url(const char *url, struct vless_node *n) {
    if (!strncmp(url, "hysteria2://", 12) || !strncmp(url, "hy2://", 6)) {
        return hy2_link_parse(url, n);
    }
    if (is_tt_tlv_url(url)) {
        size_t cnt = 0;
        return tt_parse_tlv_multi(url, n, 1, &cnt);
    }
    if (!strncmp(url, "tt://", 5)) {
        const char *sec = NULL;
        size_t sec_n = 0;
        int rc = sl_link_parse(url, "tt://", n, NULL, &sec, &sec_n, 0);
        if (rc) return rc;
        sl_set_pct(n->uuid, sizeof(n->uuid), sec, sec_n);
        snprintf(n->proto_str, sizeof(n->proto_str), "tt");
        if (!n->security[0]) snprintf(n->security, sizeof(n->security), "tls");
        if (!strcmp(n->type, "h2")) snprintf(n->type, sizeof(n->type), "tcp");
        return node_usable(n);
    }
    if (!strncmp(url, "naive+https://", 14) || !strncmp(url, "naive://", 8)) {
        const char *scheme = !strncmp(url, "naive+https://", 14) ? "naive+https://" : "naive://";
        const char *sec = NULL;
        size_t sec_n = 0;
        int rc = sl_link_parse(url, scheme, n, NULL, &sec, &sec_n, 0);
        if (rc) return rc;
        sl_set_pct(n->uuid, sizeof(n->uuid), sec, sec_n);
        snprintf(n->proto_str, sizeof(n->proto_str), "naive");
        if (!n->security[0]) snprintf(n->security, sizeof(n->security), "tls");
        if (!n->sni[0] && sl_host_is_name(n->host)) snprintf(n->sni, sizeof(n->sni), "%s", n->host);
        if (!strcmp(n->type, "h2")) snprintf(n->type, sizeof(n->type), "tcp");
        return node_usable(n);
    }
    if (!strncmp(url, "trojan://", 9)) {
        const char *sec = NULL;
        size_t sec_n = 0;
        int rc = sl_link_parse(url, "trojan://", n, NULL, &sec, &sec_n, 0);
        if (rc) return rc;
        sl_set_pct(n->uuid, sizeof(n->uuid), sec, sec_n);
        snprintf(n->proto_str, sizeof(n->proto_str), "trojan");
        if (!n->security[0]) snprintf(n->security, sizeof(n->security), "tls");
        if (!strcmp(n->security, "tls") && !n->sni[0] && sl_host_is_name(n->host))
            snprintf(n->sni, sizeof(n->sni), "%s", n->host);
        return node_usable(n);
    }
    if (!strncmp(url, "ss://", 5)) {
        return ss_parse_url(url, n);
    }
    if (!strncmp(url, "vmess://", 8)) {
        return vmess_parse_url(url, n);
    }
    if (!strncmp(url, "mieru://", 8) || !strncmp(url, "mierus://", 9)) {
        return mieru_parse_url(url, n);
    }
    if (!strncmp(url, "anytls://", 9)) {
        return anytls_parse_url(url, n);
    }
    if (!strncmp(url, "finiteproto://", 14) || !strncmp(url, "fp://", 5)) {
        const char *scheme = !strncmp(url, "finiteproto://", 14) ? "finiteproto://" : "fp://";
        const char *sec = NULL;
        size_t sec_n = 0;
        int rc = sl_link_parse(url, scheme, n, vless_own, &sec, &sec_n, 0);
        if (rc) return rc;
        snprintf(n->proto_str, sizeof(n->proto_str), "vless");
        snprintf(n->type, sizeof(n->type), "xhttp");
        snprintf(n->mode, sizeof(n->mode), "finiteproto");
        if (!n->security[0]) snprintf(n->security, sizeof(n->security), "tls");
        return node_usable(n);
    }
    if (!strncmp(url, "socks5://", 9) || !strncmp(url, "socks://", 8)) {
        return socks_parse_url(url, n);
    }
    /* Omitted fields default to type=tcp and security=none (sl_link_parse, node_usable).
     * security=none is plain VLESS over TCP, without TLS: supported, and sensible inside a
     * trusted network or over an already protected channel. */
    int rc = sl_link_parse(url, "vless://", n, vless_own, NULL, NULL, 0);
    if (rc) return rc;
    /* Checked here, not at connect time, so that an unusable node never becomes a candidate. */
    return node_usable(n);
}

/* Whether a parsed node is usable: 0 — yes, 1 — no, reason in n->skip_reason. Every parser
 * (links, Xray, sing-box, Clash) goes through it, so a node cannot be usable in one form and
 * unusable in another. */
static int node_usable(struct vless_node *n) {
    if (n->proto == VLESS_PROTO_HY2) return n->skip_reason[0] ? 1 : hy2_node_usable(n);
    int def_tls = !strcmp(n->proto_str, "tt") || !strcmp(n->proto_str, "naive") ||
                  !strcmp(n->proto_str, "trojan") || !strcmp(n->proto_str, "anytls");
    if (!n->security[0]) {
        snprintf(n->security, sizeof(n->security), "%s", def_tls ? "tls" : "none");
    }
    if ((!strcmp(n->proto_str, "tt") || !strcmp(n->proto_str, "naive")) &&
        (!n->type[0] || !strcmp(n->type, "h2"))) {
        snprintf(n->type, sizeof(n->type), "tcp");
    }

    /* flow=xtls-rprx-vision-udp443 is Vision with UDP to port 443 allowed: without the suffix
     * Xray's client refuses UDP/443 over Vision ("XTLS rejected UDP/443 traffic") to push QUIC
     * back to TCP. This client never refuses it, so the suffix changes nothing, and the name
     * becomes the usual one: Xray also sends the flow without the suffix in the VLESS request. */
    if (!strcmp(n->flow, "xtls-rprx-vision-udp443")) snprintf(n->flow, sizeof(n->flow), "xtls-rprx-vision");

    if (n->encryption == SUB_BAD_ENC) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "encryption is not supported");
        return 1;
    }
    if (sl_link_usable_pre(n)) return 1;

    if (!strcmp(n->proto_str, "tt") || !strcmp(n->proto_str, "naive") || !strcmp(n->proto_str, "mieru")) {
        const char *col = strchr(n->uuid, ':');
        if (!col || col == n->uuid || !col[1]) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "%s: missing username or password", n->proto_str);
            return 1;
        }
        return sl_link_usable_post(n);
    }
    if (!strcmp(n->proto_str, "trojan") || !strcmp(n->proto_str, "anytls")) {
        if (!n->uuid[0]) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "%s: password is empty", n->proto_str);
            return 1;
        }
        return sl_link_usable_post(n);
    }
    if (!strcmp(n->proto_str, "ss")) {
        if (!ss_cipher_ok(n->uuid)) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "ss: unsupported cipher");
            return 1;
        }
        return sl_link_usable_post(n);
    }
    if (!strcmp(n->proto_str, "socks5")) {
        if (n->uuid[0]) {
            const char *col = strchr(n->uuid, ':');
            if (!col || col == n->uuid) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "socks5: invalid user:password");
                return 1;
            }
        }
        return sl_link_usable_post(n);
    }
    if (!strcmp(n->proto_str, "vmess")) {
        const char *id = n->uuid;
        const char *col = strchr(n->uuid, ':');
        if (col) {
            size_t cl = (size_t)(col - n->uuid);
            if (!((cl == 4 && !strncmp(n->uuid, "auto", 4)) ||
                  (cl == 11 && !strncmp(n->uuid, "aes-128-gcm", 11)) ||
                  (cl == 17 && !strncmp(n->uuid, "chacha20-poly1305", 17)) ||
                  (cl == 22 && !strncmp(n->uuid, "chacha20-ietf-poly1305", 22)) ||
                  (cl == 4 && !strncmp(n->uuid, "none", 4)) ||
                  (cl == 4 && !strncmp(n->uuid, "zero", 4)))) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "vmess: unsupported cipher");
                return 1;
            }
            id = col + 1;
        }
        switch (vless_uuid_form(id)) {
        case VLESS_UUID_EMPTY:
            snprintf(n->skip_reason, sizeof(n->skip_reason), "id is empty");
            return 1;
        case VLESS_UUID_GAP:
            snprintf(n->skip_reason, sizeof(n->skip_reason), "id: 31 characters, need a UUID");
            return 1;
        case VLESS_UUID_TOOLONG:
            snprintf(n->skip_reason, sizeof(n->skip_reason), "id longer than a UUID");
            return 1;
        case VLESS_UUID_NOTHEX:
            snprintf(n->skip_reason, sizeof(n->skip_reason), "UUID with an invalid character");
            return 1;
        default:
            break;
        }
        return sl_link_usable_post(n);
    }

    /* The user id, by Xray's rule (vless_uuid_form): a hex UUID of 32-36 characters, or a
     * string of up to 30 characters that is hashed into a UUID. Panels use both, and
     * "TMG_74317ba5f91" is a valid id. Unusable: an empty string, exactly 31 characters (too
     * long to hash, too short for a UUID), longer than a UUID, or a UUID-length string with a
     * character that is not hex. */
    switch (vless_uuid_form(n->uuid)) {
    case VLESS_UUID_EMPTY:
        snprintf(n->skip_reason, sizeof(n->skip_reason), "id is empty");
        return 1;
    case VLESS_UUID_GAP:
        snprintf(n->skip_reason, sizeof(n->skip_reason),
                 "id: 31 characters, need a UUID");
        return 1;
    case VLESS_UUID_TOOLONG:
        snprintf(n->skip_reason, sizeof(n->skip_reason), "id longer than a UUID");
        return 1;
    case VLESS_UUID_NOTHEX:
        snprintf(n->skip_reason, sizeof(n->skip_reason), "UUID with an invalid character");
        return 1;
    default:
        break;
    }

    if (sl_link_usable_post(n)) return 1;
    if (n->fm || (vless_dl(n) && vless_dl(n)->fm)) {
        struct vless_node c = *n;
        vless_name_cut(c.name);
        if (vless_node_link_len(&c) >= VLESS_LINK_MAX) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "finalmask: too long for a link");
            return 1;
        }
    }
    return 0;
}

/* ---- a subscription as an Xray config -----------------------------------------------------------
 *
 * Panels that bind subscriptions to devices pick the format of the answer by User-Agent, and a
 * list of vless:// links may not be among the formats at all. An unknown client (curl, sing-box,
 * Nekoray) may get a stub of ss:// links to localhost:1234 named "Wrong client"; Happ, v2rayNG
 * and Streisand get an Xray config in JSON, Clash its YAML, SFI a sing-box config.
 *
 * What is read: an array of configs `[{...},{...}]` or one config `{...}`; in each, the
 * `outbounds` whose `protocol` is `vless`. Everything else (dns, routing, inbounds, freedom,
 * blackhole) configures the client, not a node, and is skipped.
 *
 * The parser is small and walks only the expected form; it is not a general JSON parser. It is
 * lenient where a subscription needs it: a node name longer than its buffer is cut, not
 * rejected, since a long label must not cost the node. The subscription tests
 * (tests/submatch.c) build this file without libraries.
 */
struct sj { const char *p; };

static void sj_ws(struct sj *j) {
    while (*j->p == ' ' || *j->p == '\t' || *j->p == '\n' || *j->p == '\r') j->p++;
}

/* A string into buf, cut to n. Escapes are handled only so that \" does not end the string:
 * panels write UTF-8 as is, not \uXXXX. An escaped character is taken literally, which beats a
 * refusal: a name may contain anything. */
static int sj_str(struct sj *j, char *buf, size_t n) {
    sj_ws(j);
    if (*j->p != '"') return -1;
    j->p++;
    size_t i = 0;
    int full = 0;
    while (*j->p && *j->p != '"') {
        char o[4] = { *j->p, 0, 0, 0 };
        size_t ol = 1;
        if (*j->p == '\\' && j->p[1]) j->p += 1 + sl_unescape(j->p + 1, 0, o, &ol);
        else j->p++;
        if (full || i + ol >= n) {
            full = 1;
            continue;
        }
        memcpy(buf + i, o, ol);
        i += ol;
    }
    if (*j->p != '"') return -1;
    j->p++;
    if (n) buf[i] = '\0';
    return 0;
}

/* Skip one value of any type, so that an unknown key is skipped rather than misread. */
static void sj_skip(struct sj *j) {
    sj_ws(j);
    if (*j->p == '"') { char t[8]; sj_str(j, t, sizeof(t)); return; }
    if (*j->p == '{' || *j->p == '[') {
        char open = *j->p, close = open == '{' ? '}' : ']';
        int depth = 0;
        do {
            if (*j->p == '"') { char t[8]; sj_str(j, t, sizeof(t)); continue; }
            if (*j->p == open) depth++;
            else if (*j->p == close) depth--;
            j->p++;
        } while (*j->p && depth > 0);
        return;
    }
    while (*j->p && *j->p != ',' && *j->p != '}' && *j->p != ']') j->p++;
}

static int qp_str(struct sj *j, char *buf, size_t n, int *flag);

/* Enter an object and return its keys one by one: 0 — a key in key, 1 — the object ended,
 * -1 — not an object. The caller reads the value, or must call sj_skip. */
static int obj_key(struct sj *j, int *first, char *key, size_t key_n, int *odd) {
    sj_ws(j);
    if (*first) {
        if (*j->p != '{') return -1;
        j->p++;
        *first = 0;
    } else {
        sj_ws(j);
        if (*j->p == ',') j->p++;
    }
    sj_ws(j);
    if (*j->p == '}') { j->p++; return 1; }
    if ((odd ? qp_str(j, key, key_n, odd) : sj_str(j, key, key_n)) != 0) return -1;
    sj_ws(j);
    if (*j->p != ':') return -1;
    j->p++;
    return 0;
}

static int sj_obj_key(struct sj *j, int *first, char *key, size_t key_n) { return obj_key(j, first, key, key_n, NULL); }

/* The same for an array: 0 — an element starts here, 1 — the array ended, -1 — malformed. */
static int sj_arr_next(struct sj *j, int *first) {
    sj_ws(j);
    if (*first) {
        if (*j->p != '[') return -1;
        j->p++;
        *first = 0;
    } else {
        sj_ws(j);
        if (*j->p == ',') j->p++;
        /* After an element only a comma or the end of the array may follow. Anything else
         * must stop the parse: neither this nor the element reader would move, and malformed
         * JSON (`[null]`, `[}`) would loop forever. */
        else if (*j->p != ']') return -1;
    }
    sj_ws(j);
    if (*j->p == ']') { j->p++; return 1; }
    return 0;
}

/* ws or httpupgrade settings of a config, held until it is known which of the two the node uses.
 * A config may carry both objects (and xhttpSettings) at once, and network, which decides, may
 * come after them; so they are collected here and moved into the node at the end (xray_stream).
 * Otherwise the path from wsSettings would overwrite the path of an xhttp node. */
struct upg_cfg {
    char path[sizeof(((struct vless_node *)0)->path)];
    char host[sizeof(((struct vless_node *)0)->http_host)];
    char headers[sizeof(((struct vless_node *)0)->headers)];
    uint8_t bad;
};

static int ci_eq(const char *a, const char *b) {
    for (; *a && *b; a++, b++) {
        char x = *a, y = *b;
        if (x >= 'A' && x <= 'Z') x = (char)(x + 32);
        if (y >= 'A' && y <= 'Z') y = (char)(y + 32);
        if (x != y) return 0;
    }
    return *a == *b;
}

/* headers of an Xray config (map[string]string) into "Name: value\n" lines.
 *
 * A bad header marks the node (bad) rather than being dropped: the headers are part of the look
 * the seller chose for the server or the CDN in front of it, and a node without them may answer
 * 403 and look dead. Bad:
 *   - a value that is not a string: Xray does not load such a config;
 *   - a name not of HTTP token characters or longer than 40, a value with a control character (a
 *     line break would turn one header into two) or longer than 250: the request is built from
 *     these strings;
 *   - for ws, Upgrade, Connection and Sec-WebSocket-Key/Version/Extensions: gorilla refuses them
 *     ("duplicate header not allowed"), so the node would not open with Xray either. httpupgrade
 *     in Xray accepts them (the key as written; the transport sets Connection and Upgrade on top
 *     with its canonical keys), and so does this parser: the request follows Xray (trupgrade.c);
 *   - Host for httpupgrade: Xray rejects the config (`"headers" can't contain "host"`). For
 *     ws, Xray moves Host from headers into host (if that is empty) and drops it from headers,
 *     and so does this parser;
 *   - more than the node's buffer holds. */
static void xray_headers(struct sj *j, struct upg_cfg *u, int hu) {
    int f = 1;
    char key[64], val[256];
    size_t o = strlen(u->headers);
    while (sj_obj_key(j, &f, key, sizeof(key)) == 0) {
        sj_ws(j);
        if (*j->p != '"') { sj_skip(j); u->bad = 1; continue; }
        val[0] = '\0';
        sj_str(j, val, sizeof(val));
        size_t kn = strlen(key), vn = strlen(val);
        if (ci_eq(key, "host")) {
            if (hu) u->bad = 1;
            else if (!u->host[0]) sl_set_field(u->host, sizeof(u->host), val, vn);
            continue;
        }
        int ok = sl_hdr_valid(key, val);
        if (!hu && (ci_eq(key, "upgrade") || ci_eq(key, "connection") ||
                    ci_eq(key, "sec-websocket-key") || ci_eq(key, "sec-websocket-version") ||
                    ci_eq(key, "sec-websocket-extensions")))
            ok = 0;
        if (!ok || o + kn + 2 + vn + 1 >= sizeof(u->headers)) { u->bad = 1; continue; }
        o += (size_t)snprintf(u->headers + o, sizeof(u->headers) - o, "%s: %s\n", key, val);
    }
}

/* wsSettings and httpupgradeSettings: path, host, headers. An empty host does not overwrite Host
 * from headers (in Xray an empty host means "not set"). */
static void xray_upg(struct sj *j, struct upg_cfg *u, int hu) {
    int f = 1;
    char k[64];
    while (sj_obj_key(j, &f, k, sizeof(k)) == 0) {
        sj_ws(j);
        if (!strcmp(k, "path") && *j->p == '"') sj_str(j, u->path, sizeof(u->path));
        else if (!strcmp(k, "host") && *j->p == '"') {
            char h[sizeof(u->host)] = "";
            sj_str(j, h, sizeof(h));
            if (h[0]) snprintf(u->host, sizeof(u->host), "%s", h);
        } else if (!strcmp(k, "headers") && *j->p == '{') xray_headers(j, u, hu);
        else sj_skip(j);
    }
}

static int sj_bool(struct sj *j);

struct xh_set { struct vless_node n; struct sl_xh a; struct upg_cfg h; struct sl_dl *dl; };

struct hx;
static void xray_stream(struct sj *j, struct vless_node *n, struct sl_dl *d, struct hx *h);

static const char XH_NOT_A_STRING[] = "\"";

static int xh_token(struct sj *j, char *buf, size_t cap) {
    sj_ws(j);
    if (*j->p == '"') return sj_str(j, buf, cap);
    size_t i = 0;
    while ((*j->p >= '0' && *j->p <= '9') || (*j->p >= 'a' && *j->p <= 'z') || *j->p == '-' || *j->p == '.' ||
           *j->p == '+') {
        if (i + 1 < cap) buf[i++] = *j->p;
        j->p++;
    }
    buf[i] = '\0';
    return i ? 0 : -1;
}

static int xh_digits(const char *s) {
    if (!*s) return 0;
    for (; *s; s++)
        if (*s < '0' || *s > '9') return 0;
    return 1;
}

static int xh_range_obj(struct sj *j, char *buf, size_t cap) {
    int f = 1, r;
    char k[16], from[24] = "", to[24] = "";
    while ((r = sj_obj_key(j, &f, k, sizeof k)) == 0) {
        char *dst = !strcasecmp(k, "from") ? from : !strcasecmp(k, "to") ? to : NULL;
        sj_ws(j);
        if (!dst || *j->p == '"') sj_skip(j);
        else if (xh_token(j, dst, sizeof from)) return -1;
    }
    if (r != 1) return -1;
    buf[0] = '\0';
    if (!xh_digits(from) || !xh_digits(to)) return 1;
    snprintf(buf, cap, "%s-%s", from, to);
    return 0;
}

static int xh_string(struct sj *j, char *buf, size_t cap, int *nul) {
    const char *b = j->p;
    if (sj_str(j, buf, cap)) return -1;
    for (const char *p = b; p + 6 <= j->p; p++)
        if (!strncmp(p, "\\u0000", 6)) *nul = 1;
    return 0;
}

static int xh_value(struct sj *j, struct sl_xh *a, const char *k) {
    char v[sizeof a->buf];
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return 0; }
    if (*j->p == '{' && (!strcasecmp(k, "uplinkChunkSize") || !strcasecmp(k, "sessionIDLength") ||
                         !strcasecmp(k, "scMinPostsIntervalMs"))) {
        int r = xh_range_obj(j, v, sizeof v);
        if (r < 0) return -1;
        if (r) { sl_xh_put(a, k, XH_NOT_A_STRING, 1); return 0; }
    } else if (*j->p == '"') {
        int nul = 0;
        if (xh_string(j, v, sizeof v, &nul)) return -1;
        if (nul) { sl_xh_put(a, k, XH_NOT_A_STRING, 1); return 0; }
    } else if (*j->p == '{' || *j->p == '[') {
        const char *b = j->p;
        sj_skip(j);
        if (j->p == b) return -1;
        sl_xh_put(a, k, XH_NOT_A_STRING, 1);
        return 0;
    } else if (xh_token(j, v, sizeof v)) {
        return -1;
    }
    sl_xh_put(a, k, v, strlen(v));
    return 0;
}

static int xh_xmux(struct sj *j, struct sl_xh *a) {
    int f = 1, r;
    char k[64], name[80];
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return 0; }
    while ((r = sj_obj_key(j, &f, k, sizeof k)) == 0) {
        snprintf(name, sizeof name, "xmux.%s", k);
        sj_ws(j);
        if (!strcasecmp(k, "hKeepAlivePeriod") && *j->p == '"') {
            const char *b = j->p;
            sj_skip(j);
            if (j->p == b) return -1;
            sl_xh_put(a, name, XH_NOT_A_STRING, 1);
        } else if (xh_value(j, a, name)) return -1;
    }
    return r == 1 ? 0 : -1;
}

static int xh_dl(struct sj *j, struct xh_set *s, int dl_ok) {
    const char *b = j->p;
    if (!dl_ok) { sj_skip(j); return j->p == b ? -1 : 0; }
    free(s->dl);
    s->dl = NULL;
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return 0; }
    s->dl = calloc(1, sizeof *s->dl);
    if (!s->dl) return -1;
    if (*j->p == '{') xray_stream(j, &s->dl->n, s->dl, NULL);
    else { sj_skip(j); s->dl->bad = 1; }
    return j->p == b ? -1 : 0;
}

static int xh_obj(struct sj *j, struct xh_set *s, struct xh_set *ex, int *ex_on, int dl_ok) {
    int f = 1, r;
    char k[64], v[48];
    while ((r = sj_obj_key(j, &f, k, sizeof k)) == 0) {
        sj_ws(j);
        if (ex && !strcasecmp(k, "path")) { if (sj_str(j, s->n.path, sizeof s->n.path)) return -1; }
        else if (ex && !strcasecmp(k, "mode")) { if (sj_str(j, s->n.mode, sizeof s->n.mode)) return -1; }
        else if (ex && !strcasecmp(k, "host")) { if (sj_str(j, s->n.http_host, sizeof s->n.http_host)) return -1; }
        else if (!strcasecmp(k, "downloadSettings")) { if (xh_dl(j, s, dl_ok)) return -1; }
        else if (!strcasecmp(k, "xmux")) { if (xh_xmux(j, &s->a)) return -1; }
        else if (!strcasecmp(k, "xPaddingBytes") || !strcasecmp(k, "scMaxEachPostBytes")) {
            if (*j->p == '{' ? xh_range_obj(j, v, sizeof v) < 0 : xh_token(j, v, sizeof v)) return -1;
            if (!strcasecmp(k, "xPaddingBytes")) sl_pad_range(&s->n, v); else sl_post_range(&s->n, v);
        }
        else if (!strcasecmp(k, "headers")) {
            if (*j->p == '{') xray_headers(j, &s->h, 1);
            else { sj_skip(j); s->h.bad = 1; }
        }
        else if (!strcasecmp(k, "fp_id") || !strcasecmp(k, "client_id")) { if (sj_str(j, s->n.fp_id, sizeof s->n.fp_id)) return -1; }
        else if (!strcasecmp(k, "fp_secret")) { if (sj_str(j, s->n.fp_secret, sizeof s->n.fp_secret)) return -1; }
        else if (!strcasecmp(k, "fp_method")) { if (sj_str(j, s->n.fp_method, sizeof s->n.fp_method)) return -1; }
        else if (ex && !strcasecmp(k, "extra")) {
            free(ex->dl);
            memset(ex, 0, sizeof *ex);
            *ex_on = 1;
            if (!strncmp(j->p, "null", 4)) j->p += 4;
            else if (xh_obj(j, ex, NULL, NULL, dl_ok)) return -1;
        }
        else if (xh_value(j, &s->a, k)) return -1;
    }
    return r == 1 ? 0 : -1;
}

static void xh_apply(struct vless_node *n, const struct xh_set *s) {
    n->pad_from = s->n.pad_from, n->pad_to = s->n.pad_to;
    n->post_from = s->n.post_from, n->post_to = s->n.post_to;
    if (s->h.headers[0] || s->h.bad) {
        snprintf(n->headers, sizeof(n->headers), "%s", s->h.headers);
        n->headers_bad = s->h.bad;
    }
    if (s->n.fp_id[0]) snprintf(n->fp_id, sizeof(n->fp_id), "%s", s->n.fp_id);
    if (s->n.fp_secret[0]) snprintf(n->fp_secret, sizeof(n->fp_secret), "%s", s->n.fp_secret);
    if (s->n.fp_method[0]) snprintf(n->fp_method, sizeof(n->fp_method), "%s", s->n.fp_method);
    sl_xh_done(n, &s->a);
    if (s->dl) sl_dl_done(n, s->dl);
}

void sl_parse_extra(struct vless_node *n, const char *extra) {
    struct xh_set *s = calloc(1, sizeof *s);
    if (!s) { n->xhx = SL_BAD_XHX_JSON; return; }
    struct sj j = { extra };
    int bad = xh_obj(&j, s, NULL, NULL, 1);
    sj_ws(&j);
    if (bad || *j.p) n->xhx = SL_BAD_XHX_JSON;
    else xh_apply(n, s);
    free(s->dl);
    free(s);
}

struct xh_cfg { struct xh_set s[2]; int ex_on, bad; };

static void xh_cfg_free(struct xh_cfg *c) {
    if (!c) return;
    free(c->s[0].dl);
    free(c->s[1].dl);
    free(c);
}

static struct xh_cfg *xh_settings(struct sj *j, struct xh_cfg *c, int dl_ok) {
    if (c) { free(c->s[0].dl); free(c->s[1].dl); }
    else c = malloc(sizeof *c);
    if (!c) { sj_skip(j); return NULL; }
    memset(c, 0, sizeof *c);
    c->bad = xh_obj(j, &c->s[0], &c->s[1], &c->ex_on, dl_ok);
    return c;
}

static void xh_settings_apply(struct vless_node *n, const struct xh_cfg *c, int seen) {
    if (!seen || strcmp(n->type, "xhttp")) return;
    if (!c || c->bad) { n->xhx = SL_BAD_XHX_JSON; return; }
    snprintf(n->path, sizeof(n->path), "%s", c->s[0].n.path);
    snprintf(n->mode, sizeof(n->mode), "%s", c->s[0].n.mode);
    snprintf(n->http_host, sizeof(n->http_host), "%s", c->s[0].n.http_host);
    xh_apply(n, &c->s[c->ex_on]);
}

static uint16_t xray_port(struct sj *j) {
    sj_ws(j);
    char num[16] = "";
    if (*j->p == '"') sj_str(j, num, sizeof(num));
    else {
        size_t i = 0;
        while (*j->p >= '0' && *j->p <= '9' && i + 1 < sizeof(num)) num[i++] = *j->p++;
        num[i] = '\0';
        if (!i) sj_skip(j);
    }
    return sl_port_of(num);
}

static int xray_present(struct sj *j) {
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return 0; }
    sj_skip(j);
    return 1;
}

static uint8_t alpn_of(const char *v) {
    return !strcmp(v, "h3") ? 1 : !strcmp(v, "http/1.1") ? 2 : 0;
}

static void xray_alpn(struct sj *j, struct sl_dl *d) {
    char v[64] = "";
    sj_ws(j);
    if (*j->p == '"') {
        sj_str(j, v, sizeof v);
        d->alpn = alpn_of(v);
        return;
    }
    if (*j->p != '[') { sj_skip(j); return; }
    int f = 1;
    size_t cnt = 0;
    while (sj_arr_next(j, &f) == 0) {
        cnt++;
        sj_ws(j);
        if (*j->p != '"') { sj_skip(j); d->bad = 1; continue; }
        if (sj_str(j, v, sizeof v)) break;
    }
    d->alpn = cnt == 1 ? alpn_of(v) : 0;
}


struct hx {
    struct hy2_in in;
    unsigned long sver, hver;
};

static int hx_str(struct sj *j, char *buf, size_t cap, int *nul) {
    sj_ws(j);
    buf[0] = '\0';
    if (*j->p == '"') return xh_string(j, buf, cap, nul);
    sj_skip(j);
    return -1;
}

static int hx_tok(struct sj *j, char *buf, size_t cap) {
    sj_ws(j);
    buf[0] = '\0';
    if (*j->p == '{' || *j->p == '[') { sj_skip(j); return -1; }
    const char *b = j->p;
    int r = xh_token(j, buf, cap);
    if (r && j->p == b) sj_skip(j);
    sj_ws(j);
    if (!r && *j->p && *j->p != ',' && *j->p != '}' && *j->p != ']') {
        sj_skip(j);
        buf[0] = '\0';
        return -1;
    }
    if (!strcmp(buf, "null")) buf[0] = '\0';
    return r;
}

static int hx_range(const char *t, unsigned long *a, unsigned long *b) {
    const char *dash = strchr(t, '-');
    size_t al = dash ? (size_t)(dash - t) : strlen(t);
    if (!al || al > 6 || (dash && (!dash[1] || strlen(dash + 1) > 6))) return 0;
    for (size_t i = 0; i < al; i++) if (t[i] < '0' || t[i] > '9') return 0;
    for (const char *p = dash ? dash + 1 : t + al; *p; p++) if (*p < '0' || *p > '9') return 0;
    *a = strtoul(t, NULL, 10);
    *b = dash ? strtoul(dash + 1, NULL, 10) : *a;
    return 1;
}

static int bps_fail(char *why, size_t why_n, const char *fmt, const char *arg) {
    if (why_n) snprintf(why, why_n, fmt, arg);
    return -1;
}

static const char *shown(const char *s, char *out, size_t n) {
    size_t o = 0;
    for (; *s && o + 1 < n; s++) out[o++] = (unsigned char)*s < 0x20 || *s == 0x7f ? '?' : *s;
    out[o] = '\0';
    return out;
}

static int bps_space(char c) { return c == ' ' || c == '\t' || c == '\n' || c == '\v' || c == '\f' || c == '\r'; }

static int xray_bps(const char *s, uint64_t *bytes, char *why, size_t why_n) {
    char t[256];
    size_t n = 0;
    while (bps_space(*s)) s++;
    for (; *s && n + 1 < sizeof t; s++) t[n++] = (*s >= 'A' && *s <= 'Z') ? (char)(*s + 32) : *s;
    while (n && bps_space(t[n - 1])) n--;
    t[n] = '\0';
    *bytes = 0;
    if (!n) return 0;
    size_t i = 0, digits = 0, dots = 0;
    for (; t[i] == '.' || (t[i] >= '0' && t[i] <= '9'); i++) {
        if (t[i] == '.') dots++; else digits++;
    }
    char num[sizeof t];
    memcpy(num, t, i);
    num[i] = '\0';
    if (!digits || dots > 1) return bps_fail(why, why_n, "strconv.ParseFloat: parsing \"%s\": invalid syntax", num);
    double val = strtod(num, NULL);
    if (val > 1.7976931348623157e308) return bps_fail(why, why_n, "strconv.ParseFloat: parsing \"%s\": value out of range", num);
    const char *u = t + i;
    while (bps_space(*u)) u++;
    static const struct { const char *name[3]; double mul; } units[] = {
        { { "", "b", "bps" }, 1 }, { { "k", "kb", "kbps" }, 1024 }, { { "m", "mb", "mbps" }, 1048576 },
        { { "g", "gb", "gbps" }, 1073741824 }, { { "t", "tb", "tbps" }, 1099511627776.0 },
    };
    double mul = 0;
    for (size_t k = 0; k < sizeof units / sizeof *units && !mul; k++)
        for (size_t m = 0; m < 3; m++)
            if (!strcmp(u, units[k].name[m])) mul = units[k].mul;
    char us[sizeof t];
    if (!mul) return bps_fail(why, why_n, "unsupported unit: %s", shown(u, us, sizeof us));
    double v = val * mul;
    *bytes = v > 1e18 ? (uint64_t)1000000000000000000ull / 8 : (uint64_t)v / 8;
    return 0;
}

enum { QPK_STR, QPK_UINT, QPK_INT, QPK_BOOL };
enum {
    QP_CONG, QP_PROFILE, QP_UP, QP_DOWN, QP_ISW, QP_MSW, QP_ICW, QP_MCW, QP_IDLE, QP_KEEP, QP_STREAMS,
    QP_DEBUG, QP_LOSS, QP_PMTUD, QP_PARROT, QP_GSO, QP_SRESET, QP_N
};

static const struct { const char *name; int kind; } qp_keys[QP_N] = {
    { "congestion", QPK_STR }, { "bbrProfile", QPK_STR }, { "brutalUp", QPK_STR }, { "brutalDown", QPK_STR },
    { "initStreamReceiveWindow", QPK_UINT }, { "maxStreamReceiveWindow", QPK_UINT },
    { "initConnectionReceiveWindow", QPK_UINT }, { "maxConnectionReceiveWindow", QPK_UINT },
    { "maxIdleTimeout", QPK_INT }, { "keepAlivePeriod", QPK_INT }, { "maxIncomingStreams", QPK_INT },
    { "debug", QPK_BOOL }, { "brutalDisableLossCompensation", QPK_BOOL }, { "disablePathMTUDiscovery", QPK_BOOL },
    { "disableChromeParrot", QPK_BOOL }, { "disableGSO", QPK_BOOL }, { "disableStatelessReset", QPK_BOOL },
};

#define QP_STR_LONG 1
#define QP_STR_NUL 2

struct qp_vals {
    char str[4][128];
    int sflag[4];
    uint64_t u[4];
    int64_t i[3];
    int b[6];
};

static int qp_str(struct sj *j, char *buf, size_t n, int *flag) {
    sj_ws(j);
    if (*j->p != '"') return -1;
    j->p++;
    size_t i = 0;
    *flag = 0;
    while (*j->p && *j->p != '"') {
        char o[4] = { *j->p, 0, 0, 0 };
        size_t ol = 1;
        if (*j->p == '\\' && j->p[1]) j->p += 1 + sl_unescape(j->p + 1, 0, o, &ol);
        else j->p++;
        if (!ol) { *flag |= QP_STR_NUL; continue; }
        if ((*flag & QP_STR_LONG) || i + ol >= n) { *flag |= QP_STR_LONG; continue; }
        memcpy(buf + i, o, ol);
        i += ol;
    }
    if (*j->p != '"') return -1;
    j->p++;
    buf[i] = '\0';
    return 0;
}

static int qp_scalar(struct sj *j, char *tok, size_t cap) {
    sj_ws(j);
    size_t n = 0;
    while (*j->p && *j->p != ',' && *j->p != '}' && *j->p != ']' && !bps_space(*j->p)) {
        if (n + 1 < cap) tok[n] = *j->p;
        n++;
        j->p++;
    }
    tok[n < cap ? n : cap - 1] = '\0';
    return n ? 0 : -1;
}

static int qp_uint(const char *t, uint64_t *v) {
    if (!*t || (t[0] == '0' && t[1])) return -1;
    uint64_t x = 0;
    for (; *t; t++) {
        if (*t < '0' || *t > '9' || x > (UINT64_MAX - (uint64_t)(*t - '0')) / 10) return -1;
        x = x * 10 + (uint64_t)(*t - '0');
    }
    *v = x;
    return 0;
}

static int qp_int(const char *t, int64_t *v) {
    int neg = *t == '-';
    uint64_t x;
    if (neg) t++;
    if (qp_uint(t, &x)) return -1;
    if (x > (uint64_t)INT64_MAX + (uint64_t)neg) return -1;
    *v = neg ? (int64_t)(0 - x) : (int64_t)x;
    return 0;
}

static int qp_read(struct sj *j, int key, struct qp_vals *v, char *why, size_t why_n) {
    static const char *const kind_name[] = { "string", "number", "number", "boolean" };
    int kind = qp_keys[key].kind, bad = 0;
    char tok[40];
    sj_ws(j);
    if (!strncmp(j->p, "null", 4) && (!j->p[4] || strchr(", \t\r\n}]", j->p[4]))) { j->p += 4; return 0; }
    if (kind == QPK_STR) {
        char *dst = v->str[key];
        if (*j->p != '"') bad = 1, sj_skip(j);
        else if (qp_str(j, dst, sizeof v->str[0], &v->sflag[key])) return -2;
    } else if (*j->p == '"' || *j->p == '{' || *j->p == '[') {
        bad = 1;
        sj_skip(j);
    } else if (qp_scalar(j, tok, sizeof tok)) {
        return -2;
    } else if (kind == QPK_UINT) {
        bad = qp_uint(tok, &v->u[key - QP_ISW]);
    } else if (kind == QPK_INT) {
        bad = qp_int(tok, &v->i[key - QP_IDLE]);
    } else if (!strcmp(tok, "true") || !strcmp(tok, "false")) {
        v->b[key - QP_DEBUG] = tok[0] == 't';
    } else {
        bad = 1;
    }
    if (bad && why_n && !why[0]) snprintf(why, why_n, "quicParams: %s is not a %s", qp_keys[key].name, kind_name[kind]);
    return 0;
}

static void qp_lower(char *s) {
    for (; *s; s++)
        if (*s >= 'A' && *s <= 'Z') *s = (char)(*s + 32);
}

static int qp_str_fail(const struct qp_vals *v, int key, char *why, size_t why_n, const char *xray) {
    int f = v->sflag[key];
    if (!f) return 0;
    if (!why_n) return -1;
    if (xray && !(f & QP_STR_LONG)) snprintf(why, why_n, "%s", xray);
    else snprintf(why, why_n, "quicParams: %s %s", qp_keys[key].name, f & QP_STR_LONG ? "is too long" : "holds a NUL byte");
    return -1;
}

static int qp_build(struct qp_vals *v, struct hy2_qp *out, char *why, size_t why_n) {
    if (qp_str_fail(v, QP_PROFILE, why, why_n, "unknown bbr profile")) return -1;
    qp_lower(v->str[QP_PROFILE]);
    const char *prof = v->str[QP_PROFILE];
    if (*prof && strcmp(prof, "conservative") && strcmp(prof, "standard") && strcmp(prof, "aggressive"))
        return bps_fail(why, why_n, "%s", "unknown bbr profile");
    if (*prof) out->ignored |= HY2_QP_IGN_PROFILE;
    if (qp_str_fail(v, QP_UP, why, why_n, NULL)) return -1;
    if (xray_bps(v->str[QP_UP], &out->up_bps, why, why_n)) return -1;
    if (qp_str_fail(v, QP_DOWN, why, why_n, NULL)) return -1;
    if (xray_bps(v->str[QP_DOWN], &out->down_bps, why, why_n)) return -1;
    if (out->up_bps > 0 && out->up_bps < 65536) return bps_fail(why, why_n, "%s", "BrutalUp must be at least 65536 bytes per second");
    if (out->down_bps > 0 && out->down_bps < 65536) return bps_fail(why, why_n, "%s", "BrutalDown must be at least 65536 bytes per second");
    if (qp_str_fail(v, QP_CONG, why, why_n, NULL)) return -1;
    qp_lower(v->str[QP_CONG]);
    const char *cc = v->str[QP_CONG];
    if (!*cc || !strcmp(cc, "brutal")) out->cc = HY2_CC_AUTO;
    else if (!strcmp(cc, "reno")) out->cc = HY2_CC_RENO;
    else if (!strcmp(cc, "bbr")) out->cc = HY2_CC_BBR;
    else if (!strcmp(cc, "force-brutal")) {
        if (!out->up_bps) return bps_fail(why, why_n, "%s", "force-brutal requires up");
        out->cc = HY2_CC_FORCE;
    } else {
        char cs[128];
        if (why_n) snprintf(why, why_n, "unknown congestion control: %s, valid values: reno, bbr, brutal, force-brutal", shown(cc, cs, sizeof cs));
        return -1;
    }
    static const char *const win_name[4] = {
        "InitStreamReceiveWindow", "MaxStreamReceiveWindow", "InitConnectionReceiveWindow", "MaxConnectionReceiveWindow",
    };
    uint64_t *win[4] = { &out->init_stream_win, &out->max_stream_win, &out->init_conn_win, &out->max_conn_win };
    for (int k = 0; k < 4; k++) {
        if (v->u[k] > 0 && v->u[k] < 16384) {
            if (why_n) snprintf(why, why_n, "%s must be at least 16384", win_name[k]);
            return -1;
        }
        *win[k] = v->u[k];
    }
    if (v->i[0] != 0 && (v->i[0] < 4 || v->i[0] > 120)) return bps_fail(why, why_n, "%s", "MaxIdleTimeout must be between 4 and 120");
    if (v->i[1] != 0 && (v->i[1] < 2 || v->i[1] > 60)) return bps_fail(why, why_n, "%s", "KeepAlivePeriod must be between 2 and 60");
    if (v->i[2] != 0 && v->i[2] < 8) return bps_fail(why, why_n, "%s", "MaxIncomingStreams must be at least 8");
    out->idle_s = (unsigned)v->i[0];
    out->keepalive_s = (unsigned)v->i[1];
    out->no_loss_comp = (uint8_t)v->b[QP_LOSS - QP_DEBUG];
    out->no_pmtud = (uint8_t)v->b[QP_PMTUD - QP_DEBUG];
    if (v->b[0]) out->ignored |= HY2_QP_IGN_DEBUG;
    out->no_parrot = (uint8_t)v->b[QP_PARROT - QP_DEBUG];
    if (v->b[QP_GSO - QP_DEBUG]) out->ignored |= HY2_QP_IGN_GSO;
    if (v->i[2]) out->ignored |= HY2_QP_IGN_STREAMS;
    if (v->b[QP_SRESET - QP_DEBUG]) out->ignored |= HY2_QP_IGN_SRESET;
    return 0;
}

int hy2_qp_parse(const char *json, size_t n, struct hy2_qp *out, char *why, size_t why_n) {
    char buf[2049];
    struct qp_vals v;
    struct sj j;
    memset(out, 0, sizeof *out);
    memset(&v, 0, sizeof v);
    if (why_n) why[0] = '\0';
    if (n >= sizeof buf) return bps_fail(why, why_n, "%s", "quicParams is too long");
    memcpy(buf, json, n);
    buf[n] = '\0';
    j.p = buf;
    int f = 1, r;
    char k[32];
    int odd = 0;
    while ((r = obj_key(&j, &f, k, sizeof k, &odd)) == 0) {
        int key = 0;
        while (key < QP_N && strcasecmp(k, qp_keys[key].name)) key++;
        if (key == QP_N || odd) { sj_skip(&j); continue; }
        if (qp_read(&j, key, &v, why, why_n)) { r = -1; break; }
    }
    if (r != 1) {
        if (why_n) snprintf(why, why_n, "quicParams is not an object");
        return -1;
    }
    if (why_n && why[0]) return -1;
    return qp_build(&v, out, why, why_n);
}

static void hx_quic(struct sj *j, struct hx *h) {
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return; }
    if (*j->p != '{') { sj_skip(j); hy2_in_fail(&h->in, "finalmask quicParams is not an object"); return; }
    const char *b = j->p;
    sj_skip(j);
    struct hy2_qp q;
    char why[sizeof h->in.why];
    if (hy2_qp_parse(b, (size_t)(j->p - b), &q, why, sizeof why)) hy2_in_fail(&h->in, why);
    h->in.up_mbps = hy2_mbps(q.up_bps);
    h->in.down_mbps = hy2_mbps(q.down_bps);
    h->in.qp = b;
    h->in.qp_n = (size_t)(j->p - b);
}

static void hx_salamander(struct sj *j, struct hx *h) {
    int f = 1;
    char k[32], pw[HY2_SECRET_MAX + 2], ps[24] = "";
    int nul = 0;
    pw[0] = '\0';
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcasecmp(k, "password")) { if (hx_str(j, pw, sizeof pw, &nul)) pw[0] = '\0'; }
        else if (!strcasecmp(k, "packetSize")) { if (hx_tok(j, ps, sizeof ps)) snprintf(ps, sizeof ps, "?"); }
        else sj_skip(j);
    }
    unsigned long a = 0, b = 0;
    if (ps[0] && (!hx_range(ps, &a, &b) || a || b)) hy2_in_fail(&h->in, "salamander packetSize is not supported");
    snprintf(h->in.obfs, sizeof h->in.obfs, "salamander");
    hy2_in_secret(&h->in, h->in.obfs_pw, pw, strlen(pw), nul);
}

static int hx_ip_ok(const char *s) {
    char a[48];
    const char *sl = strchr(s, '/');
    size_t l = sl ? (size_t)(sl - s) : strlen(s);
    unsigned char buf[16];
    if (l >= sizeof a) return 0;
    memcpy(a, s, l);
    a[l] = '\0';
    int v6 = strchr(a, ':') != NULL;
    if (inet_pton(v6 ? AF_INET6 : AF_INET, a, buf) != 1) return 0;
    if (!sl) return 1;
    const char *d = sl + 1;
    size_t dl = strlen(d);
    if (!dl || dl > 3 || (dl > 1 && d[0] == '0')) return 0;
    for (size_t i = 0; i < dl; i++)
        if (d[i] < '0' || d[i] > '9') return 0;
    return atoi(d) <= (v6 ? 128 : 32);
}

static int hx_ips(struct sj *j, struct hy2_in *in) {
    static const char notlist[] = "remoteIPs is not a list of addresses";
    int f = 1, n = 0;
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return 0; }
    if (*j->p != '[') { sj_skip(j); hy2_in_fail(in, notlist); return 0; }
    while (sj_arr_next(j, &f) == 0) {
        char e[64];
        sj_ws(j);
        if (*j->p != '"' || sj_str(j, e, sizeof e)) {
            sj_skip(j);
            hy2_in_fail(in, notlist);
        } else if (!hx_ip_ok(e)) {
            char why[96];
            size_t o = (size_t)snprintf(why, sizeof why, "invalid ip ");
            for (size_t i = 0; e[i] && o + 1 < sizeof why; i++)
                why[o++] = (unsigned char)e[i] < 0x20 || e[i] == 0x7f ? '?' : e[i];
            why[o] = '\0';
            hy2_in_fail(in, why);
        }
        n++;
    }
    return n > 0;
}

static void hx_udphop(struct sj *j, struct hx *h) {
    int f = 1;
    char k[32], mode[48], iv[24] = "", ports[1024] = "";
    int have_mode = 0, bad_ports = 0, bad_iv = 0, nul = 0, long_ports = 0, ips = 0;
    mode[0] = '\0';
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcasecmp(k, "mode")) have_mode = hx_str(j, mode, sizeof mode, &nul) == 0;
        else if (!strcasecmp(k, "interval")) bad_iv = hx_tok(j, iv, sizeof iv) != 0;
        else if (!strcasecmp(k, "remoteIPs")) ips = hx_ips(j, &h->in);
        else if (!strcasecmp(k, "remotePorts")) {
            char raw[sizeof ports];
            bad_ports = hx_tok(j, raw, sizeof raw) != 0;
            long_ports = strlen(raw) + 1 >= sizeof raw;
            size_t o = 0;
            for (const char *p = raw; *p; ) {
                const char *e = strchr(p, ',');
                const char *ie = e ? e : p + strlen(p);
                const char *ib = p;
                while (ib < ie && (*ib == ' ' || *ib == '\t' || *ib == '\n' || *ib == '\r' || *ib == '\v' || *ib == '\f')) ib++;
                while (ie > ib && (ie[-1] == ' ' || ie[-1] == '\t' || ie[-1] == '\n' || ie[-1] == '\r' || ie[-1] == '\v' || ie[-1] == '\f')) ie--;
                if (ie > ib && o + (size_t)(ie - ib) + 2 < sizeof ports) {
                    if (o) ports[o++] = ',';
                    memcpy(ports + o, ib, (size_t)(ie - ib));
                    o += (size_t)(ie - ib);
                }
                if (!e) break;
                p = e + 1;
            }
            ports[o] = '\0';
        }
        else sj_skip(j);
    }
    if (!have_mode) mode[0] = '\0';
    char why[96];
    unsigned mb = 0;
    for (const char *p = mode;;) {
        const char *e = strchr(p, ',');
        size_t pl = e ? (size_t)(e - p) : strlen(p);
        if (pl == 13 && !strncasecmp(p, "intervallocal", 13)) mb |= HY2_HOP_LOCAL;
        else if (pl == 14 && !strncasecmp(p, "intervalremote", 14)) mb |= HY2_HOP_REMOTE;
        else if (pl == 13 && !strncasecmp(p, "perconnremote", 13)) mb |= HY2_HOP_ONCE;
        else {
            size_t o = (size_t)snprintf(why, sizeof why, "invalid mode ");
            for (size_t i = 0; i < pl && o + 1 < sizeof why; i++)
                why[o++] = (unsigned char)p[i] < 0x20 || p[i] == 0x7f ? '?' : p[i];
            why[o] = '\0';
            hy2_in_fail(&h->in, why);
            return;
        }
        if (!e) break;
        p = e + 1;
    }
    unsigned hops = mb & (HY2_HOP_REMOTE | HY2_HOP_ONCE);
    if (bad_ports) { hy2_in_fail(&h->in, "remotePorts: not a port list"); return; }
    if (long_ports && hops) { hy2_in_fail(&h->in, "port list too long"); return; }
    struct hy2_in t;
    memset(&t, 0, sizeof t);
    if (!long_ports) hy2_in_ports(&t, ports, strlen(ports), "remotePorts");
    if (t.why[0] && (hops || strcmp(t.why, "port list too long"))) { hy2_in_fail(&h->in, t.why); return; }
    if (hops) {
        memcpy(h->in.ports, t.ports, sizeof t.ports);
        h->in.first = t.first;
    }
    h->in.hop_mode = (uint8_t)(mb | (hops && ips ? HY2_HOP_IPS : 0u));
    const char *iw = bad_iv ? "interval is not a number or range" : hy2_interval(iv, &h->in.hop_from, &h->in.hop_to);
    if (iw) hy2_in_fail(&h->in, iw);
}

static void hx_udp(struct sj *j, struct hx *h) {
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return; }
    if (*j->p != '[') { sj_skip(j); hy2_in_fail(&h->in, "finalmask udp is not a list"); return; }
    int f = 1, sal = 0, hop = 0;
    while (sj_arr_next(j, &f) == 0) {
        sj_ws(j);
        if (*j->p != '{') { sj_skip(j); hy2_in_fail(&h->in, "finalmask udp mask is not an object"); continue; }
        int fo = 1, nul = 0;
        char k[32], type[24] = "";
        const char *set = NULL;
        while (sj_obj_key(j, &fo, k, sizeof k) == 0) {
            if (!strcasecmp(k, "type")) { if (hx_str(j, type, sizeof type, &nul)) type[0] = '\0'; }
            else if (!strcasecmp(k, "settings")) { sj_ws(j); set = j->p; sj_skip(j); }
            else sj_skip(j);
        }
        struct sj s = { set ? set : "{}" };
        if (!type[0]) hy2_in_fail(&h->in, "finalmask udp mask has no type");
        else if (!strcasecmp(type, "salamander")) {
            if (sal++) hy2_in_fail(&h->in, "finalmask udp salamander twice is not supported");
            else hx_salamander(&s, h);
        } else if (!strcasecmp(type, "udphop")) {
            if (hop++) hy2_in_fail(&h->in, "finalmask udp udphop twice is not supported");
            else hx_udphop(&s, h);
        } else {
            char why[96], t[24];
            snprintf(why, sizeof why, "finalmask udp %s is not supported", shown(type, t, sizeof t));
            hy2_in_fail(&h->in, why);
        }
    }
}

static void hx_finalmask(const char *p, struct hx *h) {
    struct sj j = { p };
    int f = 1;
    char k[32];
    while (sj_obj_key(&j, &f, k, sizeof k) == 0) {
        if (!strcasecmp(k, "udp")) hx_udp(&j, h);
        else if (!strcasecmp(k, "quicParams")) hx_quic(&j, h);
        else sj_skip(&j);
    }
}

static unsigned long hx_version(struct sj *j) {
    char v[16];
    return hx_tok(j, v, sizeof v) ? 0 : hy2_digits(v, strlen(v), 1000000);
}

static void hx_hysteria(struct sj *j, struct hx *h) {
    sj_ws(j);
    if (*j->p != '{') { sj_skip(j); return; }
    int f = 1, nul;
    char k[32];
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcmp(k, "version")) h->hver = hx_version(j);
        else if (!strcmp(k, "auth")) {
            char a[HY2_SECRET_MAX + 2];
            nul = 0;
            if (hx_str(j, a, sizeof a, &nul)) a[0] = '\0';
            hy2_in_secret(&h->in, h->in.auth, a, strlen(a), nul);
        } else sj_skip(j);
    }
}

/* streamSettings: the transport, security and everything that depends on them. */
static void xray_stream(struct sj *j, struct vless_node *n, struct sl_dl *d, struct hx *h) {
    int first = 1;
    char k[64];
    struct upg_cfg ws, hu;
    struct xh_cfg *xc = NULL;
    int xc_seen = 0;
    memset(&ws, 0, sizeof(ws));
    memset(&hu, 0, sizeof(hu));
    while (sj_obj_key(j, &first, k, sizeof(k)) == 0) {
        if (!strcmp(k, "network")) {
            sj_str(j, n->type, sizeof(n->type));
            /* raw is Xray's name for tcp since 24.9.30. */
            if (!strcmp(n->type, "raw")) snprintf(n->type, sizeof(n->type), "tcp");
            /* websocket is another name for ws in Xray (infra/conf: case "ws", "websocket"). */
            if (!strcmp(n->type, "websocket")) snprintf(n->type, sizeof(n->type), "ws");
            if (d && !strcmp(n->type, "splithttp")) snprintf(n->type, sizeof(n->type), "xhttp");
        }
        else if (d && !strcmp(k, "address")) { sj_str(j, n->host, sizeof(n->host)); sl_host_unbracket(n->host); }
        else if (d && !strcmp(k, "port")) n->port = xray_port(j);
        else if (!strcasecmp(k, "finalmask")) {
            sj_ws(j);
            const char *p0 = j->p;
            sj_skip(j);
            sl_set_fm(n, p0, (size_t)(j->p - p0));
            if (h) hx_finalmask(p0, h);
        }
        else if (h && !strcmp(k, "hysteriaSettings")) hx_hysteria(j, h);
        else if (d && !strcmp(k, "quicParams")) d->quic = (uint8_t)xray_present(j);
        else if (!strcmp(k, "tcpSettings") || !strcmp(k, "rawSettings")) {
            int f2 = 1;
            char k2[64];
            while (sj_obj_key(j, &f2, k2, sizeof(k2)) == 0) {
                if (strcmp(k2, "header") != 0) { sj_skip(j); continue; }
                int f3 = 1;
                char k3[64], ty[16] = "";
                while (sj_obj_key(j, &f3, k3, sizeof(k3)) == 0) {
                    if (!strcmp(k3, "type")) sj_str(j, ty, sizeof ty); else sj_skip(j);
                }
                if (!strcmp(ty, "http")) n->tcp_http = 1;
            }
        }
        else if (!strcmp(k, "wsSettings")) xray_upg(j, &ws, 0);
        else if (!strcmp(k, "httpupgradeSettings")) xray_upg(j, &hu, 1);
        else if (!strcmp(k, "security")) sj_str(j, n->security, sizeof(n->security));
        else if (!strcmp(k, "realitySettings") || !strcmp(k, "tlsSettings")) {
            int tls = k[0] == 't';
            /* Both carry serverName and fingerprint; only reality has publicKey and shortId.
             * One parser serves both because the field names do not clash and a node declares
             * exactly one of the two. */
            int f2 = 1;
            char k2[64];
            while (sj_obj_key(j, &f2, k2, sizeof(k2)) == 0) {
                if (!strcmp(k2, "serverName")) sj_str(j, n->sni, sizeof(n->sni));
                else if (!strcmp(k2, "fingerprint")) sj_str(j, n->fp, sizeof(n->fp));
                else if (!strcmp(k2, "publicKey")) sj_str(j, n->pbk, sizeof(n->pbk));
                else if (!strcmp(k2, "shortId")) sj_str(j, n->sid, sizeof(n->sid));
                else if (!strcmp(k2, "mldsa65Verify") || !strcmp(k2, "pqv")) {
                    char *pv = malloc(4096);
                    if (pv) { pv[0] = '\0'; sj_str(j, pv, 4096); sl_set_pqv(n, pv); free(pv); }
                    else sj_skip(j);
                }
                else if (!strcmp(k2, "pinnedPeerCertSha256") || !strcmp(k2, "pcs")) {
                    char pv[1100] = "";
                    sj_str(j, pv, sizeof pv);
                    sl_add_pins(n, pv, 0);
                }
                else if (!strcmp(k2, "verifyPeerCertByName") || !strcmp(k2, "vcn")) {
                    char vv[300] = "";
                    sj_str(j, vv, sizeof vv);
                    sl_set_vcn(n, vv);
                }
                else if (!strcmp(k2, "allowInsecure")) { if (sj_bool(j)) n->allow_insecure = 1; }
                else if (d && tls && !strcmp(k2, "alpn")) xray_alpn(j, d);
                else if (!strcmp(k2, "echConfigList")) {
                    char ev[1400] = "";
                    sj_str(j, ev, sizeof ev);
                    sl_set_ech(n, ev);
                }
                else sj_skip(j);
            }
        } else if (!strcmp(k, "grpcSettings")) {
            int f2 = 1;
            char k2[64];
            while (sj_obj_key(j, &f2, k2, sizeof(k2)) == 0) {
                if (!strcmp(k2, "serviceName")) sj_str(j, n->service, sizeof(n->service));
                else sj_skip(j);
            }
        } else if (!strcmp(k, "xhttpSettings") || !strcmp(k, "splithttpSettings")) {
            /* splithttpSettings is the old name of xhttpSettings; some panels still use it. */
            xc = xh_settings(j, xc, !d);
            xc_seen = 1;
        } else sj_skip(j);
    }
    xh_settings_apply(n, xc, xc_seen);
    xh_cfg_free(xc);
    const struct upg_cfg *u = !strcmp(n->type, "ws") ? &ws : !strcmp(n->type, "httpupgrade") ? &hu : NULL;
    if (u) {
        snprintf(n->path, sizeof(n->path), "%s", u->path);
        snprintf(n->http_host, sizeof(n->http_host), "%s", u->host);
        snprintf(n->headers, sizeof(n->headers), "%s", u->headers);
        n->headers_bad = u->bad;
    }
}

static void json_encryption(struct sj *j, struct vless_node *n) {
    char *ev = malloc(4096);
    if (!ev) { sj_skip(j); return; }
    ev[0] = '\0';
    if (sj_str(j, ev, 4096) == 0) set_encryption(n, ev);
    free(ev);
}

/* settings of a vless outbound: vnext[0] gives the address, the port and the first user. Only
 * the first: a subscription describes a node for one user. */
static void xray_settings(struct sj *j, struct vless_node *n, struct hx *h) {
    int first = 1;
    char k[64];
    while (sj_obj_key(j, &first, k, sizeof(k)) == 0) {
        /* The short outbound form (Xray 25+): address, port, id, flow and encryption right in
         * settings, without vnext and users. */
        if (!strcmp(k, "address")) { sj_str(j, n->host, sizeof(n->host)); sl_host_unbracket(n->host); continue; }
        if (!strcmp(k, "id")) { sj_str(j, n->uuid, sizeof(n->uuid)); continue; }
        if (!strcmp(k, "flow")) { sj_str(j, n->flow, sizeof(n->flow)); continue; }
        if (!strcmp(k, "encryption")) { json_encryption(j, n); continue; }
        if (!strcmp(k, "port")) { n->port = xray_port(j); continue; }
        if (!strcmp(k, "version")) { h->sver = hx_version(j); continue; }
        if (strcmp(k, "vnext") != 0) { sj_skip(j); continue; }
        int fa = 1, taken = 0;
        while (sj_arr_next(j, &fa) == 0) {
            if (taken) { sj_skip(j); continue; }
            taken = 1;
            int fo = 1;
            char k2[64];
            while (sj_obj_key(j, &fo, k2, sizeof(k2)) == 0) {
                if (!strcmp(k2, "address")) { sj_str(j, n->host, sizeof(n->host)); sl_host_unbracket(n->host); }
                else if (!strcmp(k2, "port")) n->port = xray_port(j);
                else if (!strcmp(k2, "users")) {
                    int fu = 1, u_taken = 0;
                    while (sj_arr_next(j, &fu) == 0) {
                        if (u_taken) { sj_skip(j); continue; }
                        u_taken = 1;
                        int fu2 = 1;
                        char k3[64];
                        while (sj_obj_key(j, &fu2, k3, sizeof(k3)) == 0) {
                            if (!strcmp(k3, "id")) sj_str(j, n->uuid, sizeof(n->uuid));
                            else if (!strcmp(k3, "flow")) sj_str(j, n->flow, sizeof(n->flow));
                            else if (!strcmp(k3, "encryption")) json_encryption(j, n);
                            else sj_skip(j);
                        }
                    }
                } else sj_skip(j);
            }
        }
    }
}

/* ---- sing-box: outbounds with type=vless --------------------------------------------------------
 *
 * Panels that serve a sing-box config (to SFI, Hiddify, Karing) put nodes into the same `outbounds`
 * array as Xray, but flat: type/tag/server/server_port/uuid/flow and the tls and transport
 * objects. The differences that need a parser of their own: the kind is in type, not protocol;
 * the port is the number server_port; TLS is an object with nested utls and reality; the Host of
 * a transport is a string or an array of strings. ws early data: max_early_data +
 * early_data_header_name; with the name Sec-WebSocket-Protocol this is Xray's `?ed=N`, which our
 * ws supports. With an empty name sing-box puts the data into the path, a form Xray does not
 * have; early data then stays off (a sing-box server accepts a plain request too). */
/* Append "Name: value\n" to the headers buffer (sing-box and Clash); -1 if it does not fit or the
 * header is not valid (sl_hdr_valid). No snprintf: it warns about truncation that the length check
 * already rules out. */
static int hdr_append(char *dst, size_t cap, const char *k, const char *v) {
    size_t o = strlen(dst), kn = strlen(k), vn = strlen(v);
    if (!sl_hdr_valid(k, v) || o + kn + vn + 4 >= cap) return -1;
    memcpy(dst + o, k, kn);
    dst[o + kn] = ':'; dst[o + kn + 1] = ' ';
    memcpy(dst + o + kn + 2, v, vn);
    dst[o + kn + 2 + vn] = '\n'; dst[o + kn + 3 + vn] = '\0';
    return 0;
}

static uint16_t port_of_num(long v) { return (v > 0 && v < 65536) ? (uint16_t)v : 0; }

static int sj_bool(struct sj *j) {
    sj_ws(j);
    if (!strncmp(j->p, "true", 4)) { j->p += 4; return 1; }
    if (!strncmp(j->p, "false", 5)) { j->p += 5; return 0; }
    sj_skip(j);
    return 0;
}

/* A number: 123 or "123". */
static long sj_num(struct sj *j) {
    sj_ws(j);
    char b[24] = "";
    if (*j->p == '"') sj_str(j, b, sizeof b);
    else { size_t i = 0; while (*j->p >= '0' && *j->p <= '9' && i + 1 < sizeof b) b[i++] = *j->p++; b[i] = 0; if (!i) sj_skip(j); }
    return atol(b);
}

static int vmess_own(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if ((klen == 10 && !strncmp(k, "encryption", 10)) || (klen == 3 && !strncmp(k, "scy", 3))) {
        char scy[32];
        sl_set_pct(scy, sizeof(scy), v, vlen);
        if (scy[0] && strcmp(scy, "auto") != 0 && strcmp(scy, "aes-128-gcm") != 0 && !strchr(n->uuid, ':')) {
            char tmp[168];
            snprintf(tmp, sizeof(tmp), "%s:%s", scy, n->uuid);
            sl_set_field(n->uuid, sizeof(n->uuid), tmp, strlen(tmp));
        }
        return 1;
    }
    return 0;
}

static int vmess_parse_url(const char *url, struct vless_node *n) {
    if (strncmp(url, "vmess://", 8) != 0) return -1;
    const char *p = url + 8;
    const char *hash = strchr(p, '#');
    const char *qmark = memchr(p, '?', (size_t)((hash ? hash : p + strlen(p)) - p));
    const char *hp_end = qmark ? qmark : hash ? hash : p + strlen(p);
    const char *at = memchr(p, '@', (size_t)(hp_end - p));

    if (at) {
        const char *sec = NULL;
        size_t sec_n = 0;
        int rc = sl_link_parse(url, "vmess://", n, vmess_own, &sec, &sec_n, 0);
        if (rc) return rc;
        snprintf(n->proto_str, sizeof(n->proto_str), "vmess");
        char cred[128];
        sl_set_pct(cred, sizeof(cred), sec, sec_n);
        if (n->uuid[0] && strchr(n->uuid, ':') && !strchr(cred, ':')) {
            /* vmess_own already stored "<scy>:<uuid>" into n->uuid */
            size_t cl = (size_t)(strchr(n->uuid, ':') - n->uuid);
            char tmp[256];
            snprintf(tmp, sizeof(tmp), "%.*s:%s", (int)cl, n->uuid, cred);
            sl_set_field(n->uuid, sizeof(n->uuid), tmp, strlen(tmp));
        } else {
            sl_set_field(n->uuid, sizeof(n->uuid), cred, strlen(cred));
        }
        return node_usable(n);
    }

    /* V2RayN base64 JSON format: vmess://<base64_json> */
    char dec[2048];
    size_t dn = b64_decode(p, (size_t)(hp_end - p), dec, sizeof(dec));
    if (!dn) return -1;
    struct sj j = { dec };
    sj_ws(&j);
    if (*j.p != '{') return -1;

    memset(n, 0, sizeof(*n));
    snprintf(n->proto_str, sizeof(n->proto_str), "vmess");
    snprintf(n->type, sizeof(n->type), "tcp");
    snprintf(n->security, sizeof(n->security), "none");

    char id[64] = "", scy[32] = "", net[32] = "", hdr_type[32] = "", host_hdr[256] = "", path_str[256] = "", svc_str[128] = "", tls_str[32] = "";
    int first = 1;
    char k[64];
    while (sj_obj_key(&j, &first, k, sizeof(k)) == 0) {
        sj_ws(&j);
        if (!strcmp(k, "ps") && *j.p == '"') {
            sj_str(&j, n->name, sizeof(n->name));
            sl_utf8_trim_tail(n->name);
        } else if (!strcmp(k, "add") && *j.p == '"') {
            sj_str(&j, n->host, sizeof(n->host));
        } else if (!strcmp(k, "port")) {
            n->port = port_of_num(sj_num(&j));
        } else if (!strcmp(k, "id") && *j.p == '"') {
            sj_str(&j, id, sizeof(id));
        } else if ((!strcmp(k, "scy") || !strcmp(k, "security")) && *j.p == '"') {
            sj_str(&j, scy, sizeof(scy));
        } else if (!strcmp(k, "net") && *j.p == '"') {
            sj_str(&j, net, sizeof(net));
        } else if (!strcmp(k, "type") && *j.p == '"') {
            sj_str(&j, hdr_type, sizeof(hdr_type));
        } else if (!strcmp(k, "host") && *j.p == '"') {
            sj_str(&j, host_hdr, sizeof(host_hdr));
        } else if (!strcmp(k, "path") && *j.p == '"') {
            sj_str(&j, path_str, sizeof(path_str));
        } else if (!strcmp(k, "serviceName") && *j.p == '"') {
            sj_str(&j, svc_str, sizeof(svc_str));
        } else if (!strcmp(k, "tls") && *j.p == '"') {
            sj_str(&j, tls_str, sizeof(tls_str));
        } else if (!strcmp(k, "sni") && *j.p == '"') {
            sj_str(&j, n->sni, sizeof(n->sni));
        } else if (!strcmp(k, "fp") && *j.p == '"') {
            sj_str(&j, n->fp, sizeof(n->fp));
        } else if (!strcmp(k, "allowInsecure") || !strcmp(k, "insecure")) {
            if (*j.p == '"') {
                char iv[16] = "";
                sj_str(&j, iv, sizeof(iv));
                if (sl_truthy(iv)) n->allow_insecure = 1;
            } else if (sj_bool(&j)) {
                n->allow_insecure = 1;
            }
        } else {
            sj_skip(&j);
        }
    }

    if (!n->host[0] || !n->port) return -1;
    if (hash && !n->name[0]) sl_set_name(n->name, sizeof(n->name), hash + 1);

    if (scy[0] && strcmp(scy, "auto") != 0 && strcmp(scy, "aes-128-gcm") != 0)
        snprintf(n->uuid, sizeof(n->uuid), "%s:%s", scy, id);
    else
        snprintf(n->uuid, sizeof(n->uuid), "%s", id);

    if (net[0]) {
        if (!strcmp(net, "raw")) snprintf(n->type, sizeof(n->type), "tcp");
        else if (!strcmp(net, "websocket")) snprintf(n->type, sizeof(n->type), "ws");
        else if (!strcmp(net, "splithttp")) snprintf(n->type, sizeof(n->type), "xhttp");
        else sl_set_field(n->type, sizeof(n->type), net, strlen(net));
    }
    if (!strcmp(n->type, "tcp") && !strcmp(hdr_type, "http")) n->tcp_http = 1;
    if (!strcmp(n->type, "grpc")) {
        const char *sv = svc_str[0] ? svc_str : path_str;
        sl_set_field(n->service, sizeof(n->service), sv, strlen(sv));
    } else {
        if (path_str[0]) sl_set_field(n->path, sizeof(n->path), path_str, strlen(path_str));
        if (host_hdr[0]) sl_set_field(n->http_host, sizeof(n->http_host), host_hdr, strlen(host_hdr));
    }
    if (!strcmp(tls_str, "tls") || sl_truthy(tls_str)) {
        snprintf(n->security, sizeof(n->security), "tls");
        if (!n->sni[0]) {
            if (host_hdr[0] && sl_host_is_name(host_hdr)) sl_set_field(n->sni, sizeof(n->sni), host_hdr, strlen(host_hdr));
            else if (sl_host_is_name(n->host)) sl_set_field(n->sni, sizeof(n->sni), n->host, strlen(n->host));
        }
    }
    return node_usable(n);
}

static int pb_varint(const unsigned char **p, const unsigned char *end, uint64_t *val) {
    uint64_t v = 0;
    int shift = 0;
    while (*p < end && shift < 64) {
        unsigned char b = *(*p)++;
        v |= (uint64_t)(b & 0x7f) << shift;
        if (!(b & 0x80)) { *val = v; return 0; }
        shift += 7;
    }
    return -1;
}

static int mieru_parse_pb(const unsigned char *buf, size_t len, struct vless_node *n) {
    const unsigned char *p = buf, *end = buf + len;
    char user[64] = "", pass[64] = "", host[256] = "";
    uint16_t tcp_port = 0;
    int saw_udp = 0;

    while (p < end) {
        uint64_t tag = 0, slen = 0;
        if (pb_varint(&p, end, &tag) < 0) return -1;
        uint32_t fn = (uint32_t)(tag >> 3), wt = (uint32_t)(tag & 7);
        if (wt == 0) {
            uint64_t v = 0;
            if (pb_varint(&p, end, &v) < 0) return -1;
        } else if (wt == 2) {
            if (pb_varint(&p, end, &slen) < 0 || slen > (uint64_t)(end - p)) return -1;
            const unsigned char *sp = p, *send = p + slen;
            p = send;
            if (fn == 1) {
                sl_set_field(n->name, sizeof(n->name), (const char *)sp, (size_t)slen);
                sl_utf8_trim_tail(n->name);
            } else if (fn == 2) {
                while (sp < send) {
                    uint64_t utag = 0, ulen = 0;
                    if (pb_varint(&sp, send, &utag) < 0) return -1;
                    if ((utag & 7) == 2) {
                        if (pb_varint(&sp, send, &ulen) < 0 || ulen > (uint64_t)(send - sp)) return -1;
                        if ((utag >> 3) == 1) sl_set_field(user, sizeof(user), (const char *)sp, (size_t)ulen);
                        else if ((utag >> 3) == 2) sl_set_field(pass, sizeof(pass), (const char *)sp, (size_t)ulen);
                        sp += ulen;
                    } else if ((utag & 7) == 0) {
                        uint64_t uv = 0;
                        if (pb_varint(&sp, send, &uv) < 0) return -1;
                    } else return -1;
                }
            } else if (fn == 3 && !tcp_port) {
                char ip[128] = "", dom[256] = "";
                while (sp < send) {
                    uint64_t stag = 0, sblen = 0;
                    if (pb_varint(&sp, send, &stag) < 0) return -1;
                    if ((stag & 7) == 2) {
                        if (pb_varint(&sp, send, &sblen) < 0 || sblen > (uint64_t)(send - sp)) return -1;
                        const unsigned char *bp = sp, *bend = sp + sblen;
                        sp = bend;
                        if ((stag >> 3) == 1) sl_set_field(ip, sizeof(ip), (const char *)bp, (size_t)sblen);
                        else if ((stag >> 3) == 2) sl_set_field(dom, sizeof(dom), (const char *)bp, (size_t)sblen);
                        else if ((stag >> 3) == 3) {
                            uint16_t bport = 0;
                            int bproto = 1; /* default TCP */
                            while (bp < bend) {
                                uint64_t ptag = 0, pv = 0;
                                if (pb_varint(&bp, bend, &ptag) < 0) return -1;
                                if ((ptag & 7) == 0) {
                                    if (pb_varint(&bp, bend, &pv) < 0) return -1;
                                    if ((ptag >> 3) == 1 && pv > 0 && pv < 65536) bport = (uint16_t)pv;
                                    else if ((ptag >> 3) == 2) bproto = (int)pv;
                                } else if ((ptag & 7) == 2) {
                                    if (pb_varint(&bp, bend, &pv) < 0 || pv > (uint64_t)(bend - bp)) return -1;
                                    if ((ptag >> 3) == 3 && !bport) {
                                        char rstr[32];
                                        sl_set_field(rstr, sizeof(rstr), (const char *)bp, (size_t)pv);
                                        long rp = atol(rstr);
                                        if (rp > 0 && rp < 65536) bport = (uint16_t)rp;
                                    }
                                    bp += pv;
                                } else return -1;
                            }
                            if (bport) {
                                if (bproto == 1 && !tcp_port) tcp_port = bport;
                                else if (bproto == 2) saw_udp = 1;
                            }
                        }
                    } else if ((stag & 7) == 0) {
                        uint64_t sv = 0;
                        if (pb_varint(&sp, send, &sv) < 0) return -1;
                    } else return -1;
                }
                if (dom[0] || ip[0]) snprintf(host, sizeof(host), "%s", dom[0] ? dom : ip);
            }
        } else if (wt == 1 && (size_t)(end - p) >= 8) {
            p += 8;
        } else if (wt == 5 && (size_t)(end - p) >= 4) {
            p += 4;
        } else {
            return -1;
        }
    }
    if (!host[0] || (!tcp_port && !saw_udp)) return -1;
    snprintf(n->proto_str, sizeof(n->proto_str), "mieru");
    snprintf(n->type, sizeof(n->type), "tcp");
    snprintf(n->security, sizeof(n->security), "none");
    sl_set_field(n->host, sizeof(n->host), host, strlen(host));
    if (user[0] || pass[0]) snprintf(n->uuid, sizeof(n->uuid), "%s:%s", user, pass);
    if (!tcp_port && saw_udp) {
        n->port = 1;
        snprintf(n->skip_reason, sizeof(n->skip_reason), "mieru: UDP transport is not supported");
        return 1;
    }
    n->port = tcp_port;
    return node_usable(n);
}

static int mieru_own(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if (klen == 8 && !strncmp(k, "protocol", 8)) {
        char pr[16];
        sl_set_pct(pr, sizeof(pr), v, vlen);
        if (!strcasecmp(pr, "UDP")) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "mieru: UDP transport is not supported");
        }
        return 1;
    }
    if (klen == 7 && !strncmp(k, "profile", 7)) {
        if (!n->name[0]) sl_set_pct(n->name, sizeof(n->name), v, vlen);
        return 1;
    }
    return 0;
}

static int mieru_parse_url(const char *url, struct vless_node *n) {
    const char *scheme = !strncmp(url, "mierus://", 9) ? "mierus://" :
                         !strncmp(url, "mieru://", 8) ? "mieru://" : NULL;
    if (!scheme) return -1;
    size_t scl = strlen(scheme);
    const char *p = url + scl;
    const char *hash = strchr(p, '#');
    const char *qmark = memchr(p, '?', (size_t)((hash ? hash : p + strlen(p)) - p));
    const char *hp_end = qmark ? qmark : hash ? hash : p + strlen(p);
    const char *at = memchr(p, '@', (size_t)(hp_end - p));

    if (!at && !strcmp(scheme, "mieru://")) {
        unsigned char dec[2048];
        size_t dn = b64_decode(p, (size_t)(hp_end - p), (char *)dec, sizeof(dec));
        if (!dn) return -1;
        memset(n, 0, sizeof(*n));
        int rc = mieru_parse_pb(dec, dn, n);
        if (rc >= 0 && hash && !n->name[0]) sl_set_name(n->name, sizeof(n->name), hash + 1);
        return rc;
    }
    if (!at) return -1;

    /* Support mierus://user:pass@host?port=1234&protocol=TCP#name where port is in query */
    char rewritten[1024];
    const char *colon_after_at = memchr(at + 1, ':', (size_t)(hp_end - (at + 1)));
    if (!colon_after_at && qmark) {
        const char *qp = strstr(qmark + 1, "port=");
        if (qp && qp > qmark && (qp[-1] == '?' || qp[-1] == '&')) {
            long port_val = atol(qp + 5);
            if (port_val > 0 && port_val < 65536) {
                int nw = snprintf(rewritten, sizeof(rewritten), "%.*s:%ld%s",
                                  (int)(hp_end - url), url, port_val, hp_end);
                if (nw < 0 || (size_t)nw >= sizeof(rewritten)) return -1;
                url = rewritten;
            }
        }
    }

    const char *sec = NULL;
    size_t sec_n = 0;
    int rc = sl_link_parse(url, scheme, n, mieru_own, &sec, &sec_n, 0);
    if (rc) return rc;
    snprintf(n->proto_str, sizeof(n->proto_str), "mieru");
    sl_set_pct(n->uuid, sizeof(n->uuid), sec, sec_n);
    if (n->skip_reason[0]) return 1;
    return node_usable(n);
}

static int anytls_own(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if (klen == 4 && !strncmp(k, "peer", 4)) {
        if (!n->sni[0]) sl_set_pct(n->sni, sizeof(n->sni), v, vlen);
        return 1;
    }
    if (klen == 9 && !strncasecmp(k, "pinsha256", 9)) {
        char *d = sl_param_dup(v, vlen);
        if (d) { sl_add_pins(n, d, 0); free(d); } else n->pcs = SL_BAD_PIN;
        return 1;
    }
    return 0;
}

static int anytls_parse_url(const char *url, struct vless_node *n) {
    if (strncmp(url, "anytls://", 9) != 0) return -1;
    const char *p = url + 9;
    const char *hash = strchr(p, '#');
    const char *hp_end = hash ? hash : p + strlen(p);
    const char *qmark = memchr(p, '?', (size_t)(hp_end - p));
    const char *auth_end = qmark ? qmark : hp_end;
    const char *at = memchr(p, '@', (size_t)(auth_end - p));
    if (!at) return -1;

    const char *host_start = at + 1;
    const char *host_end = auth_end;
    if (host_end > host_start && host_end[-1] == '/') host_end--;
    const char *colon = memchr(host_start, ':', (size_t)(host_end - host_start));

    char rewritten[2048];
    if (!colon) {
        int nw = snprintf(rewritten, sizeof(rewritten), "%.*s:443%s",
                          (int)(host_end - url), url, auth_end);
        if (nw < 0 || (size_t)nw >= sizeof(rewritten)) return -1;
        url = rewritten;
    } else if (host_end != auth_end) {
        int nw = snprintf(rewritten, sizeof(rewritten), "%.*s%s",
                          (int)(host_end - url), url, auth_end);
        if (nw < 0 || (size_t)nw >= sizeof(rewritten)) return -1;
        url = rewritten;
    }

    const char *sec = NULL;
    size_t sec_n = 0;
    int rc = sl_link_parse(url, "anytls://", n, anytls_own, &sec, &sec_n, 0);
    if (rc) return rc;
    snprintf(n->proto_str, sizeof(n->proto_str), "anytls");
    snprintf(n->type, sizeof(n->type), "tcp");
    snprintf(n->security, sizeof(n->security), "tls");
    sl_set_pct(n->uuid, sizeof(n->uuid), sec, sec_n);
    if (n->skip_reason[0]) return 1;
    return node_usable(n);
}

struct sb_ws { long ed; char ed_hdr[40]; };

static void sb_tls(struct sj *j, struct vless_node *n, int *enabled, int *reality) {
    int f = 1;
    char k[64];
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcmp(k, "enabled")) *enabled = sj_bool(j);
        else if (!strcmp(k, "server_name")) sj_str(j, n->sni, sizeof n->sni);
        else if (!strcmp(k, "insecure")) { if (sj_bool(j)) n->allow_insecure = 1; }
        else if (!strcmp(k, "ech")) {
            /* {"enabled": true, "config": ["-----BEGIN ECH CONFIGS-----", "base64…",
             * "-----END ECH CONFIGS-----"]}. The PEM lines are joined without the frame lines.
             * Without config (only query_server_name) it is a DNS lookup: not supported. */
            int f2 = 1, on = 1, have = 0;
            char k2[64], joined[1400] = "";
            size_t jl = 0;
            while (sj_obj_key(j, &f2, k2, sizeof k2) == 0) {
                if (!strcmp(k2, "enabled")) on = sj_bool(j);
                else if (!strcmp(k2, "config")) {
                    int fa = 1;
                    char line[1400];
                    int r = sj_arr_next(j, &fa);          /* < 0: a single string, not an array */
                    do {
                        line[0] = '\0';
                        if (sj_str(j, line, sizeof line) != 0) break;
                        size_t ll = strlen(line);
                        if (line[0] != '-' && jl + ll < sizeof joined) { memcpy(joined + jl, line, ll + 1); jl += ll; have = 1; }
                        r = r < 0 ? 1 : sj_arr_next(j, &fa);
                    } while (r == 0);
                } else sj_skip(j);
            }
            if (on && have) sl_set_ech(n, joined);
            else if (on) n->ech = SL_ECH_DNS;
        }
        else if (!strcmp(k, "certificate_public_key_sha256")) {
            /* Base64 SHA-256 hashes of the SubjectPublicKeyInfo: an array, or a single string. */
            char pv[80];
            int fa = 1;
            int r = sj_arr_next(j, &fa);
            if (r == 0) {
                do { pv[0] = '\0'; sj_str(j, pv, sizeof pv); sl_add_pins(n, pv, 1); } while (sj_arr_next(j, &fa) == 0);
            } else if (r < 0) { pv[0] = '\0'; sj_str(j, pv, sizeof pv); sl_add_pins(n, pv, 1); }
        }
        else if (!strcmp(k, "utls")) {
            int f2 = 1, on = 1;
            char k2[64], fp[sizeof n->fp] = "";
            while (sj_obj_key(j, &f2, k2, sizeof k2) == 0) {
                if (!strcmp(k2, "enabled")) on = sj_bool(j);
                else if (!strcmp(k2, "fingerprint")) sj_str(j, fp, sizeof fp);
                else sj_skip(j);
            }
            if (on && fp[0]) snprintf(n->fp, sizeof n->fp, "%s", fp);
        } else if (!strcmp(k, "reality")) {
            int f2 = 1;
            char k2[64];
            while (sj_obj_key(j, &f2, k2, sizeof k2) == 0) {
                if (!strcmp(k2, "enabled")) *reality = sj_bool(j);
                else if (!strcmp(k2, "public_key")) sj_str(j, n->pbk, sizeof n->pbk);
                else if (!strcmp(k2, "short_id")) sj_str(j, n->sid, sizeof n->sid);
                else sj_skip(j);
            }
        } else sj_skip(j);
    }
}

static void sb_transport(struct sj *j, struct vless_node *n, struct sb_ws *w, struct upg_cfg *u) {
    int f = 1;
    char k[64];
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcmp(k, "type")) {
            sj_str(j, n->type, sizeof n->type);
            if (!strcmp(n->type, "websocket")) snprintf(n->type, sizeof n->type, "ws");
        }
        else if (!strcmp(k, "path")) sj_str(j, u->path, sizeof u->path);
        else if (!strcmp(k, "host")) {
            sj_ws(j);
            if (*j->p == '[') { int fa = 1; char h[sizeof u->host]; int got = 0;
                while (sj_arr_next(j, &fa) == 0) { if (got++) sj_skip(j); else sj_str(j, h, sizeof h); }
                if (got) snprintf(u->host, sizeof u->host, "%s", h); }
            else sj_str(j, u->host, sizeof u->host);
        }
        else if (!strcmp(k, "service_name")) sj_str(j, n->service, sizeof n->service);
        else if (!strcmp(k, "max_early_data")) w->ed = sj_num(j);
        else if (!strcmp(k, "early_data_header_name")) sj_str(j, w->ed_hdr, sizeof w->ed_hdr);
        else if (!strcmp(k, "headers")) {
            int f2 = 1;
            char k2[64];
            while (sj_obj_key(j, &f2, k2, sizeof k2) == 0) {
                sj_ws(j);
                char v[sizeof u->host] = "";
                if (*j->p == '[') { int fa = 1, got = 0;
                    while (sj_arr_next(j, &fa) == 0) { if (got++) sj_skip(j); else sj_str(j, v, sizeof v); } }
                else if (*j->p == '"') sj_str(j, v, sizeof v);
                else { sj_skip(j); u->bad = 1; continue; }
                if (ci_eq(k2, "host")) { if (!u->host[0]) snprintf(u->host, sizeof u->host, "%s", v); }
                else {
                    if (hdr_append(u->headers, sizeof u->headers, k2, v) != 0) u->bad = 1;
                }
            }
        } else sj_skip(j);
    }
}

/* A sing-box outbound: it has type where Xray has protocol. xray_outbound calls it after looking
 * ahead; it reads the whole object. */
static int sb_dur(const char *s, int64_t *out) {
    const char *p = s;
    int neg = 0;
    uint64_t total = 0;
    if (*p == '-' || *p == '+') neg = *p++ == '-';
    *out = 0;
    if (!strcmp(p, "0")) return 0;
    if (!*p) return -1;
    while (*p) {
        uint64_t v = 0, f = 0, scale = 1;
        size_t nd = 0, nf = 0;
        for (; *p >= '0' && *p <= '9'; p++, nd++) {
            if (v > (UINT64_MAX - 9) / 10) return -1;
            v = v * 10 + (uint64_t)(*p - '0');
        }
        if (*p == '.') {
            for (p++; *p >= '0' && *p <= '9'; p++, nf++) {
                if (nf < 18) { f = f * 10 + (uint64_t)(*p - '0'); scale *= 10; }
            }
        }
        if (!nd && !nf) return -1;
        const char *u = p;
        while (*p && *p != '.' && (*p < '0' || *p > '9')) p++;
        size_t ul = (size_t)(p - u);
        uint64_t unit = 0;
        if (ul == 2 && !memcmp(u, "ns", 2)) unit = 1;
        else if (ul == 2 && !memcmp(u, "us", 2)) unit = 1000;
        else if (ul == 3 && (!memcmp(u, "\xc2\xb5s", 3) || !memcmp(u, "\xce\xbcs", 3))) unit = 1000;
        else if (ul == 2 && !memcmp(u, "ms", 2)) unit = 1000000;
        else if (ul == 1 && *u == 's') unit = 1000000000ull;
        else if (ul == 1 && *u == 'm') unit = 60000000000ull;
        else if (ul == 1 && *u == 'h') unit = 3600000000000ull;
        else if (ul == 1 && *u == 'd') unit = 86400000000000ull;
        if (!unit || v > ((uint64_t)1 << 63) / unit) return -1;
        v *= unit;
        v += (uint64_t)((double)f * ((double)unit / (double)scale));
        if (v > (uint64_t)1 << 63 || total > ((uint64_t)1 << 63) - v) return -1;
        total += v;
    }
    if (total == (uint64_t)1 << 63) {
        if (!neg) return -1;
        *out = INT64_MIN;
        return 0;
    }
    *out = neg ? -(int64_t)total : (int64_t)total;
    return 0;
}

static void sb_hop_key(struct sj *j, struct hy2_in *h, const char *key, int64_t *ns) {
    char t[48], why[48];
    int nul = 0;
    if (hx_str(j, t, sizeof t, &nul) || sb_dur(t, ns)) {
        snprintf(why, sizeof why, "%s: not a duration", key);
        hy2_in_fail(h, why);
    }
}

static void sb_hop(struct hy2_in *h, int64_t lo, int64_t hi) {
    if (!h->first || (!lo && !hi)) return;
    if (lo < 0 || hi < 0) { hy2_in_fail(h, "hop_interval must be at least 5 seconds"); return; }
    if (lo % 1000000000 || hi % 1000000000) { hy2_in_fail(h, "hop_interval: not whole seconds up to 65535"); return; }
    if (lo > 65535000000000ll || hi > 65535000000000ll) { hy2_in_fail(h, "hop_interval: not whole seconds up to 65535"); return; }
    if (!hi) hi = lo;
    else if (!lo) { hy2_in_fail(h, "hop_interval_max needs hop_interval"); return; }
    if (lo > hi) { hy2_in_fail(h, "hop_interval must not exceed hop_interval_max"); return; }
    if (lo < 5000000000ll) { hy2_in_fail(h, "hop_interval must be at least 5 seconds"); return; }
    h->hop_from = (uint16_t)(lo / 1000000000);
    h->hop_to = (uint16_t)(hi / 1000000000);
}

static void sb_port_item(struct sj *j, char *spec, size_t *o, size_t cap, int *bad) {
    char one[48];
    sj_ws(j);
    if (*j->p != '"') { sj_skip(j); *bad = 1; return; }
    if (sj_str(j, one, sizeof one)) { *bad = 1; return; }
    char *colon = strchr(one, ':');
    size_t ol = strlen(one);
    if (!colon || ol + 6 >= sizeof one) { *bad = 1; return; }
    if (colon[1] == '\0') snprintf(colon + 1, sizeof one - (size_t)(colon + 1 - one), "65535");
    *colon = '-';
    ol = strlen(one);
    if (*o + ol + 2 > cap) { *bad = 1; return; }
    if (*o) spec[(*o)++] = ',';
    memcpy(spec + *o, one, ol);
    *o += ol;
}

static void sb_server_ports(struct sj *j, struct hy2_in *h) {
    char spec[256];
    size_t o = 0;
    int bad = 0;
    sj_ws(j);
    if (*j->p == '[') {
        int f = 1;
        while (sj_arr_next(j, &f) == 0) sb_port_item(j, spec, &o, sizeof spec, &bad);
    } else sb_port_item(j, spec, &o, sizeof spec, &bad);
    if (bad) { hy2_in_fail(h, "server_ports: not a port list"); return; }
    hy2_in_ports(h, spec, o, "server_ports");
}

static void sb_obfs(struct sj *j, struct hy2_in *h) {
    sj_ws(j);
    if (!strncmp(j->p, "null", 4)) { j->p += 4; return; }
    if (*j->p != '{') { sj_skip(j); hy2_in_fail(h, "obfs is not an object"); return; }
    int f = 1, nul = 0;
    char k[32], pw[HY2_SECRET_MAX + 2];
    while (sj_obj_key(j, &f, k, sizeof k) == 0) {
        if (!strcmp(k, "type")) { if (hx_str(j, h->obfs, sizeof h->obfs, &nul)) h->obfs[0] = '\0'; }
        else if (!strcmp(k, "password")) {
            nul = 0;
            if (hx_str(j, pw, sizeof pw, &nul)) pw[0] = '\0';
            hy2_in_secret(h, h->obfs_pw, pw, strlen(pw), nul);
        } else sj_skip(j);
    }
    if (!h->obfs_pw[0]) hy2_in_fail(h, "obfs-password missing");
    else if (!h->obfs[0]) hy2_in_fail(h, "obfs type is missing");
}

static unsigned long sb_mbps(struct sj *j) {
    char v[24];
    return hx_tok(j, v, sizeof v) ? 0 : hy2_digits(v, strlen(v), 100000);
}

static void sb_outbound_body(struct sj *j, struct vless_node *n, char *proto, size_t proto_n) {
    int first = 1, tls_on = 0, reality = 0;
    char k[64];
    struct hy2_in hin;
    int64_t hop = 0, hop_max = 0;
    memset(&hin, 0, sizeof hin);
    struct sb_ws w; struct upg_cfg u;
    memset(&w, 0, sizeof w); memset(&u, 0, sizeof u);
    while (sj_obj_key(j, &first, k, sizeof k) == 0) {
        if (!strcmp(k, "type")) sj_str(j, proto, proto_n);
        else if (!strcmp(k, "tag")) { sj_str(j, n->name, sizeof n->name); sl_utf8_trim_tail(n->name); }
        else if (!strcmp(k, "server")) { sj_str(j, n->host, sizeof n->host); sl_host_unbracket(n->host); }
        else if (!strcmp(k, "server_port")) n->port = port_of_num(sj_num(j));
        else if (!strcmp(k, "uuid")) sj_str(j, n->uuid, sizeof n->uuid);
        else if (!strcmp(k, "flow")) sj_str(j, n->flow, sizeof n->flow);
        else if (!strcmp(k, "tls")) sb_tls(j, n, &tls_on, &reality);
        else if (!strcmp(k, "transport")) sb_transport(j, n, &w, &u);
        else if (!strcmp(k, "server_ports")) sb_server_ports(j, &hin);
        else if (!strcmp(k, "hop_interval")) sb_hop_key(j, &hin, k, &hop);
        else if (!strcmp(k, "hop_interval_max")) sb_hop_key(j, &hin, k, &hop_max);
        else if (!strcmp(k, "up_mbps")) hin.up_mbps = (uint32_t)sb_mbps(j);
        else if (!strcmp(k, "down_mbps")) hin.down_mbps = (uint32_t)sb_mbps(j);
        else if (!strcmp(k, "password")) {
            char pw[HY2_SECRET_MAX + 2];
            int nul = 0;
            if (hx_str(j, pw, sizeof pw, &nul)) pw[0] = '\0';
            hy2_in_secret(&hin, hin.auth, pw, strlen(pw), nul);
        }
        else if (!strcmp(k, "obfs")) sb_obfs(j, &hin);
        else sj_skip(j);
    }
    sb_hop(&hin, hop, hop_max);
    if (!tls_on) hy2_in_fail(&hin, "hysteria2 needs tls");
    snprintf(n->security, sizeof n->security, "%s", reality ? "reality" : tls_on ? "tls" : "none");
    if (!n->type[0]) snprintf(n->type, sizeof n->type, "tcp");
    if (!strcmp(n->type, "ws") && w.ed > 0 && !strcmp(w.ed_hdr, "Sec-WebSocket-Protocol") && !strchr(u.path, '?')) {
        size_t o = strlen(u.path);
        if (!o) { u.path[0] = '/'; u.path[1] = 0; o = 1; }
        snprintf(u.path + o, sizeof u.path - o, "?ed=%ld", w.ed);
    }
    if (!strcmp(n->type, "ws") || !strcmp(n->type, "httpupgrade")) {
        snprintf(n->path, sizeof n->path, "%s", u.path);
        snprintf(n->http_host, sizeof n->http_host, "%s", u.host);
        snprintf(n->headers, sizeof n->headers, "%s", u.headers);
        n->headers_bad = u.bad;
    }
    if (!strcmp(proto, "hysteria2")) hy2_apply(n, &hin);
}

/* One outbound. 1 — a vless node, written into n; 0 — not ours. */
static int xray_outbound(struct sj *j, struct vless_node *n) {
    memset(n, 0, sizeof(*n));
    /* tcp by default, as in a link: a config without streamSettings or network is valid, and
     * Xray then means tcp. */
    snprintf(n->type, sizeof(n->type), "tcp");
    int first = 1, is_vless = 0;
    char k[64], proto[32] = "";
    struct hx h;
    memset(&h, 0, sizeof h);
    /* Xray (protocol) or sing-box (type)? Look ahead at the top-level keys, as for remarks: the
     * key order is not defined, and the parse is a single pass. */
    {
        const char *save = j->p;
        int f0 = 1, has_protocol = 0, has_type = 0;
        char k0[64];
        while (sj_obj_key(j, &f0, k0, sizeof k0) == 0) {
            if (!strcmp(k0, "protocol")) has_protocol = 1;
            else if (!strcmp(k0, "type")) has_type = 1;
            sj_skip(j);
        }
        j->p = save;
        if (has_type && !has_protocol) {
            sb_outbound_body(j, n, proto, sizeof proto);
            return !strcmp(proto, "vless") || !strcmp(proto, "hysteria2");
        }
    }
    /* The key order is not defined, so protocol may come after settings: read everything and
     * decide at the end. Parsing another protocol's outbound into the fields does no harm, the
     * node is not taken. */
    while (sj_obj_key(j, &first, k, sizeof(k)) == 0) {
        if (!strcmp(k, "protocol")) sj_str(j, proto, sizeof(proto));
        /* tag is cut by bytes like a link's name, so an incomplete UTF-8 tail is dropped the
         * same way. */
        else if (!strcmp(k, "tag")) { sj_str(j, n->name, sizeof(n->name)); sl_utf8_trim_tail(n->name); }
        else if (!strcmp(k, "settings")) xray_settings(j, n, &h);
        else if (!strcmp(k, "streamSettings")) xray_stream(j, n, NULL, &h);
        else sj_skip(j);
    }
    is_vless = !strcmp(proto, "vless");
    if (strcmp(proto, "hysteria")) return is_vless;
    if (h.sver != 2)
        snprintf(n->skip_reason, sizeof n->skip_reason, "hysteria version %lu is not supported", h.sver);
    else if (h.hver != 2)
        snprintf(n->skip_reason, sizeof n->skip_reason, "hysteriaSettings version %lu is not supported", h.hver);
    else if (strcmp(n->type, "hysteria"))
        snprintf(n->skip_reason, sizeof n->skip_reason, "hysteria needs network hysteria");
    else if (strcmp(n->security, "tls"))
        snprintf(n->skip_reason, sizeof n->skip_reason, "hysteria needs security tls");
    hy2_apply(n, &h.in);
    return 1;
}

/* The name the panel shows lives in the config's remarks, not in the outbound's tag: panels give
 * many outbounds the same tag ("proxy"), or a tag with a random suffix that changes on every
 * request ("tl-8-1-43al6bgvgg4"), and only remarks ("Germany", "Finland", …) tells nodes apart.
 *
 * remarks may come after outbounds, so the config object is first scanned for remarks alone
 * (sj_skip copies nothing) and then parsed from its start. The scan covers one config, not the
 * whole subscription.
 *
 * remarks is not percent-decoded, unlike a link's #fragment: in a JSON field "%2F" means exactly
 * these three characters. */
static void xray_remarks(struct sj *j, char *out, size_t n) {
    out[0] = '\0';
    const char *save = j->p;
    int first = 1;
    char k[64];
    while (sj_obj_key(j, &first, k, sizeof(k)) == 0) {
        if (!strcmp(k, "remarks")) { sj_str(j, out, n); sl_utf8_trim_tail(out); }
        else sj_skip(j);
    }
    j->p = save;
}

/* Node name from the config's remarks; ord counts the vless outbounds of this config. The second
 * and later get a number, since a config with a balancer has a main and a spare node. The number
 * is in parentheses ("Mobile #1 (2)") because panels number their nodes with '#' themselves.
 * When remarks nearly fills the buffer the number is dropped rather than cutting the name. */
static void xray_name(struct vless_node *nd, const char *remarks, size_t ord) {
    char num[24] = "";
    if (ord) snprintf(num, sizeof(num), " (%zu)", ord + 1);
    size_t room = sizeof(nd->name) - 1;
    size_t rl = strnlen(remarks, room);
    size_t nl = strlen(num);
    if (nl > room - rl) nl = room - rl;
    memcpy(nd->name, remarks, rl);
    memcpy(nd->name + rl, num, nl);
    nd->name[rl + nl] = '\0';
    sl_utf8_trim_tail(nd->name);
}

/* A whole config: an array of configs or one config. Returns the number of usable nodes. */
static size_t parse_xray(const char *text, struct vless_node *out, size_t max,
                         struct vless_sub_stats *st) {
    struct sj j = { text };
    size_t n = 0;
    sj_ws(&j);
    /* A single config is treated as an array of one. */
    int wrapped = (*j.p == '{');
    int fa = 1;
    if (wrapped) fa = 0;                    /* no array: parse the object at once */
    for (;;) {
        if (!wrapped) {
            int r = sj_arr_next(&j, &fa);
            if (r != 0) break;
        }
        const char *cfg_before = j.p;
        /* One config: nodes from outbounds, their names from remarks (xray_remarks). */
        char remarks[sizeof(((struct vless_node *)0)->name)];
        xray_remarks(&j, remarks, sizeof(remarks));
        size_t ord = 0;
        int fc = 1;
        char k[64];
        int seen_ob = 0;
        while (sj_obj_key(&j, &fc, k, sizeof(k)) == 0) {
            if (strcmp(k, "outbounds") != 0) { sj_skip(&j); continue; }
            seen_ob = 1;
            int fo = 1;
            while (sj_arr_next(&j, &fo) == 0) {
                struct vless_node node;
                const char *before = j.p;
                int ours = xray_outbound(&j, &node);
                if (j.p == before) break;               /* no progress: stop */
                if (!ours) continue;
                /* Before the usability check: sl_skip_note uses the name as its example, and
                 * it should be the name the panel shows. */
                if (remarks[0]) xray_name(&node, remarks, ord);
                ord++;
                if (n >= max) {
                    /* No room left: count the node as skipped, so the numbers add up. */
                    sl_skip_node(st, &node, "more nodes than fit");
                    continue;
                }
                if (node_usable(&node) == 0) { sl_node_ok(&node); out[n++] = node; }
                else sl_skip_node(st, &node, node.skip_reason);
            }
        }
        (void)seen_ob;
        if (wrapped) break;
        if (j.p == cfg_before) break;                   /* the config did not parse: do not loop */
    }
    return n;
}

/* ---- Clash / Mihomo: proxies with type: vless ---------------------------------------------------
 *
 * Panels give Clash clients YAML: a `proxies:` list, each node a flat set of keys and nested
 * *-opts (ws-opts, reality-opts, grpc-opts, xhttp-opts). Nodes come in block form (`- name: x`
 * and indented keys) and in flow form (`- {name: x, type: vless, reality-opts: {public-key: k}}`,
 * as converters write), and both must parse.
 *
 * No general YAML parser: one that refuses anchors and aliases would lose every node of a
 * subscription with an anchor in proxy-groups, and the tests build this file without libyaml.
 * Instead each node is flattened into path=value pairs (`reality-opts.public-key`,
 * `ws-opts.headers.Host`, `alpn.0`) and built from the paths. A value that does not parse (an
 * alias `*a`, a multi-line scalar) is skipped, and the node gets the rest. Anchors `&a` before a
 * value are dropped. */
#define YF_MAX 96
struct yflat {
    char buf[12288];
    size_t used, n;
    struct { const char *k, *v; } kv[YF_MAX];
    uint8_t nulv[YF_MAX];
};

static void yf_add(struct yflat *f, const char *path, size_t pn, const char *val, size_t vn) {
    if (f->n >= YF_MAX || f->used + pn + vn + 2 > sizeof f->buf) return;
    char *k = f->buf + f->used;
    memcpy(k, path, pn); k[pn] = 0;
    char *v = k + pn + 1;
    memcpy(v, val, vn); v[vn] = 0;
    f->used += pn + vn + 2;
    f->kv[f->n].k = k; f->kv[f->n].v = v; f->n++;
}

static void yf_addz(struct yflat *f, const char *path, size_t pn, const char *val, size_t vn, int nul) {
    size_t n0 = f->n;
    yf_add(f, path, pn, val, vn);
    if (f->n > n0) f->nulv[n0] = (uint8_t)nul;
}

static int yf_nul(const struct yflat *f, const char *path) {
    for (size_t i = 0; i < f->n; i++) if (!strcmp(f->kv[i].k, path)) return f->nulv[i];
    return 0;
}

static const char *yf_get(const struct yflat *f, const char *path) {
    for (size_t i = 0; i < f->n; i++) if (!strcmp(f->kv[i].k, path)) return f->kv[i].v;
    return NULL;
}
/* Case-insensitive: Host or host in headers. */
static const char *yf_geti(const struct yflat *f, const char *path) {
    for (size_t i = 0; i < f->n; i++) if (ci_eq(f->kv[i].k, path)) return f->kv[i].v;
    return NULL;
}

/* The scalar at p, quoted or plain. flow != 0: a plain scalar ends at , } ]. Returns a pointer past
 * the scalar; the value (unquoted) goes to out, its length to *on. */
static const char *y_scalar2(const char *p, const char *end, int flow, char *out, size_t cap, size_t *on, int *nul) {
    size_t o = 0;
    while (p < end && (*p == ' ' || *p == '\t')) p++;
    /* An anchor or a tag before the value. */
    while (p < end && (*p == '&' || *p == '!')) { while (p < end && *p != ' ' && *p != '\t') p++; while (p < end && (*p == ' ' || *p == '\t')) p++; }
    if (p < end && (*p == '"' || *p == '\'')) {
        char q = *p++;
        int full = 0;
        while (p < end && *p != q) {
            char e[4] = { *p, 0, 0, 0 };
            size_t el = 1;
            if (q == '"' && *p == '\\' && p + 1 < end) {
                p += sl_unescape(p + 1, 1, e, &el);
                if (!el && nul) *nul = 1;
            }
            else if (q == '\'' && *p == '\'' && p + 1 < end && p[1] == '\'') p++;
            if (!full && o + el < cap) {
                memcpy(out + o, e, el);
                o += el;
            } else {
                full = 1;
            }
            p++;
        }
        if (p < end) p++;
    } else {
        const char *st0 = p;
        while (p < end && *p != '\n' && *p != '\r') {
            if (flow && (*p == ',' || *p == '}' || *p == ']')) break;
            /* flow == 2: a key, which ends at ':' followed by a blank or the end. */
            if (flow == 2 && *p == ':' && (p + 1 >= end || p[1] == ' ' || p[1] == '\n' || p[1] == '\r')) break;
            if (*p == '#' && p > st0 && (p[-1] == ' ' || p[-1] == '\t')) break;
            if (o + 1 < cap) out[o++] = *p;
            p++;
        }
        while (o && (out[o - 1] == ' ' || out[o - 1] == '\t')) o--;
    }
    out[o] = 0;
    *on = o;
    return p;
}

static const char *y_scalar(const char *p, const char *end, int flow, char *out, size_t cap, size_t *on) {
    return y_scalar2(p, end, flow, out, cap, on, NULL);
}

static const char *y_flow(struct yflat *f, const char *p, const char *end, char *path, size_t pn, int depth);

/* A value in flow form: {…}, […] or a scalar. */
static const char *y_flow_val(struct yflat *f, const char *p, const char *end, char *path, size_t pn, int depth) {
    while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r')) p++;
    if (p < end && (*p == '{' || *p == '[')) return y_flow(f, p, end, path, pn, depth + 1);
    char v[3300];
    size_t vn;
    int z = 0;
    p = y_scalar2(p, end, 1, v, sizeof v, &vn, &z);
    if (v[0] != '*') yf_addz(f, path, pn, v, vn, z);
    return p;
}

static const char *y_flow(struct yflat *f, const char *p, const char *end, char *path, size_t pn, int depth) {
    if (depth > 6 || p >= end) return end;
    char open = *p++;
    unsigned idx = 0;
    for (;;) {
        while (p < end && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r' || *p == ',')) p++;
        if (p >= end) return end;
        if (*p == '}' || *p == ']') return p + 1;
        char np[200];
        size_t npn;
        if (open == '{') {
            char key[96];
            size_t kn;
            p = y_scalar(p, end, 2, key, sizeof key, &kn);
            while (p < end && (*p == ' ' || *p == '\t')) p++;
            if (p < end && *p == ':') p++;
            npn = (size_t)snprintf(np, sizeof np, "%.*s%s%s", (int)pn, path, pn ? "." : "", key);
        } else {
            npn = (size_t)snprintf(np, sizeof np, "%.*s%s%u", (int)pn, path, pn ? "." : "", idx++);
        }
        if (npn >= sizeof np) npn = sizeof np - 1;
        p = y_flow_val(f, p, end, np, npn, depth);
    }
}

/* A line of a block: its indent, and the text without it. */
static const char *y_line(const char *p, const char *end, size_t *ind, const char **e) {
    size_t i = 0;
    while (p + i < end && p[i] == ' ') i++;
    const char *s = p + i, *q = s;
    while (q < end && *q != '\n') q++;
    *ind = i;
    *e = q;
    return s;
}

/* One node of a block: p starts its "- …" line (indent dash). Returns the line after the node. */
static const char *y_item(struct yflat *f, const char *p, const char *end, size_t dash) {
    struct lvl { size_t ind; char path[200]; } st[6];
    int sp = 1, first = 1;
    st[0].ind = 0; st[0].path[0] = 0;
    char lastkey[200] = "";
    unsigned seq = 0;
    while (p < end) {
        size_t ind;
        const char *e;
        const char *s = y_line(p, end, &ind, &e);
        const char *next = e < end ? e + 1 : e;
        if (e > s && e[-1] == '\r') e--;
        if (s >= e || *s == '#' || (e - s >= 3 && !strncmp(s, "---", 3))) { p = next; continue; }
        if (first) {
            first = 0;
            s += 1; ind += 1;                                   /* the dash */
            while (s < e && *s == ' ') { s++; ind++; }
            if (s < e && *s == '{') {
                char none[1] = "";
                y_flow(f, s, end, none, 0, 0);
                int d = 0;
                const char *q = s;
                for (; q < end; q++) { if (*q == '{') d++; else if (*q == '}' && --d == 0) break; }
                while (q < end && *q != '\n') q++;
                return q < end ? q + 1 : q;
            }
        } else if (ind <= dash) {
            if (!(ind == dash && 0)) return p;
        }
        if (!first && ind > dash && (*s == '-' && (e - s == 1 || s[1] == ' '))) {
            char v[3300];
            size_t vn;
            int z = 0;
            y_scalar2(s + 1, e, 0, v, sizeof v, &vn, &z);
            char np[200];
            int n2 = snprintf(np, sizeof np, "%s.%u", lastkey, seq++);
            if (lastkey[0] && n2 > 0 && n2 < (int)sizeof np && v[0] != '*') yf_addz(f, np, (size_t)n2, v, vn, z);
            p = next;
            continue;
        }
        while (sp > 1 && st[sp - 1].ind >= ind) sp--;
        char key[96];
        size_t kn;
        const char *c = y_scalar(s, e, 2, key, sizeof key, &kn);
        while (c < e && (*c == ' ' || *c == '\t')) c++;
        if (c >= e || *c != ':') { p = next; continue; }
        c++;
        char np[200];
        int n2 = snprintf(np, sizeof np, "%s%s%s", st[sp - 1].path, st[sp - 1].path[0] ? "." : "", key);
        if (n2 <= 0 || n2 >= (int)sizeof np) { p = next; continue; }
        while (c < e && (*c == ' ' || *c == '\t')) c++;
        if (c >= e || *c == '#') {
            snprintf(lastkey, sizeof lastkey, "%s", np);
            seq = 0;
            if (sp < 6) { st[sp].ind = ind; snprintf(st[sp].path, sizeof st[sp].path, "%s", np); sp++; }
        } else if (*c == '{' || *c == '[') {
            y_flow(f, c, end, np, (size_t)n2, 0);
        } else {
            char v[3300];
            size_t vn;
            int z = 0;
            y_scalar2(c, e, 0, v, sizeof v, &vn, &z);
            if (v[0] != '*') yf_addz(f, np, (size_t)n2, v, vn, z);
        }
        p = next;
    }
    return p;
}

static int clash_tls(const char *v) {
    return v && (!strcmp(v, "true") || !strcmp(v, "True"));
}

#define DS "xhttp-opts.download-settings."

static const char *ds_get(const struct yflat *f, const char *k) {
    char p[96];
    snprintf(p, sizeof p, DS "%s", k);
    return yf_get(f, p);
}

static int ds_has(const struct yflat *f, const char *k) {
    char p[96];
    size_t l = (size_t)snprintf(p, sizeof p, DS "%s.", k);
    for (size_t i = 0; i < f->n; i++) if (!strncmp(f->kv[i].k, p, l)) return 1;
    return 0;
}

static int clash_alpn(const struct yflat *f, const char *key, uint8_t *code) {
    char p[96];
    if (yf_get(f, key)) return -1;
    snprintf(p, sizeof p, "%s.0", key);
    const char *v0 = yf_get(f, p);
    if (!v0) return 0;
    snprintf(p, sizeof p, "%s.1", key);
    *code = yf_get(f, p) ? 0 : alpn_of(v0);
    return 1;
}

static const char *const clash_reuse[][2] = {
    { "max-concurrency", "xmux.maxConcurrency" }, { "max-connections", "xmux.maxConnections" },
    { "c-max-reuse-times", "xmux.cMaxReuseTimes" }, { "h-max-request-times", "xmux.hMaxRequestTimes" },
    { "h-max-reusable-secs", "xmux.hMaxReusableSecs" }, { "h-keep-alive-period", "xmux.hKeepAlivePeriod" },
};

static void clash_reuse_put(struct sl_xh *a, const char *k, const char *v) {
    for (size_t m = 0; m < sizeof clash_reuse / sizeof *clash_reuse; m++)
        if (!strcmp(k, clash_reuse[m][0])) sl_xh_put(a, clash_reuse[m][1], v, strlen(v));
}

static void clash_dl_reuse(const struct yflat *f, const char *main_xhx, struct vless_node *b) {
    struct sl_xh a;
    memset(&a, 0, sizeof a);
    char key[64];
    for (const char *l = main_xhx ? main_xhx : "", *e; (e = strchr(l, '\n')); l = e + 1) {
        const char *eq = memchr(l, '=', (size_t)(e - l));
        if (!eq || !strncmp(l, "xmux.", 5)) continue;
        snprintf(key, sizeof key, "%.*s", (int)(eq - l), l);
        sl_xh_put(&a, key, eq + 1, (size_t)(e - eq - 1));
    }
    size_t pl = strlen(DS "reuse-settings.");
    for (size_t i = 0; i < f->n; i++)
        if (!strncmp(f->kv[i].k, DS "reuse-settings.", pl)) clash_reuse_put(&a, f->kv[i].k + pl, f->kv[i].v);
    sl_xh_done(b, &a);
}

static void clash_dl(const struct yflat *f, const struct vless_node *main, struct vless_node *n) {
    struct sl_dl *d = calloc(1, sizeof *d);
    if (!d) { n->dl = &SL_DL_FULL; return; }
    struct vless_node *b = &d->n;
    const char *v;
    snprintf(b->type, sizeof b->type, "xhttp");
    if (yf_get(f, "xhttp-opts.download-settings")) d->bad = 1;
    if ((v = ds_get(f, "server"))) { sl_set_field(b->host, sizeof b->host, v, strlen(v)); sl_host_unbracket(b->host); }
    else memcpy(b->host, main->host, sizeof b->host);
    b->port = (v = ds_get(f, "port")) ? sl_port_of(v) : main->port;
    if ((v = ds_get(f, "servername"))) sl_set_field(b->sni, sizeof b->sni, v, strlen(v));
    else memcpy(b->sni, main->sni, sizeof b->sni);
    if ((v = ds_get(f, "client-fingerprint"))) sl_set_field(b->fp, sizeof b->fp, v, strlen(v));
    else memcpy(b->fp, main->fp, sizeof b->fp);
    if ((v = ds_get(f, "fingerprint"))) { if (v[0]) sl_add_pins(b, v, 0); }
    else { b->pcs = main->pcs; b->pks = main->pks; }
    b->allow_insecure = (v = ds_get(f, "skip-cert-verify")) ? (uint8_t)sl_truthy(v) : main->allow_insecure;
    if (ds_has(f, "ech-opts")) { if ((v = ds_get(f, "ech-opts.config")) && v[0]) sl_set_ech(b, v); }
    else b->ech = main->ech;
    if (ds_has(f, "reality-opts")) {
        const char *pbk = ds_get(f, "reality-opts.public-key");
        if (!pbk) d->bad = 1;
        else if (pbk[0]) {
            sl_set_field(b->pbk, sizeof b->pbk, pbk, strlen(pbk));
            if ((v = ds_get(f, "reality-opts.short-id"))) sl_set_field(b->sid, sizeof b->sid, v, strlen(v));
            if ((v = ds_get(f, "reality-opts.mldsa65-verify")) || (v = ds_get(f, "reality-opts.pqv"))) sl_set_pqv(b, v);
        }
    } else {
        memcpy(b->pbk, main->pbk, sizeof b->pbk);
        memcpy(b->sid, main->sid, sizeof b->sid);
        b->pqv = main->pqv;
    }
    int tls = clash_tls((v = ds_get(f, "tls")) ? v : yf_get(f, "tls"));
    if (b->pbk[0] && !tls) d->notls = 1;
    snprintf(b->security, sizeof b->security, "%s", b->pbk[0] ? "reality" : tls ? "tls" : "none");
    if (clash_alpn(f, DS "alpn", &d->alpn) < 0) d->bad = 1;
    if ((v = ds_get(f, "path"))) sl_set_field(b->path, sizeof b->path, v, strlen(v));
    else memcpy(b->path, main->path, sizeof b->path);
    if ((v = ds_get(f, "host"))) sl_set_field(b->http_host, sizeof b->http_host, v, strlen(v));
    else memcpy(b->http_host, main->http_host, sizeof b->http_host);
    if (ds_has(f, "headers")) {
        for (size_t i = 0; i < f->n; i++) {
            const char *k = f->kv[i].k;
            if (strncmp(k, DS "headers.", strlen(DS "headers.")) != 0) continue;
            k += strlen(DS "headers.");
            if (ci_eq(k, "host") || hdr_append(b->headers, sizeof b->headers, k, f->kv[i].v) != 0) b->headers_bad = 1;
        }
    } else {
        memcpy(b->headers, main->headers, sizeof b->headers);
        b->headers_bad = main->headers_bad;
    }
    b->pad_from = main->pad_from;
    b->pad_to = main->pad_to;
    b->xhx = main->xhx;
    if (ds_has(f, "reuse-settings") && main->xhx != SL_BAD_XHX && main->xhx != SL_FULL) clash_dl_reuse(f, main->xhx, b);
    sl_dl_done(n, d);
    free(d);
}
#undef DS

/* A flattened node into a vless node. 1 — it is vless, written into n. */
static unsigned clash_rate(const char *v) {
    if (!*v) return 0;
    const char *p = v;
    int neg = *p == '-';
    if (*p == '-' || *p == '+') p++;
    const char *d = p;
    while (*p >= '0' && *p <= '9') p++;
    if (p != d && !*p) {
        if (neg || p - d > 9) return 0;
        unsigned long m = strtoul(d, NULL, 10);
        return m > 100000 ? 0 : (unsigned)m;
    }
    p = v;
    uint64_t n = 0;
    while (*p >= '0' && *p <= '9') {
        if (n > 100000000000000000ull) return 0;
        n = n * 10 + (uint64_t)(*p++ - '0');
    }
    if (p == v || p - v > 18) return 0;
    while (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\f' || *p == '\r') p++;
    uint64_t mul = 1;
    if (*p && strchr("KMGT", *p)) {
        for (const char *q = "KMGT"; *q != *p; q++) mul *= 1000;
        mul *= 1000;
        p++;
    }
    if ((*p != 'b' && *p != 'B') || strcmp(p + 1, "ps")) return 0;
    if (n > UINT64_MAX / mul) return 0;
    n *= mul;
    return hy2_mbps(*p == 'b' ? n / 8 : n);
}

static void clash_secret(const struct yflat *f, struct hy2_in *h, char *dst, const char *key) {
    const char *v = yf_get(f, key);
    if (v) hy2_in_secret(h, dst, v, strlen(v), yf_nul(f, key));
}

static int clash_hy2(const struct yflat *f, struct vless_node *n) {
    struct hy2_in h;
    const char *v;
    memset(&h, 0, sizeof h);
    snprintf(n->type, sizeof n->type, "quic");
    if ((v = yf_get(f, "name"))) { sl_set_field(n->name, sizeof n->name, v, strlen(v)); sl_utf8_trim_tail(n->name); }
    if ((v = yf_get(f, "server"))) { sl_set_field(n->host, sizeof n->host, v, strlen(v)); sl_host_unbracket(n->host); }
    if ((v = yf_get(f, "port"))) n->port = sl_port_of(v);
    if ((v = yf_get(f, "sni"))) sl_set_field(n->sni, sizeof n->sni, v, strlen(v));
    if ((v = yf_get(f, "fingerprint")) && v[0]) sl_add_pins(n, v, 0);
    if ((v = yf_get(f, "name-cert-verify")) && v[0]) sl_set_vcn(n, v);
    if ((v = yf_get(f, "skip-cert-verify")) && sl_truthy(v)) n->allow_insecure = 1;
    if ((v = yf_get(f, "ech-opts.config")) && v[0]) sl_set_ech(n, v);
    clash_secret(f, &h, h.auth, "password");
    if ((v = yf_get(f, "obfs"))) snprintf(h.obfs, sizeof h.obfs, "%s", v);
    clash_secret(f, &h, h.obfs_pw, "obfs-password");
    if ((v = yf_get(f, "up"))) h.up_mbps = clash_rate(v);
    if ((v = yf_get(f, "down"))) h.down_mbps = clash_rate(v);
    if ((v = yf_get(f, "ports"))) {
        char spec[160];
        size_t o = 0;
        for (; *v && o + 1 < sizeof spec; v++) {
            char c = *v == '/' ? ',' : *v;
            if (c == ' ' || c == '\t' || (c == ',' && (!o || spec[o - 1] == ','))) continue;
            spec[o++] = c;
        }
        while (o && spec[o - 1] == ',') o--;
        if (*v) hy2_in_fail(&h, "port list too long");
        else hy2_in_ports(&h, spec, o, "ports");
    }
    if ((v = yf_get(f, "hop-interval")) && v[0]) {
        unsigned long a, b;
        if (!hx_range(v, &a, &b) || a > 65535 || b > 65535) hy2_in_fail(&h, "hop-interval: not a number or range");
        else if (a || b) {
            if (a > b) { unsigned long t = a; a = b; b = t; }
            if (a == 0) a = 30;
            else if (a < 5) a = 5;
            if (b < a) b = a;
            h.hop_from = (uint16_t)a;
            h.hop_to = (uint16_t)b;
        }
    }
    hy2_apply(n, &h);
    return 1;
}

static int clash_node(const struct yflat *f, struct vless_node *n) {
    memset(n, 0, sizeof *n);
    const char *v;
    if (!(v = yf_get(f, "type"))) return 0;
    if (!strcmp(v, "hysteria2")) return clash_hy2(f, n);
    if (strcmp(v, "vless")) return 0;
    snprintf(n->type, sizeof n->type, "tcp");
    if ((v = yf_get(f, "name"))) { sl_set_field(n->name, sizeof n->name, v, strlen(v)); sl_utf8_trim_tail(n->name); }
    if ((v = yf_get(f, "server"))) { sl_set_field(n->host, sizeof n->host, v, strlen(v)); sl_host_unbracket(n->host); }
    if ((v = yf_get(f, "port"))) n->port = sl_port_of(v);
    if ((v = yf_get(f, "uuid"))) sl_set_field(n->uuid, sizeof n->uuid, v, strlen(v));
    if ((v = yf_get(f, "flow"))) sl_set_field(n->flow, sizeof n->flow, v, strlen(v));
    if ((v = yf_get(f, "servername")) || (v = yf_get(f, "sni"))) sl_set_field(n->sni, sizeof n->sni, v, strlen(v));
    if ((v = yf_get(f, "client-fingerprint"))) sl_set_field(n->fp, sizeof n->fp, v, strlen(v));
    /* Clash/mihomo: `fingerprint` is the SHA-256 of the node's certificate (Xray's
     * pinnedPeerCertSha256), `skip-cert-verify` is allowInsecure. */
    if ((v = yf_get(f, "fingerprint")) && v[0]) sl_add_pins(n, v, 0);
    if ((v = yf_get(f, "skip-cert-verify")) && sl_truthy(v)) n->allow_insecure = 1;
    if ((v = yf_get(f, "ech-opts.config")) && v[0]) sl_set_ech(n, v);
    const char *pbk = yf_get(f, "reality-opts.public-key");
    if (pbk) {
        sl_set_field(n->pbk, sizeof n->pbk, pbk, strlen(pbk));
        if ((v = yf_get(f, "reality-opts.short-id"))) sl_set_field(n->sid, sizeof n->sid, v, strlen(v));
        if ((v = yf_get(f, "reality-opts.mldsa65-verify")) || (v = yf_get(f, "reality-opts.pqv"))) sl_set_pqv(n, v);
        snprintf(n->security, sizeof n->security, "reality");
    } else {
        snprintf(n->security, sizeof n->security, "%s", clash_tls(yf_get(f, "tls")) ? "tls" : "none");
    }
    if ((v = yf_get(f, "encryption"))) set_encryption(n, v);
    const char *net = yf_get(f, "network");
    if (net) {
        if (!strcmp(net, "raw")) net = "tcp";
        sl_set_field(n->type, sizeof n->type, net, strlen(net));
    }
    const char *upg = yf_get(f, "ws-opts.v2ray-http-upgrade");
    if (!strcmp(n->type, "ws") && upg && !strcmp(upg, "true")) snprintf(n->type, sizeof n->type, "httpupgrade");
    if (!strcmp(n->type, "ws") || !strcmp(n->type, "httpupgrade")) {
        struct upg_cfg u;
        memset(&u, 0, sizeof u);
        if ((v = yf_get(f, "ws-opts.path"))) sl_set_field(u.path, sizeof u.path, v, strlen(v));
        if ((v = yf_geti(f, "ws-opts.headers.host"))) sl_set_field(u.host, sizeof u.host, v, strlen(v));
        for (size_t i = 0; i < f->n; i++) {
            const char *k = f->kv[i].k;
            if (strncmp(k, "ws-opts.headers.", 16) != 0 || ci_eq(k + 16, "host")) continue;
            if (hdr_append(u.headers, sizeof u.headers, k + 16, f->kv[i].v) != 0) u.bad = 1;
        }
        const char *ed = yf_get(f, "ws-opts.max-early-data"), *eh = yf_get(f, "ws-opts.early-data-header-name");
        if (ed && atol(ed) > 0 && eh && !strcmp(eh, "Sec-WebSocket-Protocol") && !strchr(u.path, '?')) {
            size_t o = strlen(u.path);
            if (!o) { u.path[0] = '/'; u.path[1] = 0; o = 1; }
            snprintf(u.path + o, sizeof u.path - o, "?ed=%ld", atol(ed));
        }
        snprintf(n->path, sizeof n->path, "%s", u.path);
        snprintf(n->http_host, sizeof n->http_host, "%s", u.host);
        snprintf(n->headers, sizeof n->headers, "%s", u.headers);
        n->headers_bad = u.bad;
    } else if (!strcmp(n->type, "grpc")) {
        if ((v = yf_get(f, "grpc-opts.grpc-service-name"))) sl_set_field(n->service, sizeof n->service, v, strlen(v));
    } else if (!strcmp(n->type, "xhttp")) {
        if ((v = yf_get(f, "xhttp-opts.path"))) sl_set_field(n->path, sizeof n->path, v, strlen(v));
        if ((v = yf_get(f, "xhttp-opts.mode"))) sl_set_field(n->mode, sizeof n->mode, v, strlen(v));
        if ((v = yf_get(f, "xhttp-opts.host"))) sl_set_field(n->http_host, sizeof n->http_host, v, strlen(v));
        if ((v = yf_get(f, "xhttp-opts.x-padding-bytes"))) sl_pad_range(n, v);
        static const char *const map[][2] = {
            { "x-padding-obfs-mode", "xPaddingObfsMode" }, { "x-padding-key", "xPaddingKey" },
            { "x-padding-header", "xPaddingHeader" }, { "x-padding-placement", "xPaddingPlacement" },
            { "x-padding-method", "xPaddingMethod" }, { "uplink-http-method", "uplinkHTTPMethod" },
            { "session-placement", "sessionIDPlacement" }, { "session-key", "sessionIDKey" },
            { "session-table", "sessionIDTable" }, { "session-length", "sessionIDLength" },
            { "seq-placement", "seqPlacement" }, { "seq-key", "seqKey" },
            { "uplink-data-placement", "uplinkDataPlacement" }, { "uplink-data-key", "uplinkDataKey" },
            { "uplink-chunk-size", "uplinkChunkSize" }, { "no-grpc-header", "noGRPCHeader" },
        };
        struct sl_xh a;
        memset(&a, 0, sizeof a);
        int ds = 0;
        for (size_t i = 0; i < f->n; i++) {
            const char *k = f->kv[i].k;
            if (strncmp(k, "xhttp-opts.", 11) != 0) continue;
            k += 11;
            if (!strncmp(k, "download-settings.", 18) ||
                (!strcmp(k, "download-settings") && f->kv[i].v[0] && strcmp(f->kv[i].v, "null"))) {
                ds = 1;
                continue;
            }
            if (!strncmp(k, "headers.", 8)) {
                if (ci_eq(k + 8, "host") || hdr_append(n->headers, sizeof n->headers, k + 8, f->kv[i].v) != 0)
                    n->headers_bad = 1;
                continue;
            }
            if (!strncmp(k, "reuse-settings.", 15)) clash_reuse_put(&a, k + 15, f->kv[i].v);
            for (size_t m = 0; m < sizeof map / sizeof *map; m++)
                if (!strcmp(k, map[m][0])) sl_xh_put(&a, map[m][1], f->kv[i].v, strlen(f->kv[i].v));
        }
        sl_xh_done(n, &a);
        if (ds) clash_dl(f, n, n);
    }
    return 1;
}

/* Whole Clash YAML. Returns the number of usable nodes; other protocols count in st->foreign. */
static size_t parse_clash(const char *text, struct vless_node *out, size_t max, struct vless_sub_stats *st) {
    const char *end = text + strlen(text), *p = text;
    size_t n = 0, dash = 0;
    int in_list = 0;
    struct yflat *f = malloc(sizeof *f);
    if (!f) return 0;
    while (p < end) {
        size_t ind;
        const char *e;
        const char *s = y_line(p, end, &ind, &e);
        const char *next = e < end ? e + 1 : e;
        if (!in_list) {
            if (ind == 0 && (!strncmp(s, "proxies:", 8) || !strncmp(s, "Proxy:", 6))) {
                const char *c = s + (s[0] == 'p' ? 8 : 6);
                while (c < e && *c == ' ') c++;
                if (c < e && *c != '#') break;                 /* "proxies: []" etc.: no nodes */
                in_list = 1;
                dash = (size_t)-1;
            }
            p = next;
            continue;
        }
        if (s >= e || *s == '#') { p = next; continue; }
        if (ind == 0 && *s != '-') break;                     /* the next top-level key */
        if (*s == '-' && (s + 1 == e || s[1] == ' ')) {
            if (dash == (size_t)-1) dash = ind;
            if (ind != dash) { p = next; continue; }
            f->n = 0; f->used = 0;
            p = y_item(f, p, end, dash);
            struct vless_node node;
            if (!clash_node(f, &node)) { if (st) st->foreign++; continue; }
            if (n >= max) { sl_skip_node(st, &node, "more nodes than fit"); continue; }
            if (node_usable(&node) == 0) { sl_node_ok(&node); out[n++] = node; }
            else sl_skip_node(st, &node, node.skip_reason);
            continue;
        }
        p = next;
    }
    free(f);
    return n;
}

/* Clash YAML? A line starts with "proxies:" (or "Proxy:", the old name). A list of links or base64
 * has no such line: base64 has no colon. */
static int looks_clash(const char *t) {
    for (const char *p = t; *p; ) {
        if (!strncmp(p, "proxies:", 8) || !strncmp(p, "Proxy:", 6)) return 1;
        const char *nl = strchr(p, '\n');
        if (!nl) break;
        p = nl + 1;
    }
    return 0;
}

/* Limit on the length of one link. A Reality link with a post-quantum signature (Xray-core 25.9+)
 * carries `pqv`, a whole ML-DSA-65 public key: 1952 bytes, 2603 base64url characters, about 2860
 * bytes for the whole link. 8192 leaves room for ML-DSA-87 (2592 bytes, 3456 characters).
 *
 * pqv is a signature key, not a key exchange: Reality's post-quantum exchange is the
 * X25519MLKEM768 group in key_share (reality.h). With pqv the server also signs its temporary
 * certificate, and the client checks that signature when pqv is set (tls13.c →
 * cert_reality_check_pq).
 *
 * The buffer is on the stack: subscriptions are parsed only on the main thread at startup, never
 * in the threads with small stacks (the stack's connectors have 128 KB). */
#define SUB_LINE_MAX 8192

/* A character of a scheme name: `scheme = ALPHA *( ALPHA / DIGIT / "+" / "-" / "." )`
 * (RFC 3986 §3.1), lowercase only, as subscriptions write schemes. */
static int scheme_ch(char c) {
    return (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
           c == '+' || c == '.' || c == '-';
}

/* A link glued to this one without a separator, with a scheme the splitter does not know.
 * Returns the "://" of the glued link, or NULL.
 *
 * The splitter in vless_parse_sub steps back by known scheme names on purpose: a rule by form
 * would read "…#one" + "vless://…" as "onevless://", and the second link would stop being a
 * vless link. When the list finds nothing the pair is still glued, but the boundary is unknown,
 * so the node is reported unusable rather than split by a guess.
 *
 * The scheme is not named, for the same reason: stepping back from "://" over scheme characters
 * in "…#oneanytls://…" gives "oneanytls". The example gives what is known for sure, the tail's
 * length and first bytes, which locate the glue in the subscription text.
 *
 * The glued link must also have '@' before any '#': sellers put their channel into node names
 * ("#channel https://t.me/shop"), and a proxy link carries credentials before the host while a
 * channel address does not. Schemes without '@' (vmess as base64, the old ss form) are in the
 * splitter's list and never get here.
 *
 * The first occurrence is returned: the earliest glued point is where the loss starts. */
static const char *glued_tail(const char *b, const char *e) {
    for (const char *q = b + 1; q + 3 <= e; q++) {
        if (strncmp(q, "://", 3) != 0) continue;
        const char *sc = q;
        while (sc > b && scheme_ch(sc[-1])) sc--;
        /* Back at the start of the link: this is its own scheme, nothing to split. */
        if (sc == b) continue;
        size_t sl = (size_t)(q - sc);
        /* 2 to 15 characters: a one-letter "x://" is more likely an accident in a node name
         * than a protocol, and proxy schemes are never longer than 15. The first character
         * is a letter, as RFC 3986 requires. */
        if (sl < 2 || sl > 15 || sc[0] < 'a' || sc[0] > 'z') continue;
        int creds = 0;
        for (const char *t = q + 3; t < e && *t != '#'; t++)
            if (*t == '@') { creds = 1; break; }
        if (!creds) continue;
        return q;
    }
    return NULL;
}

/* Count a glued pair as skipped. Its end is e, not a terminator: the too-long branch does not
 * copy the link and must report a glued pair too, or a glued pair and a really long link would
 * read the same ("link longer than 8191 bytes") although they need different fixes. */
static void glue_note(struct vless_sub_stats *st, const char *glue, const char *e) {
    size_t tail = (size_t)(e - glue);
    size_t show = tail < 32 ? tail : 32;
    struct vless_node t;
    memset(&t, 0, sizeof t);
    snprintf(t.name, sizeof t.name, "tail of %zu bytes: %.*s", tail, (int)show, glue);
    sl_skip_note(st, &t, "links glued without a separator");
}

/* Parse subscription text (already decoded from base64) into nodes. Returns the number of usable
 * nodes; the rest is counted in st (may be NULL). */
size_t vless_parse_sub(const char *text, struct vless_node *out, size_t max,
                       struct vless_sub_stats *st) {
    size_t n = 0;
    if (st) memset(st, 0, sizeof(*st));
    const char *p = text;
    /* The form is told by the first non-blank character, not by a substring search: only JSON
     * starts with '[' or '{'. A search for "://" would match an Xray config only by accident
     * (an https:// URL in its DNS settings). */
    while (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r') p++;
    if (*p == '[' || *p == '{') return parse_xray(p, out, max, st);
    if (looks_clash(p)) return parse_clash(p, out, max, st);
    while (*p) {
        while (*p == '\n' || *p == '\r' || *p == ' ' || *p == '\t') p++;
        if (!*p) break;
        /* A link ends at a line break or where the next link's scheme starts: some panels
         * glue links without any separator. */
        const char *e = p;
        while (*e && *e != '\n' && *e != '\r') {
            if (e > p && !strncmp(e, "://", 3)) {
                /* Step back to the start of the scheme by its name, not by character
                 * class: stepping back over letters and digits would take in the tail of
                 * the node name ("…#onevless://b@…"), and the second link would no longer
                 * be a vless link.
                 *
                 * The list is sorted longest first and the first match wins: "ss" matches
                 * the end of "vless", so a shorter name must lose to a longer one. For the
                 * same reason shorter names are not tried after the longest match: if it
                 * is the start of this link, there is nothing to split.
                 *
                 * A node name that ends like a scheme ("…#Express" before "ss://") still
                 * splits right: only the bytes before "://" are compared. */
                static const char *const schemes[] = {
                    "finiteproto", "naive+https", "hysteria2", "wireguard", "hysteria", "trojan", "socks5", "mierus",
                    "anytls", "mieru", "vmess", "vless", "naive", "socks", "tuic", "hy2", "ssr", "fp", "tt", "ss"
                };
                const char *s2 = NULL;
                for (size_t si = 0; si < sizeof(schemes) / sizeof(*schemes); si++) {
                    size_t sl = strlen(schemes[si]);
                    if ((size_t)(e - p) < sl) continue;
                    if (strncmp(e - sl, schemes[si], sl) != 0) continue;
                    /* Strictly greater: equal means the scheme of this link itself. */
                    if ((size_t)(e - p) > sl) s2 = e - sl;
                    break;
                }
                if (s2) { e = s2; break; }
            }
            e++;
        }

        char line[SUB_LINE_MAX];
        size_t len = (size_t)(e - p);
        if (len >= sizeof(line)) {
            /* A link longer than the buffer is counted as unusable, not dropped, so the
             * counters still add up. The limit in the message comes from the buffer size, so
             * the two cannot drift apart. */
            char why[64];
            snprintf(why, sizeof why, "link longer than %zu bytes", sizeof(line) - 1);
            const char *glue = vless_link_scheme(p) ? glued_tail(p, e) : NULL;
            if (glue) {
                /* A glued pair is reported before the length: the length is only its
                 * consequence, and "link longer than 8191 bytes" does not tell what to fix. */
                glue_note(st, glue, e);
            } else if (vless_link_scheme(p)) {
                /* The example gives the length and the start of the address, telling a link
                 * just over the limit from a blob of many kilobytes; the reason itself cannot,
                 * since sl_skip_note groups by it. It starts after '@': the id before it does
                 * not belong in a log. */
                const char *at = memchr(p, '@', len);
                const char *from = at ? at + 1 : p;
                size_t rest = (size_t)(e - from);
                struct vless_node t;
                memset(&t, 0, sizeof t);
                snprintf(t.name, sizeof t.name, "%zu bytes: %s%.*s", len, at ? "…@" : "",
                         (int)(rest < 32 ? rest : 32), from);
                sl_skip_note(st, &t, why);
            }
            /* A long link of another protocol is counted in foreign, like a short one, so
             * that usable + skipped + foreign still adds up. The test is the same ("://"
             * present), bounded by e since the link was not copied. */
            else if (st) {
                for (const char *q = p; q + 3 <= e; q++)
                    if (!strncmp(q, "://", 3)) { st->foreign++; break; }
            }
        } else {
            memcpy(line, p, len);
            line[len] = '\0';
            /* One reason for every glued pair; what tells the cases apart goes into the
             * example. A glued link is not parsed at all: its name is certainly wrong and its
             * address may be, and a "may be" is worse than a refusal. */
            const char *glue = vless_link_scheme(line)
                                   ? glued_tail(line, line + len) : NULL;
            if (glue) {
                glue_note(st, glue, line + len);
            } else if (is_tt_tlv_url(line)) {
                struct vless_node tnodes[16];
                size_t tn = 0;
                int rc = tt_parse_tlv_multi(line, tnodes, 16, &tn);
                if (rc < 0) {
                    if (st) st->foreign++;
                } else {
                    for (size_t ti = 0; ti < tn; ti++) {
                        if (!tnodes[ti].skip_reason[0] && n < max) { sl_node_ok(&tnodes[ti]); out[n++] = tnodes[ti]; }
                        else if (!tnodes[ti].skip_reason[0]) sl_skip_node(st, &tnodes[ti], "more nodes than fit");
                        else sl_skip_node(st, &tnodes[ti], tnodes[ti].skip_reason);
                    }
                }
            } else if (!strncmp(line, "ss://", 5) || !strncmp(line, "vmess://", 8) ||
                       !strncmp(line, "mieru://", 8) || !strncmp(line, "mierus://", 9)) {
                struct vless_node node;
                int rc = vless_parse_url(line, &node);
                if (rc < 0) {
                    if (st) st->foreign++;
                } else if (rc == 0 && n < max) { sl_node_ok(&node); out[n++] = node; }
                else if (rc == 0) sl_skip_node(st, &node, "more nodes than fit");
                else sl_skip_node(st, &node, node.skip_reason);
            } else if (vless_link_scheme(line)) {
                struct vless_node node;
                int rc = vless_parse_url(line, &node);
                /* rc 1: the parser named the reason. rc -1: the link did not parse at all
                 * (an IPv6 literal host, for one), so the reason is named here. Both are
                 * counted, and so is a node that does not fit: usable + skipped + foreign
                 * must add up to the number of links. */
                if (rc == 0 && n < max) { sl_node_ok(&node); out[n++] = node; }
                else if (rc == 0) sl_skip_node(st, &node, "more nodes than fit");
                else sl_skip_node(st, &node, rc > 0 ? node.skip_reason
                                                 : "cannot parse the link");
            } else if (strstr(line, "://") && st) {
                /* Another protocol: counted, not parsed. */
                st->foreign++;
            }
        }
        p = e;
    }
    return n;
}

/* Turn a subscription file as read into text that vless_parse_sub understands:
 *   '[' or '{' first   — a JSON config, returned as is;
 *   a "proxies:" line  — Clash YAML, returned as is;
 *   "://" anywhere     — a list of links, returned as is;
 *   otherwise          — base64, decoded into dec.
 * Returns raw or dec; both are the caller's buffers. */
const char *vless_sub_text(const char *raw, size_t raw_n, char *dec, size_t dec_n) {
    const char *p = raw;
    while (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r') p++;
    if (*p == '[' || *p == '{') return raw;
    if (looks_clash(p)) return raw;
    if (strstr(raw, "://")) return raw;
    b64_decode(raw, raw_n, dec, dec_n);
    const char *dp = dec;
    while (*dp == ' ' || *dp == '\t' || *dp == '\n' || *dp == '\r') dp++;
    if (*dp && *dp != '[' && *dp != '{' && !looks_clash(dp) && !strstr(dp, "://")) {
        size_t dlen = strlen(dp);
        char *tmp = malloc(dlen + 1);
        if (tmp) {
            memcpy(tmp, dp, dlen + 1);
            b64_decode(tmp, dlen, dec, dec_n);
            free(tmp);
        }
    }
    return dec;
}

size_t vless_sub_count(const char *text) {
    size_t n = 0;
    for (const char *q = text; *q; q++) {
        if ((*q == 'v' || *q == 'V') && (!strncasecmp(q, "vless", 5) || !strncasecmp(q, "vmess", 5))) n++;
        else if ((*q == 'h' || *q == 'H') && (!strncasecmp(q, "hysteria", 8) || !strncasecmp(q, "hy2", 3))) n++;
        else if (!strncmp(q, "tt://", 5) || !strncasecmp(q, "trusttunnel", 11)) n += 8;
        else if (!strncmp(q, "?d=", 3) || !strncmp(q, "&d=", 3)) n += 8;
        else if (!strncasecmp(q, "trojan", 6) || !strncasecmp(q, "shadowsocks", 11)) n++;
        else if (!strncmp(q, "ss://", 5) && !(q >= text + 3 && (!strncasecmp(q - 3, "vless://", 8) || !strncasecmp(q - 3, "vmess://", 8)))) n++;
        else if (!strncasecmp(q, "mieru", 5)) n++;
        else if (!strncasecmp(q, "anytls", 6)) n++;
        else if (!strncasecmp(q, "finiteproto", 11) || !strncmp(q, "fp://", 5)) n++;
        else if (!strncasecmp(q, "naive", 5) || !strncasecmp(q, "socks", 5)) n++;
    }
    return n;
}

/* A whole subscription file. Buffers and the node array are on the heap, sized by the file: one
 * node slot per "vless" in the text.
 * The 64 MiB cap guards against a file that is not a subscription (a thousand nodes take hundreds
 * of kilobytes). NULL — the file did not open, is too large, or no memory; otherwise an array to
 * free, *cnt — usable nodes. */
#define VLESS_SUB_FILE_MAX ((size_t)64 << 20)
struct vless_node *vless_load_sub(const char *path, size_t *cnt, struct vless_sub_stats *st) {
    *cnt = 0;
    if (st) memset(st, 0, sizeof(*st));
    FILE *f = fopen(path, "r");
    if (!f) return NULL;
    fseek(f, 0, SEEK_END);
    long fl = ftell(f);
    rewind(f);
    if (fl < 0 || (size_t)fl > VLESS_SUB_FILE_MAX) { fclose(f); return NULL; }
    size_t sz = (size_t)fl;
    char *raw = malloc(sz + 1), *dec = malloc(sz + 16);
    if (!raw || !dec) { fclose(f); free(raw); free(dec); return NULL; }
    size_t n = fread(raw, 1, sz, f);
    fclose(f);
    raw[n] = '\0';
    dec[0] = '\0';
    const char *text = vless_sub_text(raw, n, dec, sz + 16);
    size_t hint = 1 + vless_sub_count(text);
    struct vless_node *nodes = calloc(hint, sizeof(*nodes));
    if (nodes) *cnt = vless_parse_sub(text, nodes, hint, st);
    free(raw);
    free(dec);
    return nodes;
}
