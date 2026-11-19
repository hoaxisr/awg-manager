#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#define ZLIB_CONST
#include <zlib.h>

#include "firc/sub_fetch.h"
#include "subparse_internal.h"

#define SRS_VERSION_MAX 5
#define SRS_INFLATED_MAX ((size_t)16 << 20)
#define SRS_LOGICAL_DEPTH_MAX 100
#define SRS_KEY_MAX 300

enum {
    IT_QUERY_TYPE,
    IT_NETWORK,
    IT_DOMAIN,
    IT_KEYWORD,
    IT_REGEX,
    IT_SOURCE_CIDR,
    IT_CIDR,
    IT_SOURCE_PORT,
    IT_SOURCE_PORT_RANGE,
    IT_PORT,
    IT_PORT_RANGE,
    IT_PROCESS_NAME,
    IT_PROCESS_PATH,
    IT_PACKAGE_NAME,
    IT_WIFI_SSID,
    IT_WIFI_BSSID,
    IT_ADGUARD,
    IT_PROCESS_PATH_REGEX,
    IT_NETWORK_TYPE,
    IT_NETWORK_EXPENSIVE,
    IT_NETWORK_CONSTRAINED,
    IT_IFACE_ADDRESS,
    IT_DEFAULT_IFACE_ADDRESS,
    IT_PACKAGE_NAME_REGEX,
    IT_COUNT,
    IT_FINAL = 0xff
};

static const char k_corrupt[] = "not a sing-box rule-set: a compiled .srs that does not decode";

typedef struct rd {
    const uint8_t *p, *end;
    bool bad;
} rd_t;

static size_t rd_left(const rd_t *r) { return (size_t)(r->end - r->p); }

static uint8_t rd_u8(rd_t *r) {
    if (r->bad || r->p >= r->end) {
        r->bad = true;
        return 0;
    }
    return *r->p++;
}

static uint64_t rd_uvarint(rd_t *r) {
    uint64_t v = 0;
    for (unsigned shift = 0; shift <= 63; shift += 7) {
        uint8_t b = rd_u8(r);
        if (r->bad || (shift == 63 && b > 1)) {
            r->bad = true;
            return 0;
        }
        v |= (uint64_t)(b & 0x7f) << shift;
        if (b < 0x80) { return v; }
    }
    r->bad = true;
    return 0;
}

static const uint8_t *rd_take(rd_t *r, uint64_t n) {
    if (r->bad || n > rd_left(r)) {
        r->bad = true;
        return NULL;
    }
    const uint8_t *at = r->p;
    r->p += n;
    return at;
}

static const uint8_t *rd_array(rd_t *r, size_t width, uint64_t *n) {
    *n = rd_uvarint(r);
    if (r->bad || *n > rd_left(r) / width) {
        r->bad = true;
        return NULL;
    }
    return rd_take(r, *n * width);
}

static uint64_t be64(const uint8_t *p) {
    uint64_t v = 0;
    for (int i = 0; i < 8; i++) { v = v << 8 | p[i]; }
    return v;
}

static void skip_strings(rd_t *r) {
    uint64_t n = rd_uvarint(r);
    for (uint64_t i = 0; i < n && !r->bad; i++) { (void)rd_take(r, rd_uvarint(r)); }
}

static const uint8_t *skip_addr(rd_t *r, uint64_t *n) {
    *n = rd_uvarint(r);
    if (*n != 4 && *n != 16) { r->bad = true; }
    return rd_take(r, *n);
}

static void skip_ipset(rd_t *r) {
    if (rd_u8(r) != 1) { r->bad = true; }
    const uint8_t *count = rd_take(r, 8);
    uint64_t n = count != NULL ? be64(count) : 0;
    for (uint64_t i = 0; i < n && !r->bad; i++) {
        uint64_t fl, tl;
        const uint8_t *from = skip_addr(r, &fl), *to = skip_addr(r, &tl);
        if (!r->bad && (fl != tl || memcmp(from, to, (size_t)fl) > 0)) { r->bad = true; }
    }
}

