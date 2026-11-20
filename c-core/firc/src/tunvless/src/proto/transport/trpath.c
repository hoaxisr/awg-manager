/* The Upgrade request path of ws and httpupgrade, as Xray builds it. Why, and what is accepted:
 * trpath.h.
 *
 * This repeats four pieces of Go's net/url (1.22; newer releases have the same logic):
 * shouldEscape for paths and query values, unescape, validEncoded with EscapedPath, and
 * url.Values.Encode. The comments below name the Go functions, for a line-by-line comparison.
 * Only the branches a node's path goes through are repeated: host, user info, fragment and opaque
 * URLs never get here, because such a path is rejected first (trpath.h).
 *
 * Verified against captured requests of a real Xray 26.3.27 (client in docker, server a listening
 * socket): the path `/p/q?x=1&ed=2048` gives `GET /p/q?x=1` for ws and `GET /p/q%3Fx=1` for
 * httpupgrade. The other cases (query re-sorted by key, `+` for a space, percent sequences, a path
 * without a leading slash) are covered by tests/wsmatch.c against the output of Go 1.22 net/url
 * on the same inputs. */
#include <string.h>
#include <stdint.h>

#include "trpath.h"

static const char HEX[] = "0123456789ABCDEF";

static int is_alnum(unsigned char c) {
    return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9');
}

static int hexval(unsigned char c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* shouldEscape(c, encodePath). Of the reserved characters only `?` is escaped in a path: RFC 3986
 * allows `: @ & = + $` there, and Go also leaves `/ ; ,` alone because it handles the path as a
 * whole. Everything else except letters, digits and `- _ . ~` is escaped (including
 * `! ' ( ) * [ ]` and all non-ASCII). */
static int esc_path(unsigned char c) {
    if (is_alnum(c)) return 0;
    switch (c) {
    case '-': case '_': case '.': case '~':
    case '$': case '&': case '+': case ',': case '/': case ':': case ';': case '=': case '@':
        return 0;
    default:
        return 1;
    }
}

/* shouldEscape(c, encodeQueryComponent): in a query value everything except letters, digits and
 * `- _ . ~` is escaped (a space is special: it becomes `+`, see put_qesc). */
static int esc_qc(unsigned char c) {
    if (is_alnum(c)) return 0;
    return !(c == '-' || c == '_' || c == '.' || c == '~');
}

struct sb { char *p; size_t n, cap; int over; };

static void put_c(struct sb *b, char c) {
    if (b->n + 1 < b->cap) b->p[b->n++] = c;
    else b->over = 1;
}

static void put_s(struct sb *b, const char *s, size_t n) {
    for (size_t i = 0; i < n; i++) put_c(b, s[i]);
}

static void put_pct(struct sb *b, unsigned char c) {
    put_c(b, '%');
    put_c(b, HEX[c >> 4]);
    put_c(b, HEX[c & 15]);
}

/* unescape(s, mode). qc — query value mode: `+` reads as a space. Returns the decoded length, or
 * -1 for an incomplete or non-hex sequence (a parse error in Go). */
static long unesc(const char *s, size_t n, char *out, size_t cap, int qc) {
    size_t o = 0;
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)s[i];
        if (c == '%') {
            if (i + 2 >= n) return -1;
            int h = hexval((unsigned char)s[i + 1]), l = hexval((unsigned char)s[i + 2]);
            if (h < 0 || l < 0) return -1;
            c = (unsigned char)(h * 16 + l);
            i += 2;
        } else if (qc && c == '+') {
            c = ' ';
        }
        if (o + 1 > cap) return -1;
        out[o++] = (char)c;
    }
    return (long)o;
}

/* EscapedPath of a path parsed by url.Parse: if the original is a valid encoding (validEncoded),
 * it is printed as is; otherwise the path is decoded and encoded again. So `%41` stays `%41` and a
 * space becomes `%20`. A path with an incomplete sequence never gets here: tr_upgrade_target
 * rejects it. */
static int escaped_path(struct sb *b, const char *p, size_t n) {
    int valid = 1;
    for (size_t i = 0; i < n && valid; i++) {
        unsigned char c = (unsigned char)p[i];
        switch (c) {
        case '!': case '$': case '&': case '\'': case '(': case ')': case '*': case '+':
        case ',': case ';': case '=': case ':': case '@': case '[': case ']': case '%':
            break;
        default:
            if (esc_path(c)) valid = 0;
        }
    }
    if (valid) { put_s(b, p, n); return 0; }
    char tmp[1024];
    long tn = unesc(p, n, tmp, sizeof(tmp), 0);
    if (tn < 0) return -1;
    for (long k = 0; k < tn; k++) {
        unsigned char c = (unsigned char)tmp[k];
        if (esc_path(c)) put_pct(b, c);
        else put_c(b, (char)c);
    }
    return 0;
}

