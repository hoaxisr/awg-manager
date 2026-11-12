/* awgm-warpscout — Cloudflare WARP endpoint scanner.
 *
 * Reads a JSON config from stdin, performs real WireGuard handshakes
 * (optionally preceded by AmneziaWG I1..I5 / junk packets) against
 * candidate endpoints and verifies traffic inside the tunnel with a
 * userspace ICMP echo + tiny TCP client (/cdn-cgi/trace, optional Telegram
 * DC SYN probe). Emits JSON Lines on stdout.
 */
#include "platform.h"
#include "wgcrypto.h"
#include "wg.h"
#include "netpkt.h"
#include "json.h"

#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <signal.h>
#include <ctype.h>

#ifdef __linux__
#include <net/if.h>
#ifndef SO_BINDTODEVICE
#define SO_BINDTODEVICE 25
#endif
#ifndef SO_MARK
#define SO_MARK 36
#endif
#endif

#define WS_VERSION "1.0.0"

#define MAX_LIST 256
#define RBUF_SIZE 4096
#define MAX_CONCURRENCY 1024

/* ------------------------------------------------------------------ util */

static volatile sig_atomic_t g_stop = 0;
static void on_signal(int sig) { (void)sig; g_stop = 1; }

static void emit(const char *fmt, ...) {
    va_list ap;
    va_start(ap, fmt);
    vfprintf(stdout, fmt, ap);
    va_end(ap);
    fputc('\n', stdout);
    if (fflush(stdout) != 0 || ferror(stdout)) exit(3);
}

static void jesc(char *out, size_t cap, const char *in) {
    size_t o = 0;
    for (; *in && o + 7 < cap; in++) {
        unsigned char c = (unsigned char)*in;
        if (c == '"' || c == '\\') { out[o++] = '\\'; out[o++] = (char)c; }
        else if (c < 0x20) o += (size_t)snprintf(out + o, cap - o, "\\u%04x", c);
        else out[o++] = (char)c;
    }
    out[o] = 0;
}

static void emit_error(const char *msg) {
    char e[512];
    jesc(e, sizeof(e), msg);
    emit("{\"type\":\"error\",\"message\":\"%s\"}", e);
}

/* splitmix64 PRNG for shuffling / junk (not for keys) */
static uint64_t g_prng;
static uint64_t prng_next(void) {
    uint64_t z = (g_prng += 0x9E3779B97F4A7C15ULL);
    z = (z ^ (z >> 30)) * 0xBF58476D1CE4E5B9ULL;
    z = (z ^ (z >> 27)) * 0x94D049BB133111EBULL;
    return z ^ (z >> 31);
}
static uint64_t prng_below(uint64_t n) { return n ? prng_next() % n : 0; }

static int parse_ipv4(const char *s, uint32_t *out, const char **end) {
    uint32_t ip = 0;
    for (int i = 0; i < 4; i++) {
        if (!isdigit((unsigned char)*s)) return -1;
        unsigned v = 0;
        int nd = 0;
        while (isdigit((unsigned char)*s)) {
            v = v * 10 + (unsigned)(*s++ - '0');
            if (++nd > 3 || v > 255) return -1;
        }
        ip = (ip << 8) | v;
        if (i < 3) {
            if (*s != '.') return -1;
            s++;
        }
    }
    *out = ip;
    if (end) *end = s;
    else if (*s) return -1;
    return 0;
}

static void fmt_ep(char *out, uint32_t ip, uint16_t port) {
    sprintf(out, "%u.%u.%u.%u:%u", (unsigned)(ip >> 24), (unsigned)((ip >> 16) & 255),
            (unsigned)((ip >> 8) & 255), (unsigned)(ip & 255), (unsigned)port);
}

/* ---------------------------------------------------------------- config */

typedef struct { uint32_t ip; uint16_t port; } endpoint;
typedef struct { uint32_t net; int bits; } prefix4;

typedef struct {
    uint8_t priv[32], peer[32], psk[32];
    int has_psk;
    uint32_t tunnel_ip;
    uint8_t reserved[3];
    endpoint *eps; size_t n_eps;
    prefix4 *pfx; size_t n_pfx;
    uint16_t *ports; size_t n_ports;
    endpoint *excl; size_t n_excl; /* port 0 = any port */
    int max_candidates, concurrency, pps, timeout_ms, deadline_ms, max_hits;
    uint64_t seed;
    char ifname[64];
    uint32_t fwmark;
    int jc, jmin, jmax;
    uint8_t *ipkt[5]; size_t ipkt_len[5];
    uint32_t ping_ip, trace_ip, tg_ip;
    int probe_trace, probe_tg;
    char trace_host[256];
    char excl_colos[MAX_LIST][8]; int n_excl_colos;
    char allowed[MAX_LIST][8]; int n_allowed;
    int max_rtt_ms, require_trace, require_tg, emit_rejects;
} config;

static config C;

static const char *DEFAULT_PEER = "bmXOC+F1FxEMF9dyiK2H5/1SUtzH0JuVo51h2wPfgyo=";
static const char *DEFAULT_PREFIXES[] = {"162.159.192.0/24", "162.159.195.0/24", "188.114.96.0/24",
                                         "188.114.97.0/24", "188.114.98.0/24", "188.114.99.0/24"};
static const uint16_t DEFAULT_PORTS[] = {
    2408, 500, 1701, 4500, 854, 859, 864, 878, 880, 890, 891, 894, 903, 908, 928, 934, 939, 942,
    943, 945, 946, 955, 968, 987, 988, 1002, 1010, 1014, 1018, 1070, 1074, 1180, 1387, 1843, 2371,
    2506, 3138, 3476, 3581, 3854, 4177, 4198, 4233, 5279, 5956, 7103, 7152, 7156, 7281, 7559, 8319,
    8742, 8854, 8886};

static char g_cfgerr[256];
#define CFG_FAIL(...) do { snprintf(g_cfgerr, sizeof(g_cfgerr), __VA_ARGS__); return -1; } while (0)

