#include "xt_internal.h"
#include "firc/mark.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <sys/socket.h>

const firc_xt_ext_desc_t firc_xt_exts[] = {
    {FIRC_XT_EXT_DNAT, "DNAT", true, 0, 1, false},
    {FIRC_XT_EXT_MASQUERADE, "MASQUERADE", true, 0, 0, false},
    {FIRC_XT_EXT_MARK, "mark", false, 1, 1, false},
    {FIRC_XT_EXT_TCP, "tcp", false, 0, 0, false},
    {FIRC_XT_EXT_UDP, "udp", false, 0, 0, false},
    {FIRC_XT_EXT_REJECT, "REJECT", true, 0, 0, false},
    {FIRC_XT_EXT_MARK_TARGET, "MARK", true, 2, 2, false},
    {FIRC_XT_EXT_CONNMARK, "CONNMARK", true, 1, 1, false},
    {FIRC_XT_EXT_CONNTRACK, "conntrack", false, 3, 3, false},
    {FIRC_XT_EXT_MULTIPORT, "multiport", false, 1, 1, false},
    {FIRC_XT_EXT_MAC, "mac", false, 0, 0, false},
    {FIRC_XT_EXT_CONNMARK_MATCH, "connmark", false, 1, 1, false},
    {FIRC_XT_EXT_REDIRECT, "REDIRECT", true, 0, 0, false},
    {FIRC_XT_EXT_ADDRTYPE, "addrtype", false, 1, 1, false},
    {FIRC_XT_EXT_TCPMSS, "TCPMSS", true, 0, 0, false},
    {FIRC_XT_EXT_LIMIT, "limit", false, 0, 0, true},
    {FIRC_XT_EXT_LENGTH, "length", false, 0, 0, true},
    {FIRC_XT_EXT_CONNBYTES, "connbytes", false, 0, 0, true},
    {FIRC_XT_EXT_STRING, "string", false, 1, 1, true},
    {FIRC_XT_EXT_NFLOG, "NFLOG", true, 0, 0, true},
};
const size_t firc_xt_n_exts = sizeof(firc_xt_exts) / sizeof(firc_xt_exts[0]);

#define MAX_MATCHES 8
#define MAX_PAYLOAD 200

_Static_assert(sizeof(xt_mark_mtinfo1_t) <= MAX_PAYLOAD, "mark fits an ext_t");
_Static_assert(sizeof(xt_connmark_mtinfo1_t) <= MAX_PAYLOAD, "connmark fits an ext_t");
_Static_assert(sizeof(xt_connmark_mtinfo1_t) == sizeof(xt_mark_mtinfo1_t) &&
                   offsetof(xt_connmark_mtinfo1_t, invert) == offsetof(xt_mark_mtinfo1_t, invert),
               "connmark and mark matches share a payload");
_Static_assert(sizeof(xt_tcp_t) <= MAX_PAYLOAD, "tcp fits an ext_t");
_Static_assert(sizeof(xt_udp_t) <= MAX_PAYLOAD, "udp fits an ext_t");
_Static_assert(sizeof(xt_conntrack_mtinfo3_t) <= MAX_PAYLOAD, "conntrack fits an ext_t");
_Static_assert(sizeof(xt_multiport_v1_t) <= MAX_PAYLOAD, "multiport fits an ext_t");
_Static_assert(sizeof(xt_mac_info_t) <= MAX_PAYLOAD, "mac fits an ext_t");
_Static_assert(sizeof(xt_rateinfo_t) <= MAX_PAYLOAD, "limit fits an ext_t");
_Static_assert(sizeof(xt_length_info_t) <= MAX_PAYLOAD, "length fits an ext_t");
_Static_assert(sizeof(xt_connbytes_info_t) <= MAX_PAYLOAD, "connbytes fits an ext_t");
_Static_assert(sizeof(xt_string_info_t) <= MAX_PAYLOAD, "string fits an ext_t");
_Static_assert(sizeof(xt_reject_info_t) <= MAX_PAYLOAD, "REJECT fits an ext_t");
_Static_assert(sizeof(xt_mark_tginfo2_t) <= MAX_PAYLOAD, "MARK fits an ext_t");
_Static_assert(sizeof(xt_connmark_tginfo1_t) <= MAX_PAYLOAD, "CONNMARK fits an ext_t");
_Static_assert(sizeof(xt_nflog_info_t) <= MAX_PAYLOAD, "NFLOG fits an ext_t");
_Static_assert(sizeof(xt_addrtype_info_v1_t) <= MAX_PAYLOAD, "addrtype fits an ext_t");
_Static_assert(sizeof(xt_nat_ipv4_compat_t) <= MAX_PAYLOAD, "REDIRECT fits an ext_t");
_Static_assert(sizeof(xt_set_info_t) <= MAX_PAYLOAD, "set fits an ext_t");
_Static_assert(sizeof(xt_tcpmss_info_t) <= MAX_PAYLOAD, "TCPMSS fits an ext_t");
#define NOT_MINE (-1)
#define BAD (-2)

typedef struct ext {
    uint32_t bit;
    const char *name;
    uint8_t rev;
    uint16_t size;
    uint16_t seen;
    uint8_t p[MAX_PAYLOAD];
} ext_t;

typedef enum { T_NONE = 0, T_VERDICT, T_JUMP, T_DNAT, T_MASQ, T_EXT } target_kind_t;

typedef struct want {
    bool v6;
    uint8_t mac_rev;
    bool has_s, has_d;
    uint8_t src[16], smsk[16], dst[16], dmsk[16];
    char in[16], out[16];
    size_t in_len, out_len;
    uint8_t invflags;
    uint16_t proto;
    ext_t m[MAX_MATCHES];
    size_t nm;
    ext_t *cur;
    target_kind_t target;
    int32_t verdict;
    char jump[FIRC_XT_NAME_LEN];
    char set[FIRC_XT_NAME_LEN];
    bool to_addr, to_port;
    uint8_t addr[16];
    uint16_t port;
    ext_t t;
} want_t;

static bool parse_addr(bool v6, bool exact, const char *s, uint8_t *addr, uint8_t *mask) {
    char buf[64];
    size_t n = strlen(s);
    if (n == 0 || n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n + 1);
    long len = v6 ? 128 : 32;
    char *slash = strchr(buf, '/');
    if (slash != NULL) {
        *slash = '\0';
        char *end = NULL;
        long l = strtol(slash + 1, &end, 10);
        if (end == slash + 1 || *end != '\0' || l < 0 || l > len) { return false; }
        len = l;
    }
    if (inet_pton(v6 ? AF_INET6 : AF_INET, buf, addr) != 1) { return false; }
    size_t bytes = v6 ? 16 : 4;
    memset(mask, 0, bytes);
    for (long b = 0; b < len; b++) { mask[b / 8] |= (uint8_t)(0x80u >> (b % 8)); }
    for (size_t i = 0; i < bytes; i++) {
        if (exact && (addr[i] & ~mask[i]) != 0) { return false; }
        addr[i] &= mask[i];
    }
    return true;
}

static bool parse_iface(const char *s, char *name, size_t *len) {
    size_t n = strlen(s);
    if (n == 0 || n > 15 || strchr(s, '+') != NULL) { return false; }
    memset(name, 0, 16);
    memcpy(name, s, n);
    *len = n;
    return true;
}

static bool parse_u16(const char *s, uint16_t *v) {
    if (s[0] < '0' || s[0] > '9') { return false; }
    char *end = NULL;
    unsigned long x = strtoul(s, &end, 10);
    if (*end != '\0' || x > 65535) { return false; }
    *v = (uint16_t)x;
    return true;
}

static bool parse_ports(const char *s, char sep, uint16_t *lo, uint16_t *hi) {
    char buf[16];
    size_t n = strlen(s);
    if (n == 0 || n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n + 1);
    char *colon = strchr(buf, sep);
    if (colon == NULL) {
        if (!parse_u16(buf, lo)) { return false; }
        *hi = *lo;
        return true;
    }
    *colon = '\0';
    return parse_u16(buf, lo) && parse_u16(colon + 1, hi) && *lo <= *hi;
}