static void skip_prefixes(rd_t *r) {
    uint64_t n = rd_uvarint(r);
    for (uint64_t i = 0; i < n && !r->bad; i++) {
        uint64_t len;
        (void)skip_addr(r, &len);
        (void)rd_u8(r);
    }
}

typedef struct trie {
    const uint8_t *leaves, *bm, *labels;
    uint64_t leaf_words, bm_words, n_labels;
    size_t nbits;
    uint32_t *rank;
} trie_t;

static unsigned popcount8(unsigned v) {
    unsigned c = 0;
    for (; v != 0; v &= v - 1) { c++; }
    return c;
}

static bool bit_at(const uint8_t *words, uint64_t n_words, size_t i) {
    if ((i >> 6) >= n_words) { return false; }
    return (words[(i >> 6) * 8 + 7 - ((i & 63) >> 3)] >> (i & 7)) & 1;
}

static firc_err_t trie_open(rd_t *r, trie_t *t, bool index) {
    memset(t, 0, sizeof(*t));
    (void)rd_u8(r);
    t->leaves = rd_array(r, 8, &t->leaf_words);
    t->bm = rd_array(r, 8, &t->bm_words);
    t->labels = rd_array(r, 1, &t->n_labels);
    if (r->bad || t->bm_words == 0) { return FIRC_ERR_INVAL; }
    if (index && (t->rank = malloc((size_t)(t->bm_words + 1) * sizeof(uint32_t))) == NULL) { return FIRC_ERR_NOMEM; }
    size_t ones = 0, last = 0;
    bool any = false;
    for (uint64_t w = 0; w < t->bm_words; w++) {
        if (index) { t->rank[w] = (uint32_t)ones; }
        for (int b = 0; b < 8; b++) {
            unsigned byte = t->bm[w * 8 + (size_t)b];
            ones += popcount8(byte);
            if (byte != 0) {
                unsigned hi = 7;
                while (!((byte >> hi) & 1)) { hi--; }
                size_t at = (size_t)w * 64 + (size_t)(7 - b) * 8 + hi;
                if (!any || at > last) { last = at; }
                any = true;
            }
        }
    }
    if (index) { t->rank[t->bm_words] = (uint32_t)ones; }
    t->nbits = any ? last + 1 : 0;
    size_t zeros = t->nbits - ones;
    return any && ones == zeros + 1 && t->n_labels == zeros ? FIRC_OK : FIRC_ERR_INVAL;
}

static void skip_set(rd_t *r) {
    trie_t t;
    if (trie_open(r, &t, false) != FIRC_OK) { r->bad = true; }
}

typedef struct rule {
    const uint8_t *at[IT_COUNT];
    bool honourable;
    bool invert;
} rule_t;

static void skip_item(rd_t *r, unsigned t) {
    uint64_t n;
    switch (t) {
    case IT_QUERY_TYPE:
    case IT_SOURCE_PORT:
    case IT_PORT:
        (void)rd_array(r, 2, &n);
        break;
    case IT_NETWORK_TYPE:
        (void)rd_array(r, 1, &n);
        break;
    case IT_DOMAIN:
    case IT_ADGUARD:
        skip_set(r);
        break;
    case IT_SOURCE_CIDR:
    case IT_CIDR:
        skip_ipset(r);
        break;
    case IT_NETWORK_EXPENSIVE:
    case IT_NETWORK_CONSTRAINED:
        break;
    case IT_IFACE_ADDRESS:
        n = rd_uvarint(r);
        for (uint64_t i = 0; i < n && !r->bad; i++) {
            (void)rd_u8(r);
            skip_prefixes(r);
        }
        break;
    case IT_DEFAULT_IFACE_ADDRESS:
        skip_prefixes(r);
        break;
    default:
        skip_strings(r);
        break;
    }
}