static int cfg_int(const jval *o, const char *k, long long def, long long lo, long long hi, long long *out) {
    const jval *v = json_get(o, k);
    if (!v || v->t == J_NULL) { *out = def; return 0; }
    if (v->t != J_NUM) CFG_FAIL("%s: expected number", k);
    if (v->num < (double)lo || v->num > (double)hi) CFG_FAIL("%s: out of range [%lld..%lld]", k, lo, hi);
    *out = (long long)v->num;
    return 0;
}
static int cfg_bool(const jval *o, const char *k, int def, int *out) {
    const jval *v = json_get(o, k);
    if (!v || v->t == J_NULL) { *out = def; return 0; }
    if (v->t != J_BOOL) CFG_FAIL("%s: expected boolean", k);
    *out = v->b;
    return 0;
}
static int cfg_str(const jval *o, const char *k, const char *def, const char **out) {
    const jval *v = json_get(o, k);
    if (!v || v->t == J_NULL) { *out = def; return 0; }
    if (v->t != J_STR) CFG_FAIL("%s: expected string", k);
    *out = v->str;
    return 0;
}
static int cfg_arr(const jval *o, const char *k, const jval **out) {
    const jval *v = json_get(o, k);
    *out = NULL;
    if (!v || v->t == J_NULL) return 0;
    if (v->t != J_ARR) CFG_FAIL("%s: expected array", k);
    *out = v;
    return 0;
}
static int cfg_key32(const char *name, const char *b64, uint8_t out[32]) {
    uint8_t tmp[48];
    if (b64_decode(b64, tmp, sizeof(tmp)) != 32) CFG_FAIL("%s: expected 32-byte base64 key", name);
    memcpy(out, tmp, 32);
    wipe(tmp, sizeof(tmp));
    return 0;
}
static int cfg_ip(const jval *o, const char *k, const char *def, uint32_t *out) {
    const char *s;
    if (cfg_str(o, k, def, &s)) return -1;
    if (parse_ipv4(s, out, NULL) != 0) CFG_FAIL("%s: invalid IPv4 address '%s'", k, s);
    return 0;
}
static int parse_endpoint(const char *s, endpoint *ep, int allow_noport) {
    const char *e;
    if (parse_ipv4(s, &ep->ip, &e) != 0) return -1;
    if (*e == 0 && allow_noport) { ep->port = 0; return 0; }
    if (*e != ':') return -1;
    char *pe;
    long p = strtol(e + 1, &pe, 10);
    if (*pe || p < 1 || p > 65535) return -1;
    ep->port = (uint16_t)p;
    return 0;
}
static int parse_prefix(const char *s, prefix4 *p) {
    const char *e;
    if (parse_ipv4(s, &p->net, &e) != 0) return -1;
    p->bits = 32;
    if (*e == '/') {
        char *pe;
        long b = strtol(e + 1, &pe, 10);
        if (*pe || b < 0 || b > 32) return -1;
        p->bits = (int)b;
    } else if (*e) return -1;
    if (p->bits < 8) return -1; /* refuse absurdly large ranges */
    if (p->bits < 32) p->net &= ~((1u << (32 - p->bits)) - 1u);
    return 0;
}
static int cfg_codes(const jval *o, const char *k, char (*dst)[8], int *n) {
    const jval *a;
    *n = 0;
    if (cfg_arr(o, k, &a)) return -1;
    if (!a) return 0;
    for (int i = 0; i < a->n && *n < MAX_LIST; i++) {
        if (a->items[i].t != J_STR) CFG_FAIL("%s: expected strings", k);
        const char *s = a->items[i].str;
        size_t l = strlen(s);
        if (l == 0) continue;
        if (l > 7) CFG_FAIL("%s: code too long '%s'", k, s);
        for (size_t j = 0; j <= l; j++) dst[*n][j] = (char)toupper((unsigned char)s[j]);
        (*n)++;
    }
    return 0;
}

