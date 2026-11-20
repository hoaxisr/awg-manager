#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "hy2link.h"
#include "sublink.h"
#include "fmask.h"

#define HY2_QP_MAX 2048

const char HY2_BAD_QP[] = "!qp";

static int num(const char **pp, const char *end, unsigned long max, unsigned long *out) {
    const char *p = *pp;
    unsigned long v = 0;
    if (p >= end) return 0;
    for (; p < end && *p >= '0' && *p <= '9'; p++) {
        v = v * 10 + (unsigned long)(*p - '0');
        if (v > max) return 0;
    }
    if (p == *pp) return 0;
    *pp = p;
    *out = v;
    return 1;
}

int hy2_ports_canon(const char *spec, size_t n, char *out, size_t cap, uint16_t *first) {
    const char *p = spec, *end = spec + n;
    size_t o = 0;
    int items = 0;
    unsigned long a = 0, b = 0, head = 0;
    int plain = 1, full = 0;
    if (!n || !cap) return -1;
    for (;;) {
        const char *e = memchr(p, ',', (size_t)(end - p));
        if (!e) e = end;
        const char *q = p;
        if (!num(&q, e, 65535, &a) || !a) return -1;
        b = a;
        if (q < e) {
            if (*q++ != '-') return -1;
            if (!num(&q, e, 65535, &b) || q != e || b < a) return -1;
        }
        if (!items) head = a;
        if (++items > HY2_RANGES_MAX) full = 1;
        if (a != b || items > 1) plain = 0;
        int w = a == b ? snprintf(out + o, cap - o, "%s%lu", items > 1 ? "," : "", a)
                       : snprintf(out + o, cap - o, "%s%lu-%lu", items > 1 ? "," : "", a, b);
        if (full) w = 0;
        if (w < 0) return -1;
        if (full || (size_t)w >= cap - o) full = 1;
        else o += (size_t)w;
        if (e == end) break;
        p = e + 1;
    }
    if (plain || full) out[0] = '\0';
    *first = (uint16_t)head;
    return full;
}

const char *hy2_interval(const char *t, uint16_t *from, uint16_t *to) {
    static const char bad[] = "interval is not a number or range";
    const char *p = t, *end = t + strlen(t);
    unsigned long a = 0, b = 0;
    *from = *to = 0;
    if (p == end) return NULL;
    if (!num(&p, end, 65535, &a)) return bad;
    b = a;
    if (p < end) {
        if (*p++ != '-' || !num(&p, end, 65535, &b) || p != end) return bad;
    }
    if (a > b) {
        unsigned long x = a;
        a = b;
        b = x;
    }
    if (!a && !b) return NULL;
    if (a < 5) return "interval must be at least 5";
    *from = (uint16_t)a;
    *to = (uint16_t)b;
    return NULL;
}

unsigned long hy2_digits(const char *v, size_t vlen, unsigned long max) {
    unsigned long x = 0;
    const char *p = v;
    return num(&p, v + vlen, max, &x) && p == v + vlen ? x : 0;
}

static void note(struct vless_node *n, const char *why) {
    if (!n->skip_reason[0]) snprintf(n->skip_reason, sizeof n->skip_reason, "%s", why);
}

static int has_nul(const char *v, size_t vlen) {
    for (size_t i = 0; i + 3 <= vlen; i++)
        if (v[i] == '%' && v[i + 1] == '0' && v[i + 2] == '0') return 1;
    return 0;
}

const char *hy2_qp_intern(const char *json, size_t n) {
    char *buf = n <= HY2_QP_MAX ? malloc(n + 1) : NULL;
    size_t cl = buf ? fm_compact(json, n, buf, n + 1) : 0;
    const char *r = cl ? sl_intern(buf, cl) : HY2_BAD_QP;
    free(buf);
    return r;
}