static bool parse_u32(const char *s, uint32_t *v) {
    if (s[0] < '0' || s[0] > '9') { return false; }
    char *end = NULL;
    unsigned long long x = strtoull(s, &end, 0);
    if (*end != '\0' || x > 0xffffffffull) { return false; }
    *v = (uint32_t)x;
    return true;
}

static bool parse_mark(const char *s, uint32_t *v, uint32_t *m) {
    char buf[32];
    size_t n = strlen(s);
    if (n == 0 || n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n + 1);
    char *slash = strchr(buf, '/');
    *m = 0xffffffffu;
    if (slash != NULL) {
        *slash = '\0';
        if (!parse_u32(slash + 1, m)) { return false; }
    }
    return parse_u32(buf, v);
}

static bool chain_name_ok(const char *s) {
    size_t n = strlen(s);
    return n > 0 && n <= 28 && s[0] != '-' && s[0] != '!';
}

static void put_ext(uint8_t *at, uint16_t size, const char *name, uint8_t rev) {
    firc_xt_wr16(at, size);
    memcpy(at + 2, name, strlen(name));
    at[31] = rev;
}

static void put_ip(const want_t *w, uint8_t *e) {
    if (w->v6) {
        xt_ip6t_ip6_t ip;
        memset(&ip, 0, sizeof(ip));
        memcpy(ip.src, w->src, 16);
        memcpy(ip.smsk, w->smsk, 16);
        memcpy(ip.dst, w->dst, 16);
        memcpy(ip.dmsk, w->dmsk, 16);
        memcpy(ip.iniface, w->in, 16);
        memcpy(ip.outiface, w->out, 16);
        memset(ip.iniface_mask, 0xff, w->in_len ? w->in_len + 1 : 0);
        memset(ip.outiface_mask, 0xff, w->out_len ? w->out_len + 1 : 0);
        ip.proto = w->proto;
        ip.flags = w->proto ? XT_IP6T_F_PROTO : 0;
        ip.invflags = w->invflags;
        memcpy(e, &ip, sizeof(ip));
    } else {
        xt_ipt_ip_t ip;
        memset(&ip, 0, sizeof(ip));
        memcpy(ip.src, w->src, 4);
        memcpy(ip.smsk, w->smsk, 4);
        memcpy(ip.dst, w->dst, 4);
        memcpy(ip.dmsk, w->dmsk, 4);
        memcpy(ip.iniface, w->in, 16);
        memcpy(ip.outiface, w->out, 16);
        memset(ip.iniface_mask, 0xff, w->in_len ? w->in_len + 1 : 0);
        memset(ip.outiface_mask, 0xff, w->out_len ? w->out_len + 1 : 0);
        ip.proto = w->proto;
        ip.invflags = w->invflags;
        memcpy(e, &ip, sizeof(ip));
    }
}

static void put_target(const want_t *w, uint8_t *t, firc_xt_entry_t *out, uint32_t *exts) {
    if (w->target == T_VERDICT || w->target == T_JUMP) {
        put_ext(t, XT_STANDARD_TARGET_LEN, "", 0);
        if (w->target == T_VERDICT) { firc_xt_wr32(t + XT_MATCH_HDR_LEN, (uint32_t)w->verdict); }
        else { snprintf(out->jump, sizeof(out->jump), "%s", w->jump); }
        return;
    }
    bool dnat = w->target == T_DNAT;
    *exts |= dnat ? FIRC_XT_EXT_DNAT : FIRC_XT_EXT_MASQUERADE;
    if (w->v6) {
        put_ext(t, XT_MATCH_HDR_LEN + 40, dnat ? "DNAT" : "MASQUERADE", dnat ? 1 : 0);
        xt_nat_range_t r;
        memset(&r, 0, sizeof(r));
        if (w->to_addr) {
            r.flags |= XT_NAT_MAP_IPS;
            memcpy(r.min_addr, w->addr, 16);
            memcpy(r.max_addr, w->addr, 16);
        }
        if (w->to_port) {
            r.flags |= XT_NAT_PROTO_SPECIFIED;
            r.min_proto = htons(w->port);
            r.max_proto = htons(w->port);
        }
        memcpy(t + XT_MATCH_HDR_LEN, &r, sizeof(r));
        return;
    }
    put_ext(t, XT_MATCH_HDR_LEN + 24, dnat ? "DNAT" : "MASQUERADE", 0);
    xt_nat_ipv4_compat_t c;
    memset(&c, 0, sizeof(c));
    c.rangesize = 1;
    if (w->to_addr) {
        c.range[0].flags |= XT_NAT_MAP_IPS;
        memcpy(c.range[0].min_ip, w->addr, 4);
        memcpy(c.range[0].max_ip, w->addr, 4);
    }
    if (w->to_port) {
        c.range[0].flags |= XT_NAT_PROTO_SPECIFIED;
        c.range[0].min_port = htons(w->port);
        c.range[0].max_port = htons(w->port);
    }
    memcpy(t + XT_MATCH_HDR_LEN, &c, sizeof(c));
}

static ext_t *open_ext(want_t *w, uint32_t bit, const char *name, uint8_t rev, uint16_t size) {
    if (w->nm == MAX_MATCHES) { return NULL; }
    for (size_t i = 0; i < w->nm; i++) {
        if (w->m[i].bit == bit) { return NULL; }
    }
    ext_t *x = &w->m[w->nm++];
    memset(x, 0, sizeof(*x));
    x->bit = bit;
    x->name = name;
    x->rev = rev;
    x->size = size;
    w->cur = x;
    return x;
}

static ext_t *open_l4(want_t *w, uint16_t proto) {
    ext_t *x = proto == 6 ? open_ext(w, FIRC_XT_EXT_TCP, "tcp", 0, sizeof(xt_tcp_t))
                          : open_ext(w, FIRC_XT_EXT_UDP, "udp", 0, sizeof(xt_udp_t));
    if (x == NULL) { return NULL; }
    uint16_t ports[4] = {0, 0xffff, 0, 0xffff};
    memcpy(x->p, ports, sizeof(ports));
    return x;
}

static bool open_match(want_t *w, const char *name) {
    if (strcmp(name, "mark") == 0) { return open_ext(w, FIRC_XT_EXT_MARK, "mark", 1, sizeof(xt_mark_mtinfo1_t)) != NULL; }
    if (strcmp(name, "connmark") == 0) {
        return open_ext(w, FIRC_XT_EXT_CONNMARK_MATCH, "connmark", 1, sizeof(xt_connmark_mtinfo1_t)) != NULL;
    }
    if (strcmp(name, "tcp") == 0) { return w->proto == 6 && open_l4(w, 6) != NULL; }
    if (strcmp(name, "udp") == 0) { return w->proto == 17 && open_l4(w, 17) != NULL; }
    if (strcmp(name, "conntrack") == 0) {
        return open_ext(w, FIRC_XT_EXT_CONNTRACK, "conntrack", 3, sizeof(xt_conntrack_mtinfo3_t)) != NULL;
    }
    if (strcmp(name, "multiport") == 0) {
        return (w->proto == 6 || w->proto == 17) &&
               open_ext(w, FIRC_XT_EXT_MULTIPORT, "multiport", 1, sizeof(xt_multiport_v1_t)) != NULL;
    }
    if (strcmp(name, "mac") == 0) { return open_ext(w, FIRC_XT_EXT_MAC, "mac", w->mac_rev, sizeof(xt_mac_info_t)) != NULL; }
    if (strcmp(name, "limit") == 0) {
        ext_t *x = open_ext(w, FIRC_XT_EXT_LIMIT, "limit", 0, sizeof(xt_rateinfo_t));
        if (x == NULL) { return false; }
        xt_rateinfo_t r;
        memset(&r, 0, sizeof(r));
        r.avg = XT_LIMIT_SCALE * 3600u / 3u;
        r.burst = 5;
        memcpy(x->p, &r, sizeof(r));
        x->seen = 1;
        return true;
    }
    if (strcmp(name, "addrtype") == 0) {
        return !w->v6 && open_ext(w, FIRC_XT_EXT_ADDRTYPE, "addrtype", 1, sizeof(xt_addrtype_info_v1_t)) != NULL;
    }
    if (strcmp(name, "set") == 0) { return open_ext(w, FIRC_XT_EXT_SET, "set", 1, sizeof(xt_set_info_t)) != NULL; }
    if (strcmp(name, "length") == 0) { return open_ext(w, FIRC_XT_EXT_LENGTH, "length", 0, sizeof(xt_length_info_t)) != NULL; }
    if (strcmp(name, "connbytes") == 0) {
        return open_ext(w, FIRC_XT_EXT_CONNBYTES, "connbytes", 0, sizeof(xt_connbytes_info_t)) != NULL;
    }
    if (strcmp(name, "string") == 0) {
        ext_t *x = open_ext(w, FIRC_XT_EXT_STRING, "string", 1, sizeof(xt_string_info_t));
        if (x == NULL) { return false; }
        xt_string_info_t si;
        memset(&si, 0, sizeof(si));
        si.to_offset = 65535;
        memcpy(x->p, &si, sizeof(si));
        return true;
    }
    return false;
}

