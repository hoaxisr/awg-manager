/* The transport and security half of a node link: link strings, transport and security
 * parameters, and whether they are usable. sublink.h says what is here and what is in sub.c. */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <arpa/inet.h>
#include "vless.h"
#include "sublink.h"
#include "hy2link.h"
/* tr_upgrade_target: a ws or httpupgrade path is checked here by the same rule the transport
 * builds the request with (src/proto/transport/trpath.c: strings only, no network or libraries). */
#include "trpath.h"
#include "xhopts.h"
/* vencp_b64url_len: the pqv key length uses the same base64url arithmetic as VLESS encryption.
 * Strings only, no cryptography. */
#include "vencp.h"
#include "fmask.h"

/* base64 decoding as subscriptions need it: line breaks inside, '=' padding optional, and the
 * URL-safe alphabet accepted too (some panels serve it). */
static int b64val(unsigned char c) {
    if (c >= 'A' && c <= 'Z') return c - 'A';
    if (c >= 'a' && c <= 'z') return c - 'a' + 26;
    if (c >= '0' && c <= '9') return c - '0' + 52;
    if (c == '+' || c == '-') return 62;
    if (c == '/' || c == '_') return 63;
    return -1;
}

size_t b64_decode(const char *in, size_t n, char *out, size_t out_n) {
    size_t o = 0;
    /* Unsigned and masked: only the low bits+8 bits are ever read (bits is at most 7 after the
     * decrement). A signed accumulator that kept the high bits overflowed on a long
     * subscription, which is undefined behaviour on untrusted text. */
    unsigned acc = 0;
    int bits = 0;
    for (size_t i = 0; i < n; i++) {
        /* '=' ends a block: the leftover bits are its remainder, not the start of the next
         * one. Without the reset, concatenated padded blocks ("QQ==QQ==") would shift
         * everything after them. */
        if (in[i] == '=') { acc = 0; bits = 0; continue; }
        int v = b64val((unsigned char)in[i]);
        if (v < 0) continue;                  /* line breaks, junk */
        acc = ((acc << 6) | (unsigned)v) & 0x3FFFu;   /* 14 bits are enough: 7 + 6 + 1 */
        bits += 6;
        if (bits >= 8) {
            bits -= 8;
            if (o + 1 < out_n) out[o++] = (char)((acc >> bits) & 0xFF);
        }
    }
    if (o < out_n) out[o] = '\0';
    return o;
}

/* An address where no peer can ever be: unspecified (0.0.0.0, ::), loopback (127.0.0.0/8, ::1)
 * and broadcast. Private networks are not on the list: a node in 10.0.0.0/8 is a valid setup
 * inside a LAN or over another tunnel.
 *
 * Names are not resolved: a parser of untrusted text does not go to the network. Panel stubs
 * write the address in digits.
 */
static int v6_leads_nowhere(const char *h) {
    char t[64];
    size_t n = strlen(h);
    if (n >= 2 && h[0] == '[' && h[n - 1] == ']') { h++; n -= 2; }
    if (n >= sizeof t) return 0;
    memcpy(t, h, n);
    t[n] = '\0';
    unsigned char b[16];
    if (inet_pton(AF_INET6, t, b) != 1) return 0;
    static const unsigned char zero[16];
    if (!memcmp(b, zero, 15)) return b[15] <= 1;
    if (memcmp(b, zero, 10) || b[10] != 0xff || b[11] != 0xff) return 0;
    return b[12] == 127 || !memcmp(b + 12, zero, 4);
}

int sl_host_leads_nowhere(const char *h) {
    if (!h || !h[0]) return 1;
    if (strchr(h, ':')) return v6_leads_nowhere(h);
    if (!strcmp(h, "0.0.0.0")) return 1;
    if (!strcmp(h, "255.255.255.255")) return 1;
    /* All of 127.0.0.0/8: stubs use both 127.0.0.1 and 127.0.0.53. */
    if (!strncmp(h, "127.", 4)) {
        const char *p = h + 4;
        while (*p) { if ((*p < '0' || *p > '9') && *p != '.') return 0; p++; }
        return 1;
    }
    return 0;
}

void sl_host_unbracket(char *h) {
    size_t n = strlen(h);
    if (n >= 2 && h[0] == '[' && h[n - 1] == ']') {
        memmove(h, h + 1, n - 2);
        h[n - 2] = '\0';
    }
}

/* A name or an address. security=tls needs to know: certificates are issued to names, so a node
 * given by an address without sni has nothing to verify against.
 *
 * Judged as a string, without inet_pton or network headers: a colon or a bracket means IPv6, only
 * digits and dots mean IPv4, anything else is a name. A wrong guess can only call something a
 * name, and the certificate is still verified for real. */
int sl_host_is_name(const char *h) {
    if (!h || !h[0]) return 0;
    if (strchr(h, ':') || h[0] == '[') return 0;
    for (const char *p = h; *p; p++)
        if ((*p < '0' || *p > '9') && *p != '.') return 1;
    return 0;
}

static int pct_hex(int c) {
    if (c >= '0' && c <= '9') return c - '0';
    c |= 32;
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    return -1;
}

void sl_pct_decode(char *s) {
    char *w = s;
    for (char *r = s; *r; r++) {
        if (*r == '%' && r[1] && r[2]) {
            /* Both must be hex digits: otherwise "%@@" in a node name would become a byte of
             * broken UTF-8. */
            int hi = pct_hex(r[1]), lo = pct_hex(r[2]);
            if (hi >= 0 && lo >= 0) {
                *w++ = (char)((hi << 4) | lo);
                r += 2;
                continue;
            }
        }
        *w++ = *r;
    }
    *w = '\0';
}

void sl_set_field(char *dst, size_t n, const char *src, size_t len) {
    if (len >= n) len = n - 1;
    memcpy(dst, src, len);
    dst[len] = '\0';
}

/* Decode first, then cut: a path in percent form (`%2Fstatic%2Fv1…`, as many panels encode it)
 * is up to three times longer than the path, and cutting it first sends a different path: the
 * xhttp or ws server answers 404 although the node is fine. */
void sl_set_pct(char *dst, size_t n, const char *src, size_t len) {
    char tmp[512];
    sl_set_field(tmp, sizeof(tmp), src, len);
    sl_pct_decode(tmp);
    sl_set_field(dst, n, tmp, strlen(tmp));
}

/* Drop an incomplete UTF-8 sequence from the end. A buffer cuts strings by byte, and the cut may
 * fall inside a character; what is left is a byte no consumer can interpret, so losing the last
 * character is better. */