static int hy2_own(struct vless_node *n, const char *k, size_t klen, const char *v, size_t vlen) {
    if ((klen == 4 && !strncmp(k, "type", 4)) || (klen == 8 && !strncmp(k, "security", 8))) return 1;
    if (klen == 4 && !strncmp(k, "obfs", 4)) {
        if (has_nul(v, vlen)) note(n, HY2_NUL);
        sl_set_pct(n->obfs, sizeof n->obfs, v, vlen);
    } else if (klen == 13 && !strncmp(k, "obfs-password", 13)) {
        char *d = sl_param_dup(v, vlen);
        if (has_nul(v, vlen)) note(n, HY2_NUL);
        if (d && strlen(d) > HY2_SECRET_MAX) note(n, HY2_LONG);
        else n->obfs_pw = d ? sl_intern(d, strlen(d)) : SL_FULL;
        free(d);
    } else if (klen == 9 && !strncmp(k, "pinSHA256", 9)) {
        char *d = sl_param_dup(v, vlen);
        if (d) sl_add_pins(n, d, 0); else n->pcs = SL_BAD_PIN;
        free(d);
    } else if (klen == 6 && !strncmp(k, "upmbps", 6)) {
        n->up_mbps = (uint32_t)hy2_digits(v, vlen, 100000);
    } else if (klen == 8 && !strncmp(k, "downmbps", 8)) {
        n->down_mbps = (uint32_t)hy2_digits(v, vlen, 100000);
    } else if (klen == 3 && !strncmp(k, "hop", 3)) {
        char *d = sl_param_dup(v, vlen);
        const char *why = d ? hy2_interval(d, &n->hop_from, &n->hop_to) : "interval is not a number or range";
        if (why) note(n, why);
        free(d);
    } else if (klen == 7 && !strncmp(k, "hopmode", 7)) {
        char *d = sl_param_dup(v, vlen);
        unsigned m = 0;
        if (!d) note(n, "hopmode: not a mode");
        else {
            for (const char *p = d;;) {
                const char *e = strchr(p, ',');
                size_t tl = e ? (size_t)(e - p) : strlen(p);
                if (tl == 5 && !strncmp(p, "local", 5)) m |= HY2_HOP_LOCAL;
                else if (tl == 6 && !strncmp(p, "remote", 6)) m |= HY2_HOP_REMOTE;
                else if (tl == 4 && !strncmp(p, "once", 4)) m |= HY2_HOP_ONCE;
                else if (tl == 3 && !strncmp(p, "ips", 3)) m |= HY2_HOP_IPS;
                else {
                    char t[40], why[96];
                    size_t o = 0;
                    for (; o < tl && o + 1 < sizeof t; o++)
                        t[o] = (unsigned char)p[o] < 0x20 || p[o] == 0x7f ? '?' : p[o];
                    t[o] = '\0';
                    snprintf(why, sizeof why, "hopmode: %s is not a mode", t);
                    note(n, why);
                    break;
                }
                if (!e) break;
                p = e + 1;
            }
        }
        if (d) n->hop_mode = (uint8_t)(m | HY2_HOP_EXPLICIT);
        free(d);
    } else if (klen == 5 && !strncmp(k, "mport", 5)) {
        char *d = sl_param_dup(v, vlen);
        if (d && d[0]) {
            char spec[2 * sizeof n->ports], out[sizeof n->ports];
            uint16_t first;
            char head[8];
            snprintf(head, sizeof head, "%u", n->port);
            int w = snprintf(spec, sizeof spec, "%s,%s", n->ports[0] ? n->ports : head, d);
            int r = w > 0 && (size_t)w < sizeof spec ? hy2_ports_canon(spec, (size_t)w, out, sizeof out, &first) : 1;
            if (r == 0) {
                snprintf(n->ports, sizeof n->ports, "%s", out);
                if (!(n->hop_mode & HY2_HOP_EXPLICIT)) n->hop_mode = HY2_HOP_LOCAL | HY2_HOP_REMOTE;
            }
            else note(n, r > 0 ? "port list too long" : "mport: not a port list");
        } else if (!d) note(n, "mport: not a port list");
        free(d);
    } else if (klen == 2 && !strncmp(k, "qp", 2)) {
        char *d = has_nul(v, vlen) ? NULL : sl_param_dup(v, vlen);
        n->qp = d ? hy2_qp_intern(d, strlen(d)) : HY2_BAD_QP;
        free(d);
    } else return 0;
    return 1;
}

int hy2_link_parse(const char *url, struct vless_node *n) {
    const char *secret = NULL;
    size_t sn = 0;
    const char *scheme = !strncmp(url, "hy2://", 6) ? "hy2://" : "hysteria2://";
    int rc = sl_link_parse(url, scheme, n, hy2_own, &secret, &sn, SL_LINK_ANON | SL_LINK_PORTS);
    if (rc) return rc;
    n->proto = VLESS_PROTO_HY2;
    snprintf(n->type, sizeof n->type, "quic");
    snprintf(n->security, sizeof n->security, "tls");
    n->uuid[0] = '\0';
    n->hop_mode &= (uint8_t)~HY2_HOP_EXPLICIT;
    if (!(n->hop_mode & (HY2_HOP_REMOTE | HY2_HOP_ONCE))) n->hop_mode &= (uint8_t)~HY2_HOP_IPS;
    if (has_nul(secret, sn)) note(n, HY2_NUL);
    char *d = sn ? sl_param_dup(secret, sn) : NULL;
    if (d && strlen(d) > HY2_SECRET_MAX) note(n, HY2_LONG);
    else if (d && d[0]) n->auth = sl_intern(d, strlen(d));
    free(d);
    return hy2_node_usable(n);
}

static int reject(struct vless_node *n, const char *why) {
    snprintf(n->skip_reason, sizeof n->skip_reason, "%s", why);
    return 1;
}