static int load_config(const jval *o) {
    const char *s;
    const jval *a;
    long long v;
    memset(&C, 0, sizeof(C));
    if (!o || o->t != J_OBJ) CFG_FAIL("config must be a JSON object");

    if (cfg_str(o, "private_key", NULL, &s)) return -1;
    if (!s || !*s) CFG_FAIL("private_key is required");
    if (cfg_key32("private_key", s, C.priv)) return -1;
    if (cfg_str(o, "peer_public_key", DEFAULT_PEER, &s)) return -1;
    if (!*s) s = DEFAULT_PEER;
    if (cfg_key32("peer_public_key", s, C.peer)) return -1;
    if (cfg_str(o, "preshared_key", "", &s)) return -1;
    if (*s) {
        if (cfg_key32("preshared_key", s, C.psk)) return -1;
        C.has_psk = 1;
    }
    if (cfg_ip(o, "tunnel_ip", "172.16.0.2", &C.tunnel_ip)) return -1;

    {   /* reserved: [a,b,c] or base64 client_id string */
        const jval *r = json_get(o, "reserved");
        if (r && r->t == J_ARR) {
            if (r->n != 3 && r->n != 0) CFG_FAIL("reserved: expected 3 numbers");
            for (int i = 0; i < r->n; i++) {
                if (r->items[i].t != J_NUM || r->items[i].num < 0 || r->items[i].num > 255)
                    CFG_FAIL("reserved: values must be 0..255");
                C.reserved[i] = (uint8_t)r->items[i].num;
            }
        } else if (r && r->t == J_STR && r->str[0]) {
            uint8_t b[8];
            if (b64_decode(r->str, b, sizeof(b)) != 3) CFG_FAIL("reserved: base64 must decode to 3 bytes");
            memcpy(C.reserved, b, 3);
        } else if (r && r->t != J_NULL && !(r->t == J_STR)) {
            CFG_FAIL("reserved: expected array");
        }
    }

    if (cfg_arr(o, "endpoints", &a)) return -1;
    if (a && a->n > 0) {
        C.eps = (endpoint *)calloc((size_t)a->n, sizeof(endpoint));
        for (int i = 0; i < a->n; i++) {
            if (a->items[i].t != J_STR || parse_endpoint(a->items[i].str, &C.eps[C.n_eps], 0) != 0)
                CFG_FAIL("endpoints[%d]: expected \"ipv4:port\"", i);
            C.n_eps++;
        }
    }
    if (cfg_arr(o, "prefixes", &a)) return -1;
    if (a && a->n > 0) {
        C.pfx = (prefix4 *)calloc((size_t)a->n, sizeof(prefix4));
        for (int i = 0; i < a->n; i++) {
            if (a->items[i].t != J_STR || parse_prefix(a->items[i].str, &C.pfx[C.n_pfx]) != 0)
                CFG_FAIL("prefixes[%d]: expected IPv4 CIDR (>= /8)", i);
            C.n_pfx++;
        }
    } else {
        size_t n = sizeof(DEFAULT_PREFIXES) / sizeof(DEFAULT_PREFIXES[0]);
        C.pfx = (prefix4 *)calloc(n, sizeof(prefix4));
        for (size_t i = 0; i < n; i++) parse_prefix(DEFAULT_PREFIXES[i], &C.pfx[C.n_pfx++]);
    }
    if (cfg_arr(o, "ports", &a)) return -1;
    if (a && a->n > 0) {
        C.ports = (uint16_t *)calloc((size_t)a->n, sizeof(uint16_t));
        for (int i = 0; i < a->n; i++) {
            if (a->items[i].t != J_NUM || a->items[i].num < 1 || a->items[i].num > 65535)
                CFG_FAIL("ports[%d]: expected 1..65535", i);
            C.ports[C.n_ports++] = (uint16_t)a->items[i].num;
        }
    } else {
        size_t n = sizeof(DEFAULT_PORTS) / sizeof(DEFAULT_PORTS[0]);
        C.ports = (uint16_t *)calloc(n, sizeof(uint16_t));
        memcpy(C.ports, DEFAULT_PORTS, sizeof(DEFAULT_PORTS));
        C.n_ports = n;
    }
    if (cfg_arr(o, "exclude_endpoints", &a)) return -1;
    if (a && a->n > 0) {
        C.excl = (endpoint *)calloc((size_t)a->n, sizeof(endpoint));
        for (int i = 0; i < a->n; i++) {
            if (a->items[i].t != J_STR || parse_endpoint(a->items[i].str, &C.excl[C.n_excl], 1) != 0)
                CFG_FAIL("exclude_endpoints[%d]: expected \"ipv4[:port]\"", i);
            C.n_excl++;
        }
    }

    if (cfg_int(o, "max_candidates", 1500, 1, 1000000, &v)) return -1; C.max_candidates = (int)v;
    if (cfg_int(o, "concurrency", 48, 1, MAX_CONCURRENCY, &v)) return -1; C.concurrency = (int)v;
    if (cfg_int(o, "pps", 200, 0, 100000, &v)) return -1; C.pps = (int)v;
    if (cfg_int(o, "timeout_ms", 1500, 100, 60000, &v)) return -1; C.timeout_ms = (int)v;
    if (cfg_int(o, "deadline_ms", 180000, 0, 86400000LL, &v)) return -1; C.deadline_ms = (int)v;
    if (cfg_int(o, "max_hits", 10, 1, 1000000, &v)) return -1; C.max_hits = (int)v;
    {
        const jval *sv = json_get(o, "seed");
        if (sv && sv->t == J_NUM) C.seed = (uint64_t)(sv->num < 0 ? -sv->num : sv->num);
        else if (sv && sv->t != J_NULL) CFG_FAIL("seed: expected number");
    }
    if (cfg_str(o, "ifname", "", &s)) return -1;
    if (strlen(s) >= sizeof(C.ifname)) CFG_FAIL("ifname too long");
    strcpy(C.ifname, s);
    if (cfg_int(o, "fwmark", 0, 0, 4294967295LL, &v)) return -1; C.fwmark = (uint32_t)v;

    {
        const jval *awg = json_get(o, "awg");
        if (awg && awg->t == J_OBJ) {
            if (cfg_int(awg, "jc", 0, 0, 128, &v)) return -1; C.jc = (int)v;
            if (cfg_int(awg, "jmin", 0, 0, 1280, &v)) return -1; C.jmin = (int)v;
            if (cfg_int(awg, "jmax", 0, 0, 1280, &v)) return -1; C.jmax = (int)v;
            if (C.jmax < C.jmin) { int t = C.jmax; C.jmax = C.jmin; C.jmin = t; }
            if (C.jc > 0 && C.jmax == 0) C.jc = 0;
            if (cfg_arr(awg, "i", &a)) return -1;
            for (int i = 0; a && i < a->n && i < 5; i++) {
                if (a->items[i].t == J_NULL) continue;
                if (a->items[i].t != J_STR) CFG_FAIL("awg.i[%d]: expected hex string", i);
                const char *hs = a->items[i].str;
                if (!*hs) continue;
                size_t cap = strlen(hs) / 2 + 1;
                C.ipkt[i] = (uint8_t *)malloc(cap);
                int n = hex_decode(hs, C.ipkt[i], cap);
                if (n < 0) CFG_FAIL("awg.i[%d]: invalid hex", i);
                if (n > 1400) CFG_FAIL("awg.i[%d]: packet too large (%d bytes)", i, n);
                C.ipkt_len[i] = (size_t)n;
            }
        } else if (awg && awg->t != J_NULL) CFG_FAIL("awg: expected object");
    }

    if (cfg_ip(o, "ping_ip", "1.1.1.1", &C.ping_ip)) return -1;
    if (cfg_bool(o, "probe_trace", 1, &C.probe_trace)) return -1;
    if (cfg_ip(o, "trace_ip", "104.16.132.229", &C.trace_ip)) return -1;
    if (cfg_str(o, "trace_host", "cp.cloudflare.com", &s)) return -1;
    if (!*s) s = "cp.cloudflare.com";
    if (strlen(s) >= sizeof(C.trace_host) || strpbrk(s, "\r\n ")) CFG_FAIL("trace_host invalid");
    strcpy(C.trace_host, s);
    if (cfg_bool(o, "probe_tg", 0, &C.probe_tg)) return -1;
    parse_ipv4("149.154.167.50", &C.tg_ip, NULL);
    if (cfg_codes(o, "exclude_colos", C.excl_colos, &C.n_excl_colos)) return -1;
    if (!json_get(o, "exclude_colos")) { strcpy(C.excl_colos[0], "DME"); C.n_excl_colos = 1; }
    if (cfg_codes(o, "allowed_countries", C.allowed, &C.n_allowed)) return -1;
    if (cfg_int(o, "max_rtt_ms", 0, 0, 600000, &v)) return -1; C.max_rtt_ms = (int)v;
    if (cfg_bool(o, "require_trace", 1, &C.require_trace)) return -1;
    if (cfg_bool(o, "require_tg", 0, &C.require_tg)) return -1;
    if (cfg_bool(o, "emit_rejects", 0, &C.emit_rejects)) return -1;
    /* a requirement implies the corresponding probe */
    if (C.require_tg) C.probe_tg = 1;
    if (!C.probe_trace) C.require_trace = 0;
    return 0;
}

/* ------------------------------------------------------------ candidates */

static endpoint *g_cands;
static size_t g_ncands;

static int is_excluded(uint32_t ip, uint16_t port) {
    for (size_t i = 0; i < C.n_excl; i++)
        if (C.excl[i].ip == ip && (C.excl[i].port == 0 || C.excl[i].port == port)) return 1;
    return 0;
}

static void index_to_ep(uint64_t idx, endpoint *ep) {
    uint64_t port_i = idx % C.n_ports;
    uint64_t host = idx / C.n_ports;
    for (size_t i = 0; i < C.n_pfx; i++) {
        uint64_t sz = 1ULL << (32 - C.pfx[i].bits);
        if (host < sz) {
            ep->ip = C.pfx[i].net + (uint32_t)host;
            ep->port = C.ports[port_i];
            return;
        }
        host -= sz;
    }
    ep->ip = 0;
    ep->port = 0;
}

