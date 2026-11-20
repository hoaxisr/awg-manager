#include "yaml_scalar.h"

#include <errno.h>
#include <stdlib.h>
#include <string.h>

static bool in_list(const char *s, const char *const *list, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        if (strcmp(s, list[i]) == 0) {
            return true;
        }
    }
    return false;
}

static const char *const TRUE_WORDS[] = {
    "y", "Y", "yes", "Yes", "YES", "true", "True", "TRUE",
    "on", "On", "ON",
};
static const char *const FALSE_WORDS[] = {
    "n", "N", "no", "No", "NO", "false", "False", "FALSE",
    "off", "Off", "OFF",
};
static const char *const NULL_WORDS[] = {"", "~", "null", "Null", "NULL"};
static const char *const FLOAT_WORDS[] = {
    ".nan", ".NaN", ".NAN", ".inf", ".Inf", ".INF",
    "+.inf", "+.Inf", "+.INF", "-.inf", "-.Inf", "-.INF",
};

#define COUNT(a) (sizeof(a) / sizeof((a)[0]))

/* strips '_' before numeric parsing */
static void strip_underscores(const char *in, char *out, size_t out_len)
{
    size_t w = 0;
    for (const char *p = in; *p != '\0' && w + 1 < out_len; p++) {
        if (*p != '_') {
            out[w++] = *p;
        }
    }
    out[w] = '\0';
}

/* base-0 int parse: 0x/0o/0b/0-octal/decimal */
static bool parse_go_int(const char *s, int64_t *iv, uint64_t *uv,
                         bool *is_uint)
{
    if (*s == '\0') {
        return false;
    }
    errno = 0;
    char *end = NULL;
    long long v = strtoll(s, &end, 0);
    if (end != NULL && *end == '\0' && errno == 0) {
        /* caller already trimmed; "0x" alone leaves "x" unconsumed and is correctly rejected */
        *iv = (int64_t)v;
        *is_uint = false;
        return true;
    }
    /* try unsigned (values > INT64_MAX) */
    if (s[0] == '-') {
        return false;
    }
    errno = 0;
    end = NULL;
    unsigned long long u = strtoull(s, &end, 0);
    if (end != NULL && *end == '\0' && errno == 0) {
        *uv = (uint64_t)u;
        *is_uint = true;
        return true;
    }
    return false;
}

/* yamlStyleFloat: ^[-+]?(\.[0-9]+|[0-9]+(\.[0-9]*)?)([eE][-+]?[0-9]+)?$ */
static bool matches_yaml_style_float(const char *s)
{
    const char *p = s;
    if (*p == '-' || *p == '+') {
        p++;
    }
    bool digits = false;
    if (*p == '.') {
        p++;
        if (*p < '0' || *p > '9') {
            return false;
        }
        while (*p >= '0' && *p <= '9') {
            p++;
        }
        digits = true;
    } else {
        if (*p < '0' || *p > '9') {
            return false;
        }
        while (*p >= '0' && *p <= '9') {
            p++;
        }
        digits = true;
        if (*p == '.') {
            p++;
            while (*p >= '0' && *p <= '9') {
                p++;
            }
        }
    }
    if (!digits) {
        return false;
    }
    if (*p == 'e' || *p == 'E') {
        p++;
        if (*p == '-' || *p == '+') {
            p++;
        }
        if (*p < '0' || *p > '9') {
            return false;
        }
        while (*p >= '0' && *p <= '9') {
            p++;
        }
    }
    return *p == '\0';
}

static bool all_digits(const char *s, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        if (s[i] < '0' || s[i] > '9') {
            return false;
        }
    }
    return n > 0;
}

/* YYYY- prefix then a date/time layout; field ranges validated (month 1-12, hour<24, zone Z or +-HH:MM) */
static bool parse_timestamp(const char *s)
{
    size_t i = 0;
    while (s[i] >= '0' && s[i] <= '9') {
        i++;
    }
    if (i != 4 || s[i] != '-') {
        return false;
    }
    const char *p = s + 5;

    /* month 1-2 digits */
    size_t n = 0;
    while (p[n] >= '0' && p[n] <= '9') {
        n++;
    }
    if (n < 1 || n > 2) {
        return false;
    }
    int month = atoi(p);
    if (month < 1 || month > 12) {
        return false;
    }
    p += n;
    if (*p != '-') {
        return false;
    }
    p++;
    n = 0;
    while (p[n] >= '0' && p[n] <= '9') {
        n++;
    }
    if (n < 1 || n > 2) {
        return false;
    }
    int day = atoi(p);
    if (day < 1 || day > 31) {
        return false;
    }
    p += n;

    if (*p == '\0') {
        return true; /* date only */
    }

    bool t_sep = (*p == 'T' || *p == 't');
    if (t_sep) {
        p++;
    } else if (*p == ' ' || *p == '\t') {
        while (*p == ' ' || *p == '\t') {
            p++;
        }
    } else {
        return false;
    }

    /* time H:M:S */
    int fields[3];
    for (int f = 0; f < 3; f++) {
        n = 0;
        while (p[n] >= '0' && p[n] <= '9') {
            n++;
        }
        if (n < 1 || n > 2) {
            return false;
        }
        fields[f] = atoi(p);
        p += n;
        if (f < 2) {
            if (*p != ':') {
                return false;
            }
            p++;
        }
    }
    if (fields[0] > 23 || fields[1] > 59 || fields[2] > 59) {
        return false;
    }
    if (*p == '.') {
        p++;
        n = 0;
        while (p[n] >= '0' && p[n] <= '9') {
            n++;
        }
        if (n < 1 || n > 9) {
            return false;
        }
        p += n;
    }
    if (t_sep) {
        /* zone required by the RFC3339-style layouts: Z or +-HH:MM */
        if (*p == 'Z') {
            p++;
        } else if (*p == '+' || *p == '-') {
            p++;
            if (!all_digits(p, 2) || p[2] != ':' || !all_digits(p + 3, 2)) {
                return false;
            }
            p += 5;
        } else {
            return false;
        }
    }
    return *p == '\0';
}