void sl_utf8_trim_tail(char *s) {
    size_t n = strlen(s);
    if (!n) return;
    unsigned char last = (unsigned char)s[n - 1];
    if (last < 0x80) return;                       /* ASCII: nothing was cut */
    if ((last & 0xC0) != 0x80) { s[n - 1] = '\0'; return; }  /* a lead byte with no continuation */

    /* A continuation byte last: step back to the lead byte and check the length. */
    size_t at = n - 1, cont = 1;
    while (at && ((unsigned char)s[at - 1] & 0xC0) == 0x80) { at--; cont++; }
    if (!at) { s[0] = '\0'; return; }              /* only continuation bytes: all junk */
    unsigned char lead = (unsigned char)s[at - 1];
    /* ASCII before the continuation bytes: there is no lead byte, so only the continuation
     * bytes go and "ab\x80" keeps its "b". */
    if (lead < 0x80) { s[at] = '\0'; return; }
    size_t need = (lead & 0xE0) == 0xC0 ? 1 :
                  (lead & 0xF0) == 0xE0 ? 2 :
                  (lead & 0xF8) == 0xF0 ? 3 : 0;
    if (need && cont == need) return;              /* the sequence is complete */
    s[at - 1] = '\0';
}

static size_t utf8_put(unsigned long cp, char out[4]) {
    if (cp < 0x80) {
        out[0] = (char)cp;
        return 1;
    }
    if (cp < 0x800) {
        out[0] = (char)(0xC0 | cp >> 6);
        out[1] = (char)(0x80 | (cp & 63));
        return 2;
    }
    if (cp < 0x10000) {
        out[0] = (char)(0xE0 | cp >> 12);
        out[1] = (char)(0x80 | (cp >> 6 & 63));
        out[2] = (char)(0x80 | (cp & 63));
        return 3;
    }
    out[0] = (char)(0xF0 | cp >> 18);
    out[1] = (char)(0x80 | (cp >> 12 & 63));
    out[2] = (char)(0x80 | (cp >> 6 & 63));
    out[3] = (char)(0x80 | (cp & 63));
    return 4;
}

static long hex_run(const char *p, int n) {
    long v = 0;
    for (int i = 0; i < n; i++) {
        int h = pct_hex((unsigned char)p[i]);
        if (h < 0) return -1;
        v = v * 16 + h;
    }
    return v;
}

size_t sl_unescape(const char *p, int yaml, char out[4], size_t *olen) {
    char c = *p;
    *olen = 1;
    out[0] = c;
    switch (c) {
    case 'n': out[0] = '\n'; return 1;
    case 't': out[0] = '\t'; return 1;
    case 'r': out[0] = '\r'; return 1;
    case 'b': out[0] = '\b'; return 1;
    case 'f': out[0] = '\f'; return 1;
    case 'u': {
        long u = hex_run(p + 1, 4);
        if (u < 0) break;
        size_t used = 5;
        if (u >= 0xD800 && u <= 0xDBFF) {
            long lo = p[5] == '\\' && p[6] == 'u' ? hex_run(p + 7, 4) : -1;
            if (lo >= 0xDC00 && lo <= 0xDFFF) {
                u = 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00);
                used = 11;
            } else {
                u = 0xFFFD;
            }
        } else if (u >= 0xDC00 && u <= 0xDFFF) {
            u = 0xFFFD;
        }
        *olen = u ? utf8_put((unsigned long)u, out) : 0;
        return used;
    }
    case 'U':
    case 'x': {
        if (!yaml) break;
        int nd = c == 'U' ? 8 : 2;
        long u = hex_run(p + 1, nd);
        if (u < 0 || u > 0x10FFFF || (u >= 0xD800 && u <= 0xDFFF)) break;
        *olen = u ? utf8_put((unsigned long)u, out) : 0;
        return (size_t)nd + 1;
    }
    default:
        break;
    }
    return 1;
}

/* Node name: decoded before the cut, then a character the cut split is dropped. */
void sl_set_name(char *dst, size_t n, const char *src) {
    sl_set_pct(dst, n, src, strlen(src));
    sl_utf8_trim_tail(dst);
}

/* xhttp padding length from `xPaddingBytes`: a number ("512") or a range ("50-150"), both valid
 * in Xray. Parsed by hand rather than with sscanf, since the text is untrusted.
 *
 * If nothing parses, the fields stay as they are and Xray's default applies: unreadable padding
 * is no reason to reject the node, and the default suits most servers. */
static int sl_range(const char *v, unsigned long max, unsigned long *from, unsigned long *to) {
    unsigned long a = 0, b = 0;
    const char *p = v;
    while (*p == ' ' || *p == '"') p++;
    if (*p < '0' || *p > '9') return -1;
    while (*p >= '0' && *p <= '9') { a = a * 10 + (unsigned)(*p - '0'); p++; if (a > max) return -1; }
    if (*p == '-') {
        p++;
        if (*p < '0' || *p > '9') return -1;
        while (*p >= '0' && *p <= '9') { b = b * 10 + (unsigned)(*p - '0'); p++; if (b > max) return -1; }
    } else {
        b = a;
    }
    if (b < a) return -1;
    *from = a;
    *to = b;
    return 0;
}

void sl_set_fm(struct vless_node *n, const char *json, size_t len) {
    static const char pre[] = "finalmask: ";
    char why[sizeof n->skip_reason - sizeof pre + 1];
    struct fm_cfg c;
    if (n->fm == SL_BAD_FM && !strncmp(n->skip_reason, pre, sizeof pre - 1)) n->skip_reason[0] = '\0';
    n->fm = NULL;
    if (fm_parse(json, len, &c, why, sizeof why)) {
        n->fm = SL_BAD_FM;
        if (!n->skip_reason[0]) snprintf(n->skip_reason, sizeof n->skip_reason, "%s%s", pre, why);
        return;
    }
    size_t masks = c.n;
    fm_cfg_free(&c);
    if (!masks) return;
    char *buf = malloc(len + 1);
    size_t cl = buf ? fm_compact(json, len, buf, len + 1) : 0;
    n->fm = cl ? sl_intern(buf, cl) : SL_FULL;
    free(buf);
}

void sl_pad_range(struct vless_node *n, const char *v) {
    unsigned long a, b;
    if (sl_range(v, 65535, &a, &b) != 0) return;
    n->pad_from = (uint16_t)a;
    n->pad_to = (uint16_t)b;
}

/* xhttp `scMaxEachPostBytes`, in the same forms. 0 is no limit Xray would accept (its client
 * cannot split by it): left at the default. */
void sl_post_range(struct vless_node *n, const char *v) {
    unsigned long a, b;
    if (sl_range(v, 100000000, &a, &b) != 0 || a == 0) return;
    n->post_from = (uint32_t)a;
    n->post_to = (uint32_t)b;
}

/* ---- long node values: encryption, pqv, certificate pins ----------------------------------------
 *
 * The values are long (an ML-DSA-65 key is 2603 base64url characters, a VLESS encryption string
 * with an ML-KEM-768 key about 1600), and a field for them would add kilobytes to every node for
 * what few nodes have. So a node holds a pointer into a shared table where equal values are
 * stored once. Strings are never freed: the pointer must outlive the node and its copies (nodes
 * are copied by value), and parsing the same subscription again finds them already there. The
 * table is bounded: when it is full the node is unusable with a named reason, so a hostile
 * subscription with random keys cannot keep growing memory. */