static void build_candidates(void) {
    size_t cap = (size_t)C.max_candidates;
    g_cands = (endpoint *)calloc(cap ? cap : 1, sizeof(endpoint));
    g_ncands = 0;
    if (C.n_eps > 0) {
        for (size_t i = 0; i < C.n_eps && g_ncands < cap; i++)
            if (!is_excluded(C.eps[i].ip, C.eps[i].port)) g_cands[g_ncands++] = C.eps[i];
        return;
    }
    uint64_t hosts = 0;
    for (size_t i = 0; i < C.n_pfx; i++) hosts += 1ULL << (32 - C.pfx[i].bits);
    uint64_t total = hosts * C.n_ports;
    if (total == 0) return;
    if (total <= 262144) {
        endpoint *all = (endpoint *)malloc((size_t)total * sizeof(endpoint));
        size_t n = 0;
        for (uint64_t i = 0; i < total; i++) {
            endpoint ep;
            index_to_ep(i, &ep);
            if (!is_excluded(ep.ip, ep.port)) all[n++] = ep;
        }
        size_t take = n < cap ? n : cap;
        for (size_t i = 0; i < take; i++) { /* partial Fisher-Yates */
            size_t j = i + (size_t)prng_below(n - i);
            endpoint t = all[i]; all[i] = all[j]; all[j] = t;
            g_cands[g_ncands++] = all[i];
        }
        free(all);
        return;
    }
    /* huge space: random sampling without replacement via a small hash set */
    size_t hsz = 1;
    while (hsz < cap * 2 + 16) hsz <<= 1;
    uint64_t *set = (uint64_t *)calloc(hsz, sizeof(uint64_t));
    size_t attempts = cap * 8 + 64;
    while (g_ncands < cap && attempts-- > 0) {
        uint64_t idx = prng_below(total);
        uint64_t key = idx + 1;
        size_t h = (size_t)((key * 0x9E3779B97F4A7C15ULL) >> 20) & (hsz - 1);
        int dup = 0;
        while (set[h]) {
            if (set[h] == key) { dup = 1; break; }
            h = (h + 1) & (hsz - 1);
        }
        if (dup) continue;
        set[h] = key;
        endpoint ep;
        index_to_ep(idx, &ep);
        if (!is_excluded(ep.ip, ep.port)) g_cands[g_ncands++] = ep;
    }
    free(set);
}

/* -------------------------------------------------------------- scanner */

enum { CS_FREE = 0, CS_HS, CS_PING, CS_PROBE };
enum { TCP_IDLE = 0, TCP_SYN_SENT, TCP_EST, TCP_DONE };

typedef struct {
    int state;
    uint16_t sport, dport;
    uint32_t dst;
    uint32_t iss, irs, snd_nxt;
    uint64_t next_retx;
    int fin_seen;
    uint32_t fin_off;
} tcpc;

typedef struct {
    int state;
    sock_t fd;
    endpoint ep;
    wg_hs hs;
    uint8_t init_pkt[WG_INIT_LEN];
    uint64_t t_hs_sent, deadline, next_retx;
    int hs_retx;
    int handshake_ms, ping_ms, ping_ok;
    uint16_t icmp_id, ping_seq;
    uint64_t ping_sent[8];
    uint16_t ip_id;
    tcpc trace, tg;
    int trace_done, trace_ok, tg_done, tg_ok;
    char colo[8], loc[8], warp[16];
    uint8_t rbuf[RBUF_SIZE];
    uint8_t rmark[RBUF_SIZE / 8];
    uint32_t contig;
} cand;

static wg_identity g_id;
static cand *g_slots;
static int g_nslots, g_active;
static size_t g_done, g_hits;
static uint16_t g_port_ctr, g_icmp_ctr;
static char g_req[512];
static size_t g_req_len;
static int g_fatal;

static uint64_t now_ms(void) { return ws_now_ms(); }

static int retx_interval(void) {
    int r = C.timeout_ms / 4;
    return r < 150 ? 150 : r;
}

static void send_raw(cand *c, const uint8_t *p, size_t len) {
    send(c->fd, (const char *)p, (int)len, 0);
}

static void send_inner(cand *c, const uint8_t *ip, size_t len) {
    uint8_t out[2048 + 64];
    size_t n = wg_encrypt_transport(&c->hs, ip, len, out);
    if (!n) return;
    memcpy(out + 1, C.reserved, 3);
    send_raw(c, out, n);
}

static void send_handshake(cand *c) {
    uint8_t junk[1280];
    for (int i = 0; i < 5; i++)
        if (C.ipkt_len[i]) send_raw(c, C.ipkt[i], C.ipkt_len[i]);
    for (int j = 0; j < C.jc; j++) {
        size_t len = (size_t)(C.jmin + (int)prng_below((uint64_t)(C.jmax - C.jmin + 1)));
        if (len == 0) continue;
        for (size_t k = 0; k < len; k += 8) {
            uint64_t r = prng_next();
            for (size_t b = 0; b < 8 && k + b < len; b++) junk[k + b] = (uint8_t)(r >> (8 * b));
        }
        send_raw(c, junk, len);
    }
    send_raw(c, c->init_pkt, WG_INIT_LEN);
    c->t_hs_sent = now_ms();
}

static uint16_t alloc_port(void) {
    g_port_ctr++;
    if (g_port_ctr < 20000 || g_port_ctr >= 60000) g_port_ctr = 20000;
    return g_port_ctr;
}

static void tcp_send(cand *c, tcpc *t, uint32_t seq, uint8_t flags, uint16_t mss, const uint8_t *d, size_t dl) {
    uint8_t pkt[1500];
    size_t n = pkt_tcp(pkt, C.tunnel_ip, t->dst, ++c->ip_id, t->sport, t->dport, seq,
                       t->irs + 1 + (t == &c->trace ? c->contig + (uint32_t)(t->fin_seen && c->contig >= t->fin_off) : 0),
                       flags, mss, d, dl);
    send_inner(c, pkt, n);
}

static void tcp_start(cand *c, tcpc *t, uint32_t dst, uint16_t dport) {
    uint32_t r;
    os_random(&r, sizeof(r));
    memset(t, 0, sizeof(*t));
    t->dst = dst;
    t->dport = dport;
    t->sport = alloc_port();
    t->iss = r;
    t->state = TCP_SYN_SENT;
    tcp_send(c, t, t->iss, TCPF_SYN, 1200, NULL, 0);
    t->next_retx = now_ms() + (uint64_t)retx_interval();
}

