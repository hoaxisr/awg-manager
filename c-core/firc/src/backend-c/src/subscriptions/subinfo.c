#include "firc/subinfo.h"

#include <inttypes.h>
#include <stdio.h>
#include <string.h>
#include <strings.h>

#define BODY_LINES_MAX 32

static bool space(char c) { return c == ' ' || c == '\t'; }

static bool number(const char *s, size_t n, uint64_t *out)
{
    uint64_t v = 0;
    size_t i = 0;
    for (; i < n && s[i] >= '0' && s[i] <= '9'; i++) {
        unsigned d = (unsigned)(s[i] - '0');
        if (v > (UINT64_MAX - d) / 10) {
            return false;
        }
        v = v * 10 + d;
    }
    if (i == 0) {
        return false;
    }
    if (i < n && s[i] == '.') {
        for (i++; i < n && s[i] >= '0' && s[i] <= '9'; i++) {
        }
    }
    if (i != n) {
        return false;
    }
    *out = v;
    return true;
}

bool firc_subinfo_parse(const char *value, firc_subinfo_t *out)
{
    memset(out, 0, sizeof *out);
    if (value == NULL) {
        return false;
    }
    bool any = false;
    const char *p = value;
    while (*p != 0) {
        const char *end = strchr(p, ';');
        size_t n = end != NULL ? (size_t)(end - p) : strlen(p);
        const char *eq = memchr(p, '=', n);
        if (eq != NULL) {
            const char *k = p, *ke = eq, *v = eq + 1, *ve = p + n;
            while (k < ke && space(*k)) k++;
            while (ke > k && space(ke[-1])) ke--;
            while (v < ve && space(*v)) v++;
            while (ve > v && space(ve[-1])) ve--;
            size_t kn = (size_t)(ke - k);
            uint64_t x = 0;
            uint64_t *slot = NULL;
            uint64_t expire = 0;
            if (kn == 6 && strncasecmp(k, "upload", 6) == 0) {
                slot = &out->upload;
            } else if (kn == 8 && strncasecmp(k, "download", 8) == 0) {
                slot = &out->download;
            } else if (kn == 5 && strncasecmp(k, "total", 5) == 0) {
                slot = &out->total;
            } else if (kn == 6 && strncasecmp(k, "expire", 6) == 0) {
                slot = &expire;
            }
            if (slot != NULL) {
                if (!number(v, (size_t)(ve - v), &x) || (slot == &expire && x > INT64_MAX)) {
                    memset(out, 0, sizeof *out);
                    return false;
                }
                *slot = x;
                if (slot == &expire) {
                    out->expire = (int64_t)x;
                }
                any = true;
            }
        }
        p += n;
        if (*p == ';') {
            p++;
        }
    }
    out->have = any;
    return any;
}

bool firc_subinfo_from_body(const char *body, size_t len, firc_subinfo_t *out)
{
    memset(out, 0, sizeof *out);
    size_t at = 0;
    if (len >= 3 && memcmp(body, "\xEF\xBB\xBF", 3) == 0) {
        at = 3;
    }
    for (int line = 0; line < BODY_LINES_MAX && at < len; line++) {
        const char *s = body + at;
        const char *nl = memchr(s, '\n', len - at);
        size_t n = nl != NULL ? (size_t)(nl - s) : len - at;
        at += n + 1;
        while (n > 0 && (s[n - 1] == '\r' || space(s[n - 1]))) n--;
        size_t i = 0;
        while (i < n && space(s[i])) i++;
        if (i == n) {
            continue;
        }
        if (s[i] != '#') {
            return false;
        }
        for (i++; i < n && space(s[i]); i++) {
        }
        size_t hn = sizeof FIRC_SUBINFO_HEADER - 1;
        if (n - i <= hn || strncasecmp(s + i, FIRC_SUBINFO_HEADER, hn) != 0) {
            continue;
        }
        i += hn;
        while (i < n && space(s[i])) i++;
        if (i == n || s[i] != ':') {
            continue;
        }
        char value[FIRC_SUBINFO_LINE_MAX + 1];
        size_t vn = n - i - 1;
        if (vn > FIRC_SUBINFO_LINE_MAX) {
            return false;
        }
        memcpy(value, s + i + 1, vn);
        value[vn] = 0;
        return firc_subinfo_parse(value, out);
    }
    return false;
}

void firc_subinfo_format(const firc_subinfo_t *info, char *buf, size_t cap)
{
    snprintf(buf, cap, "upload=%" PRIu64 "; download=%" PRIu64 "; total=%" PRIu64 "; expire=%" PRId64, info->upload,
             info->download, info->total, info->expire);
}