#define SUB_INTERN_MAX (VLESS_SUB_NODES_MAX * 4)
#define SUB_INTERN_DL_MAX 512
static const char *g_intern[SUB_INTERN_MAX];
static size_t g_intern_len[SUB_INTERN_MAX];
static const char *g_intern_dl[SUB_INTERN_DL_MAX];
static size_t g_intern_dl_len[SUB_INTERN_DL_MAX];
static volatile int g_intern_lock;
/* Markers of unusable values (did not parse, or the table is full); sl_link_usable_pre names
 * the reason. */
const char SL_BAD_PQV[] = "!pqv";
const char SL_FULL[] = "!full";
const char SL_BAD_XHX[] = "!xhx";
const char SL_BAD_XHX_JSON[] = "!xhx-json";
const char SL_BAD_FM[] = "!fm";
const struct vless_node SL_DL_FULL;

static __thread const char *(*g_interner)(const char *v, size_t n);

void sl_set_interner(const char *(*fn)(const char *v, size_t n)) { g_interner = fn; }

static const char *intern_in(const char **tab, size_t *lens, int max, const char *v, size_t n) {
    while (__atomic_test_and_set(&g_intern_lock, __ATOMIC_ACQUIRE)) { }
    const char *r = NULL;
    for (int i = 0; i < max; i++) {
        if (!tab[i]) {
            char *c = malloc(n + 1);
            if (c) { memcpy(c, v, n); c[n] = '\0'; tab[i] = c; lens[i] = n; r = c; }
            break;
        }
        if (lens[i] == n && !memcmp(tab[i], v, n)) { r = tab[i]; break; }
    }
    __atomic_clear(&g_intern_lock, __ATOMIC_RELEASE);
    return r;
}

const char *sl_intern(const char *v, size_t n) {
    const char *r = g_interner ? g_interner(v, n) : intern_in(g_intern, g_intern_len, SUB_INTERN_MAX, v, n);
    return r ? r : SL_FULL;
}

const struct vless_node *sl_intern_node(const struct vless_node *d) {
    if (g_interner) return (const struct vless_node *)g_interner((const char *)d, sizeof *d);
    return (const struct vless_node *)intern_in(g_intern_dl, g_intern_dl_len, SUB_INTERN_DL_MAX,
                                                (const char *)d, sizeof *d);
}

static const char *const xh_names[] = {
    "sessionIDPlacement", "sessionPlacement", "sessionIDKey", "sessionKey", "sessionIDTable",
    "sessionIDLength", "seqPlacement", "seqKey", "uplinkDataPlacement", "uplinkDataKey",
    "uplinkChunkSize", "uplinkHTTPMethod", "xPaddingObfsMode", "xPaddingKey", "xPaddingHeader",
    "xPaddingPlacement", "xPaddingMethod", "scMaxBufferedPosts", "noGRPCHeader",
    "serverMaxHeaderBytes", "scMinPostsIntervalMs", "xmux.maxConcurrency", "xmux.maxConnections",
    "xmux.cMaxReuseTimes", "xmux.hMaxRequestTimes", "xmux.hMaxReusableSecs", "xmux.hKeepAlivePeriod",
};
#define XH_NAMES (sizeof xh_names / sizeof *xh_names)

static size_t xh_index(const char *k, size_t kn) {
    size_t i = 0;
    while (i < XH_NAMES && !(strlen(xh_names[i]) == kn && !strncasecmp(xh_names[i], k, kn))) i++;
    return i;
}

void sl_xh_put(struct sl_xh *a, const char *key, const char *val, size_t vlen) {
    size_t idx = xh_index(key, strlen(key));
    if (idx == XH_NAMES) return;
    key = xh_names[idx];
    for (size_t i = 0; i < vlen; i++) {
        unsigned char c = (unsigned char)val[i];
        if (c < 0x20 || c == 0x7f || strchr("\"\\=;,", c)) { a->bad = 1; return; }
    }
    char tmp[sizeof a->buf];
    size_t o = 0, kn = strlen(key);
    int placed = !vlen;
    for (size_t at = 0; at < a->n;) {
        const char *line = a->buf + at, *e = memchr(line, '\n', a->n - at);
        size_t len = (size_t)(e - line) + 1;
        size_t li = xh_index(line, (size_t)((const char *)memchr(line, '=', len) - line));
        at += len;
        if (li == idx) continue;
        if (!placed && li > idx) {
            if (o + kn + vlen + 2 >= sizeof tmp) { a->bad = 1; return; }
            o += (size_t)snprintf(tmp + o, sizeof tmp - o, "%s=%.*s\n", key, (int)vlen, val);
            placed = 1;
        }
        if (o + len > sizeof tmp) { a->bad = 1; return; }
        memcpy(tmp + o, line, len);
        o += len;
    }
    if (!placed) {
        if (o + kn + vlen + 2 >= sizeof tmp) { a->bad = 1; return; }
        o += (size_t)snprintf(tmp + o, sizeof tmp - o, "%s=%.*s\n", key, (int)vlen, val);
    }
    memcpy(a->buf, tmp, o);
    a->n = o;
}

void sl_xh_done(struct vless_node *n, const struct sl_xh *a) {
    n->xhx = a->bad ? SL_BAD_XHX : a->n ? sl_intern(a->buf, a->n) : NULL;
}

void sl_intern_reset(void) {
    while (__atomic_test_and_set(&g_intern_lock, __ATOMIC_ACQUIRE)) { }
    for (int i = 0; i < SUB_INTERN_MAX; i++) {
        free((void *)g_intern[i]);
        g_intern[i] = NULL;
    }
    for (int i = 0; i < SUB_INTERN_DL_MAX; i++) {
        free((void *)g_intern_dl[i]);
        g_intern_dl[i] = NULL;
    }
    __atomic_clear(&g_intern_lock, __ATOMIC_RELEASE);
}

/* pqv / mldsa65Verify: an ML-DSA-65 public key, base64url, exactly 1952 bytes. */
void sl_set_pqv(struct vless_node *n, const char *v) {
    n->pqv = NULL;
    if (!v[0]) return;
    if (vencp_b64url_len(v, strlen(v)) != 1952) { n->pqv = SL_BAD_PQV; return; }
    n->pqv = sl_intern(v, strlen(v));
}

/* ---- certificate checks of a node: pcs, pks, vcn, allowInsecure ---------------------------------
 *
 * Xray-core (transport/internet/tls/config.go, infra/conf/transport_security.go) has two ways not
 * to rely on the root store: pinnedPeerCertSha256 (`pcs` in a link) — hex SHA-256 of the
 * certificate, comma separated, OpenSSL colons allowed; verifyPeerCertByName (`vcn`) — names the
 * chain is verified against INSTEAD of the SNI. Xray dropped allowInsecure (a config with it does
 * not load), but links and other subscriptions still carry it. sing-box pins differently: base64
 * SHA-256 of the SubjectPublicKeyInfo (`certificate_public_key_sha256`). It is kept apart (pks)
 * because it hashes another part of the certificate, and the two kinds must not be mixed.
 *
 * A pin is brought to one form (64 lowercase hex digits) here, when parsed: a bad pin makes the
 * node unusable with a named reason, rather than a check that silently compares nothing. */