typedef struct named_bits {
    const char *name;
    uint16_t bits;
} named_bits_t;

static bool parse_names(const char *s, const named_bits_t *k, size_t nk, uint16_t *out) {
    *out = 0;
    const char *p = s;
    for (;;) {
        const char *end = strchr(p, ',');
        size_t len = end != NULL ? (size_t)(end - p) : strlen(p);
        size_t i = 0;
        while (i < nk && (strlen(k[i].name) != len || strncmp(k[i].name, p, len) != 0)) { i++; }
        if (i == nk) { return false; }
        *out = (uint16_t)(*out | k[i].bits);
        if (end == NULL) { return true; }
        p = end + 1;
    }
}

static bool parse_tcp_flags(const char *s, uint8_t *out) {
    static const named_bits_t k[] = {
        {"FIN", XT_TCP_FIN}, {"SYN", XT_TCP_SYN}, {"RST", XT_TCP_RST}, {"PSH", XT_TCP_PSH},
        {"ACK", XT_TCP_ACK}, {"URG", XT_TCP_URG}, {"ALL", 0x3f},       {"NONE", 0},
    };
    uint16_t v = 0;
    if (!parse_names(s, k, sizeof(k) / sizeof(k[0]), &v)) { return false; }
    *out = (uint8_t)v;
    return true;
}

static bool parse_ctstate(const char *s, uint16_t *out) {
    static const named_bits_t k[] = {
        {"INVALID", XT_CT_STATE_INVALID},
        {"ESTABLISHED", XT_CT_STATE_ESTABLISHED},
        {"RELATED", XT_CT_STATE_RELATED},
        {"NEW", XT_CT_STATE_NEW},
    };
    return parse_names(s, k, sizeof(k) / sizeof(k[0]), out);
}

static bool parse_multiport(const char *s, xt_multiport_v1_t *mp) {
    memset(mp, 0, sizeof(*mp));
    mp->flags = XT_MULTIPORT_DESTINATION;
    size_t i = 0;
    const char *p = s;
    for (;;) {
        const char *end = strchr(p, ',');
        char item[16];
        size_t len = end != NULL ? (size_t)(end - p) : strlen(p);
        if (len == 0 || len >= sizeof(item)) { return false; }
        memcpy(item, p, len);
        item[len] = '\0';
        char *colon = strchr(item, ':');
        if (colon != NULL) {
            *colon = '\0';
            uint16_t lo = 0, hi = 0;
            if (i + 2 > XT_MULTI_PORTS || !parse_u16(item, &lo) || !parse_u16(colon + 1, &hi) || lo >= hi) { return false; }
            mp->ports[i] = lo;
            mp->pflags[i] = 1;
            mp->ports[i + 1] = hi;
            i += 2;
        } else {
            uint16_t port = 0;
            if (i + 1 > XT_MULTI_PORTS || !parse_u16(item, &port)) { return false; }
            mp->ports[i++] = port;
        }
        if (end == NULL) { break; }
        p = end + 1;
    }
    mp->count = (uint8_t)i;
    return true;
}

static int hex_digit(char c) {
    if (c >= '0' && c <= '9') { return c - '0'; }
    if (c >= 'A' && c <= 'F') { return c - 'A' + 10; }
    if (c >= 'a' && c <= 'f') { return c - 'a' + 10; }
    return -1;
}

static bool parse_mac(const char *s, uint8_t out[6]) {
    if (strlen(s) != 17) { return false; }
    for (size_t i = 0; i < 6; i++) {
        int hi = hex_digit(s[i * 3]), lo = hex_digit(s[i * 3 + 1]);
        if (hi < 0 || lo < 0 || (i < 5 && s[i * 3 + 2] != ':')) { return false; }
        out[i] = (uint8_t)(hi * 16 + lo);
    }
    return true;
}

static int mark_option(ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (strcmp(opt, "--mark") != 0) { return NOT_MINE; }
    xt_mark_mtinfo1_t mi;
    memset(&mi, 0, sizeof(mi));
    if (na < 1 || x->seen != 0 || !parse_mark(a[0], &mi.mark, &mi.mask)) { return BAD; }
    mi.invert = inv ? 1 : 0;
    memcpy(x->p, &mi, sizeof(mi));
    x->seen = 1;
    return 1;
}

static int l4_option(ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (strcmp(opt, "--dport") == 0) {
        uint16_t lo = 0, hi = 0;
        if (na < 1 || inv || (x->seen & 1u) || !parse_ports(a[0], ':', &lo, &hi)) { return BAD; }
        memcpy(x->p + 4, &lo, 2);
        memcpy(x->p + 6, &hi, 2);
        x->seen |= 1u;
        return 1;
    }
    if (strcmp(opt, "--sport") == 0) {
        uint16_t lo = 0, hi = 0;
        if (na < 1 || inv || (x->seen & 4u) || !parse_ports(a[0], ':', &lo, &hi)) { return BAD; }
        memcpy(x->p, &lo, 2);
        memcpy(x->p + 2, &hi, 2);
        x->seen |= 4u;
        return 1;
    }
    if (x->bit == FIRC_XT_EXT_TCP && strcmp(opt, "--tcp-flags") == 0) {
        uint8_t mask = 0, cmp = 0;
        if (na < 2 || inv || (x->seen & 2u) || !parse_tcp_flags(a[0], &mask) || !parse_tcp_flags(a[1], &cmp)) {
            return BAD;
        }
        xt_tcp_t t;
        memcpy(&t, x->p, sizeof(t));
        t.flg_mask = mask;
        t.flg_cmp = cmp;
        memcpy(x->p, &t, sizeof(t));
        x->seen |= 2u;
        return 2;
    }
    return NOT_MINE;
}

static int conntrack_option(const want_t *w, ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    xt_conntrack_mtinfo3_t c;
    memcpy(&c, x->p, sizeof(c));
    if (strcmp(opt, "--ctdir") == 0) {
        if (na < 1 || inv || (c.match_flags & XT_CT_DIRECTION)) { return BAD; }
        if (strcmp(a[0], "REPLY") == 0) {
            c.invert_flags = (uint16_t)(c.invert_flags | XT_CT_DIRECTION);
        } else if (strcmp(a[0], "ORIGINAL") != 0) {
            return BAD;
        }
        c.match_flags = (uint16_t)(c.match_flags | XT_CT_DIRECTION);
    } else if (strcmp(opt, "--ctstate") == 0) {
        if (na < 1 || inv || (c.match_flags & XT_CT_STATE) || !parse_ctstate(a[0], &c.state_mask)) { return BAD; }
        c.match_flags = (uint16_t)(c.match_flags | XT_CT_STATE);
    } else if (strcmp(opt, "--ctorigdst") == 0) {
        if (na < 1 || (c.match_flags & XT_CT_ORIGDST) || !parse_addr(w->v6, true, a[0], c.origdst_addr, c.origdst_mask)) {
            return BAD;
        }
        c.match_flags = (uint16_t)(c.match_flags | XT_CT_ORIGDST);
        if (inv) { c.invert_flags = (uint16_t)(c.invert_flags | XT_CT_ORIGDST); }
    } else {
        return NOT_MINE;
    }
    memcpy(x->p, &c, sizeof(c));
    x->seen = 1;
    return 1;
}