static void tcp_reset(cand *c, tcpc *t) {
    if (t->state == TCP_SYN_SENT || t->state == TCP_EST) {
        uint32_t seq = t->state == TCP_EST ? t->snd_nxt : t->iss + 1;
        tcp_send(c, t, seq, (uint8_t)(TCPF_RST | (t->state == TCP_EST ? TCPF_ACK : 0)), 0, NULL, 0);
    }
    t->state = TCP_DONE;
}

static void send_ping(cand *c) {
    static const uint8_t payload[16] = "awgm-warpscout!";
    uint8_t pkt[64];
    uint16_t seq = c->ping_seq++;
    c->ping_sent[seq & 7] = now_ms();
    size_t n = pkt_icmp_echo(pkt, C.tunnel_ip, C.ping_ip, ++c->ip_id, c->icmp_id, seq, payload, sizeof(payload));
    send_inner(c, pkt, n);
    c->next_retx = now_ms() + (uint64_t)retx_interval();
}

static int code_in(const char *code, char (*list)[8], int n) {
    char up[8];
    size_t i;
    for (i = 0; code[i] && i < 7; i++) up[i] = (char)toupper((unsigned char)code[i]);
    up[i] = 0;
    for (int k = 0; k < n; k++)
        if (strcmp(up, list[k]) == 0) return 1;
    return 0;
}

/* final=0: only checks that can already be decided; returns NULL if none failed */
static const char *evaluate(cand *c, int final) {
    if (!c->ping_ok) return final ? "no_ping" : NULL;
    if (C.max_rtt_ms > 0 && c->ping_ms > C.max_rtt_ms) return "rtt";
    if (c->trace_done || final) {
        if (C.probe_trace && !c->trace_ok && C.require_trace) return "no_trace";
        if (c->colo[0] && code_in(c->colo, C.excl_colos, C.n_excl_colos)) return "colo_excluded";
        if (C.n_allowed > 0 && (!c->loc[0] || !code_in(c->loc, C.allowed, C.n_allowed))) return "country";
    }
    if (final && C.require_tg && !c->tg_ok) return "no_tg";
    return NULL;
}

static void release(cand *c) {
    if (c->hs.established) {
        tcp_reset(c, &c->trace);
        tcp_reset(c, &c->tg);
    }
    if (c->fd != WS_BAD_SOCK) ws_closesock(c->fd);
    c->fd = WS_BAD_SOCK;
    c->state = CS_FREE;
    wipe(&c->hs, sizeof(c->hs));
    g_active--;
    g_done++;
}

static void finish(cand *c, const char *reason) {
    char ep[32], colo[32], loc[32], warp[48];
    fmt_ep(ep, c->ep.ip, c->ep.port);
    jesc(colo, sizeof(colo), c->colo);
    jesc(loc, sizeof(loc), c->loc);
    jesc(warp, sizeof(warp), c->warp);
    if (!reason) {
        g_hits++;
        emit("{\"type\":\"hit\",\"endpoint\":\"%s\",\"handshake_ms\":%d,\"ping_ms\":%d,\"colo\":\"%s\","
             "\"loc\":\"%s\",\"warp\":\"%s\",\"tg_ok\":%s}",
             ep, c->handshake_ms, c->ping_ms, colo, loc, warp, c->tg_ok ? "true" : "false");
    } else if (C.emit_rejects) {
        emit("{\"type\":\"reject\",\"endpoint\":\"%s\",\"reason\":\"%s\",\"colo\":\"%s\",\"loc\":\"%s\","
             "\"ping_ms\":%d,\"handshake_ms\":%d}",
             ep, reason, colo, loc, c->ping_ok ? c->ping_ms : -1, c->handshake_ms);
    }
    release(c);
}

static void check_probes(cand *c) {
    const char *r = evaluate(c, 0);
    if (r) { finish(c, r); return; }
    int trace_res = !C.probe_trace || c->trace_done;
    int tg_res = !C.probe_tg || c->tg_done;
    if (trace_res && tg_res) finish(c, evaluate(c, 1));
}

static void on_established(cand *c) {
    uint64_t now = now_ms();
    c->handshake_ms = (int)(now - c->t_hs_sent);
    c->state = CS_PING;
    c->deadline = now + (uint64_t)C.timeout_ms;
    send_ping(c);
}

static void on_echo_reply(cand *c, uint16_t seq) {
    if (seq >= c->ping_seq) return;
    uint64_t now = now_ms();
    c->ping_ms = (int)(now - c->ping_sent[seq & 7]);
    c->ping_ok = 1;
    if (!C.probe_trace && !C.probe_tg) { finish(c, evaluate(c, 1)); return; }
    const char *r = evaluate(c, 0);
    if (r) { finish(c, r); return; }
    c->state = CS_PROBE;
    c->deadline = now + 2ULL * (uint64_t)C.timeout_ms;
    if (C.probe_trace) tcp_start(c, &c->trace, C.trace_ip, 80);
    if (C.probe_tg) tcp_start(c, &c->tg, C.tg_ip, 443);
}

static void parse_trace(cand *c) {
    const char *b = (const char *)c->rbuf;
    size_t n = c->contig;
    size_t hdr_end = 0;
    for (size_t i = 0; i + 3 < n; i++)
        if (b[i] == '\r' && b[i + 1] == '\n' && b[i + 2] == '\r' && b[i + 3] == '\n') { hdr_end = i + 4; break; }
    if (!hdr_end) return;
    if (n < 12 || memcmp(b, "HTTP/1.", 7) != 0) return;
    size_t i = hdr_end;
    int have_warp = 0;
    while (i < n) {
        size_t j = i;
        while (j < n && b[j] != '\n') j++;
        if (j >= n) break; /* incomplete line */
        size_t l = j - i;
        if (l && b[i + l - 1] == '\r') l--;
        const char *line = b + i;
        if (l > 5 && !memcmp(line, "colo=", 5) && l - 5 < sizeof(c->colo)) {
            memcpy(c->colo, line + 5, l - 5); c->colo[l - 5] = 0;
        } else if (l > 4 && !memcmp(line, "loc=", 4) && l - 4 < sizeof(c->loc)) {
            memcpy(c->loc, line + 4, l - 4); c->loc[l - 4] = 0;
        } else if (l > 5 && !memcmp(line, "warp=", 5) && l - 5 < sizeof(c->warp)) {
            memcpy(c->warp, line + 5, l - 5); c->warp[l - 5] = 0;
            have_warp = 1;
        }
        i = j + 1;
    }
    int fin = c->trace.fin_seen && c->contig >= c->trace.fin_off;
    if ((c->colo[0] && c->loc[0] && have_warp) || fin || c->contig >= RBUF_SIZE) {
        c->trace_done = 1;
        c->trace_ok = c->colo[0] != 0;
    }
}