const char SL_BAD_PIN[] = "!pin";

static const char *pin_slot(const struct vless_node *n, int spki) { return spki ? n->pks : n->pcs; }

static void add_pins(struct vless_node *n, const char *v, int spki, int hex) {
    const char *cur = pin_slot(n, spki);
    if (cur == SL_BAD_PIN || cur == SL_FULL) return;
    char buf[1100];
    size_t bl = 0;
    if (cur) bl = (size_t)snprintf(buf, sizeof buf, "%s", cur);
    const char *p = v;
    int bad = 0;
    while (*p && !bad) {
        const char *e = strchr(p, ',');
        size_t tn = e ? (size_t)(e - p) : strlen(p);
        while (tn && (*p == ' ' || *p == '\t')) { p++; tn--; }
        while (tn && (p[tn - 1] == ' ' || p[tn - 1] == '\t')) tn--;
        unsigned char raw[32] = { 0 };
        if (tn) {
            if (!hex) {
                char d[48];
                bad = tn < 43 || tn > 44 || b64_decode(p, tn, d, sizeof d) != 32;
                if (!bad) memcpy(raw, d, 32);
            } else {
                /* Colons are the usual OpenSSL notation (`AB:CD:…`); Xray drops them. */
                size_t k = 0;
                for (size_t i = 0; i < tn && !bad; i++) {
                    int c = (unsigned char)p[i], h;
                    if (c == ':') continue;
                    h = c >= '0' && c <= '9' ? c - '0' : c >= 'a' && c <= 'f' ? c - 'a' + 10
                        : c >= 'A' && c <= 'F' ? c - 'A' + 10 : -1;
                    if (h < 0 || k >= 64) { bad = 1; break; }
                    if (k & 1) raw[k / 2] = (unsigned char)(raw[k / 2] << 4 | h);
                    else raw[k / 2] = (unsigned char)h;
                    k++;
                }
                if (!bad && k != 64) bad = 1;
            }
            if (!bad) {
                if (bl + 66 >= sizeof buf) bad = 1;
                else {
                    if (bl) buf[bl++] = ',';
                    for (int i = 0; i < 32; i++) bl += (size_t)snprintf(buf + bl, 3, "%02x", raw[i]);
                }
            }
        }
        if (!e) break;
        p = e + 1;
    }
    const char *r = bad ? SL_BAD_PIN : bl ? sl_intern(buf, bl) : NULL;
    if (spki) n->pks = r; else n->pcs = r;
}

void sl_add_pins(struct vless_node *n, const char *v, int spki) { add_pins(n, v, spki, !spki); }

/* verifyPeerCertByName: comma separated names, surrounding blanks dropped, empty ones skipped
 * (as Xray reads it). */
void sl_set_vcn(struct vless_node *n, const char *v) {
    char buf[300];
    size_t bl = 0;
    n->vcn = NULL;
    const char *p = v;
    while (*p) {
        const char *e = strchr(p, ',');
        size_t tn = e ? (size_t)(e - p) : strlen(p);
        while (tn && (*p == ' ' || *p == '\t')) { p++; tn--; }
        while (tn && (p[tn - 1] == ' ' || p[tn - 1] == '\t')) tn--;
        if (tn) {
            if (bl + tn + 2 >= sizeof buf) { n->vcn = SL_BAD_PIN; return; }
            if (bl) buf[bl++] = ',';
            memcpy(buf + bl, p, tn);
            bl += tn;
        }
        if (!e) break;
        p = e + 1;
    }
    if (bl) n->vcn = sl_intern(buf, bl);
}

/* ECH: the node's ECHConfigList in base64 (Xray echConfigList, `ech=` in a link, Clash
 * ech-opts.config, sing-box ech.config). Only the form is checked here: the list starts with its
 * length and holds a version 0xfe0d entry. Which entry is usable is decided by ech_pick at connect
 * time (ech.c is not linked into the subscription tests). A "domain+https://DNS server" value
 * (Xray fetches the config from DNS) is not supported: the node is skipped with a reason rather
 * than sent without ECH, since sending the name in clear is what ECH is there to prevent. */
const char SL_BAD_ECH[] = "!ech";
const char SL_ECH_DNS[] = "!ech-dns";
void sl_set_ech(struct vless_node *n, const char *v) {
    char raw[1100];
    n->ech = NULL;
    if (!v[0]) return;
    if (strstr(v, "://")) { n->ech = SL_ECH_DNS; return; }
    size_t bl = b64_decode(v, strlen(v), raw, sizeof raw);
    int ok = bl >= 8 && bl < sizeof raw && (size_t)(((unsigned char)raw[0] << 8) | (unsigned char)raw[1]) + 2 == bl;
    if (ok) {
        ok = 0;
        for (size_t p = 2; p + 4 <= bl;) {
            size_t l = ((unsigned char)raw[p + 2] << 8) | (unsigned char)raw[p + 3];
            if (p + 4 + l > bl) { ok = 0; break; }
            if ((((unsigned char)raw[p] << 8) | (unsigned char)raw[p + 1]) == 0xfe0d) ok = 1;
            p += 4 + l;
        }
    }
    n->ech = ok ? sl_intern(v, strlen(v)) : SL_BAD_ECH;
}

/* allowInsecure / insecure / skip-cert-verify: 1, true, yes — on. Anything else, empty included, is
 * off: certificate checks are never turned off by a guess. */
static int sl_printable(const char *s) {
    for (const unsigned char *p = (const unsigned char *)s; *p; p++)
        if (*p < 0x20 || *p == 0x7f) return 0;
    return 1;
}

int sl_truthy(const char *v) {
    return !strcmp(v, "1") || !strcasecmp(v, "true") || !strcasecmp(v, "yes");
}

/* --insecure (vless.h). */
static volatile int g_insecure;
void vless_set_insecure(int on) { g_insecure = on ? 1 : 0; }
int vless_insecure(void) { return g_insecure; }

static const char *volatile g_fp;
void vless_set_fp(const char *fp) { g_fp = fp && fp[0] ? fp : NULL; }
const char *vless_fp(void) { return g_fp; }

/* On the heap, at the value's full length: pqv and encryption values run to thousands of
 * characters, and a fixed buffer would cut them. */
char *sl_param_dup(const char *v, size_t vlen) {
    char *c = malloc(vlen + 1);
    if (!c) return NULL;
    memcpy(c, v, vlen);
    c[vlen] = '\0';
    sl_pct_decode(c);
    return c;
}

int sl_uuid_parse(const char *s, unsigned char out[16]) {
    int hi = -1;
    size_t k = 0;
    for (; *s && k < 16; s++) {
        if (*s == '-') continue;
        int c = (unsigned char)*s, v;
        v = c >= '0' && c <= '9' ? c - '0' : (c | 32) >= 'a' && (c | 32) <= 'f' ? (c | 32) - 'a' + 10 : -1;
        if (v < 0) return -1;
        if (hi < 0) hi = v;
        else { out[k++] = (unsigned char)(hi << 4 | v); hi = -1; }
    }
    /* Exactly 16 bytes, no dangling nibble, and nothing after it but hyphens. */
    while (*s == '-') s++;
    return (k == 16 && hi < 0 && !*s) ? 0 : -1;
}

