/* The HTTP/1.1 Upgrade request shared by ws and httpupgrade, and the httpupgrade transport.
 *
 * Both transports start the same way, a GET with `Connection: Upgrade` and `Upgrade: websocket`
 * and a 101 response, and part after it: ws continues with frames (trws.c), httpupgrade with the
 * stream as is. httpupgrade is an "upgrade without WebSocket" that Xray and sing-box added for
 * proxies (CDNs) that pass only WebSocket-like requests, without the cost of frames and masking.
 *
 * WE SEND WHAT XRAY SENDS, BYTE FOR BYTE, so as not to stand out: the Upgrade request is the
 * first thing after TLS (in clear text with security=none), and a middlebox that tells clients
 * apart by header set and order sees all of it. The model is the Xray client
 * (transport/internet/websocket/dialer.go, httpupgrade/dialer.go), not sing-box: Xray looks like
 * Chrome (common/utils/browser.go, applyMasqueradedHeaders with variant "ws"), sing-box sends
 * "Go-http-client/1.1", plainly not a browser. Checked against a capture of Xray 26.3.27 (client
 * in docker, server a listening socket):
 *
 *   GET /p/q?x=1 HTTP/1.1                         <- ws; httpupgrade sends `?` as %3F
 *   Host: cdn.example.com                         <- host, else sni, else the node address
 *   User-Agent: Mozilla/5.0 (Windows NT 10.0; …) Chrome/151.0.0.0 Safari/537.36
 *   Accept: * / *                                 <- no spaces (a C comment cannot hold it)
 *   Accept-Language: en-US,en;q=0.9
 *   Cache-Control: no-cache
 *   Connection: Upgrade
 *   DNT: 1
 *   Pragma: no-cache
 *   Sec-CH-UA: "Not=A?Brand";v="99", "Google Chrome";v="151", "Chromium";v="151"
 *   Sec-CH-UA-Mobile: ?0
 *   Sec-CH-UA-Platform: "Windows"
 *   Sec-Fetch-Dest: empty
 *   Sec-Fetch-Mode: websocket
 *   Sec-Fetch-Site: same-origin
 *   Sec-WebSocket-Key: U9ViAcDvC6DKctF7OLzaBA==   <- ws only
 *   Sec-WebSocket-Version: 13                     <- ws only
 *   Upgrade: websocket
 *
 * The order is Go's net/http, not Xray's choice: http.Request.Write prints Host, then User-Agent,
 * then ALL other headers sorted by key (bytewise: capitals before lowercase), each in its own
 * case. Key case comes from Go too: headers Xray sets by direct assignment (`Sec-CH-UA`, `DNT`,
 * gorilla's `Sec-WebSocket-Key`) stay as written; those set with Set and Add are canonicalized
 * (`Sec-Fetch-Mode`); the node's own headers are canonicalized for ws (header.Add) but not for
 * httpupgrade (AddHeader in dialer.go keeps the key as is, for those who want "WebSocket" with a
 * capital S). Below this is modelled as a map with exact keys printed in sorted order, not as a
 * fixed list of lines, so the result matches Xray also when a node header overrides the browser
 * look.
 *
 * The Chrome version is ours (UA_CHROME in h2.h), not the 151 of the capture: Xray derives it
 * from the date with a CPU-dependent shift, so two Xray clients differ too. The sec-ch-ua line
 * is what Xray would build for our version (h2.h).
 *
 * Where Xray and sing-box differ we follow Xray: Xray-core is the reference for the bytes of
 * VLESS and its transports on the wire. In particular:
 *   - the other looks, picked by a word in the node's own User-Agent (firefox, safari, edge,
 *     curl, golang): try_default_ws below;
 *   - EARLY DATA (`?ed=N`, Xray's Ed, trpath.h). ws, as delayDialConn: the request waits for the
 *     first write, which goes in Sec-WebSocket-Protocol if no longer than Ed (unpadded
 *     base64url, tr_h1_send), otherwise as frames after the 101 (trws.c). httpupgrade, as
 *     ConnRF: the request goes out on open, but the response is not awaited; data follows, and
 *     the first read parses the response (hu_read_wait); over TLS or Reality only, see
 *     tr_h1_upgrade for security=none. sing-box differs (it cuts the first write at Ed and
 *     sends the tail separately); we do not follow it. Unlike Xray, the tunnel loop cannot
 *     wait for the ws response inside a write, so writes made before the 101 are
 *     queued and sent right after it: the wire order is the same, request, response, frames
 *     (frames must not precede the 101: gorilla on the server drops such a connection, "client
 *     sent data before handshake is complete"). The one cost: the deferred response has no
 *     deadline of its own (Xray: HandshakeTimeout 8 s); a silent server is left to the stack's
 *     idle cleanup of the connection (IDLE_EVICT_S in stack.c).
 *
 * ALPN is http/1.1 only, as in Xray (tls.WithNextProto("http/1.1"), and uTLS's
 * WebsocketHandshakeContext rewrites the fingerprint's ALPN extension to http/1.1 alone). With
 * "h2, http/1.1" the server behind TLS may pick h2, and then our HTTP/1.1 request is garbage to
 * it (see alpn_http11 in reality.h). Chrome itself does the same for WebSocket: a separate
 * connection with ALPN http/1.1. */