int hy2_node_usable(struct vless_node *n) {
    if (sl_link_usable_pre(n)) {
        if (!strncmp(n->skip_reason, "pcs: ", 5))
            snprintf(n->skip_reason, sizeof n->skip_reason, "pinSHA256: not a hex SHA-256");
        return 1;
    }
    if (n->skip_reason[0]) return 1;
    if (n->auth == SL_FULL || n->obfs_pw == SL_FULL || n->qp == SL_FULL) return reject(n, "too many distinct keys");
    if (!n->auth || !n->auth[0]) return reject(n, "auth is empty");
    if (n->obfs[0] && strcmp(n->obfs, "salamander")) {
        char x[sizeof n->obfs];
        size_t i = 0;
        for (; n->obfs[i]; i++) x[i] = (unsigned char)n->obfs[i] < 0x20 || n->obfs[i] == 0x7f ? '?' : n->obfs[i];
        x[i] = '\0';
        snprintf(n->skip_reason, sizeof n->skip_reason, "obfs %s is not supported", x);
        return 1;
    }
    if (n->obfs_pw && strlen(n->obfs_pw) < 4) return reject(n, "obfs-password shorter than 4 bytes");
    if (!strcmp(n->obfs, "salamander") && !n->obfs_pw) return reject(n, "obfs-password missing");
    if (n->pcs) {
        size_t pins = 1;
        for (const char *c = n->pcs; *c; c++) pins += *c == ',';
        if (pins > 8) return reject(n, "pinSHA256: more than 8 pins");
    }
    if (n->pks) return reject(n, "pks is not supported for hysteria2");
    if (n->ech) return reject(n, "ech is not supported for hysteria2");
    if (n->vcn && strchr(n->vcn, ',')) return reject(n, "verifyPeerCertByName: one name only");
    if (n->fm) return reject(n, "finalmask tcp is not supported for hysteria2");
    if (n->qp == HY2_BAD_QP) return reject(n, "qp does not parse");
    if (n->qp) {
        struct hy2_qp q;
        char why[sizeof n->skip_reason];
        if (hy2_qp_parse(n->qp, strlen(n->qp), &q, why, sizeof why)) return reject(n, why);
    }
    return sl_link_nowhere(n);
}

uint32_t hy2_mbps(uint64_t bytes_per_s) {
    if (bytes_per_s > 20000000000ull) return 0;
    uint64_t m = bytes_per_s * 8 / 1000000;
    return m > 100000 ? 0 : (uint32_t)m;
}

void hy2_node_qp(const struct vless_node *n, struct hy2_qp *out) {
    char why[sizeof n->skip_reason];
    memset(out, 0, sizeof *out);
    if (n->qp && n->qp != HY2_BAD_QP && hy2_qp_parse(n->qp, strlen(n->qp), out, why, sizeof why)) memset(out, 0, sizeof *out);
    if (!out->up_bps) out->up_bps = (uint64_t)n->up_mbps * 125000;
    if (!out->down_bps) out->down_bps = (uint64_t)n->down_mbps * 125000;
}

void hy2_in_fail(struct hy2_in *h, const char *why) {
    if (!h->why[0]) snprintf(h->why, sizeof h->why, "%s", why);
}

void hy2_in_secret(struct hy2_in *h, char *dst, const char *v, size_t n, int nul) {
    if (nul || memchr(v, '\0', n)) hy2_in_fail(h, HY2_NUL);
    else if (n > HY2_SECRET_MAX) hy2_in_fail(h, HY2_LONG);
    else {
        memcpy(dst, v, n);
        dst[n] = '\0';
    }
}

void hy2_in_ports(struct hy2_in *h, const char *spec, size_t n, const char *what) {
    char why[48];
    if (!n) return;
    int r = hy2_ports_canon(spec, n, h->ports, sizeof h->ports, &h->first);
    if (r == 0) {
        h->hop_mode = HY2_HOP_LOCAL | HY2_HOP_REMOTE;
        return;
    }
    h->ports[0] = '\0';
    h->first = 0;
    if (r > 0) hy2_in_fail(h, "port list too long");
    else {
        snprintf(why, sizeof why, "%s: not a port list", what);
        hy2_in_fail(h, why);
    }
}

void hy2_apply(struct vless_node *n, const struct hy2_in *h) {
    n->proto = VLESS_PROTO_HY2;
    snprintf(n->type, sizeof n->type, "quic");
    snprintf(n->security, sizeof n->security, "tls");
    n->uuid[0] = '\0';
    if (h->why[0]) note(n, h->why);
    if (h->auth[0]) n->auth = sl_intern(h->auth, strlen(h->auth));
    snprintf(n->obfs, sizeof n->obfs, "%s", h->obfs);
    if (h->obfs_pw[0]) n->obfs_pw = sl_intern(h->obfs_pw, strlen(h->obfs_pw));
    memcpy(n->ports, h->ports, sizeof n->ports);
    n->hop_from = h->hop_from;
    n->hop_to = h->hop_to;
    n->hop_mode = h->hop_mode;
    n->up_mbps = h->up_mbps;
    n->down_mbps = h->down_mbps;
    if (h->qp && h->qp_n) n->qp = hy2_qp_intern(h->qp, h->qp_n);
    if (n->qp && n->qp != HY2_BAD_QP && !strcmp(n->qp, "{}")) n->qp = NULL;
    if (h->first) n->port = h->first;
    if (!n->host[0]) note(n, "address is empty");
    else if (!n->port) note(n, "port is missing");
}