uint16_t sl_port_of(const char *s) {
    if (!*s) return 0;
    unsigned long v = 0;
    for (; *s; s++) {
        if (*s < '0' || *s > '9') return 0;
        v = v * 10 + (unsigned long)(*s - '0');
        if (v > 65535) return 0;
    }
    return (uint16_t)v;
}


/* A header the request can carry as it is: a name of HTTP token characters, up to 40, and a value
 * up to 250 without control characters (a line break would turn one header into two). */
int sl_hdr_valid(const char *key, const char *val) {
    size_t kn = strlen(key), vn = strlen(val);
    if (!kn || kn > 40 || vn > 250) return 0;
    for (size_t i = 0; i < kn; i++) {
        unsigned char c = (unsigned char)key[i];
        if (!((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
              strchr("!#$%&'*+-.^_`|~", c)))
            return 0;
    }
    for (size_t i = 0; i < vn; i++) {
        unsigned char c = (unsigned char)val[i];
        if ((c < 0x20 && c != '\t') || c == 0x7f) return 0;
    }
    return 1;
}

static int hdr_lines_ok(const char *s, int ws) {
    while (*s) {
        const char *e = strchr(s, '\n'), *c = strchr(s, ':');
        if (!e || !c || c > e || c[1] != ' ') return 0;
        char k[64], v[256];
        size_t kn = (size_t)(c - s), vn = (size_t)(e - c - 2);
        if (kn >= sizeof k || vn >= sizeof v) return 0;
        memcpy(k, s, kn); k[kn] = '\0';
        memcpy(v, c + 2, vn); v[vn] = '\0';
        if (!sl_hdr_valid(k, v) || !strcasecmp(k, "host")) return 0;
        if (ws && (!strcasecmp(k, "upgrade") || !strcasecmp(k, "connection") || !strcasecmp(k, "sec-websocket-key") ||
                   !strcasecmp(k, "sec-websocket-version") || !strcasecmp(k, "sec-websocket-extensions")))
            return 0;
        s = e + 1;
    }
    return 1;
}

/* A ws or httpupgrade node: what Xray itself would fail on, caught in advance with a reason.
 * 1 — unusable (reason in skip_reason).
 *
 *   - a path Xray would not parse unambiguously, or ws would not open at all (trpath.h): the same
 *     rule as the transport, so "usable" here means "opens" there;
 *   - host is the Host header: no blanks or control characters, or the request line breaks;
 *   - config headers that did not fit or are invalid (headers_bad, see xray_headers in sub.c). */
static int upg_node_bad(struct vless_node *n, int ws) {
    char tgt[1024];
    const char *why = "";
    if (tr_upgrade_target(n->path, ws, tgt, sizeof(tgt), &why) != 0) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "%s", why);
        return 1;
    }
    for (const char *p = n->http_host; *p; p++) {
        if ((unsigned char)*p <= 0x20 || *p == 0x7f) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "invalid host for %s", n->type);
            return 1;
        }
    }
    if (n->headers_bad || !hdr_lines_ok(n->headers, ws)) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "invalid headers for %s", n->type);
        return 1;
    }
    return 0;
}


int sl_link_param(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if (klen == 4 && !strncmp(k, "type", 4)) {
        sl_set_pct(n->type, sizeof(n->type), v, vlen);
        if (!strcmp(n->type, "raw")) snprintf(n->type, sizeof(n->type), "tcp");
        else if (!strcmp(n->type, "websocket")) snprintf(n->type, sizeof(n->type), "ws");
        else if (!strcmp(n->type, "finiteproto") || !strcmp(n->type, "fp")) {
            snprintf(n->type, sizeof(n->type), "xhttp");
            snprintf(n->mode, sizeof(n->mode), "finiteproto");
        }
    }
    else if (klen == 8 && !strncmp(k, "security", 8)) sl_set_pct(n->security, sizeof(n->security), v, vlen);
    else if (klen == 3 && !strncmp(k, "sni", 3)) sl_set_pct(n->sni, sizeof(n->sni), v, vlen);
    else if (klen == 2 && !strncmp(k, "fp", 2)) sl_set_pct(n->fp, sizeof(n->fp), v, vlen);
    else if (klen == 3 && !strncmp(k, "pbk", 3)) sl_set_pct(n->pbk, sizeof(n->pbk), v, vlen);
    else if (klen == 3 && !strncmp(k, "sid", 3)) sl_set_pct(n->sid, sizeof(n->sid), v, vlen);
    else if (klen == 10 && !strncmp(k, "headerType", 10)) { if (vlen == 4 && !strncmp(v, "http", 4)) n->tcp_http = 1; }
    /* pqv: Xray-core's post-quantum check of Reality, an ML-DSA-65 signature in the server's
     * temporary certificate. A long value, see sl_intern. */
    else if (klen == 3 && !strncmp(k, "pqv", 3)) {
        char *d = sl_param_dup(v, vlen);
        if (d) { sl_set_pqv(n, d); free(d); } else n->pqv = SL_BAD_PQV;
    }
    /* pcs / vcn: Xray-core's pinnedPeerCertSha256 and verifyPeerCertByName. A subscription may
     * carry allowInsecure (or insecure, as panels write it), but cannot turn the check off by
     * itself: see sl_link_usable_pre. */
    else if ((klen == 3 && !strncmp(k, "pcs", 3)) || (klen == 3 && !strncmp(k, "vcn", 3))) {
        char *d = sl_param_dup(v, vlen);
        if (d) {
            if (k[0] == 'p') sl_add_pins(n, d, 0); else sl_set_vcn(n, d);
            free(d);
        } else if (k[0] == 'p') n->pcs = SL_BAD_PIN; else n->vcn = SL_BAD_PIN;
    }
    else if (klen == 3 && !strncmp(k, "pks", 3)) {
        char *d = sl_param_dup(v, vlen);
        if (d) { add_pins(n, d, 1, 1); free(d); } else n->pks = SL_BAD_PIN;
    }
    else if (klen == 7 && !strncmp(k, "headers", 7)) {
        char *d = sl_param_dup(v, vlen);
        if (d && strlen(d) < sizeof(n->headers) && hdr_lines_ok(d, 0)) snprintf(n->headers, sizeof(n->headers), "%s", d);
        else n->headers_bad = 1;
        free(d);
    }
    else if (klen == 3 && !strncmp(k, "ech", 3)) {
        char *d = sl_param_dup(v, vlen);
        if (d) { sl_set_ech(n, d); free(d); } else n->ech = SL_BAD_ECH;
    }
    else if ((klen == 13 && !strncmp(k, "allowInsecure", 13)) ||
             (klen == 14 && !strncmp(k, "allow_insecure", 14)) ||
             (klen == 8 && !strncmp(k, "insecure", 8))) {
        char *d = sl_param_dup(v, vlen);
        if (d) { if (sl_truthy(d)) n->allow_insecure = 1; free(d); }
    }
    else if (klen == 2 && !strncmp(k, "fm", 2)) {
        char *d = sl_param_dup(v, vlen);
        if (d) sl_set_fm(n, d[0] ? d : "null", d[0] ? strlen(d) : 4);
        else n->fm = SL_FULL;
        free(d);
    }
    else if ((klen == 15 && !strncmp(k, "record_fragment", 15)) || (klen == 8 && !strncmp(k, "anti_dpi", 8))) {
        char *d = sl_param_dup(v, vlen);
        if (d) { if (sl_truthy(d)) n->record_fragment = 1; free(d); }
    }
    else if (klen == 4 && !strncmp(k, "path", 4)) sl_set_pct(n->path, sizeof(n->path), v, vlen);
    else if (klen == 11 && !strncmp(k, "serviceName", 11)) sl_set_pct(n->service, sizeof(n->service), v, vlen);
    else if (klen == 4 && !strncmp(k, "mode", 4)) sl_set_pct(n->mode, sizeof(n->mode), v, vlen);
    /* FiniteProto v2 parameters */
    else if ((klen == 5 && !strncmp(k, "fp_id", 5)) || (klen == 9 && !strncmp(k, "client_id", 9)))
        sl_set_pct(n->fp_id, sizeof(n->fp_id), v, vlen);
    else if (klen == 9 && !strncmp(k, "fp_secret", 9))
        sl_set_pct(n->fp_secret, sizeof(n->fp_secret), v, vlen);
    else if (klen == 9 && !strncmp(k, "fp_method", 9))
        sl_set_pct(n->fp_method, sizeof(n->fp_method), v, vlen);
    else if (klen == 8 && !strncmp(k, "fp_chunk", 8)) {
        char *d = sl_param_dup(v, vlen);
        if (d) {
            char *ep = NULL;
            unsigned long cv = (*d >= '0' && *d <= '9') ? strtoul(d, &ep, 10) : 0;
            if (ep && !*ep && cv > 0) n->fp_chunk = cv > 1048576UL ? 1048576U : (uint32_t)cv;
            free(d);
        }
    }
    else if (klen == 7 && !strncmp(k, "fp_conc", 7)) {
        char *d = sl_param_dup(v, vlen);
        if (d) {
            char *ep = NULL;
            unsigned long cv = (*d >= '0' && *d <= '9') ? strtoul(d, &ep, 10) : 0;
            if (ep && !*ep && cv > 0) n->fp_conc = cv > 255UL ? 255U : (uint8_t)cv;
            free(d);
        }
    }
    else if (klen == 4 && !strncmp(k, "fp_v", 4)) {
        /* Version indicator, accepted */
    }
    /* host: the Host header of ws and httpupgrade. xhttp links carry it too, but xhttp does not
     * use it: its :authority is the sni, else the address. */
    else if (klen == 4 && !strncmp(k, "host", 4)) sl_set_pct(n->http_host, sizeof(n->http_host), v, vlen);
    else return 0;
    return 1;
}