static int multiport_option(ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (strcmp(opt, "--dports") != 0) { return NOT_MINE; }
    xt_multiport_v1_t mp;
    if (na < 1 || inv || x->seen != 0 || !parse_multiport(a[0], &mp)) { return BAD; }
    memcpy(x->p, &mp, sizeof(mp));
    x->seen = 1;
    return 1;
}

static int mac_option(ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (strcmp(opt, "--mac-source") != 0) { return NOT_MINE; }
    xt_mac_info_t mi;
    memset(&mi, 0, sizeof(mi));
    if (na < 1 || x->seen != 0 || !parse_mac(a[0], mi.srcaddr)) { return BAD; }
    mi.invert = inv ? 1 : 0;
    memcpy(x->p, &mi, sizeof(mi));
    x->seen = 1;
    return 1;
}

static bool parse_rate(const char *s, uint32_t *avg) {
    static const struct { const char *unit; uint32_t secs; } units[] = {
        {"sec", 1}, {"second", 1}, {"min", 60}, {"minute", 60}, {"hour", 3600}, {"day", 86400},
    };
    char buf[32];
    size_t n = strlen(s);
    if (n == 0 || n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n + 1);
    uint32_t mult = 1;
    char *slash = strchr(buf, '/');
    if (slash != NULL) {
        *slash = '\0';
        size_t i = 0;
        while (i < sizeof(units) / sizeof(units[0]) && strcmp(units[i].unit, slash + 1) != 0) { i++; }
        if (i == sizeof(units) / sizeof(units[0])) { return false; }
        mult = units[i].secs;
    }
    uint32_t rate = 0;
    if (!parse_u32(buf, &rate) || rate == 0 || XT_LIMIT_SCALE * mult / rate == 0) { return false; }
    *avg = XT_LIMIT_SCALE * mult / rate;
    return true;
}

static int limit_option(ext_t *x, const char *opt, char *const *a, size_t na) {
    xt_rateinfo_t r;
    memcpy(&r, x->p, sizeof(r));
    uint16_t bit;
    if (strcmp(opt, "--limit") == 0) {
        bit = 2u;
        if (na < 1 || (x->seen & bit) || !parse_rate(a[0], &r.avg)) { return BAD; }
    } else if (strcmp(opt, "--limit-burst") == 0) {
        bit = 4u;
        if (na < 1 || (x->seen & bit) || !parse_u32(a[0], &r.burst) || r.burst == 0 || r.burst > 10000u) { return BAD; }
    } else {
        return NOT_MINE;
    }
    memcpy(x->p, &r, sizeof(r));
    x->seen |= bit;
    return 1;
}

static int length_option(ext_t *x, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--length") != 0) { return NOT_MINE; }
    xt_length_info_t l;
    memset(&l, 0, sizeof(l));
    char buf[16];
    if (na < 1 || x->seen != 0 || strlen(a[0]) >= sizeof(buf)) { return BAD; }
    memcpy(buf, a[0], strlen(a[0]) + 1);
    char *colon = strchr(buf, ':');
    if (colon != NULL) { *colon = '\0'; }
    if (!parse_u16(buf, &l.min)) { return BAD; }
    if (colon == NULL) {
        l.max = l.min;
    } else if (colon[1] == '\0') {
        l.max = 65535;
    } else if (!parse_u16(colon + 1, &l.max)) {
        return BAD;
    }
    if (l.min > l.max) { return BAD; }
    memcpy(x->p, &l, sizeof(l));
    x->seen = 1;
    return 1;
}

static bool parse_u64(const char *s, uint64_t *v) {
    if (s[0] < '0' || s[0] > '9') { return false; }
    char *end = NULL;
    unsigned long long x = strtoull(s, &end, 0);
    if (*end != '\0') { return false; }
    *v = (uint64_t)x;
    return true;
}

static bool parse_count(const char *s, uint64_t *from, uint64_t *to) {
    char buf[48];
    size_t n = strlen(s);
    if (n == 0 || n >= sizeof(buf)) { return false; }
    memcpy(buf, s, n + 1);
    char *colon = strchr(buf, ':');
    if (colon != NULL) { *colon = '\0'; }
    *to = UINT64_MAX;
    if (!parse_u64(buf, from)) { return false; }
    if (colon != NULL && colon[1] != '\0' && !parse_u64(colon + 1, to)) { return false; }
    return *from <= *to;
}

static bool pick(const char *s, const char *const *names, size_t n, uint8_t *out) {
    for (size_t i = 0; i < n; i++) {
        if (strcmp(s, names[i]) == 0) {
            *out = (uint8_t)i;
            return true;
        }
    }
    return false;
}

static int connbytes_option(ext_t *x, const char *opt, char *const *a, size_t na) {
    static const char *const dirs[] = {"original", "reply", "both"};
    static const char *const modes[] = {"packets", "bytes", "avgpkt"};
    xt_connbytes_info_t c;
    memcpy(&c, x->p, sizeof(c));
    uint16_t bit;
    bool ok;
    if (strcmp(opt, "--connbytes") == 0) {
        bit = 1u;
        ok = na >= 1 && parse_count(a[0], &c.from, &c.to);
    } else if (strcmp(opt, "--connbytes-dir") == 0) {
        bit = 2u;
        ok = na >= 1 && pick(a[0], dirs, 3, &c.direction);
    } else if (strcmp(opt, "--connbytes-mode") == 0) {
        bit = 4u;
        ok = na >= 1 && pick(a[0], modes, 3, &c.what);
    } else {
        return NOT_MINE;
    }
    if (!ok || (x->seen & bit)) { return BAD; }
    memcpy(x->p, &c, sizeof(c));
    x->seen |= bit;
    return 1;
}

static bool parse_hex_string(const char *s, uint8_t *out, size_t *n) {
    bool hex = false;
    *n = 0;
    for (size_t i = 0; s[i] != '\0';) {
        if (s[i] == '|') {
            hex = !hex;
            i++;
            continue;
        }
        if (hex && s[i] == ' ') {
            i++;
            continue;
        }
        if (*n == XT_STRING_MAX_PATTERN) { return false; }
        if (hex) {
            int hi = hex_digit(s[i]), lo = hi < 0 ? -1 : hex_digit(s[i + 1]);
            if (hi < 0 || lo < 0) { return false; }
            out[(*n)++] = (uint8_t)(hi * 16 + lo);
            i += 2;
        } else {
            if (s[i] == '\\' && s[i + 1] != '\0') { i++; }
            out[(*n)++] = (uint8_t)s[i++];
        }
    }
    return !hex && *n > 0;
}

static int string_option(ext_t *x, const char *opt, char *const *a, size_t na) {
    xt_string_info_t si;
    memcpy(&si, x->p, sizeof(si));
    uint16_t bit;
    if (na < 1) { return BAD; }
    if (strcmp(opt, "--algo") == 0) {
        bit = 1u;
        if (strcmp(a[0], "bm") != 0 && strcmp(a[0], "kmp") != 0) { return BAD; }
        memset(si.algo, 0, sizeof(si.algo));
        memcpy(si.algo, a[0], strlen(a[0]));
    } else if (strcmp(opt, "--hex-string") == 0 || strcmp(opt, "--string") == 0) {
        bit = 2u;
        size_t n = strlen(a[0]);
        memset(si.pattern, 0, sizeof(si.pattern));
        if (opt[2] == 'h') {
            if (!parse_hex_string(a[0], (uint8_t *)si.pattern, &n)) { return BAD; }
        } else {
            if (n == 0 || n > sizeof(si.pattern)) { return BAD; }
            memcpy(si.pattern, a[0], n);
        }
        si.patlen = (uint8_t)n;
    } else if (strcmp(opt, "--from") == 0 || strcmp(opt, "--to") == 0) {
        bit = opt[2] == 'f' ? 4u : 8u;
        if (!parse_u16(a[0], bit == 4u ? &si.from_offset : &si.to_offset)) { return BAD; }
        if (si.from_offset > si.to_offset) { return BAD; }
    } else {
        return NOT_MINE;
    }
    if (x->seen & bit) { return BAD; }
    memcpy(x->p, &si, sizeof(si));
    x->seen |= bit;
    return 1;
}