static bool item_is_mapped(unsigned t) {
    return t == IT_NETWORK || t == IT_DOMAIN || t == IT_KEYWORD || t == IT_REGEX || t == IT_CIDR ||
           t == IT_PORT || t == IT_PORT_RANGE || t == IT_ADGUARD;
}

static void read_default(rd_t *r, rule_t *ru) {
    memset(ru, 0, sizeof(*ru));
    ru->honourable = true;
    for (;;) {
        unsigned t = rd_u8(r);
        if (r->bad) { return; }
        if (t == IT_FINAL) {
            ru->invert = rd_u8(r) != 0;
            return;
        }
        if (t >= IT_COUNT) {
            r->bad = true;
            return;
        }
        if (ru->at[t] != NULL || !item_is_mapped(t)) { ru->honourable = false; }
        ru->at[t] = r->p;
        skip_item(r, t);
        if (r->bad) { return; }
    }
}

static void skip_rule(rd_t *r, unsigned depth) {
    if (depth > SRS_LOGICAL_DEPTH_MAX) {
        r->bad = true;
        return;
    }
    unsigned type = rd_u8(r);
    if (type == 0) {
        rule_t ru;
        read_default(r, &ru);
        return;
    }
    if (type != 1 || rd_u8(r) > 1) {
        r->bad = true;
        return;
    }
    uint64_t n = rd_uvarint(r);
    for (uint64_t i = 0; i < n && !r->bad; i++) { skip_rule(r, depth + 1); }
    (void)rd_u8(r);
}

static rd_t rd_at(const uint8_t *at, const uint8_t *end) { return (rd_t){at, end, false}; }

static bool read_string(rd_t *r, char *buf, size_t cap) {
    uint64_t n = rd_uvarint(r);
    const uint8_t *s = rd_take(r, n);
    if (s == NULL || n >= cap) { return false; }
    size_t len = (size_t)n;
    if (memchr(s, 0, len) != NULL) { return false; }
    memcpy(buf, s, len);
    buf[len] = '\0';
    return true;
}

static size_t trie_select1(const trie_t *t, size_t k) {
    size_t lo = 0, hi = (size_t)t->bm_words - 1;
    while (lo < hi) {
        size_t mid = lo + (hi - lo + 1) / 2;
        if (t->rank[mid] <= k) { lo = mid; }
        else { hi = mid - 1; }
    }
    size_t left = k - t->rank[lo];
    for (int b = 7; b >= 0; b--) {
        unsigned byte = t->bm[lo * 8 + (size_t)b];
        unsigned c = popcount8(byte);
        if (left >= c) {
            left -= c;
            continue;
        }
        for (unsigned i = 0; i < 8; i++) {
            if (((byte >> i) & 1) && left-- == 0) { return lo * 64 + (size_t)(7 - b) * 8 + i; }
        }
    }
    return t->nbits;
}

static size_t trie_start(const trie_t *t, size_t node) { return node == 0 ? 0 : trie_select1(t, node - 1) + 1; }

static size_t trie_child(const trie_t *t, size_t node, uint8_t label) {
    for (size_t b = trie_start(t, node); b < t->nbits && !bit_at(t->bm, t->bm_words, b); b++) {
        if (t->labels[b - node] == label) { return b - node + 1; }
    }
    return 0;
}

typedef struct emit {
    firc_sub_walk_t *w;
    bool constrained;
    bool adguard;
} emit_t;

static firc_err_t emit_domain(emit_t *e, const trie_t *t, size_t node, const char *f) {
    if (f[0] == '\n' || f[0] == '\r') { return firc_sub_walk_name(e->w, f + 1, FIRC_RULE_NAMESPACE, e->constrained); }
    size_t dot = trie_child(t, node, '.');
    size_t cr = dot != 0 ? trie_child(t, dot, '\r') : 0;
    if (cr != 0 && bit_at(t->leaves, t->leaf_words, cr)) { return FIRC_OK; }
    return firc_sub_walk_name(e->w, f, FIRC_RULE_DOMAIN, e->constrained);
}