static void trace_input(cand *c, const pkt_info *pi) {
    tcpc *t = &c->trace;
    if (t->state == TCP_DONE || t->state == TCP_IDLE) return;
    if (pi->flags & TCPF_RST) {
        t->state = TCP_DONE;
        c->trace_done = 1;
        return;
    }
    if (t->state == TCP_SYN_SENT) {
        if ((pi->flags & (TCPF_SYN | TCPF_ACK)) == (TCPF_SYN | TCPF_ACK) && pi->ack == t->iss + 1) {
            t->irs = pi->seq;
            t->state = TCP_EST;
            t->snd_nxt = t->iss + 1 + (uint32_t)g_req_len;
            tcp_send(c, t, t->iss + 1, TCPF_PSH | TCPF_ACK, 0, (const uint8_t *)g_req, g_req_len);
            t->next_retx = now_ms() + (uint64_t)retx_interval();
        }
        return;
    }
    /* established */
    if (pi->flags & TCPF_SYN) { /* duplicate SYN-ACK: our request was probably lost */
        tcp_send(c, t, t->iss + 1, TCPF_PSH | TCPF_ACK, 0, (const uint8_t *)g_req, g_req_len);
        return;
    }
    if (pi->dlen == 0 && !(pi->flags & TCPF_FIN)) return;
    uint32_t off = pi->seq - (t->irs + 1);
    if (off < RBUF_SIZE) {
        size_t take = pi->dlen;
        if (off + take > RBUF_SIZE) take = RBUF_SIZE - off;
        memcpy(c->rbuf + off, pi->data, take);
        for (size_t k = 0; k < take; k++) c->rmark[(off + k) >> 3] |= (uint8_t)(1u << ((off + k) & 7));
        while (c->contig < RBUF_SIZE && (c->rmark[c->contig >> 3] & (1u << (c->contig & 7)))) c->contig++;
    }
    if ((pi->flags & TCPF_FIN) && off + pi->dlen <= RBUF_SIZE) {
        t->fin_seen = 1;
        t->fin_off = off + (uint32_t)pi->dlen;
    }
    tcp_send(c, t, t->snd_nxt, TCPF_ACK, 0, NULL, 0);
    parse_trace(c);
    if (c->trace_done) tcp_reset(c, t);
}

static void tg_input(cand *c, const pkt_info *pi) {
    tcpc *t = &c->tg;
    if (t->state != TCP_SYN_SENT) return;
    if (pi->flags & TCPF_RST) {
        t->state = TCP_DONE;
        c->tg_done = 1;
        return;
    }
    if ((pi->flags & (TCPF_SYN | TCPF_ACK)) == (TCPF_SYN | TCPF_ACK) && pi->ack == t->iss + 1) {
        t->irs = pi->seq;
        c->tg_ok = 1;
        c->tg_done = 1;
        tcp_send(c, t, t->iss + 1, TCPF_RST, 0, NULL, 0);
        t->state = TCP_DONE;
    }
}

static void dispatch_inner(const uint8_t *p, size_t len) {
    pkt_info pi;
    if (pkt_parse(p, len, &pi) != 0) return;
    for (int i = 0; i < g_nslots; i++) {
        cand *c = &g_slots[i];
        if (pi.proto == 1) {
            if (c->state == CS_PING && pi.icmp_type == 0 && pi.icmp_id == c->icmp_id && pi.src == C.ping_ip) {
                on_echo_reply(c, pi.icmp_seq);
                return;
            }
        } else if (pi.proto == 6 && c->state == CS_PROBE) {
            if (C.probe_trace && c->trace.state != TCP_IDLE && pi.dport == c->trace.sport &&
                pi.src == c->trace.dst && pi.sport == c->trace.dport) {
                trace_input(c, &pi);
                check_probes(c);
                return;
            }
            if (C.probe_tg && c->tg.state != TCP_IDLE && pi.dport == c->tg.sport &&
                pi.src == c->tg.dst && pi.sport == c->tg.dport) {
                tg_input(c, &pi);
                check_probes(c);
                return;
            }
        }
    }
}

static cand *find_by_idx(uint32_t idx) {
    for (int i = 0; i < g_nslots; i++)
        if (g_slots[i].state >= CS_PING && g_slots[i].hs.local_idx == idx) return &g_slots[i];
    return NULL;
}

static int idx_in_use(uint32_t idx) {
    for (int i = 0; i < g_nslots; i++)
        if (g_slots[i].state != CS_FREE && g_slots[i].hs.local_idx == idx) return 1;
    return 0;
}

static int apply_sockopts(sock_t s) {
#ifdef __linux__
    if (C.ifname[0]) {
        if (setsockopt(s, SOL_SOCKET, SO_BINDTODEVICE, C.ifname, (socklen_t)strlen(C.ifname) + 1) != 0) {
            char m[160];
            snprintf(m, sizeof(m), "SO_BINDTODEVICE(%s) failed: %s", C.ifname, strerror(errno));
            emit_error(m);
            return -1;
        }
    }
    if (C.fwmark) {
        int mark = (int)C.fwmark;
        if (setsockopt(s, SOL_SOCKET, SO_MARK, &mark, sizeof(mark)) != 0) {
            char m[160];
            snprintf(m, sizeof(m), "SO_MARK(%u) failed: %s", (unsigned)C.fwmark, strerror(errno));
            emit_error(m);
            return -1;
        }
    }
    return 0;
#else
    (void)s;
    if (C.ifname[0] || C.fwmark) {
        emit_error("ifname/fwmark are only supported on Linux");
        return -1;
    }
    return 0;
#endif
}