static int addrtype_option(ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (strcmp(opt, "--dst-type") != 0) { return NOT_MINE; }
    if (na < 1 || x->seen != 0 || strcmp(a[0], "LOCAL") != 0) { return BAD; }
    xt_addrtype_info_v1_t at;
    memset(&at, 0, sizeof(at));
    at.dest = XT_ADDRTYPE_LOCAL;
    at.flags = inv ? XT_ADDRTYPE_INVERT_DEST : 0u;
    memcpy(x->p, &at, sizeof(at));
    x->seen = 1;
    return 1;
}

static int set_option(want_t *w, ext_t *x, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--match-set") != 0) { return NOT_MINE; }
    size_t len = na >= 1 ? strlen(a[0]) : 0;
    if (na < 2 || x->seen != 0 || len == 0 || len >= sizeof(w->set) || strcmp(a[1], "dst") != 0) { return BAD; }
    xt_set_info_t si = {0, 1, 0};
    memcpy(x->p, &si, sizeof(si));
    memcpy(w->set, a[0], len + 1);
    x->seen = 1;
    return 2;
}

static int match_option(want_t *w, ext_t *x, const char *opt, char *const *a, size_t na, bool inv) {
    if (x == NULL) { return NOT_MINE; }
    switch (x->bit) {
    case FIRC_XT_EXT_MARK:
    case FIRC_XT_EXT_CONNMARK_MATCH: return mark_option(x, opt, a, na, inv);
    case FIRC_XT_EXT_TCP:
    case FIRC_XT_EXT_UDP: return l4_option(x, opt, a, na, inv);
    case FIRC_XT_EXT_CONNTRACK: return conntrack_option(w, x, opt, a, na, inv);
    case FIRC_XT_EXT_MULTIPORT: return multiport_option(x, opt, a, na, inv);
    case FIRC_XT_EXT_MAC: return mac_option(x, opt, a, na, inv);
    case FIRC_XT_EXT_LIMIT: return limit_option(x, opt, a, na);
    case FIRC_XT_EXT_LENGTH: return length_option(x, opt, a, na);
    case FIRC_XT_EXT_CONNBYTES: return connbytes_option(x, opt, a, na);
    case FIRC_XT_EXT_STRING: return string_option(x, opt, a, na);
    case FIRC_XT_EXT_ADDRTYPE: return addrtype_option(x, opt, a, na, inv);
    case FIRC_XT_EXT_SET: return set_option(w, x, opt, a, na);
    default: return NOT_MINE;
    }
}

static ext_t *l4_of(want_t *w) {
    for (size_t i = 0; i < w->nm; i++) {
        if (w->m[i].bit == FIRC_XT_EXT_TCP || w->m[i].bit == FIRC_XT_EXT_UDP) { return &w->m[i]; }
    }
    return NULL;
}

static int any_match_option(want_t *w, const char *opt, char *const *a, size_t na, bool inv) {
    int used = match_option(w, w->cur, opt, a, na, inv);
    if (used != NOT_MINE) { return used; }
    bool l4opt = strcmp(opt, "--dport") == 0 || strcmp(opt, "--sport") == 0 || strcmp(opt, "--tcp-flags") == 0;
    if (!l4opt || (w->proto != 6 && w->proto != 17)) { return BAD; }
    ext_t *l4 = l4_of(w);
    if (l4 == NULL) { l4 = open_l4(w, w->proto); }
    if (l4 == NULL) { return BAD; }
    w->cur = l4;
    used = match_option(w, l4, opt, a, na, inv);
    return used == NOT_MINE ? BAD : used;
}

static bool open_target(want_t *w, const char *name) {
    w->cur = NULL;
    if (strcmp(name, "ACCEPT") == 0 || strcmp(name, "DROP") == 0 || strcmp(name, "RETURN") == 0) {
        w->target = T_VERDICT;
        w->verdict = name[0] == 'A' ? XT_VERDICT_ACCEPT : name[0] == 'D' ? XT_VERDICT_DROP : XT_VERDICT_RETURN;
        return true;
    }
    if (strcmp(name, "MASQUERADE") == 0) {
        w->target = T_MASQ;
        return true;
    }
    if (strcmp(name, "DNAT") == 0) {
        w->target = T_DNAT;
        return true;
    }
    ext_t *t = &w->t;
    memset(t, 0, sizeof(*t));
    if (strcmp(name, "REJECT") == 0) {
        *t = (ext_t){.bit = FIRC_XT_EXT_REJECT, .name = "REJECT", .rev = 0, .size = sizeof(xt_reject_info_t)};
    } else if (strcmp(name, "MARK") == 0) {
        *t = (ext_t){.bit = FIRC_XT_EXT_MARK_TARGET, .name = "MARK", .rev = 2, .size = sizeof(xt_mark_tginfo2_t)};
    } else if (strcmp(name, "CONNMARK") == 0) {
        *t = (ext_t){.bit = FIRC_XT_EXT_CONNMARK, .name = "CONNMARK", .rev = 1, .size = sizeof(xt_connmark_tginfo1_t)};
    } else if (strcmp(name, "NFLOG") == 0) {
        *t = (ext_t){.bit = FIRC_XT_EXT_NFLOG, .name = "NFLOG", .rev = 0, .size = sizeof(xt_nflog_info_t), .seen = 1};
    } else if (strcmp(name, "REDIRECT") == 0) {
        if (w->v6) { return false; }
        *t = (ext_t){.bit = FIRC_XT_EXT_REDIRECT, .name = "REDIRECT", .rev = 0, .size = sizeof(xt_nat_ipv4_compat_t)};
    } else if (strcmp(name, "TCPMSS") == 0) {
        *t = (ext_t){.bit = FIRC_XT_EXT_TCPMSS, .name = "TCPMSS", .rev = 0, .size = sizeof(xt_tcpmss_info_t)};
    } else {
        if (!chain_name_ok(name)) { return false; }
        w->target = T_JUMP;
        snprintf(w->jump, sizeof(w->jump), "%s", name);
        return true;
    }
    w->target = T_EXT;
    return true;
}

static int dnat_option(want_t *w, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--to-destination") != 0 || na < 1 || w->to_addr || w->to_port) { return BAD; }
    const char *to = a[0];
    if (to[0] == ':' && (!w->v6 || strchr(to + 1, ':') == NULL)) {
        if (w->proto == 0 || !parse_u16(to + 1, &w->port) || w->port == 0) { return BAD; }
        w->to_port = true;
    } else {
        if (inet_pton(w->v6 ? AF_INET6 : AF_INET, to, w->addr) != 1) { return BAD; }
        w->to_addr = true;
    }
    return 1;
}

static int reject_option(const want_t *w, ext_t *t, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--reject-with") != 0 || na < 1 || t->seen != 0) { return BAD; }
    uint32_t with;
    if (strcmp(a[0], "tcp-reset") == 0) {
        with = w->v6 ? XT_REJECT_V6_TCP_RESET : XT_REJECT_V4_TCP_RESET;
    } else if (!w->v6 && strcmp(a[0], "icmp-host-unreachable") == 0) {
        with = XT_REJECT_V4_HOST_UNREACH;
    } else if (w->v6 && strcmp(a[0], "icmp6-addr-unreachable") == 0) {
        with = XT_REJECT_V6_ADDR_UNREACH;
    } else {
        return BAD;
    }
    memcpy(t->p, &with, sizeof(with));
    t->seen = 1;
    return 1;
}

static int mark_target_option(ext_t *t, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--set-xmark") != 0 || na < 1 || t->seen != 0) { return BAD; }
    xt_mark_tginfo2_t mi;
    if (!parse_mark(a[0], &mi.mark, &mi.mask)) { return BAD; }
    memcpy(t->p, &mi, sizeof(mi));
    t->seen = 1;
    return 1;
}