int sl_link_parse(const char *url, const char *scheme, struct vless_node *n, sl_own_fn own,
                  const char **secret, size_t *secret_n, unsigned flags) {
    memset(n, 0, sizeof(*n));
    size_t scl = strlen(scheme);
    if (strncmp(url, scheme, scl) != 0) return -1;
    const char *p = url + scl;

    const char *at = NULL;
    if (flags & SL_LINK_ANON) {
        for (const char *q = p + strcspn(p, "/?#"); q > p;)
            if (*--q == '@') { at = q; break; }
    } else at = strchr(p, '@');
    if (!at && !(flags & SL_LINK_ANON)) return -1;
    size_t sn = at ? (size_t)(at - p) : 0;
    sl_set_field(n->uuid, sizeof(n->uuid), p, sn);
    if (secret) *secret = p;
    if (secret_n) *secret_n = sn;

    if (at) p = at + 1;
    /* Host and port end at '?' or '#', parameters end at '#': a name "Fast?type=ws" is not
     * parameters, and "host?type=tcp#name:1" has no port 1. */
    const char *hash = strchr(p, '#');
    const char *hp_end = hash ? hash : p + strlen(p);
    const char *qmark = memchr(p, '?', (size_t)(hp_end - p));
    const char *hend = qmark ? qmark : hp_end;
    if (flags & SL_LINK_PORTS) {
        const char *slash = memchr(p, '/', (size_t)(hend - p));
        if (slash) hend = slash;
    }
    const char *colon;
    if (*p == '[') {
        const char *rb = memchr(p, ']', (size_t)(hend - p));
        if (!rb || rb == p + 1) return -1;
        if (rb + 1 < hend ? rb[1] != ':' : !(flags & SL_LINK_PORTS)) return -1;
        int has_colon = 0;
        for (const char *c = p + 1; c < rb; c++) {
            if (*c == ':') has_colon = 1;
            else if (!((*c >= '0' && *c <= '9') || (*c >= 'a' && *c <= 'f') || (*c >= 'A' && *c <= 'F') || *c == '.'))
                return -1;
        }
        if (!has_colon) return -1;
        colon = rb + 1;
        sl_set_field(n->host, sizeof(n->host), p + 1, (size_t)(rb - p - 1));
    } else {
        colon = memchr(p, ':', (size_t)(hend - p));
        if (!colon && !(flags & SL_LINK_PORTS)) return -1;
        if (!colon) colon = hend;
        sl_set_field(n->host, sizeof(n->host), p, (size_t)(colon - p));
    }
    if ((flags & SL_LINK_PORTS) && !n->host[0]) return -1;
    if (colon >= hend) {
        n->port = 443;
    } else if (flags & SL_LINK_PORTS) {
        int pr = hy2_ports_canon(colon + 1, (size_t)(hend - colon - 1), n->ports, sizeof n->ports, &n->port);
        if (pr < 0) return -1;
        if (pr > 0) snprintf(n->skip_reason, sizeof n->skip_reason, "port list too long");
        else if (memchr(colon + 1, ',', (size_t)(hend - colon - 1)) || memchr(colon + 1, '-', (size_t)(hend - colon - 1)))
            n->hop_mode = HY2_HOP_LOCAL | HY2_HOP_REMOTE;
    } else {
        /* Digits only, up to the end of the host, 1..65535: atoi with a cast would turn
         * ":70000" into 4464, ":-1" into 65535 and ":443abc" into 443. */
        char pnum[8];
        const char *pe = colon + 1;
        size_t pl = 0;
        while (pe < hend && *pe >= '0' && *pe <= '9' && pl + 1 < sizeof(pnum))
            pnum[pl++] = *pe++;
        pnum[pl] = '\0';
        if (!pl || pe != hend) return -1;
        n->port = sl_port_of(pnum);
    }
    if (!n->port) return -1;

    if (hash) {
        sl_set_name(n->name, sizeof(n->name), hash + 1);
    }

    /* Defaults VLESS implies for omitted fields: type=tcp here, security=none in
     * sl_link_usable_pre. */
    snprintf(n->type, sizeof(n->type), "tcp");
    const char *ex = NULL;
    size_t exn = 0;
    if (qmark) {
        const char *end = hash && hash > qmark ? hash : qmark + strlen(qmark);
        const char *k = qmark + 1;
        while (k < end) {
            const char *amp = memchr(k, '&', (size_t)(end - k));
            const char *stop = amp ? amp : end;
            const char *eq = memchr(k, '=', (size_t)(stop - k));
            if (eq) {
                size_t klen = (size_t)(eq - k), vlen = (size_t)(stop - eq - 1);
                const char *v = eq + 1;
                if (klen == 5 && !strncmp(k, "extra", 5)) ex = v, exn = vlen;
                else if (!own || !own(n, k, klen, v, vlen)) sl_link_param(n, k, klen, v, vlen);
            }
            if (!amp) break;
            k = amp + 1;
        }
    }

    if (ex && !strcmp(n->type, "xhttp")) {
        char *d = sl_param_dup(ex, exn);
        if (d) { sl_parse_extra(n, d); free(d); } else n->xhx = SL_BAD_XHX_JSON;
    }

    return 0;
}