static firc_err_t emit_adguard(emit_t *e, char *f, size_t n) {
    enum { START, SUFFIX, FREE } left = f[0] == '\n' ? SUFFIX : f[0] == '\r' ? FREE : START;
    if (left != START) {
        f++;
        n--;
    }
    bool end = n == 0 || f[n - 1] != '\b';
    if (!end) { f[--n] = '\0'; }
    if (n == 0 || strspn(f, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_.*") != n) {
        return firc_sub_walk_drop(e->w);
    }
    if (end && strchr(f, '*') == NULL && left != FREE) {
        return firc_sub_walk_name(e->w, f, left == SUFFIX ? FIRC_RULE_NAMESPACE : FIRC_RULE_DOMAIN, e->constrained);
    }
    char re[3 * SRS_KEY_MAX];
    size_t k = (size_t)snprintf(re, sizeof(re), "%s", left == SUFFIX ? "(?:^|\\.)" : left == START ? "^" : "");
    for (size_t i = 0; i < n; i++) {
        if (f[i] == '.') { re[k++] = '\\'; }
        if (f[i] == '*') { re[k++] = '.'; }
        re[k++] = f[i];
    }
    if (end) { re[k++] = '$'; }
    re[k] = '\0';
    return firc_sub_walk_name(e->w, re, FIRC_RULE_REGEX, e->constrained);
}

static firc_err_t emit_key(emit_t *e, const trie_t *t, size_t node, const uint8_t *key, size_t n) {
    char f[SRS_KEY_MAX + 1];
    for (size_t i = 0; i < n; i++) { f[i] = (char)key[n - 1 - i]; }
    f[n] = '\0';
    if (n == 0 || memchr(f, 0, n) != NULL) { return firc_sub_walk_drop(e->w); }
    return e->adguard ? emit_adguard(e, f, n) : emit_domain(e, t, node, f);
}

typedef struct frame {
    size_t node, b;
} frame_t;

static firc_err_t trie_walk(rd_t *r, emit_t *e, const char **why) {
    trie_t t;
    firc_err_t open = trie_open(r, &t, true);
    if (open != FIRC_OK) {
        free(t.rank);
        if (open == FIRC_ERR_INVAL) { *why = k_corrupt; }
        return open;
    }
    frame_t fr[SRS_KEY_MAX + 1];
    uint8_t key[SRS_KEY_MAX];
    size_t depth = 1;
    fr[0] = (frame_t){0, 0};
    firc_err_t err = bit_at(t.leaves, t.leaf_words, 0) ? firc_sub_walk_drop(e->w) : FIRC_OK;
    while (err == FIRC_OK && depth > 0) {
        frame_t *top = &fr[depth - 1];
        if (top->b >= t.nbits) {
            *why = k_corrupt;
            err = FIRC_ERR_INVAL;
            break;
        }
        if (bit_at(t.bm, t.bm_words, top->b)) {
            if (--depth > 0) { fr[depth - 1].b++; }
            continue;
        }
        size_t child = top->b - top->node + 1;
        if (depth > SRS_KEY_MAX) {
            err = firc_sub_walk_drop(e->w);
            top->b++;
            continue;
        }
        key[depth - 1] = t.labels[child - 1];
        if (bit_at(t.leaves, t.leaf_words, child)) { err = emit_key(e, &t, child, key, depth); }
        fr[depth++] = (frame_t){child, trie_start(&t, child)};
    }
    free(t.rank);
    return err;
}

static void format_prefix(char *out, size_t cap, const uint8_t *a, size_t len, unsigned bits) {
    if (len == 4) {
        snprintf(out, cap, "%u.%u.%u.%u/%u", a[0], a[1], a[2], a[3], bits);
        return;
    }
    static const uint8_t mapped[12] = {0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff};
    if (memcmp(a, mapped, 12) == 0) {
        snprintf(out, cap, "::ffff:%u.%u.%u.%u/%u", a[12], a[13], a[14], a[15], bits);
        return;
    }
    unsigned g[8];
    for (size_t i = 0; i < 8; i++) { g[i] = (unsigned)a[2 * i] << 8 | a[2 * i + 1]; }
    int best = -1, best_len = 1;
    for (int i = 0; i < 8;) {
        int j = i;
        while (j < 8 && g[j] == 0) { j++; }
        if (j - i > best_len) {
            best = i;
            best_len = j - i;
        }
        i = j > i ? j : i + 1;
    }
    size_t k = 0;
    for (int i = 0; i < 8; i++) {
        if (i == best) {
            k += (size_t)snprintf(out + k, cap - k, "::");
            i += best_len - 1;
            continue;
        }
        k += (size_t)snprintf(out + k, cap - k, "%s%x", k > 0 && out[k - 1] != ':' ? ":" : "", g[i]);
    }
    snprintf(out + k, cap - k, "/%u", bits);
}

static bool low_bit(const uint8_t *a, size_t len, unsigned h) { return (a[len - 1 - h / 8] >> (h % 8)) & 1; }

static void fill_low(uint8_t *out, const uint8_t *a, size_t len, unsigned h) {
    memcpy(out, a, len);
    for (unsigned i = 0; i < h; i++) { out[len - 1 - i / 8] |= (uint8_t)(1u << (i % 8)); }
}

static firc_err_t emit_range(firc_sub_walk_t *w, const firc_sub_cidr_plan_t *plan, const uint8_t *from,
                             const uint8_t *to, size_t len) {
    uint8_t a[16], top[16];
    memcpy(a, from, len);
    unsigned bits = (unsigned)len * 8;
    for (;;) {
        unsigned h = 0;
        while (h < bits && !low_bit(a, len, h)) {
            fill_low(top, a, len, h + 1);
            if (memcmp(top, to, len) > 0) { break; }
            h++;
        }
        char text[64];
        format_prefix(text, sizeof(text), a, len, bits - h);
        firc_err_t err = firc_sub_walk_cidr(w, plan, text);
        if (err != FIRC_OK) { return err; }
        fill_low(top, a, len, h);
        if (memcmp(top, to, len) == 0) { return FIRC_OK; }
        memcpy(a, top, len);
        for (size_t i = len; i-- > 0;) {
            if (++a[i] != 0) { break; }
        }
    }
}

static firc_err_t emit_cidrs(firc_sub_walk_t *w, rd_t *r, const firc_sub_cidr_plan_t *plan, const char **why) {
    (void)rd_u8(r);
    const uint8_t *count = rd_take(r, 8);
    uint64_t n = count != NULL ? be64(count) : 0;
    for (uint64_t i = 0; i < n; i++) {
        uint64_t fl, tl;
        const uint8_t *from = skip_addr(r, &fl), *to = skip_addr(r, &tl);
        if (r->bad) {
            *why = k_corrupt;
            return FIRC_ERR_INVAL;
        }
        firc_err_t err = emit_range(w, plan, from, to, (size_t)fl);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static firc_err_t emit_strings(firc_sub_walk_t *w, rd_t *r, const char *type, bool constrained) {
    uint64_t n = rd_uvarint(r);
    for (uint64_t i = 0; i < n && !r->bad; i++) {
        uint64_t n_len = rd_uvarint(r);
        const uint8_t *s = rd_take(r, n_len);
        if (s == NULL) { break; }
        size_t len = (size_t)n_len;
        if (memchr(s, 0, len) != NULL) {
            firc_err_t err = firc_sub_walk_drop(w);
            if (err != FIRC_OK) { return err; }
            continue;
        }
        char *copy = malloc(len + 1);
        if (copy == NULL) { return FIRC_ERR_NOMEM; }
        memcpy(copy, s, len);
        copy[len] = '\0';
        firc_err_t err = firc_sub_walk_name(w, copy, type, constrained);
        free(copy);
        if (err != FIRC_OK) { return err; }
    }
    return FIRC_OK;
}

static bool read_constraints(const rule_t *ru, const uint8_t *end, int *net, firc_sub_ports_t *ports) {
    char buf[32];
    if (ru->at[IT_NETWORK] != NULL) {
        rd_t r = rd_at(ru->at[IT_NETWORK], end);
        uint64_t n = rd_uvarint(&r);
        for (uint64_t i = 0; i < n; i++) {
            if (!read_string(&r, buf, sizeof(buf)) || !firc_sub_net_add(net, buf)) { return false; }
        }
    }
    if (ru->at[IT_PORT] != NULL) {
        rd_t r = rd_at(ru->at[IT_PORT], end);
        uint64_t n;
        const uint8_t *p = rd_array(&r, 2, &n);
        for (uint64_t i = 0; i < n; i++) {
            long v = (long)p[2 * i] << 8 | p[2 * i + 1];
            if (!firc_sub_ports_add(ports, v, v)) { return false; }
        }
    }
    if (ru->at[IT_PORT_RANGE] != NULL) {
        rd_t r = rd_at(ru->at[IT_PORT_RANGE], end);
        uint64_t n = rd_uvarint(&r);
        for (uint64_t i = 0; i < n; i++) {
            if (!read_string(&r, buf, sizeof(buf)) || !firc_sub_ports_add_range(ports, buf)) { return false; }
        }
    }
    return true;
}

static firc_err_t emit_rule(firc_sub_walk_t *w, const rule_t *ru, const uint8_t *end, const char **why) {
    int net = FIRC_SUB_NET_ANY;
    firc_sub_ports_t ports = {.n = 0};
    if (!read_constraints(ru, end, &net, &ports)) { return firc_sub_walk_drop(w); }
    emit_t e = {w, net != FIRC_SUB_NET_ANY || ports.n > 0, false};
    firc_err_t err = FIRC_OK;
    rd_t r;
    if (ru->at[IT_DOMAIN] != NULL) {
        r = rd_at(ru->at[IT_DOMAIN], end);
        if ((err = trie_walk(&r, &e, why)) != FIRC_OK) { return err; }
    }
    if (ru->at[IT_KEYWORD] != NULL) {
        r = rd_at(ru->at[IT_KEYWORD], end);
        if ((err = emit_strings(w, &r, FIRC_RULE_WILDCARD, e.constrained)) != FIRC_OK) { return err; }
    }
    if (ru->at[IT_REGEX] != NULL) {
        r = rd_at(ru->at[IT_REGEX], end);
        if ((err = emit_strings(w, &r, FIRC_RULE_REGEX, e.constrained)) != FIRC_OK) { return err; }
    }
    if (ru->at[IT_ADGUARD] != NULL) {
        r = rd_at(ru->at[IT_ADGUARD], end);
        e.adguard = true;
        if ((err = trie_walk(&r, &e, why)) != FIRC_OK) { return err; }
    }
    bool had_field = ru->at[IT_DOMAIN] != NULL || ru->at[IT_KEYWORD] != NULL || ru->at[IT_REGEX] != NULL ||
                     ru->at[IT_ADGUARD] != NULL;
    if (ru->at[IT_CIDR] == NULL) { return had_field ? FIRC_OK : firc_sub_walk_drop(w); }
    firc_sub_cidr_plan_t plan;
    firc_sub_cidr_plan(&plan, net, &ports);
    r = rd_at(ru->at[IT_CIDR], end);
    return emit_cidrs(w, &r, &plan, why);
}

static firc_err_t inflate_body(const uint8_t *in, size_t n, uint8_t **out, size_t *out_n, const char **why) {
    z_stream zs;
    memset(&zs, 0, sizeof(zs));
    if (inflateInit(&zs) != Z_OK) { return FIRC_ERR_NOMEM; }
    size_t cap = n * 4 < SRS_INFLATED_MAX ? n * 4 + 64 : SRS_INFLATED_MAX + 1, have = 0;
    uint8_t *buf = malloc(cap);
    firc_err_t err = buf != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
    while (err == FIRC_OK) {
        if (have == cap) {
            if (cap > SRS_INFLATED_MAX) {
                *why = "sing-box rule-set too large: over 16 MiB decompressed";
                err = FIRC_ERR_INVAL;
                break;
            }
            size_t grown = cap * 2 < SRS_INFLATED_MAX ? cap * 2 : SRS_INFLATED_MAX + 1;
            uint8_t *nb = realloc(buf, grown);
            if (nb == NULL) {
                err = FIRC_ERR_NOMEM;
                break;
            }
            buf = nb;
            cap = grown;
        }
        if (zs.avail_in == 0) {
            size_t rest = n - (size_t)zs.total_in;
            zs.next_in = in + zs.total_in;
            zs.avail_in = (uInt)(rest < 0x40000000u ? rest : 0x40000000u);
        }
        zs.next_out = buf + have;
        zs.avail_out = (uInt)(cap - have < 0x40000000u ? cap - have : 0x40000000u);
        uInt before = zs.avail_out;
        int rc = inflate(&zs, Z_NO_FLUSH);
        have += before - zs.avail_out;
        if (rc == Z_STREAM_END) {
            if (have > SRS_INFLATED_MAX) {
                *why = "sing-box rule-set too large: over 16 MiB decompressed";
                err = FIRC_ERR_INVAL;
            } else if (zs.avail_in != 0 || (size_t)zs.total_in != n) {
                *why = k_corrupt;
                err = FIRC_ERR_INVAL;
            }
            break;
        }
        if (rc == Z_MEM_ERROR) {
            err = FIRC_ERR_NOMEM;
        } else if (rc != Z_OK && !(rc == Z_BUF_ERROR && have == cap)) {
            *why = k_corrupt;
            err = FIRC_ERR_INVAL;
        }
    }
    inflateEnd(&zs);
    if (err != FIRC_OK) {
        free(buf);
        return err;
    }
    *out = buf;
    *out_n = have;
    return FIRC_OK;
}

bool firc_sub_body_is_srs(const char *body, size_t len) {
    if (len < 4 || memcmp(body, "SRS", 3) != 0) { return false; }
    unsigned char v = (unsigned char)body[3];
    return v < 0x20 && v != '\t' && v != '\n' && v != '\v' && v != '\f' && v != '\r';
}

firc_err_t firc_sub_parse_srs(const char *body, size_t len, firc_sub_sink_t *sink, firc_sub_parse_stats_t *st,
                              firc_sub_parse_progress_fn progress, void *ud) {
    if ((unsigned char)body[3] > SRS_VERSION_MAX) {
        st->why = "a compiled sing-box rule-set of a version newer than 5";
        return FIRC_ERR_INVAL;
    }
    uint8_t *raw = NULL;
    size_t n = 0;
    firc_err_t err = inflate_body((const uint8_t *)body + 4, len - 4, &raw, &n, &st->why);
    if (err != FIRC_OK) { return err; }
    firc_sub_walk_t w = {sink, st, progress, ud, 0, 0, FIRC_SUB_FETCH_MAX_BODY_BYTES};
    rd_t r = rd_at(raw, raw + n);
    uint64_t rules = rd_uvarint(&r);
    for (uint64_t i = 0; i < rules && err == FIRC_OK && !r.bad; i++) {
        const uint8_t *at = r.p;
        unsigned type = rd_u8(&r);
        if (type != 0) {
            r.p = at;
            skip_rule(&r, 0);
            if (!r.bad) { err = firc_sub_walk_drop(&w); }
            continue;
        }
        rule_t ru;
        read_default(&r, &ru);
        if (r.bad) { break; }
        err = ru.honourable && !ru.invert ? emit_rule(&w, &ru, r.p, &st->why) : firc_sub_walk_drop(&w);
    }
    if (err == FIRC_OK && (r.bad || r.p != r.end)) {
        st->why = k_corrupt;
        err = FIRC_ERR_INVAL;
    }
    free(raw);
    return err;
}