/* QueryEscape. */
static void put_qesc(struct sb *b, const char *s, size_t n) {
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)s[i];
        if (c == ' ') put_c(b, '+');
        else if (esc_qc(c)) put_pct(b, c);
        else put_c(b, (char)c);
    }
}

/* strip_ed cuts early data out of the query, as Xray's Build does:
 *
 *   if q := u.Query(); q.Get("ed") != "" { q.Del("ed"); u.RawQuery = q.Encode() }
 *
 * u.Query() is ParseQuery with the error swallowed: pairs are split on `&`, a pair with `;` or
 * with a bad percent sequence is skipped, key and value are decoded (`+` is a space). Get takes
 * the FIRST `ed` value, so an empty one (`?ed=`, even followed by `ed=5`) leaves the query alone.
 * Encode prints keys sorted bytewise, values of one key in order of appearance, each through
 * QueryEscape. *stripped — whether `ed` was cut; then out holds the new query (possibly empty).
 * More than QP_MAX pairs fail the call (reported as "path too long"). */
#define QP_MAX 64

/* Ed as Build computes it: `Ed, _ := strconv.Atoi(q.Get("ed")); ed = uint32(Ed)`. Atoi takes a
 * sign and digits, nothing else; a syntax error (letters, nothing after the sign) gives 0. Out of
 * range, Atoi clamps to the limits of int, which is 32-bit on mips and arm and 64-bit on arm64 and
 * x86_64. This code follows a 64-bit int and does not clamp; both differences only touch numbers
 * beyond ±2^31, which links never carry. A negative value becomes a huge uint32, as in Xray:
 * `ed=-1` means early data for a first write of any length. */
static uint32_t go_atoi_u32(const char *s, size_t n) {
    size_t i = 0;
    int neg = 0;
    if (i < n && (s[i] == '+' || s[i] == '-')) { neg = s[i] == '-'; i++; }
    if (i == n) return 0;
    uint64_t v = 0;
    for (; i < n; i++) {
        if (s[i] < '0' || s[i] > '9') return 0;
        if (v > (UINT64_MAX - 9) / 10) return 0;     /* the next digit would wrap uint64 */
        v = v * 10 + (uint64_t)(s[i] - '0');
        if (v > (uint64_t)INT64_MAX + (uint64_t)neg) return 0;
    }
    int64_t sv = neg ? (int64_t)(0 - v) : (int64_t)v;
    return (uint32_t)sv;
}

static int strip_ed(const char *q, size_t qn, struct sb *out, int *stripped, uint32_t *ed) {
    struct { size_t ko, kl, vo, vl; } pr[QP_MAX];
    size_t np = 0;
    char dec[1024];
    size_t dn = 0;
    *stripped = 0;

    size_t i = 0;
    while (i <= qn) {
        const char *s = q + i;
        const char *amp = memchr(s, '&', qn - i);
        size_t sl = amp ? (size_t)(amp - s) : qn - i;
        i += sl + 1;
        if (!sl || memchr(s, ';', sl)) continue;
        const char *eq = memchr(s, '=', sl);
        size_t kl = eq ? (size_t)(eq - s) : sl;
        if (np >= QP_MAX) return -1;
        long k = unesc(s, kl, dec + dn, sizeof(dec) - dn, 1);
        if (k < 0) continue;
        long v = eq ? unesc(eq + 1, sl - kl - 1, dec + dn + (size_t)k, sizeof(dec) - dn - (size_t)k, 1) : 0;
        if (v < 0) continue;
        pr[np].ko = dn; pr[np].kl = (size_t)k;
        pr[np].vo = dn + (size_t)k; pr[np].vl = (size_t)v;
        dn += (size_t)k + (size_t)v;
        np++;
    }

    size_t first_ed = np;
    for (size_t a = 0; a < np; a++)
        if (pr[a].kl == 2 && !memcmp(dec + pr[a].ko, "ed", 2)) { first_ed = a; break; }
    if (first_ed == np || pr[first_ed].vl == 0) return 0;
    *ed = go_atoi_u32(dec + pr[first_ed].vo, pr[first_ed].vl);

    /* Stable insertion sort by key: there are only a few pairs, and stability is Encode's rule
     * (values of one key stay in order of appearance). */
    size_t ord[QP_MAX], no = 0;
    for (size_t a = 0; a < np; a++) {
        if (pr[a].kl == 2 && !memcmp(dec + pr[a].ko, "ed", 2)) continue;
        size_t j = no++;
        while (j > 0) {
            size_t p = ord[j - 1];
            size_t m = pr[p].kl < pr[a].kl ? pr[p].kl : pr[a].kl;
            int c = memcmp(dec + pr[p].ko, dec + pr[a].ko, m);
            if (c < 0 || (c == 0 && pr[p].kl <= pr[a].kl)) break;
            ord[j] = p;
            j--;
        }
        ord[j] = a;
    }
    for (size_t j = 0; j < no; j++) {
        size_t a = ord[j];
        if (j) put_c(out, '&');
        put_qesc(out, dec + pr[a].ko, pr[a].kl);
        put_c(out, '=');
        put_qesc(out, dec + pr[a].vo, pr[a].vl);
    }
    *stripped = 1;
    return 0;
}