/* First half of the usability check (sublink.h). Reasons must fit in skip_reason (96 bytes). */
int sl_link_usable_pre(struct vless_node *n) {
    if (!n->security[0]) snprintf(n->security, sizeof(n->security), "none");
    if (n->fm == SL_BAD_FM) return 1;

    /* Post-quantum fields. The markers come from parsing (set_encryption in sub.c, sl_set_pqv): a
     * value that breaks Xray's rule, or a full table of long values. */
    if (!sl_printable(n->type) || !sl_printable(n->security) || !sl_printable(n->mode)) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "control byte in type, security or mode");
        return 1;
    }
    if (n->pqv == SL_BAD_PQV) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "pqv: not an ML-DSA-65 key");
        return 1;
    }
    if (n->tcp_http && !strcmp(n->type, "tcp")) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "tcp headerType=http is not supported");
        return 1;
    }
    if (!strcmp(n->type, "xhttp")) {
        if (!strcmp(n->mode, "finiteproto")) {
            if (!n->fp_id[0] || !sl_printable(n->fp_id)) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "finiteproto: missing fp_id");
                return 1;
            }
            size_t slen = strlen(n->fp_secret);
            int hex_ok = (slen == 64);
            for (size_t i = 0; hex_ok && i < 64; i++) {
                char c = n->fp_secret[i];
                if (!((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')))
                    hex_ok = 0;
            }
            if (!hex_ok) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "finiteproto: fp_secret must be 64 hex chars");
                return 1;
            }
            for (const char *p = n->http_host; *p; p++) {
                if ((unsigned char)*p <= 0x20 || *p == 0x7f) {
                    snprintf(n->skip_reason, sizeof(n->skip_reason), "invalid host for xhttp");
                    return 1;
                }
            }
            if (n->headers_bad || !hdr_lines_ok(n->headers, 0)) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "invalid headers for xhttp");
                return 1;
            }
        } else {
            struct xh_opts o;
            if (n->xhx == SL_BAD_XHX || n->xhx == SL_BAD_XHX_JSON) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "%s",
                         n->xhx == SL_BAD_XHX ? "xhttp: invalid extra value" : "xhttp: extra does not parse");
                return 1;
            }
            if (n->xhx == SL_FULL) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "too many distinct keys");
                return 1;
            }
            if (n->headers_bad || !hdr_lines_ok(n->headers, 0)) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "invalid headers for xhttp");
                return 1;
            }
            if (n->dl == &SL_DL_FULL) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "too many distinct download blocks");
                return 1;
            }
            if (n->dl && n->dl->skip_reason[0]) {
                snprintf(n->skip_reason, sizeof(n->skip_reason), "downloadSettings: %.77s", n->dl->skip_reason);
                return 1;
            }
            if (xh_opts_parse(n->xhx, n->mode, n->pad_to, &o, n->skip_reason, sizeof(n->skip_reason)))
                return 1;
        }
    }
    if (n->encryption == SL_FULL || n->pqv == SL_FULL || n->pcs == SL_FULL || n->pks == SL_FULL ||
        n->vcn == SL_FULL || n->fm == SL_FULL) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "too many distinct keys");
        return 1;
    }
    /* Certificate checks concern only security=tls: reality proves the server by its own
     * protocol, and pcs/vcn/allowInsecure in such a link mean nothing. A bad pin on tls makes the
     * node unusable rather than leaving a check that silently verifies nothing. */
    if (!strcmp(n->security, "tls")) {
        if (n->ech == SL_BAD_ECH) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "ech: not a base64 ECHConfigList");
            return 1;
        }
        if (n->ech == SL_ECH_DNS) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "ech: DNS lookup is not supported");
            return 1;
        }
        if (n->ech == SL_FULL) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "too many distinct keys");
            return 1;
        }
        if (n->pcs == SL_BAD_PIN) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "pcs: not a hex SHA-256");
            return 1;
        }
        if (n->pks == SL_BAD_PIN) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "certificate_public_key_sha256: not a SHA-256");
            return 1;
        }
        if (n->vcn == SL_BAD_PIN) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "vcn: name list too long");
            return 1;
        }
        /* A security decision: a subscription cannot turn certificate checks off by itself. A
         * node with allowInsecure is usable only when the user gave --insecure (except for
         * TrustTunnel/AnyTLS links where allow_insecure is part of the URI spec). */
        if (n->allow_insecure && !g_insecure && strcmp(n->proto_str, "tt") != 0 &&
            strcmp(n->proto_str, "anytls") != 0) {
            snprintf(n->skip_reason, sizeof(n->skip_reason), "allowInsecure: needs --insecure");
            return 1;
        }
        n->insecure = (g_insecure || (n->allow_insecure &&
                       (!strcmp(n->proto_str, "tt") || !strcmp(n->proto_str, "anytls")))) ? 1 : 0;
    }

    return 0;
}

int sl_link_nowhere(struct vless_node *n) {
    if (!sl_host_leads_nowhere(n->host)) return 0;
    snprintf(n->skip_reason, sizeof(n->skip_reason), "%.20s: nobody to answer", n->host);
    return 1;
}