firc_scalar_value_t firc_yaml_resolve_plain(const char *s)
{
    firc_scalar_value_t v;
    memset(&v, 0, sizeof(v));
    v.kind = FIRC_SCALAR_STR;

    char first = s[0];
    /* first-byte hints */
    bool hint_map = (first == '\0') || strchr("yYnNtTfFoO~", first) != NULL;
    bool hint_digit = (first >= '0' && first <= '9');
    bool hint_sign = (first == '-' || first == '+');
    bool hint_dot = (first == '.');
    bool hint_special = strchr("<", first) != NULL;

    if (!(hint_map || hint_digit || hint_sign || hint_dot || hint_special)) {
        return v; /* no hint -> plain string */
    }

    /* map lookups (apply for every hinted char) */
    if (in_list(s, TRUE_WORDS, COUNT(TRUE_WORDS))) {
        v.kind = FIRC_SCALAR_BOOL;
        v.b = true;
        return v;
    }
    if (in_list(s, FALSE_WORDS, COUNT(FALSE_WORDS))) {
        v.kind = FIRC_SCALAR_BOOL;
        v.b = false;
        return v;
    }
    if (in_list(s, NULL_WORDS, COUNT(NULL_WORDS))) {
        v.kind = FIRC_SCALAR_NULL;
        return v;
    }
    if (in_list(s, FLOAT_WORDS, COUNT(FLOAT_WORDS))) {
        v.kind = FIRC_SCALAR_FLOAT;
        v.f = strtod(strstr(s, "nan") || strstr(s, "NaN") || strstr(s, "NAN")
                         ? "nan"
                         : (first == '-' ? "-inf" : "inf"),
                     NULL);
        return v;
    }
    if (strcmp(s, "<<") == 0) {
        v.kind = FIRC_SCALAR_MERGE;
        return v;
    }

    if (hint_dot) {
        errno = 0;
        char *end = NULL;
        double f = strtod(s, &end);
        if (end != NULL && *end == '\0' && errno == 0) {
            v.kind = FIRC_SCALAR_FLOAT;
            v.f = f;
        }
        return v;
    }

    if (hint_digit || hint_sign) {
        if (parse_timestamp(s)) {
            v.kind = FIRC_SCALAR_TIMESTAMP;
            return v;
        }
        char plain[128];
        strip_underscores(s, plain, sizeof(plain));
        int64_t iv = 0;
        uint64_t uv = 0;
        bool is_uint = false;
        if (parse_go_int(plain, &iv, &uv, &is_uint)) {
            v.kind = FIRC_SCALAR_INT;
            v.is_uint = is_uint;
            v.i = iv;
            v.u = uv;
            return v;
        }
        if (matches_yaml_style_float(plain)) {
            errno = 0;
            char *end = NULL;
            double f = strtod(plain, &end);
            if (end != NULL && *end == '\0' && errno == 0) {
                v.kind = FIRC_SCALAR_FLOAT;
                v.f = f;
                return v;
            }
        }
        /* glibc strtoll base 0 doesn't accept 0b/-0b; parsed by hand here */
        const char *bs = plain;
        bool neg = false;
        if (*bs == '-') {
            neg = true;
            bs++;
        }
        if (bs[0] == '0' && bs[1] == 'b' && bs[2] != '\0') {
            errno = 0;
            char *end = NULL;
            unsigned long long b = strtoull(bs + 2, &end, 2);
            if (end != NULL && *end == '\0' && errno == 0) {
                v.kind = FIRC_SCALAR_INT;
                if (neg) {
                    v.i = -(int64_t)b;
                } else if (b > INT64_MAX) {
                    v.is_uint = true;
                    v.u = b;
                } else {
                    v.i = (int64_t)b;
                }
                return v;
            }
        }
    }
    return v;
}

bool firc_yaml_is_base60_float(const char *s)
{
    /* ^[-+]?[0-9][0-9_]*(:[0-5]?[0-9])+(\.[0-9_]*)?$ */
    const char *p = s;
    if (*p == '-' || *p == '+') {
        p++;
    }
    if (*p < '0' || *p > '9') {
        return false;
    }
    p++;
    while ((*p >= '0' && *p <= '9') || *p == '_') {
        p++;
    }
    if (*p != ':') {
        return false;
    }
    while (*p == ':') {
        p++;
        if (*p >= '0' && *p <= '5' && p[1] >= '0' && p[1] <= '9') {
            p += 2;
        } else if (*p >= '0' && *p <= '9') {
            p++;
        } else {
            return false;
        }
    }
    if (*p == '.') {
        p++;
        while ((*p >= '0' && *p <= '9') || *p == '_') {
            p++;
        }
    }
    return *p == '\0';
}

bool firc_yaml_string_needs_quote(const char *s)
{
    if (firc_yaml_resolve_plain(s).kind != FIRC_SCALAR_STR) {
        return true;
    }
    return firc_yaml_is_base60_float(s);
}