static int connmark_option(ext_t *t, const char *opt, char *const *a, size_t na) {
    xt_connmark_tginfo1_t c;
    memcpy(&c, t->p, sizeof(c));
    if (strcmp(opt, "--save-mark") == 0) {
        if (t->seen != 0) { return BAD; }
        c.mode = XT_CONNMARK_MODE_SAVE;
        c.ctmask = 0xffffffffu;
        c.nfmask = 0xffffffffu;
        t->seen = 1;
        memcpy(t->p, &c, sizeof(c));
        return 0;
    }
    bool nf = strcmp(opt, "--nfmask") == 0;
    if (!nf && strcmp(opt, "--ctmask") != 0) { return BAD; }
    uint16_t bit = nf ? 2u : 4u;
    uint32_t v = 0;
    if (!(t->seen & 1u) || (t->seen & bit) || na < 1 || !parse_u32(a[0], &v)) { return BAD; }
    if (nf) {
        c.nfmask = v;
    } else {
        c.ctmask = v;
    }
    t->seen |= bit;
    memcpy(t->p, &c, sizeof(c));
    return 1;
}

static int nflog_option(ext_t *t, const char *opt, char *const *a, size_t na) {
    xt_nflog_info_t nf;
    memcpy(&nf, t->p, sizeof(nf));
    uint16_t bit;
    if (strcmp(opt, "--nflog-group") == 0) {
        bit = 2u;
        if (na < 1 || (t->seen & bit) || !parse_u16(a[0], &nf.group)) { return BAD; }
    } else if (strcmp(opt, "--nflog-threshold") == 0) {
        bit = 4u;
        if (na < 1 || (t->seen & bit) || !parse_u16(a[0], &nf.threshold)) { return BAD; }
    } else {
        return BAD;
    }
    memcpy(t->p, &nf, sizeof(nf));
    t->seen |= bit;
    return 1;
}

static int redirect_option(ext_t *t, const char *opt, char *const *a, size_t na) {
    if (strcmp(opt, "--to-ports") != 0 || na < 1 || t->seen != 0) { return BAD; }
    uint16_t lo = 0, hi = 0;
    if (!parse_ports(a[0], '-', &lo, &hi) || lo == 0) { return BAD; }
    xt_nat_ipv4_compat_t c;
    memset(&c, 0, sizeof(c));
    c.rangesize = 1;
    c.range[0].flags = XT_NAT_PROTO_SPECIFIED;
    c.range[0].min_port = htons(lo);
    c.range[0].max_port = htons(hi);
    memcpy(t->p, &c, sizeof(c));
    t->seen = 1;
    return 1;
}

static int tcpmss_option(ext_t *t, const char *opt) {
    if (strcmp(opt, "--clamp-mss-to-pmtu") != 0 || t->seen != 0) { return BAD; }
    xt_tcpmss_info_t m = {.mss = XT_TCPMSS_CLAMP_PMTU};
    memcpy(t->p, &m, sizeof(m));
    t->seen = 1;
    return 0;
}

static bool has_syn_match(const want_t *w) {
    for (size_t i = 0; i < w->nm; i++) {
        if (w->m[i].bit != FIRC_XT_EXT_TCP) { continue; }
        xt_tcp_t t;
        memcpy(&t, w->m[i].p, sizeof(t));
        if (t.flg_cmp & XT_TCP_SYN) { return true; }
    }
    return false;
}

static int target_option(want_t *w, const char *opt, char *const *a, size_t na) {
    if (w->target == T_DNAT) { return dnat_option(w, opt, a, na); }
    if (w->target != T_EXT) { return BAD; }
    switch (w->t.bit) {
    case FIRC_XT_EXT_REJECT: return reject_option(w, &w->t, opt, a, na);
    case FIRC_XT_EXT_MARK_TARGET: return mark_target_option(&w->t, opt, a, na);
    case FIRC_XT_EXT_CONNMARK: return connmark_option(&w->t, opt, a, na);
    case FIRC_XT_EXT_NFLOG: return nflog_option(&w->t, opt, a, na);
    case FIRC_XT_EXT_REDIRECT: return redirect_option(&w->t, opt, a, na);
    case FIRC_XT_EXT_TCPMSS: return tcpmss_option(&w->t, opt);
    default: return BAD;
    }
}

static bool target_complete(const want_t *w) {
    switch (w->target) {
    case T_VERDICT:
    case T_JUMP:
    case T_MASQ:
        return true;
    case T_DNAT:
        return w->to_addr || w->to_port;
    case T_EXT: {
        if (w->t.seen == 0) { return false; }
        if (w->t.bit == FIRC_XT_EXT_REDIRECT) { return w->proto == 6 || w->proto == 17; }
        if (w->t.bit == FIRC_XT_EXT_TCPMSS) { return has_syn_match(w); }
        if (w->t.bit != FIRC_XT_EXT_REJECT) { return true; }
        uint32_t with;
        memcpy(&with, w->t.p, sizeof(with));
        return with != (w->v6 ? XT_REJECT_V6_TCP_RESET : XT_REJECT_V4_TCP_RESET) || w->proto == 6;
    }
    default:
        return false;
    }
}

static bool matches_complete(const want_t *w) {
    for (size_t i = 0; i < w->nm; i++) {
        uint32_t b = w->m[i].bit;
        uint16_t seen = w->m[i].seen;
        if (b == FIRC_XT_EXT_TCP || b == FIRC_XT_EXT_UDP) { continue; }
        if (b == FIRC_XT_EXT_CONNBYTES ? seen != 7u : b == FIRC_XT_EXT_STRING ? (seen & 3u) != 3u : seen == 0) {
            return false;
        }
    }
    return true;
}

static bool generic_option(want_t *w, const char *a, const char *b, bool inv) {
    if (inv) {
        bool in = strcmp(a, "-i") == 0;
        if (!in && strcmp(a, "-o") != 0) { return false; }
        w->invflags = (uint8_t)(w->invflags | (in ? XT_INV_VIA_IN : XT_INV_VIA_OUT));
    }
    if (strcmp(a, "-s") == 0) {
        if (w->has_s || !parse_addr(w->v6, false, b, w->src, w->smsk)) { return false; }
        w->has_s = true;
        return true;
    }
    if (strcmp(a, "-d") == 0) {
        if (w->has_d || !parse_addr(w->v6, false, b, w->dst, w->dmsk)) { return false; }
        w->has_d = true;
        return true;
    }
    if (strcmp(a, "-i") == 0) { return w->in_len == 0 && parse_iface(b, w->in, &w->in_len); }
    if (strcmp(a, "-o") == 0) { return w->out_len == 0 && parse_iface(b, w->out, &w->out_len); }
    if (w->proto != 0) { return false; }
    if (strcmp(b, "tcp") == 0) {
        w->proto = 6;
    } else if (strcmp(b, "udp") == 0) {
        w->proto = 17;
    } else {
        return false;
    }
    return true;
}

static bool is_generic(const char *a) {
    return strcmp(a, "-s") == 0 || strcmp(a, "-d") == 0 || strcmp(a, "-i") == 0 || strcmp(a, "-o") == 0 ||
           strcmp(a, "-p") == 0;
}

static bool invertible(const char *a) {
    return strcmp(a, "--mark") == 0 || strcmp(a, "--ctorigdst") == 0 || strcmp(a, "--mac-source") == 0 ||
           strcmp(a, "--dst-type") == 0 || strcmp(a, "-i") == 0 || strcmp(a, "-o") == 0;
}

static bool read_rule(firc_ipt_proto_t fam, uint8_t mac_rev, const firc_ipt_rule_t *rule, want_t *w) {
    memset(w, 0, sizeof(*w));
    w->v6 = fam == FIRC_IPT_PROTO_IPV6;
    w->mac_rev = mac_rev;
    char *const *p = rule->parts;
    size_t n = rule->n_parts, i = 0;
    bool inv = false;
    while (i < n) {
        const char *a = p[i];
        const char *b = i + 1 < n ? p[i + 1] : NULL;
        if (strcmp(a, "!") == 0) {
            if (inv || w->target != T_NONE || b == NULL || !invertible(b)) { return false; }
            inv = true;
            i++;
            continue;
        }
        int used;
        if (w->target == T_DNAT || w->target == T_EXT) {
            used = target_option(w, a, p + i + 1, n - i - 1);
        } else if (w->target != T_NONE) {
            return false;
        } else if (is_generic(a)) {
            used = b != NULL && generic_option(w, a, b, inv) ? 1 : BAD;
            inv = false;
        } else if (strcmp(a, "-m") == 0) {
            used = b != NULL && open_match(w, b) ? 1 : BAD;
        } else if (strcmp(a, "-j") == 0) {
            used = b != NULL && open_target(w, b) ? 1 : BAD;
        } else if (a[0] == '-' && a[1] == '-') {
            used = any_match_option(w, a, p + i + 1, n - i - 1, inv);
            inv = false;
        } else {
            return false;
        }
        if (used < 0) { return false; }
        i += 1u + (size_t)used;
    }
    return !inv && w->target != T_NONE && matches_complete(w) && target_complete(w);
}