#define _GNU_SOURCE
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <errno.h>
#include <poll.h>
#include <time.h>
#include "osrand.h"

#include "transport.h"
#include "fmlink.h"
#include "trpath.h"

/* ---- helpers: randomness, base64, SHA-1 ------------------------------------------------ */

/* Random bytes from the kernel. Randomness in the project comes from getrandom directly
 * (reality.c, trxhttp.c, vision.c), and the request key and frame mask have no reason to differ.
 * A failure fails the connection instead of using zeros: a predictable mask is exactly what RFC
 * 6455 (10.3) introduces masking against. */
int tr_h1_random(unsigned char *out, size_t n) {
    size_t got = 0;
    while (got < n) {
        ssize_t r = os_getrandom(out + got, n - got, 0);
        if (r < 0) {
            if (errno == EINTR) continue;
            return -1;
        }
        got += (size_t)r;
    }
    return 0;
}

static size_t b64_std(const unsigned char *in, size_t n, char *out) {
    static const char T[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    size_t o = 0;
    for (size_t i = 0; i < n; i += 3) {
        unsigned v = (unsigned)in[i] << 16;
        if (i + 1 < n) v |= (unsigned)in[i + 1] << 8;
        if (i + 2 < n) v |= in[i + 2];
        out[o++] = T[(v >> 18) & 63];
        out[o++] = T[(v >> 12) & 63];
        out[o++] = i + 1 < n ? T[(v >> 6) & 63] : '=';
        out[o++] = i + 2 < n ? T[v & 63] : '=';
    }
    out[o] = '\0';
    return o;
}

/* SHA-1 (FIPS 180-4), only for Sec-WebSocket-Accept.
 *
 * Our own rather than sc_hash(SC_SHA1) of the scrypto layer, on purpose. Accept is not
 * protection: it only proves that a server that understood the WebSocket request answered, not
 * a cache or proxy returning someone else's 101 (RFC 6455, 1.3). And the unit tests of this
 * transport (wsmatch, xhupmatch) are built without the crypto library, so with the layer's SHA-1
 * they could no longer check it. Forty lines for one check per connection are cheaper. */
struct sha1 { uint32_t h[5]; unsigned char b[64]; size_t bn; uint64_t len; };

static uint32_t rol(uint32_t x, int k) { return (x << k) | (x >> (32 - k)); }

static void sha1_block(struct sha1 *s, const unsigned char *p) {
    uint32_t w[80];
    for (int i = 0; i < 16; i++)
        w[i] = ((uint32_t)p[4 * i] << 24) | ((uint32_t)p[4 * i + 1] << 16) |
               ((uint32_t)p[4 * i + 2] << 8) | p[4 * i + 3];
    for (int i = 16; i < 80; i++) w[i] = rol(w[i - 3] ^ w[i - 8] ^ w[i - 14] ^ w[i - 16], 1);
    uint32_t a = s->h[0], b = s->h[1], c = s->h[2], d = s->h[3], e = s->h[4];
    for (int i = 0; i < 80; i++) {
        uint32_t f, k;
        if (i < 20)      { f = (b & c) | (~b & d);          k = 0x5A827999; }
        else if (i < 40) { f = b ^ c ^ d;                   k = 0x6ED9EBA1; }
        else if (i < 60) { f = (b & c) | (b & d) | (c & d); k = 0x8F1BBCDC; }
        else             { f = b ^ c ^ d;                   k = 0xCA62C1D6; }
        uint32_t t = rol(a, 5) + f + e + k + w[i];
        e = d; d = c; c = rol(b, 30); b = a; a = t;
    }
    s->h[0] += a; s->h[1] += b; s->h[2] += c; s->h[3] += d; s->h[4] += e;
}

static void sha1_update(struct sha1 *s, const void *d, size_t n) {
    const unsigned char *p = d;
    s->len += n;
    while (n) {
        size_t take = 64 - s->bn;
        if (take > n) take = n;
        memcpy(s->b + s->bn, p, take);
        s->bn += take; p += take; n -= take;
        if (s->bn == 64) { sha1_block(s, s->b); s->bn = 0; }
    }
}

static void sha1(const void *d, size_t n, unsigned char out[20]) {
    struct sha1 s = { { 0x67452301, 0xEFCDAB89, 0x98BADCFE, 0x10325476, 0xC3D2E1F0 }, { 0 }, 0, 0 };
    sha1_update(&s, d, n);
    uint64_t bits = s.len * 8;
    unsigned char pad = 0x80;
    sha1_update(&s, &pad, 1);
    unsigned char z = 0;
    while (s.bn != 56) sha1_update(&s, &z, 1);
    unsigned char l[8];
    for (int i = 0; i < 8; i++) l[i] = (unsigned char)(bits >> (56 - 8 * i));
    sha1_update(&s, l, 8);
    for (int i = 0; i < 5; i++) {
        out[4 * i] = (unsigned char)(s.h[i] >> 24); out[4 * i + 1] = (unsigned char)(s.h[i] >> 16);
        out[4 * i + 2] = (unsigned char)(s.h[i] >> 8); out[4 * i + 3] = (unsigned char)s.h[i];
    }
}

void tr_ws_accept(const char *key, char out[29]) {
    char buf[96];
    int n = snprintf(buf, sizeof(buf), "%s258EAFA5-E914-47DA-95CA-C5AB0DC85B11", key);
    unsigned char h[20];
    sha1(buf, n > 0 && (size_t)n < sizeof(buf) ? (size_t)n : 0, h);
    b64_std(h, sizeof(h), out);
}

/* ---- request headers: the net/http map -------------------------------------------------- */

#define HM_MAX 40
struct hent { char k[48]; const char *v; size_t vn; };
struct hmap { struct hent e[HM_MAX]; size_t n; int over; };

/* textproto.CanonicalMIMEHeaderKey: the first letter and letters after `-` upper case, the rest
 * lower case; a key with a character not valid in a header name is returned as is. */
static int token_char(unsigned char c) {
    if (c >= 'a' && c <= 'z') return 1;
    if (c >= 'A' && c <= 'Z') return 1;
    if (c >= '0' && c <= '9') return 1;
    return strchr("!#$%&'*+-.^_`|~", c) != NULL && c != '\0';
}

static void canon(const char *k, size_t kn, char *out, size_t cap) {
    size_t n = kn < cap - 1 ? kn : cap - 1;
    int ok = 1;
    for (size_t i = 0; i < n; i++) if (!token_char((unsigned char)k[i])) ok = 0;
    int up = 1;
    for (size_t i = 0; i < n; i++) {
        char c = k[i];
        if (ok) {
            if (up && c >= 'a' && c <= 'z') c = (char)(c - 32);
            else if (!up && c >= 'A' && c <= 'Z') c = (char)(c + 32);
            up = c == '-';
        }
        out[i] = c;
    }
    out[n] = '\0';
}

static void hm_add(struct hmap *m, const char *k, const char *v, size_t vn) {
    if (m->n >= HM_MAX) { m->over = 1; return; }
    snprintf(m->e[m->n].k, sizeof(m->e[m->n].k), "%s", k);
    m->e[m->n].v = v;
    m->e[m->n].vn = vn;
    m->n++;
}

static void hm_del(struct hmap *m, const char *k) {
    size_t o = 0;
    for (size_t i = 0; i < m->n; i++)
        if (strcmp(m->e[i].k, k) != 0) m->e[o++] = m->e[i];
    m->n = o;
}

/* Assignment by EXACT key: header.Set (the key is already canonical) and header["Sec-CH-UA"]. */
static void hm_set(struct hmap *m, const char *k, const char *v) {
    hm_del(m, k);
    hm_add(m, k, v, strlen(v));
}

static const struct hent *hm_get(const struct hmap *m, const char *k) {
    for (size_t i = 0; i < m->n; i++)
        if (!strcmp(m->e[i].k, k)) return &m->e[i];
    return NULL;
}

static void hm_set_empty(struct hmap *m, const char *k, const char *v) {
    const struct hent *e = hm_get(m, k);
    if (!e || !e->vn) hm_set(m, k, v);
}

enum browser { BR_CHROME, BR_EDGE, BR_FIREFOX, BR_SAFARI, BR_CURL, BR_GOLANG };

/* applyMasqueradedHeaders(header, browser, "ws"), line by line. */
static void masq_ws(struct hmap *m, enum browser b) {
    switch (b) {
    case BR_CHROME:
    case BR_EDGE:
        hm_set(m, "Sec-CH-UA", b == BR_EDGE ? UA_CH_EDGE : UA_CH_CHROME);
        hm_set(m, "Sec-CH-UA-Mobile", "?0");
        hm_set(m, "Sec-CH-UA-Platform", "\"Windows\"");
        hm_set(m, "DNT", "1");
        hm_set(m, "User-Agent", b == BR_EDGE ? UA_EDGE : UA_CHROME);
        hm_set(m, "Accept-Language", "en-US,en;q=0.9");
        break;
    case BR_FIREFOX:
        hm_set(m, "User-Agent", UA_FIREFOX);
        hm_set(m, "DNT", "1");
        hm_set(m, "Accept-Language", "en-US,en;q=0.5");
        break;
    case BR_SAFARI:
        hm_set(m, "User-Agent", UA_SAFARI);
        hm_set(m, "Accept-Language", "en-US,en;q=0.9");
        break;
    case BR_GOLANG:
        /* "Show the net/http default": User-Agent is removed, and http.Request.Write then sets
         * Go-http-client/1.1 itself; the variant has no headers (return in browser.go). */
        hm_del(m, "User-Agent");
        return;
    case BR_CURL:
        hm_set(m, "User-Agent", UA_CURL);
        return;
    }
    hm_set(m, "Sec-Fetch-Mode", "websocket");
    /* "Safari is NOT web-compliant here!": Safari's Sec-Fetch-Dest differs (browser.go). */
    hm_set(m, "Sec-Fetch-Dest", b == BR_SAFARI ? "websocket" : "empty");
    hm_set(m, "Sec-Fetch-Site", "same-origin");
    hm_set_empty(m, "Cache-Control", "no-cache");
    hm_set_empty(m, "Pragma", "no-cache");
    hm_set_empty(m, "Accept", "*/*");
}

/* TryDefaultHeadersWith(header, "ws"): no User-Agent of its own gets the Chrome look; one of
 * Xray's words gets that client's look; otherwise nothing, the User-Agent goes as written. */
static void try_default_ws(struct hmap *m) {
    const struct hent *ua = hm_get(m, "User-Agent");
    if (!ua) { masq_ws(m, BR_CHROME); return; }
    static const struct { const char *w; enum browser b; } W[] = {
        { "chrome", BR_CHROME }, { "firefox", BR_FIREFOX }, { "safari", BR_SAFARI },
        { "edge", BR_EDGE }, { "curl", BR_CURL }, { "golang", BR_GOLANG },
    };
    for (size_t i = 0; i < sizeof(W) / sizeof(*W); i++)
        if (ua->vn == strlen(W[i].w) && !strncmp(ua->v, W[i].w, ua->vn)) { masq_ws(m, W[i].b); return; }
}

struct wb { char *p; size_t n, cap; int over; };

static void w_s(struct wb *b, const char *s, size_t n) {
    if (b->n + n >= b->cap) { b->over = 1; return; }
    memcpy(b->p + b->n, s, n);
    b->n += n;
}
static void w_z(struct wb *b, const char *s) { w_s(b, s, strlen(s)); }

/* textproto.TrimString: spaces and tabs at both ends. */
static void trim(const char **v, size_t *vn) {
    while (*vn && (**v == ' ' || **v == '\t')) { (*v)++; (*vn)--; }
    while (*vn && ((*v)[*vn - 1] == ' ' || (*v)[*vn - 1] == '\t')) (*vn)--;
}

/* The name for Host: host, else sni, else the node address (Xray: wsSettings.Host,
 * tConfig.ServerName, dest.Address). An IPv6 address goes in brackets, as Xray prints it. */
static const char *host_of(const struct tr_node *n, char *buf, size_t cap) {
    if (n->http_host && n->http_host[0]) return n->http_host;
    if (n->sni && n->sni[0]) return n->sni;
    return tr_host_literal(n->host, buf, cap);
}

size_t tr_h1_request(const struct tr_node *n, int ws, const char *key, const char *proto,
                     char *out, size_t cap) {
    char target[1024];
    if (tr_upgrade_target(n->path ? n->path : "", ws, target, sizeof(target), NULL) != 0) return 0;

    struct hmap m;
    m.n = 0;
    m.over = 0;
    char hostbuf[160];
    const char *host = host_of(n, hostbuf, sizeof(hostbuf));

    /* The node's own headers. A Host among them Xray moves into host for ws (Build, with a
     * "deprecated" warning) and rejects for httpupgrade; the subscription parser (sub.c) does
     * that, so no Host arrives here. */
    const char *h = n->headers ? n->headers : "";
    while (*h) {
        const char *eol = strchr(h, '\n');
        size_t ln = eol ? (size_t)(eol - h) : strlen(h);
        const char *colon = memchr(h, ':', ln);
        if (colon) {
            char key_raw[48], key_c[48];
            size_t kn = (size_t)(colon - h);
            snprintf(key_raw, sizeof(key_raw), "%.*s", (int)kn, h);
            const char *v = colon + 1;
            size_t vn = ln - kn - 1;
            trim(&v, &vn);
            if (ws) { canon(h, kn, key_c, sizeof(key_c)); hm_add(&m, key_c, v, vn); }
            else hm_add(&m, key_raw, v, vn);
        }
        h += ln + (eol ? 1 : 0);
    }

    try_default_ws(&m);

    if (ws) {
        /* Early data: Xray does header.Set("Sec-WebSocket-Protocol", ...), i.e. the canonical key
         * Sec-Websocket-Protocol (replacing the node's own), and gorilla moves it under the key
         * Sec-WebSocket-Protocol. The node's own value, without early data, goes under the moved
         * key too. */
        const struct hent *sp = hm_get(&m, "Sec-Websocket-Protocol");
        if (proto) {
            hm_del(&m, "Sec-Websocket-Protocol");
            hm_set(&m, "Sec-WebSocket-Protocol", proto);
        } else if (sp) {
            struct hent e = *sp;
            hm_del(&m, "Sec-Websocket-Protocol");
            hm_add(&m, "Sec-WebSocket-Protocol", e.v, e.vn);
        }
        /* gorilla/websocket (client.go, DialContext): its four headers, exact keys. */
        hm_set(&m, "Upgrade", "websocket");
        hm_set(&m, "Connection", "Upgrade");
        hm_set(&m, "Sec-WebSocket-Key", key ? key : "");
        hm_set(&m, "Sec-WebSocket-Version", "13");
    } else {
        /* httpupgrade/dialer.go: req.Header.Set, canonical keys. */
        hm_set(&m, "Connection", "Upgrade");
        hm_set(&m, "Upgrade", "websocket");
    }
    if (m.over) return 0;

    /* http.Request.Write: request line, Host, User-Agent (none of its own: Go's default), then
     * the rest sorted by key; equal keys in insertion order. */
    struct wb b = { out, 0, cap, 0 };
    w_z(&b, "GET ");
    w_z(&b, target);
    w_z(&b, " HTTP/1.1\r\nHost: ");
    w_z(&b, host);
    w_z(&b, "\r\n");
    const struct hent *ua = hm_get(&m, "User-Agent");
    const char *uav = ua ? ua->v : "Go-http-client/1.1";
    size_t uan = ua ? ua->vn : strlen(uav);
    if (uan) { w_z(&b, "User-Agent: "); w_s(&b, uav, uan); w_z(&b, "\r\n"); }

    size_t ord[HM_MAX], no = 0;
    for (size_t i = 0; i < m.n; i++) {
        const char *k = m.e[i].k;
        if (!strcmp(k, "Host") || !strcmp(k, "User-Agent") || !strcmp(k, "Content-Length") ||
            !strcmp(k, "Transfer-Encoding") || !strcmp(k, "Trailer"))
            continue;
        size_t j = no++;
        while (j > 0 && strcmp(m.e[ord[j - 1]].k, k) > 0) { ord[j] = ord[j - 1]; j--; }
        ord[j] = i;
    }
    for (size_t j = 0; j < no; j++) {
        const struct hent *e = &m.e[ord[j]];
        const char *v = e->v;
        size_t vn = e->vn;
        trim(&v, &vn);
        w_z(&b, e->k);
        w_z(&b, ": ");
        w_s(&b, v, vn);
        w_z(&b, "\r\n");
    }
    w_z(&b, "\r\n");
    if (b.over) return 0;
    out[b.n] = '\0';
    return b.n;
}

/* ---- response ---------------------------------------------------------------------------- */

/* Response header limit. A 101 response is about a hundred bytes; 16 KB is enough for any proxy,
 * and without a limit a server sending an endless line would hold the connector until the
 * connect timeout. */
#define H1_RESP_MAX 16384

#define SEEN_UP   1u
#define SEEN_CONN 2u
#define SEEN_ACC  4u

static int eqfold(const char *a, size_t an, const char *b) {
    size_t bn = strlen(b);
    if (an != bn) return 0;
    for (size_t i = 0; i < an; i++) {
        char x = a[i], y = b[i];
        if (x >= 'A' && x <= 'Z') x = (char)(x + 32);
        if (y >= 'A' && y <= 'Z') y = (char)(y + 32);
        if (x != y) return 0;
    }
    return 1;
}

/* Whether a word is in a comma-separated list (gorilla's tokenListContainsValue). */
static int token_in(const char *v, size_t vn, const char *want) {
    size_t i = 0;
    while (i <= vn) {
        const char *s = v + i;
        const char *c = memchr(s, ',', vn - i);
        size_t sl = c ? (size_t)(c - s) : vn - i;
        const char *t = s;
        size_t tn = sl;
        trim(&t, &tn);
        if (eqfold(t, tn, want)) return 1;
        i += sl + 1;
    }
    return 0;
}

/* Status line by the http.ReadResponse rules: "HTTP/x.y CODE reason", CODE exactly three digits. */
static int status_of(const char *l, size_t n) {
    if (n < 12 || strncmp(l, "HTTP/", 5) != 0) return -1;
    if (l[5] < '0' || l[5] > '9' || l[6] != '.' || l[7] < '0' || l[7] > '9' || l[8] != ' ') return -1;
    if (l[9] < '0' || l[9] > '9' || l[10] < '0' || l[10] > '9' || l[11] < '0' || l[11] > '9') return -1;
    if (n > 12 && l[12] != ' ') return -1;
    return (l[9] - '0') * 100 + (l[10] - '0') * 10 + (l[11] - '0');
}

static void resp_line(struct h1_resp *r, int ws, const char *accept) {
    const char *l = r->line;
    size_t n = r->line_n;
    if (n && l[n - 1] == '\r') n--;
    if (!r->status) {
        int st = status_of(l, n);
        if (st < 0) { r->bad = 1; return; }
        r->status = st;
        return;
    }
    if (!n) { r->done = 1; return; }
    if (l[0] == ' ' || l[0] == '\t') return;      /* continuation line: not ours */
    const char *colon = memchr(l, ':', n);
    if (!colon) return;
    size_t kn = (size_t)(colon - l);
    const char *v = colon + 1;
    size_t vn = n - kn - 1;
    trim(&v, &vn);
    if (eqfold(l, kn, "Upgrade")) {
        /* gorilla (ws) looks for the word in ALL Upgrade lines; Xray and sing-box (httpupgrade)
         * compare the first value whole. */
        if (ws) { if (token_in(v, vn, "websocket")) r->up_ok = 1; }
        else if (!(r->seen & SEEN_UP)) r->up_ok = (uint8_t)eqfold(v, vn, "websocket");
        r->seen |= SEEN_UP;
    } else if (eqfold(l, kn, "Connection")) {
        if (ws) { if (token_in(v, vn, "upgrade")) r->conn_ok = 1; }
        else if (!(r->seen & SEEN_CONN)) r->conn_ok = (uint8_t)eqfold(v, vn, "upgrade");
        r->seen |= SEEN_CONN;
    } else if (ws && eqfold(l, kn, "Sec-WebSocket-Accept") && !(r->seen & SEEN_ACC)) {
        r->acc_ok = (uint8_t)(accept && vn == strlen(accept) && !memcmp(v, accept, vn));
        r->seen |= SEEN_ACC;
    }
}

void tr_h1_resp_feed(struct h1_resp *r, int ws, const char *accept,
                     const unsigned char *in, size_t n, size_t *used) {
    size_t i = 0;
    while (i < n && !r->done && !r->bad) {
        unsigned char c = in[i++];
        if (++r->total > H1_RESP_MAX) { r->bad = 1; break; }
        if (c == '\n') {
            resp_line(r, ws, accept);
            r->line_n = 0;
            continue;
        }
        /* A line longer than the buffer is cut, not rejected: the headers we need are short,
         * and a long proxy line (Set-Cookie, CSP) is legal and not needed. A cut line with a
         * name we need gives a value mismatch, i.e. an honest failure. */
        if (r->line_n < sizeof(r->line)) r->line[r->line_n++] = (char)c;
    }
    *used = i;
}

int tr_h1_resp_verdict(const struct h1_resp *r, int ws) {
    if (r->bad || !r->done) return TR_EUPTOOBIG;
    if (r->status != 101) return TR_EUPSTATUS;
    if (!r->up_ok || !r->conn_ok) return TR_ENOUPGRADE;
    if (ws && !r->acc_ok) return TR_EWSACCEPT;
    return 0;
}

/* The status of the last TR_EUPSTATUS failure, for the text (transport_strerror). Per thread:
 * connectors fail in parallel. */
static __thread int g_last_status;
int tr_h1_last_status(void) { return g_last_status; }

static int64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

/* base64.RawURLEncoding, Xray's early data encoding ("RawURLEncoding is support by both
 * V2Ray/V2Fly and XRay", websocket/dialer.go): the URL alphabet, no `=` padding. */
static size_t b64_url(const unsigned char *in, size_t n, char *out) {
    static const char T[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
    size_t o = 0;
    for (size_t i = 0; i < n; i += 3) {
        unsigned v = (unsigned)in[i] << 16;
        if (i + 1 < n) v |= (unsigned)in[i + 1] << 8;
        if (i + 2 < n) v |= in[i + 2];
        out[o++] = T[(v >> 18) & 63];
        out[o++] = T[(v >> 12) & 63];
        if (i + 1 < n) out[o++] = T[(v >> 6) & 63];
        if (i + 2 < n) out[o++] = T[v & 63];
    }
    out[o] = '\0';
    return o;
}

int tr_h1_send(struct transport *t, int ws, const unsigned char *ed, size_t ed_n) {
    struct h1_state *s = &t->h1;
    char key[32] = "";
    if (ws) {
        unsigned char r[16];
        if (tr_h1_random(r, sizeof(r)) != 0) return TR_EIO;
        b64_std(r, sizeof(r), key);
        tr_ws_accept(key, s->accept);
    }
    /* On the heap, not __thread: with early data the request carries up to the whole first
     * write in base64, tens of kilobytes not worth keeping per thread. */
    size_t bn = ed ? (ed_n + 2) / 3 * 4 + 1 : 0;
    size_t cap = 4096 + bn;
    char *req = malloc(cap + bn);
    if (!req) return TR_EIO;
    char *proto = NULL;
    if (ed) { proto = req + cap; b64_url(ed, ed_n, proto); }
    size_t rn = tr_h1_request(&s->node, ws, ws ? key : NULL, proto, req, cap);
    int rc = rn ? tr_link_write(&t->link, (const unsigned char *)req, rn) : TR_EUPTOOBIG;
    free(req);
    return rc;
}

int tr_h1_lazy(struct transport *t, int ws, const unsigned char *in, size_t n, size_t *used) {
    struct h1_state *s = &t->h1;
    *used = 0;
    if (!s->resp) return TR_EIO;
    tr_h1_resp_feed(s->resp, ws, ws ? s->accept : NULL, in, n, used);
    if (s->resp->bad) return TR_EUPTOOBIG;
    if (!s->resp->done) return 0;
    int rc = tr_h1_resp_verdict(s->resp, ws);
    if (rc == TR_EUPSTATUS) g_last_status = s->resp->status;
    free(s->resp);
    s->resp = NULL;
    if (rc) return rc;
    s->phase = H1_OPEN;
    s->upgraded = 1;
    return 1;
}

/* The 101 response synchronously: the path without early data. */
static int h1_wait(struct transport *t, int ws, int timeout_s) {
    int rc;
    const char *accept = t->h1.accept;
    /* Waiting here is fine: opening runs in a connector thread, within the connect timeout. */
    struct h1_resp r;
    memset(&r, 0, sizeof(r));
    int64_t deadline = mono_ms() + (int64_t)(timeout_s > 0 ? timeout_s : 8) * 1000;
    unsigned char raw[4096];
    for (;;) {
        const unsigned char *in = NULL;
        size_t got = 0;
        if (!t->link.plain && tls13_has_record(&t->link.tls)) {
            rc = tls13_read_ref(&t->link.tls, &in, &got);
            if (rc) return rc;
        } else {
            int64_t left = deadline - mono_ms();
            if (left <= 0) return TR_EUPTIMEOUT;
            if (!fm_pending(t->link.tls.fm)) {
                struct pollfd p = { .fd = t->link.fd, .events = POLLIN, .revents = 0 };
                int pr = poll(&p, 1, (int)left);
                if (pr < 0 && errno == EINTR) continue;
                if (pr <= 0) return pr == 0 ? TR_EUPTIMEOUT : TR_EIO;
            }
            if (t->link.plain) {
                ssize_t k = tr_lk_recv(&t->link, raw, sizeof(raw), 0);
                if (k == 0) return TR_ECLOSED;
                if (k < 0) {
                    if (errno == EINTR) continue;
                    if (errno == EPROTO) return TR_EMASK;
                    return errno == EAGAIN || errno == EWOULDBLOCK ? TR_EUPTIMEOUT : TR_EIO;
                }
                in = raw;
                got = (size_t)k;
            } else {
                rc = tls13_read_ref(&t->link.tls, &in, &got);
                if (rc) return rc;
            }
        }
        if (!got) continue;
        size_t used = 0;
        tr_h1_resp_feed(&r, ws, ws ? accept : NULL, in, got, &used);
        if (r.bad) return TR_EUPTOOBIG;
        if (!r.done) continue;
        rc = tr_h1_resp_verdict(&r, ws);
        if (rc == TR_EUPSTATUS) g_last_status = r.status;
        if (rc) return rc;
        /* What came after the response in the same chunk is already the stream: keep it
         * (see h1_state). */
        if (used < got) {
            t->h1.stash = malloc(got - used);
            if (!t->h1.stash) return TR_EIO;
            memcpy(t->h1.stash, in + used, got - used);
            t->h1.stash_n = (uint32_t)(got - used);
            t->h1.stash_off = 0;
        }
        t->h1.upgraded = 1;
        return 0;
    }
}

/* Opening by Ed, as in Xray:
 *   ws, Ed > 0          delayDialConn: nothing is sent until the first write (trws.c, H1_DEFER).
 *                       Xray defers even the TCP connect; we open TCP and TLS in advance: the
 *                       bytes on the wire are the same, only the timing is earlier;
 *   httpupgrade, Ed > 0 the request at once, the response read by the first read (Xray's ConnRF,
 *                       H1_WAIT): client data follows the request without waiting for the 101.
 *                       Over TLS or Reality only, see below;
 *   Ed == 0             request and response synchronously. */
int tr_h1_upgrade(struct transport *t, const struct tr_node *n, int ws, int timeout_s) {
    struct h1_state *s = &t->h1;
    s->node = *n;
    char target[1024];
    if (tr_upgrade_target_ed(n->path ? n->path : "", ws, target, sizeof(target), NULL, &s->ed) != 0)
        return TR_EUPTOOBIG;
    if (ws && s->ed) { s->phase = H1_DEFER; return 0; }
    int rc = tr_h1_send(t, ws, NULL, 0);
    if (rc) return rc;
    /* httpupgrade with Ed > 0 does not wait for the response, but only over TLS or REALITY.
     * Without TLS (security=none) it waits as with Ed == 0: Xray's httpupgrade server reads the
     * request through bufio and then hands over the bare connection (hub.go), so data that came
     * with the request in ONE TCP segment is lost. Seen on Xray 26.3.27: our request and first
     * write sent back to back gave "invalid request version" on every connection. The Xray
     * client avoids it only because its first write goes later, in a separate segment. Over TLS
     * the server reads one whole TLS record and no more, and nothing is lost. The bytes on the
     * wire are Xray's in both cases; only the moment of the first write differs. */
    if (s->ed && !t->link.plain) {
        s->resp = calloc(1, sizeof(*s->resp));
        if (!s->resp) return TR_EIO;
        s->phase = H1_WAIT;
        return 0;
    }
    return h1_wait(t, ws, timeout_s);
}

void tr_h1_free(struct transport *t) {
    free(t->h1.stash);
    t->h1.stash = NULL;
    t->h1.stash_n = t->h1.stash_off = 0;
    free(t->h1.resp);
    t->h1.resp = NULL;
    free(t->h1.q);
    t->h1.q = NULL;
    t->h1.q_n = t->h1.q_cap = 0;
}

/* ---- the httpupgrade transport ------------------------------------------------------------- */

static int hu_open(struct transport *t, const struct tr_node *n, int timeout_s) {
    return tr_h1_upgrade(t, n, 0, timeout_s);
}

/* Like Xray's ConnRF.Write: the stream as is, in H1_WAIT too; a write does not wait for the 101. */
static int hu_write(struct transport *t, const unsigned char *d, size_t n) {
    return tr_link_write(&t->link, d, n);
}

/* Deferred 101 response (Ed > 0): the first read finishes the response, and what follows is
 * already the stream (Xray's ConnRF.Read returns the data buffered past the response in the
 * same call). */
static int hu_read_wait(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    const unsigned char *in = d;
    size_t n = 0;
    int rc = tls13_read_ref(&t->link.tls, &in, &n);
    if (rc) return rc;
    if (!n) return 0;
    size_t used = 0;
    rc = tr_h1_lazy(t, 0, in, n, &used);
    if (rc <= 0) return rc;
    size_t rest = n - used;
    if (rest > cap) return H2_ETOOBIG;
    memmove(d, in + used, rest);
    *got = rest;
    return 0;
}

/* First the rest that came with the 101 response: it is the start of the stream, and returning
 * it after the next socket read would reorder bytes. */
static int hu_read(struct transport *t, unsigned char *d, size_t cap, size_t *got) {
    struct h1_state *s = &t->h1;
    if (s->phase == H1_WAIT) return hu_read_wait(t, d, cap, got);
    if (s->stash) {
        size_t left = s->stash_n - s->stash_off;
        size_t take = left < cap ? left : cap;
        memcpy(d, s->stash + s->stash_off, take);
        s->stash_off += (uint32_t)take;
        *got = take;
        if (s->stash_off >= s->stash_n) tr_h1_free(t);
        return 0;
    }
    return tr_link_read(&t->link, d, cap, got);
}

static int hu_pending(const struct transport *t) { return t->h1.stash != NULL; }

static int hu_busy(const struct transport *t) { return t->h1.phase == H1_WAIT; }

static void hu_close(struct transport *t) { tr_h1_free(t); }

/* zc = 1: after the 101 the data sits in the TLS records as is, as with tcp, and zero-copy reads
 * work while nothing is stashed and the response is parsed (transport_read_zc asks pending and
 * busy). */
const struct transport_ops tr_httpupgrade = {
    .name = "httpupgrade", .alpn = "http/1.1", .zc = 1,
    .open = hu_open, .write = hu_write, .read = hu_read,
    .moved = NULL, .close = hu_close, .pending = hu_pending, .busy = hu_busy,
};