/* Second half (sublink.h): what VLESS checks after the id. */
int sl_link_usable_post(struct vless_node *n) {
    if (strcmp(n->security, "reality") != 0 && strcmp(n->security, "none") != 0 &&
        strcmp(n->security, "tls") != 0) {
        /* xtls is not TLS with chain verification but an exchange of its own, which the client
         * does not have. tls is supported (certverify.c). */
        snprintf(n->skip_reason, sizeof(n->skip_reason), "security=%s is not supported",
                 n->security);
        return 1;
    }

    /* Plain TLS needs a name: the sni is what the certificate is verified against. Without it
     * that is the address, and a certificate is almost never issued to an IP. Saying so here is
     * clearer than a failed handshake that blames the server. (For reality an empty sni is
     * valid, see below.) */
    if (strcmp(n->security, "tls") == 0 && !n->sni[0] && !sl_host_is_name(n->host) &&
        !((!strcmp(n->proto_str, "anytls") || !strcmp(n->proto_str, "tt")) &&
          (n->insecure || n->pcs || n->pks))) {
        snprintf(n->skip_reason, sizeof(n->skip_reason),
                 "tls to an IP address without sni: nothing to verify");
        return 1;
    }
    /* Reality needs the server key. It does not need sni: the server matches the name against
     * its `serverNames`, and an empty string there is valid — the server then expects a
     * ClientHello without server_name, which is what Xray clients send. Panels serve such nodes
     * in every format (vless://, Clash YAML), so it is the server owner's choice, not a loss on
     * the way. reality.c builds the ClientHello without a name. */
    if (!strcmp(n->security, "reality") && !n->pbk[0]) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "reality without pbk");
        return 1;
    }
    const int upg_ws = !strcmp(n->type, "ws"), upg = upg_ws || !strcmp(n->type, "httpupgrade");
    if (strcmp(n->type, "tcp") != 0 && strcmp(n->type, "grpc") != 0 &&
        strcmp(n->type, "xhttp") != 0 && !upg) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "transport %s is not supported", n->type);
        return 1;
    }
    if (n->flow[0] && strcmp(n->type, "tcp") != 0) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "vision cannot run over %s", n->type);
        return 1;
    }
    if (upg && upg_node_bad(n, upg_ws)) return 1;

    /* A node that leads nowhere gets a reason of its own rather than failing to connect. Panels
     * that bind a subscription to devices answer an unknown client with a stub: valid vless://
     * links to `0.0.0.0:1`, with the message for the user in the node name ("📱 Wrong client",
     * "🔌 Device limit reached"). Such a link parses, but nothing can answer there: connect
     * would reach this machine (the kernel takes 0.0.0.0 as local) or someone else's network.
     * The reason is reported with the node name as its example, so the panel's message reaches
     * the user. Private networks are not rejected (sl_host_leads_nowhere). */
    if (strcmp(n->proto_str, "socks5") != 0 && sl_link_nowhere(n)) return 1;

    /* Supported xhttp modes (empty means auto):
     *
     *   stream-one — one POST, the request body goes up and the response body down. Xray picks
     *     it for reality with mode=auto, stream-up when a download block is set;
     *   stream-up  — a GET for the download and a long POST for the upload;
     *   packet-up  — a GET for the download and a series of short POSTs, a chunk in each.
     *
     * Anything else is rejected, stream-down included: it has no upload at all. stream-one has
     * no GET, so a download block with it is refused, as Xray does. */
    if (!strcmp(n->type, "xhttp") && n->mode[0] &&
        strcmp(n->mode, "auto") != 0 && strcmp(n->mode, "stream-one") != 0 &&
        strcmp(n->mode, "stream-up") != 0 && strcmp(n->mode, "packet-up") != 0 &&
        strcmp(n->mode, "finiteproto") != 0) {
        snprintf(n->skip_reason, sizeof(n->skip_reason),
                 "xhttp mode=%s is not supported", n->mode);
        return 1;
    }
    if (!strcmp(n->type, "xhttp") && n->dl && !strcmp(n->mode, "stream-one")) {
        snprintf(n->skip_reason, sizeof(n->skip_reason), "xhttp: downloadSettings cannot be used in stream-one mode");
        return 1;
    }
    return 0;
}

static int dl_refused(struct sl_dl *d) {
    struct vless_node *b = &d->n;
    const char *why = d->bad ? "does not parse" : !b->host[0] ? "no address" : !b->port ? "no port" : NULL;
    if (!why && strcmp(b->type, "xhttp")) {
        snprintf(b->skip_reason, sizeof(b->skip_reason), "network %s is not xhttp", b->type);
        return 1;
    }
    if (!why) why = d->notls ? "reality requires tls" :
                    d->quic ? "quicParams is not supported" :
                    d->alpn == 1 ? "alpn h3 is not supported" : d->alpn == 2 ? "alpn http/1.1 is not supported" : NULL;
    if (why) snprintf(b->skip_reason, sizeof(b->skip_reason), "%s", why);
    return why != NULL;
}

static void dl_drop_id(struct vless_node *b) {
    if (!b->xhx || b->xhx == SL_BAD_XHX || b->xhx == SL_BAD_XHX_JSON || b->xhx == SL_FULL) return;
    struct sl_xh a = { .n = 0 };
    char key[32];
    for (const char *l = b->xhx, *e; (e = strchr(l, '\n')); l = e + 1) {
        const char *eq = memchr(l, '=', (size_t)(e - l));
        if (!eq) continue;
        snprintf(key, sizeof key, "%.*s", (int)(eq - l), l);
        if (strcmp(key, "sessionIDTable") && strcmp(key, "sessionIDLength"))
            sl_xh_put(&a, key, eq + 1, (size_t)(e - eq - 1));
    }
    sl_xh_done(b, &a);
}

void sl_dl_done(struct vless_node *n, struct sl_dl *d) {
    struct vless_node *b = &d->n;
    if (!b->type[0]) snprintf(b->type, sizeof(b->type), "tcp");
    if (!dl_refused(d)) {
        dl_drop_id(b);
        snprintf(b->mode, sizeof(b->mode), "packet-up");
        if (!sl_link_usable_pre(b)) sl_link_usable_post(b);
    }
    memset(b->mode, 0, sizeof(b->mode));
    const struct vless_node *r = sl_intern_node(b);
    n->dl = r ? r : &SL_DL_FULL;
}

/* The only place skipped grows, so the counter and the reasons always agree. */
static vless_node_fn g_node_fn;
static void *g_node_ud;

void vless_set_node_hook(vless_node_fn fn, void *ud) {
    g_node_fn = fn;
    g_node_ud = ud;
}

void sl_node_ok(const struct vless_node *n) {
    if (g_node_fn) g_node_fn(g_node_ud, n, NULL);
}

void sl_skip_node(struct vless_sub_stats *st, const struct vless_node *n, const char *reason) {
    if (g_node_fn) g_node_fn(g_node_ud, n, reason);
    sl_skip_note(st, n, reason);
}

void sl_skip_note(struct vless_sub_stats *st, const struct vless_node *n, const char *reason) {
    if (!st) return;
    st->skipped++;
    for (size_t i = 0; i < st->reasons_n; i++) {
        if (!strcmp(st->reasons[i].reason, reason)) { st->reasons[i].count++; return; }
    }
    if (st->reasons_n >= VLESS_SKIP_REASONS) { st->reasons_dropped++; return; }
    struct vless_skip *s = &st->reasons[st->reasons_n++];
    snprintf(s->reason, sizeof(s->reason), "%s", reason);
    /* The example ties the reason to a node in the subscription. A link without '#' has no
     * name, and one that did not parse may have no host: the example then stays empty. */
    if (n && n->name[0]) snprintf(s->example, sizeof(s->example), "%s", n->name);
    else if (n && n->host[0]) snprintf(s->example, sizeof(s->example), "%s:%u",
                                      n->host, n->port);
    s->count = 1;
}