/* returns 0 started, 1 skipped (socket error), -1 fatal */
static int start_candidate(cand *c, endpoint ep) {
    struct sockaddr_in sa;
    uint32_t idx;
    memset(c, 0, sizeof(*c));
    c->fd = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
    if (c->fd == WS_BAD_SOCK) { c->state = CS_FREE; return 1; }
    if (apply_sockopts(c->fd) != 0) { ws_closesock(c->fd); c->fd = WS_BAD_SOCK; return -1; }
    ws_set_nonblock(c->fd);
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(ep.port);
    sa.sin_addr.s_addr = htonl(ep.ip);
    if (connect(c->fd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        ws_closesock(c->fd);
        c->fd = WS_BAD_SOCK;
        return 1;
    }
    do { os_random(&idx, sizeof(idx)); } while (idx == 0 || idx_in_use(idx));
    c->ep = ep;
    if (wg_create_initiation(&g_id, &c->hs, idx, NULL, c->init_pkt) != 0) {
        ws_closesock(c->fd);
        c->fd = WS_BAD_SOCK;
        return 1;
    }
    /* reserved bytes are written after mac1 (same as Xray / sing-box / warp-plus) */
    memcpy(c->init_pkt + 1, C.reserved, 3);
    c->icmp_id = ++g_icmp_ctr;
    os_random(&c->ip_id, sizeof(c->ip_id));
    c->state = CS_HS;
    g_active++;
    send_handshake(c);
    c->deadline = c->t_hs_sent + (uint64_t)C.timeout_ms;
    c->next_retx = c->t_hs_sent + (uint64_t)(C.timeout_ms / 2);
    return 0;
}

static void handle_readable(cand *c) {
    uint8_t buf[2048], pt[2048];
    for (int iter = 0; iter < 32 && c->state != CS_FREE; iter++) {
        int n = recv(c->fd, (char *)buf, sizeof(buf), 0);
        if (n < 0) {
            int e = ws_sockerr();
            if (ws_would_block(e) || ws_intr(e)) return;
            /* ICMP port unreachable etc. */
            if (c->state == CS_HS) release(c);
            return;
        }
        if (n < 4) continue;
        buf[1] = buf[2] = buf[3] = 0;
        if (buf[0] == WG_MSG_RESP && n == WG_RESP_LEN) {
            if (c->state == CS_HS && wg_consume_response(&g_id, &c->hs, buf, (size_t)n) == 0) on_established(c);
        } else if (buf[0] == WG_MSG_DATA && n >= 32) {
            cand *owner = find_by_idx(wg_ld32(buf + 4));
            if (!owner) continue;
            int m = wg_decrypt_transport(&owner->hs, buf, (size_t)n, pt);
            if (m > 0) dispatch_inner(pt, (size_t)m);
        }
    }
}

static void handle_timers(cand *c, uint64_t now) {
    switch (c->state) {
    case CS_HS:
        if (now >= c->deadline) { release(c); return; }
        if (c->hs_retx == 0 && now >= c->next_retx) {
            c->hs_retx = 1;
            send_handshake(c);
        }
        break;
    case CS_PING:
        if (now >= c->deadline) { finish(c, evaluate(c, 1)); return; }
        if (now >= c->next_retx) send_ping(c);
        break;
    case CS_PROBE:
        if (now >= c->deadline) {
            if (C.probe_trace && !c->trace_done) { parse_trace(c); c->trace_done = 1; c->trace_ok = c->colo[0] != 0; }
            if (C.probe_tg && !c->tg_done) c->tg_done = 1;
            finish(c, evaluate(c, 1));
            return;
        }
        if (C.probe_trace && now >= c->trace.next_retx) {
            tcpc *t = &c->trace;
            if (t->state == TCP_SYN_SENT) tcp_send(c, t, t->iss, TCPF_SYN, 1200, NULL, 0);
            else if (t->state == TCP_EST && c->contig == 0)
                tcp_send(c, t, t->iss + 1, TCPF_PSH | TCPF_ACK, 0, (const uint8_t *)g_req, g_req_len);
            t->next_retx = now + (uint64_t)retx_interval();
        }
        if (C.probe_tg && c->tg.state == TCP_SYN_SENT && now >= c->tg.next_retx) {
            tcp_send(c, &c->tg, c->tg.iss, TCPF_SYN, 1200, NULL, 0);
            c->tg.next_retx = now + (uint64_t)retx_interval();
        }
        break;
    default:
        break;
    }
}

static int run_scan(void) {
    uint64_t t0 = now_ms(), last_progress = 0;
    size_t next = 0, last_done_reported = (size_t)-1;
    uint64_t started = 0;
    int aborted = 0;
    ws_pollfd *pfds;
    int *pmap;

    g_nslots = C.concurrency;
    g_slots = (cand *)calloc((size_t)g_nslots, sizeof(cand));
    pfds = (ws_pollfd *)calloc((size_t)g_nslots, sizeof(ws_pollfd));
    pmap = (int *)calloc((size_t)g_nslots, sizeof(int));
    if (!g_slots || !pfds || !pmap) { emit_error("out of memory"); return 2; }
    for (int i = 0; i < g_nslots; i++) g_slots[i].fd = WS_BAD_SOCK;
    os_random(&g_port_ctr, sizeof(g_port_ctr));
    os_random(&g_icmp_ctr, sizeof(g_icmp_ctr));
    g_req_len = (size_t)snprintf(g_req, sizeof(g_req),
                                 "GET /cdn-cgi/trace HTTP/1.1\r\nHost: %s\r\nUser-Agent: awgm-warpscout\r\n"
                                 "Connection: close\r\n\r\n", C.trace_host);

    emit("{\"type\":\"start\",\"total\":%lu,\"version\":\"" WS_VERSION "\"}", (unsigned long)g_ncands);

    for (;;) {
        uint64_t now = now_ms();
        if (g_stop) { aborted = 1; break; }
        if (C.deadline_ms > 0 && now - t0 >= (uint64_t)C.deadline_ms) break;
        if (g_hits >= (size_t)C.max_hits) break;

        /* start new candidates (concurrency + pps limits) */
        while (g_active < g_nslots && next < g_ncands) {
            if (C.pps > 0 && started >= (now - t0) * (uint64_t)C.pps / 1000ULL + 1ULL) break;
            int slot = -1;
            for (int i = 0; i < g_nslots; i++)
                if (g_slots[i].state == CS_FREE) { slot = i; break; }
            if (slot < 0) break;
            int rc = start_candidate(&g_slots[slot], g_cands[next++]);
            started++;
            if (rc < 0) { g_fatal = 1; break; }
            if (rc > 0) g_done++;
        }
        if (g_fatal) break;
        if (g_active == 0 && next >= g_ncands) break;

        /* poll */
        int np = 0;
        int timeout = 50;
        for (int i = 0; i < g_nslots; i++) {
            cand *c = &g_slots[i];
            if (c->state == CS_FREE) continue;
            pfds[np].fd = c->fd;
            pfds[np].events = POLLIN;
            pfds[np].revents = 0;
            pmap[np] = i;
            np++;
            uint64_t due = c->deadline;
            if (c->state == CS_HS && !c->hs_retx && c->next_retx < due) due = c->next_retx;
            if (c->state == CS_PING && c->next_retx < due) due = c->next_retx;
            int d = due > now ? (int)(due - now) : 0;
            if (d < timeout) timeout = d;
        }
        if (C.pps > 0 && next < g_ncands && g_active < g_nslots) {
            int d = 1000 / C.pps;
            if (d < 1) d = 1;
            if (d < timeout) timeout = d;
        }
        if (np == 0) {
            ws_sleep_ms(timeout > 0 ? timeout : 1);
        } else {
            int pr = ws_poll(pfds, np, timeout);
            if (pr > 0) {
                for (int k = 0; k < np; k++) {
                    cand *c = &g_slots[pmap[k]];
                    if (c->state == CS_FREE || c->fd != pfds[k].fd) continue;
                    if (pfds[k].revents & (POLLIN | POLLERR | POLLHUP)) handle_readable(c);
                }
            }
        }
        now = now_ms();
        for (int i = 0; i < g_nslots; i++)
            if (g_slots[i].state != CS_FREE) handle_timers(&g_slots[i], now);

        if (g_done != last_done_reported && now - last_progress >= 200) {
            emit("{\"type\":\"progress\",\"done\":%lu,\"total\":%lu,\"hits\":%lu}",
                 (unsigned long)g_done, (unsigned long)g_ncands, (unsigned long)g_hits);
            last_progress = now;
            last_done_reported = g_done;
        }
    }

    for (int i = 0; i < g_nslots; i++)
        if (g_slots[i].state != CS_FREE) { release(&g_slots[i]); g_done--; }
    if (g_fatal) return 2;
    emit("{\"type\":\"done\",\"scanned\":%lu,\"hits\":%lu,\"elapsed_ms\":%lu,\"aborted\":%s}",
         (unsigned long)g_done, (unsigned long)g_hits, (unsigned long)(now_ms() - t0),
         aborted ? "true" : "false");
    return 0;
}

/* ------------------------------------------------------------------ main */

static int selftest(void) {
    int fails = crypto_selftest(1);
    {   /* JSON */
        const char *t = "{\"a\":[1,2,{\"b\":\"x\\u00e9\\n\"}],\"c\":true,\"d\":null,\"e\":-1.5e2}";
        jval v;
        char err[128];
        if (json_parse(t, strlen(t), &v, err, sizeof(err)) != 0) { fprintf(stderr, "selftest json FAIL %s\n", err); fails++; }
        else {
            const jval *a = json_get(&v, "a"), *e = json_get(&v, "e");
            int ok = a && a->n == 3 && json_get(&a->items[2], "b") &&
                     strcmp(json_get(&a->items[2], "b")->str, "x\xc3\xa9\n") == 0 && e && e->num == -150.0 &&
                     json_get(&v, "c")->b == 1;
            fprintf(stderr, "selftest %-34s %s\n", "json parser", ok ? "ok" : "FAIL");
            fails += !ok;
            json_free(&v);
        }
    }
    {   /* checksums: RFC 1071 example-ish, and IP header verify */
        uint8_t pkt[128];
        pkt_info pi;
        size_t n = pkt_tcp(pkt, 0xAC100002, 0x68108405, 1, 40000, 80, 1000, 0, TCPF_SYN, 1200, NULL, 0);
        int ok = pkt_parse(pkt, n, &pi) == 0 && pi.sport == 40000 && pi.flags == TCPF_SYN;
        n = pkt_icmp_echo(pkt, 0xAC100002, 0x01010101, 2, 7, 9, (const uint8_t *)"hi", 2);
        ok = ok && pkt_parse(pkt, n, &pi) == 0 && pi.icmp_id == 7 && pi.icmp_seq == 9;
        pkt[25] ^= 1;
        ok = ok && pkt_parse(pkt, n, &pi) != 0;
        fprintf(stderr, "selftest %-34s %s\n", "ipv4/icmp/tcp checksums", ok ? "ok" : "FAIL");
        fails += !ok;
    }
    fprintf(stderr, "selftest: %s\n", fails ? "FAILED" : "all passed");
    return fails ? 1 : 0;
}

static char *read_stdin(size_t *len) {
    size_t cap = 65536, n = 0;
    char *b = (char *)malloc(cap + 1);
    if (!b) return NULL;
#ifdef _WIN32
    _setmode(_fileno(stdin), _O_BINARY);
#endif
    for (;;) {
        if (n == cap) {
            if (cap >= (8u << 20)) { free(b); return NULL; }
            cap *= 2;
            char *nb = (char *)realloc(b, cap + 1);
            if (!nb) { free(b); return NULL; }
            b = nb;
        }
        size_t r = fread(b + n, 1, cap - n, stdin);
        if (r == 0) break;
        n += r;
    }
    b[n] = 0;
    *len = n;
    return b;
}

int main(int argc, char **argv) {
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--version") || !strcmp(argv[i], "-v")) {
            printf("awgm-warpscout %s\n", WS_VERSION);
            return 0;
        }
        if (!strcmp(argv[i], "--selftest")) return selftest();
        if (!strcmp(argv[i], "--help") || !strcmp(argv[i], "-h")) {
            printf("usage: awgm-warpscout [--version|--selftest] < config.json\n"
                   "Reads the scan configuration as one JSON object from stdin; emits JSON Lines.\n");
            return 0;
        }
        fprintf(stderr, "unknown argument: %s (configuration is read from stdin only)\n", argv[i]);
        return 1;
    }

    signal(SIGINT, on_signal);
    signal(SIGTERM, on_signal);