int tr_upgrade_target(const char *path, int ws, char *out, size_t cap, const char **why) {
    return tr_upgrade_target_ed(path, ws, out, cap, why, NULL);
}

int tr_upgrade_target_ed(const char *path, int ws, char *out, size_t cap, const char **why,
                         uint32_t *ed_out) {
    const char *dummy;
    uint32_t ed_dummy;
    if (!why) why = &dummy;
    if (!ed_out) ed_out = &ed_dummy;
    *why = "";
    *ed_out = 0;
    size_t n = strlen(path);

    /* Whatever Xray would not parse unambiguously is refused with a reason (see trpath.h). */
    for (size_t i = 0; i < n; i++) {
        unsigned char c = (unsigned char)path[i];
        if (c < 0x20 || c == 0x7f) { *why = "control character in path"; return -1; }
        if (c == '#') { *why = "# in path"; return -1; }
    }
    if (n >= 2 && path[0] == '/' && path[1] == '/') { *why = "path starts with //"; return -1; }
    const char *qm = memchr(path, '?', n);
    size_t pn = qm ? (size_t)(qm - path) : n;
    if (n && path[0] != '/') {
        const char *sl = memchr(path, '/', pn);
        size_t seg = sl ? (size_t)(sl - path) : pn;
        if (memchr(path, ':', seg)) { *why = "path has : before its first /"; return -1; }
    }
    char tmp[1024];
    int path_ok = unesc(path, pn, tmp, sizeof(tmp), 0) >= 0;
    /* For ws, gorilla's url.Parse parses the path again and fails on an incomplete sequence, so
     * Xray would never open such a node. httpupgrade has no second parse: the whole path goes out
     * escaped, `%zz` becomes `%25zz`, and the node works. */
    if (!path_ok && ws) { *why = "bad %XX in path"; return -1; }

    /* Build: early data. If the path does not parse, it stays as is, as in Xray (where a
     * url.Parse error silently keeps the string). */
    char p1[1024];
    struct sb b1 = { p1, 0, sizeof(p1), 0 };
    int stripped = 0;
    if (path_ok && qm) {
        char qb[1024];
        struct sb bq = { qb, 0, sizeof(qb), 0 };
        if (strip_ed(qm + 1, n - pn - 1, &bq, &stripped, ed_out) != 0 || bq.over) {
            *why = "path too long";
            return -1;
        }
        if (stripped) {
            if (escaped_path(&b1, path, pn) != 0) { *why = "bad %XX in path"; return -1; }
            if (bq.n) { put_c(&b1, '?'); put_s(&b1, qb, bq.n); }
        }
    }
    if (!stripped) put_s(&b1, path, n);

    /* GetNormalizedPath: empty becomes "/", a missing leading slash is added. */
    char p2[1040];
    struct sb b2 = { p2, 0, sizeof(p2), 0 };
    if (!b1.n || p1[0] != '/') put_c(&b2, '/');
    put_s(&b2, p1, b1.n);
    if (b1.over || b2.over) { *why = "path too long"; return -1; }

    struct sb o = { out, 0, cap, 0 };
    if (ws) {
        /* gorilla: url.Parse("ws://host" + path), then RequestURI: EscapedPath plus the query
         * as is. */
        const char *q2 = memchr(p2, '?', b2.n);
        size_t pn2 = q2 ? (size_t)(q2 - p2) : b2.n;
        if (escaped_path(&o, p2, pn2) != 0) { *why = "bad %XX in path"; return -1; }
        if (q2) put_s(&o, q2, b2.n - pn2);
    } else {
        /* httpupgrade: URL.Path = the whole path, RequestURI = escape(Path, encodePath). */
        for (size_t i = 0; i < b2.n; i++) {
            unsigned char c = (unsigned char)p2[i];
            if (esc_path(c)) put_pct(&o, c);
            else put_c(&o, (char)c);
        }
    }
    if (o.over || !cap) { *why = "path too long"; return -1; }
    out[o.n] = '\0';
    return 0;
}