static uint8_t *put_ext_of(const ext_t *x, uint8_t *at, uint32_t *exts) {
    uint16_t size = (uint16_t)(XT_MATCH_HDR_LEN + XT_ALIGN8(x->size));
    put_ext(at, size, x->name, x->rev);
    memcpy(at + XT_MATCH_HDR_LEN, x->p, x->size);
    *exts |= x->bit;
    return at + size;
}

firc_err_t firc_xt_encode(firc_ipt_proto_t fam, uint8_t mac_rev, const firc_ipt_rule_t *rule, firc_xt_entry_t *out,
                          uint32_t *exts, char set[FIRC_XT_NAME_LEN]) {
    set[0] = '\0';
    memset(out, 0, sizeof(*out));
    out->old_index = -1;
    out->old_off = FIRC_XT_NONE;
    out->points_at = FIRC_XT_NONE;
    want_t w;
    if (rule == NULL || mac_rev > 1 || !read_rule(fam, mac_rev, rule, &w)) { return FIRC_ERR_INVAL; }
    uint32_t eh = firc_xt_ehdr(fam), msize = 0;
    for (size_t i = 0; i < w.nm; i++) { msize += XT_MATCH_HDR_LEN + XT_ALIGN8(w.m[i].size); }
    uint32_t tsize;
    if (w.target == T_VERDICT || w.target == T_JUMP) {
        tsize = XT_STANDARD_TARGET_LEN;
    } else if (w.target == T_EXT) {
        tsize = XT_MATCH_HDR_LEN + XT_ALIGN8(w.t.size);
    } else {
        tsize = XT_MATCH_HDR_LEN + (w.v6 ? 40u : 24u);
    }
    uint32_t len = eh + msize + tsize;
    uint8_t *e = calloc(1, len);
    if (e == NULL) { return FIRC_ERR_NOMEM; }
    put_ip(&w, e);
    firc_xt_wr16(e + firc_xt_at_toff(fam), (uint16_t)(eh + msize));
    firc_xt_wr16(e + firc_xt_at_next(fam), (uint16_t)len);
    uint8_t *m = e + eh;
    for (size_t i = 0; i < w.nm; i++) {
        if (w.m[i].bit == FIRC_XT_EXT_SET) { out->set_at = (uint16_t)((size_t)(m - e) + XT_MATCH_HDR_LEN); }
        m = put_ext_of(&w.m[i], m, exts);
    }
    memcpy(set, w.set, sizeof(w.set));
    if (w.target == T_EXT) {
        put_ext_of(&w.t, m, exts);
    } else {
        put_target(&w, m, out, exts);
    }
    out->bytes = e;
    out->len = len;
    out->owned = true;
    return FIRC_OK;
}

void firc_xt_block_init(firc_xt_block_t *b) { memset(b, 0, sizeof(*b)); }

void firc_xt_block_free(firc_xt_block_t *b) {
    if (b == NULL) { return; }
    free(b->bytes);
    free(b->jumps);
    free(b->sets);
    memset(b, 0, sizeof(*b));
}

static uint8_t *block_room(firc_xt_block_t *b, size_t len) {
    if (b->len + len > b->cap) {
        size_t cap = b->cap ? b->cap * 2 : 4096;
        while (cap < b->len + len) { cap *= 2; }
        uint8_t *grown = realloc(b->bytes, cap);
        if (grown == NULL) { return NULL; }
        b->bytes = grown;
        b->cap = cap;
    }
    memset(b->bytes + b->len, 0, len);
    return b->bytes + b->len;
}