#ifndef _WIN32
    signal(SIGPIPE, SIG_IGN);
    signal(SIGHUP, on_signal);
#endif

    if (ws_net_init() != 0) { emit_error("network init failed"); return 2; }

    size_t len = 0;
    char *text = read_stdin(&len);
    if (!text) { emit_error("failed to read config from stdin"); return 1; }
    jval root;
    char err[160];
    if (json_parse(text, len, &root, err, sizeof(err)) != 0) {
        wipe(text, len);
        free(text);
        emit_error(err);
        return 1;
    }
    wipe(text, len);
    free(text);
    int rc = load_config(&root);
    {   /* scrub the private key from the DOM before freeing */
        const jval *pk = json_get(&root, "private_key");
        if (pk && pk->t == J_STR) wipe(pk->str, strlen(pk->str));
        const jval *ps = json_get(&root, "preshared_key");
        if (ps && ps->t == J_STR) wipe(ps->str, strlen(ps->str));
    }
    json_free(&root);
    if (rc != 0) { emit_error(g_cfgerr); return 1; }

    if (wg_identity_init(&g_id, C.priv, C.peer, C.has_psk ? C.psk : NULL) != 0) {
        emit_error("invalid key pair (degenerate shared secret)");
        return 1;
    }
    wipe(C.priv, sizeof(C.priv));

    {   /* verify sockopts up-front so a bad ifname fails fast */
        sock_t s = socket(AF_INET, SOCK_DGRAM, IPPROTO_UDP);
        if (s == WS_BAD_SOCK) { emit_error("socket() failed"); return 2; }
        int bad = apply_sockopts(s);
        ws_closesock(s);
        if (bad) return 2;
    }

    if (C.seed) g_prng = C.seed;
    else os_random(&g_prng, sizeof(g_prng));
    build_candidates();
    rc = run_scan();
    wipe(&g_id, sizeof(g_id));
    return rc;
}