static firc_err_t block_jump(firc_xt_block_t *b, const char *chain) {
    if (b->n_jumps == b->cap_jumps) {
        size_t cap = b->cap_jumps ? b->cap_jumps * 2 : 8;
        firc_xt_jump_t *grown = realloc(b->jumps, cap * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        b->jumps = grown;
        b->cap_jumps = cap;
    }
    b->jumps[b->n_jumps].entry = b->n;
    snprintf(b->jumps[b->n_jumps].chain, sizeof(b->jumps[0].chain), "%s", chain);
    b->n_jumps++;
    return FIRC_OK;
}

static firc_err_t block_set(firc_xt_block_t *b, uint32_t at, const char *name) {
    if (b->n_sets == b->cap_sets) {
        size_t cap = b->cap_sets ? b->cap_sets * 2 : 8;
        firc_xt_setref_t *grown = realloc(b->sets, cap * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        b->sets = grown;
        b->cap_sets = cap;
    }
    firc_xt_setref_t *r = &b->sets[b->n_sets++];
    r->entry = b->n;
    r->at = at;
    snprintf(r->name, sizeof(r->name), "%s", name);
    return FIRC_OK;
}

static bool block_takes(const firc_xt_block_t *b, firc_ipt_proto_t fam) {
    return (fam == FIRC_IPT_PROTO_IPV4 || fam == FIRC_IPT_PROTO_IPV6) &&
           (b->n == 0 || (b->fam == fam && b->first_mac_rev == b->mac_rev));
}

static void block_took(firc_xt_block_t *b, firc_ipt_proto_t fam) {
    if (b->n == 0) { b->first_mac_rev = b->mac_rev; }
    b->fam = fam;
    b->n++;
}

firc_err_t firc_xt_block_add_rule(firc_xt_block_t *b, firc_ipt_proto_t fam, const firc_ipt_rule_t *rule) {
    if (!block_takes(b, fam)) { return FIRC_ERR_INVAL; }
    firc_xt_entry_t e;
    uint32_t exts = 0;
    char set[FIRC_XT_NAME_LEN];
    firc_err_t err = firc_xt_encode(fam, b->mac_rev, rule, &e, &exts, set);
    if (err != FIRC_OK) { return err; }
    uint8_t *at = block_room(b, e.len);
    bool jumped = at != NULL && e.jump[0] != '\0';
    if (at == NULL || (jumped && block_jump(b, e.jump) != FIRC_OK) || (set[0] != '\0' && block_set(b, e.set_at, set) != FIRC_OK)) {
        if (jumped && b->n_jumps > 0 && b->jumps[b->n_jumps - 1].entry == b->n) { b->n_jumps--; }
        firc_xt_entry_clear(&e);
        return FIRC_ERR_NOMEM;
    }
    memcpy(at, e.bytes, e.len);
    b->len += e.len;
    block_took(b, fam);
    b->exts |= exts;
    firc_xt_entry_clear(&e);
    return FIRC_OK;
}

firc_err_t firc_xt_block_add_dnat(firc_xt_block_t *b, firc_ipt_proto_t fam, const uint8_t *fake, const uint8_t *real) {
    if (b == NULL || fake == NULL || real == NULL || !block_takes(b, fam)) { return FIRC_ERR_INVAL; }
    want_t w;
    memset(&w, 0, sizeof(w));
    w.v6 = fam == FIRC_IPT_PROTO_IPV6;
    size_t bytes = w.v6 ? 16 : 4;
    memcpy(w.dst, fake, bytes);
    memset(w.dmsk, 0xff, bytes);
    memcpy(w.addr, real, bytes);
    w.to_addr = true;
    w.target = T_DNAT;
    xt_mark_mtinfo1_t mi;
    memset(&mi, 0, sizeof(mi));
    mi.mark = FIRC_MARK_HANDLED;
    mi.mask = FIRC_MARK_HANDLED;
    ext_t *handled = open_ext(&w, FIRC_XT_EXT_MARK, "mark", 1, sizeof(mi));
    memcpy(handled->p, &mi, sizeof(mi));
    uint32_t eh = firc_xt_ehdr(fam), toff = eh + XT_MATCH_HDR_LEN + XT_ALIGN8(handled->size);
    uint32_t len = toff + XT_MATCH_HDR_LEN + (w.v6 ? 40u : 24u);
    uint8_t *e = block_room(b, len);
    if (e == NULL) { return FIRC_ERR_NOMEM; }
    put_ip(&w, e);
    firc_xt_wr16(e + firc_xt_at_toff(fam), (uint16_t)toff);
    firc_xt_wr16(e + firc_xt_at_next(fam), (uint16_t)len);
    put_ext_of(handled, e + eh, &b->exts);
    put_target(&w, e + toff, NULL, &b->exts);
    b->len += len;
    block_took(b, fam);
    return FIRC_OK;
}

bool firc_xt_block_equal(const firc_xt_block_t *a, const firc_xt_block_t *b) {
    if (a->n != b->n || a->len != b->len || a->n_jumps != b->n_jumps || a->n_sets != b->n_sets ||
        (a->n > 0 && a->fam != b->fam)) {
        return false;
    }
    for (size_t i = 0; i < a->n_jumps; i++) {
        if (a->jumps[i].entry != b->jumps[i].entry || strcmp(a->jumps[i].chain, b->jumps[i].chain) != 0) { return false; }
    }
    for (size_t i = 0; i < a->n_sets; i++) {
        if (a->sets[i].entry != b->sets[i].entry || a->sets[i].at != b->sets[i].at ||
            strcmp(a->sets[i].name, b->sets[i].name) != 0) {
            return false;
        }
    }
    return a->len == 0 || memcmp(a->bytes, b->bytes, a->len) == 0;
}

static bool block_sound(const firc_xt_block_t *b, firc_ipt_proto_t fam) {
    if (b->n > 0 && b->fam != fam) { return false; }
    uint32_t eh = firc_xt_ehdr(fam), i = 0;
    size_t off = 0, jump = 0, set = 0;
    for (; i < b->n && b->len - off >= eh; i++) {
        uint32_t len = firc_xt_rd16(b->bytes + off + firc_xt_at_next(fam));
        uint32_t toff = firc_xt_rd16(b->bytes + off + firc_xt_at_toff(fam));
        if (len > b->len - off || toff < eh || len < toff + XT_MATCH_HDR_LEN) { return false; }
        if (jump < b->n_jumps && b->jumps[jump].entry == i) { jump++; }
        if (set < b->n_sets && b->sets[set].entry == i) {
            if (b->sets[set].at < eh + XT_MATCH_HDR_LEN || b->sets[set].at + 2u > toff) { return false; }
            set++;
        }
        off += len;
    }
    return i == b->n && off == b->len && jump == b->n_jumps && set == b->n_sets;
}

static bool ip_only_dst(firc_ipt_proto_t fam, const uint8_t *e, uint8_t *addr, uint8_t *mask) {
    memset(addr, 0, 16);
    memset(mask, 0, 16);
    bool inverted, only;
    if (fam == FIRC_IPT_PROTO_IPV6) {
        xt_ip6t_ip6_t ip;
        memcpy(&ip, e, sizeof(ip));
        memcpy(addr, ip.dst, 16);
        memcpy(mask, ip.dmsk, 16);
        inverted = (ip.invflags & XT_INV_DSTIP) != 0;
        memset(ip.dst, 0, sizeof(ip.dst));
        memset(ip.dmsk, 0, sizeof(ip.dmsk));
        ip.flags = (uint8_t)(ip.flags & ~XT_IP6T_F_GOTO);
        xt_ip6t_ip6_t zero;
        memset(&zero, 0, sizeof(zero));
        only = memcmp(&ip, &zero, sizeof(ip)) == 0;
    } else {
        xt_ipt_ip_t ip;
        memcpy(&ip, e, sizeof(ip));
        memcpy(addr, ip.dst, 4);
        memcpy(mask, ip.dmsk, 4);
        inverted = (ip.invflags & XT_INV_DSTIP) != 0;
        memset(ip.dst, 0, sizeof(ip.dst));
        memset(ip.dmsk, 0, sizeof(ip.dmsk));
        ip.flags = (uint8_t)(ip.flags & ~XT_IPT_F_GOTO);
        xt_ipt_ip_t zero;
        memset(&zero, 0, sizeof(zero));
        only = memcmp(&ip, &zero, sizeof(ip)) == 0;
    }
    if (inverted) {
        memset(addr, 0, 16);
        memset(mask, 0, 16);
    }
    return only;
}

firc_err_t firc_xt_block_roles(const firc_xt_block_t *b, firc_ipt_proto_t fam, firc_xt_role_fn fn, void *ud) {
    if ((fam != FIRC_IPT_PROTO_IPV4 && fam != FIRC_IPT_PROTO_IPV6) || !block_sound(b, fam)) { return FIRC_ERR_INVAL; }
    uint32_t eh = firc_xt_ehdr(fam);
    size_t off = 0, jump = 0;
    for (uint32_t i = 0; i < b->n; i++) {
        const uint8_t *e = b->bytes + off;
        uint32_t len = firc_xt_rd16(e + firc_xt_at_next(fam)), toff = firc_xt_rd16(e + firc_xt_at_toff(fam));
        bool jumps = jump < b->n_jumps && b->jumps[jump].entry == i;
        if (jumps) { jump++; }
        uint8_t addr[16], mask[16];
        bool only = ip_only_dst(fam, e, addr, mask) && toff == eh;
        bool has_dst = false;
        for (size_t k = 0; k < 16; k++) { has_dst = has_dst || mask[k] != 0; }
        const uint8_t *t = e + toff;
        firc_xt_role_t role = FIRC_XT_ROLE_OTHER;
        if (only && has_dst && (jumps || memcmp(t + 2, "MARK", 5) == 0)) {
            role = FIRC_XT_ROLE_MARKS;
        } else if (memcmp(t + 2, "DNAT", 5) == 0) {
            uint32_t at = XT_MATCH_HDR_LEN + (t[31] == 0 ? 4u : 0u);
            if (at + 4u <= len - toff && (firc_xt_rd32(t + at) & XT_NAT_MAP_IPS) != 0) { role = FIRC_XT_ROLE_REWRITES; }
        }
        fn(ud, i, role, addr, mask);
        off += len;
    }
    return FIRC_OK;
}

firc_err_t firc_xt_block_keep(const firc_xt_block_t *in, firc_ipt_proto_t fam, const bool *keep, firc_xt_block_t *out) {
    if (out->n != 0 || out->len != 0 || out->n_jumps != 0 || out->n_sets != 0 || (fam != FIRC_IPT_PROTO_IPV4 && fam != FIRC_IPT_PROTO_IPV6) ||
        !block_sound(in, fam)) {
        return FIRC_ERR_INVAL;
    }
    out->fam = fam;
    out->mac_rev = in->mac_rev;
    out->first_mac_rev = in->first_mac_rev;
    out->exts = in->exts;
    size_t off = 0, jump = 0, set = 0;
    for (uint32_t i = 0; i < in->n; i++) {
        const uint8_t *e = in->bytes + off;
        uint32_t len = firc_xt_rd16(e + firc_xt_at_next(fam));
        bool jumps = jump < in->n_jumps && in->jumps[jump].entry == i;
        bool names = set < in->n_sets && in->sets[set].entry == i;
        if (keep[i]) {
            if (jumps && block_jump(out, in->jumps[jump].chain) != FIRC_OK) { return FIRC_ERR_NOMEM; }
            if (names && block_set(out, in->sets[set].at, in->sets[set].name) != FIRC_OK) { return FIRC_ERR_NOMEM; }
            uint8_t *at = block_room(out, len);
            if (at == NULL) { return FIRC_ERR_NOMEM; }
            memcpy(at, e, len);
            out->len += len;
            out->n++;
        }
        if (jumps) { jump++; }
        if (names) { set++; }
        off += len;
    }
    return FIRC_OK;
}
