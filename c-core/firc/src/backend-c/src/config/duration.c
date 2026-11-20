#include "firc/duration.h"

#include <stdbool.h>
#include <string.h>

#define U64_MAX_DIV10 (UINT64_MAX / 10)

/* overflow bound is 1<<63 (int64 range), not UINT64_MAX */
static firc_err_t leading_int(const char **sp, uint64_t *out)
{
    const char *s = *sp;
    uint64_t x = 0;
    while (*s >= '0' && *s <= '9') {
        if (x > (UINT64_C(1) << 63) / 10) {
            return FIRC_ERR_INVAL;
        }
        x = x * 10 + (uint64_t)(*s - '0');
        if (x > (UINT64_C(1) << 63)) {
            return FIRC_ERR_INVAL;
        }
        s++;
    }
    *sp = s;
    *out = x;
    return FIRC_OK;
}

static void leading_fraction(const char **sp, uint64_t *num, double *scale)
{
    const char *s = *sp;
    uint64_t x = 0;
    double sc = 1.0;
    bool overflow = false;
    while (*s >= '0' && *s <= '9') {
        if (overflow) {
            s++;
            continue;
        }
        if (x > (UINT64_C(1) << 63) / 10) {
            overflow = true;
            s++;
            continue;
        }
        uint64_t y = x * 10 + (uint64_t)(*s - '0');
        if (y > (UINT64_C(1) << 63)) {
            overflow = true;
            s++;
            continue;
        }
        x = y;
        sc *= 10;
        s++;
    }
    *sp = s;
    *num = x;
    *scale = sc;
}

struct unit_entry {
    const char *name;
    uint64_t nanos;
};

static const struct unit_entry UNITS[] = {
    {"ns", 1},
    {"us", 1000},
    {"\xc2\xb5s", 1000},     /* µs U+00B5 */
    {"\xce\xbcs", 1000},     /* μs U+03BC */
    {"ms", 1000000},
    {"s", 1000000000},
    {"m", UINT64_C(60000000000)},
    {"h", UINT64_C(3600000000000)},
};

static bool lookup_unit(const char *s, size_t len, uint64_t *nanos)
{
    for (size_t i = 0; i < sizeof(UNITS) / sizeof(UNITS[0]); i++) {
        if (strlen(UNITS[i].name) == len &&
            memcmp(UNITS[i].name, s, len) == 0) {
            *nanos = UNITS[i].nanos;
            return true;
        }
    }
    return false;
}

firc_err_t firc_duration_parse(const char *s, firc_duration_t *out)
{
    bool neg = false;
    uint64_t d = 0;

    if (*s == '-' || *s == '+') {
        neg = (*s == '-');
        s++;
    }
    if (strcmp(s, "0") == 0) {
        *out = 0;
        return FIRC_OK;
    }
    if (*s == '\0') {
        return FIRC_ERR_INVAL;
    }
    while (*s != '\0') {
        uint64_t v = 0;
        uint64_t f = 0;
        double scale = 1.0;

        if (!(*s == '.' || (*s >= '0' && *s <= '9'))) {
            return FIRC_ERR_INVAL;
        }
        const char *pl = s;
        if (leading_int(&s, &v) != FIRC_OK) {
            return FIRC_ERR_INVAL;
        }
        bool pre = (pl != s);

        bool post = false;
        if (*s == '.') {
            s++;
            const char *pf = s;
            leading_fraction(&s, &f, &scale);
            post = (pf != s);
        }
        if (!pre && !post) {
            return FIRC_ERR_INVAL;
        }

        const char *u = s;
        while (*s != '\0' && *s != '.' && !(*s >= '0' && *s <= '9')) {
            s++;
        }
        if (u == s) {
            return FIRC_ERR_INVAL;
        }
        uint64_t unit = 0;
        if (!lookup_unit(u, (size_t)(s - u), &unit)) {
            return FIRC_ERR_INVAL;
        }

        if (v > ((UINT64_C(1) << 63) - 1) / unit) {
            return FIRC_ERR_INVAL;
        }
        v *= unit;
        if (f > 0) {
            v += (uint64_t)((double)f * ((double)unit / scale));
            if (v > (UINT64_C(1) << 63)) {
                return FIRC_ERR_INVAL;
            }
        }
        d += v;
        if (d > (UINT64_C(1) << 63)) {
            return FIRC_ERR_INVAL;
        }
    }

    if (neg) {
        *out = (firc_duration_t)(-(int64_t)d); /* INT64_MIN via unsigned negation */
        return FIRC_OK;
    }
    if (d > (UINT64_C(1) << 63) - 1) {
        return FIRC_ERR_INVAL;
    }
    *out = (firc_duration_t)d;
    return FIRC_OK;
}

/* writes digits backwards from position w, trailing zeros dropped */
static size_t fmt_frac(char *buf, size_t w, uint64_t v, int prec,
                       uint64_t *nv)
{
    bool print = false;
    for (int i = 0; i < prec; i++) {
        uint64_t digit = v % 10;
        print = print || digit != 0;
        if (print) {
            w--;
            buf[w] = (char)('0' + digit);
        }
        v /= 10;
    }
    if (print) {
        w--;
        buf[w] = '.';
    }
    *nv = v;
    return w;
}

static size_t fmt_int(char *buf, size_t w, uint64_t v)
{
    if (v == 0) {
        w--;
        buf[w] = '0';
    } else {
        while (v > 0) {
            w--;
            buf[w] = (char)('0' + v % 10);
            v /= 10;
        }
    }
    return w;
}

void firc_duration_format(firc_duration_t d, char *out, size_t out_len)
{
    char buf[32];
    size_t w = sizeof(buf);
    uint64_t u;
    bool neg = d < 0;
    if (neg) {
        u = (uint64_t)(-(d + 1)) + 1; /* handles INT64_MIN */
    } else {
        u = (uint64_t)d;
    }

    if (u < (uint64_t)FIRC_DURATION_SEC) {
        int prec;
        w--;
        buf[w] = 's';
        if (u == 0) {
            if (out_len >= 3) {
                memcpy(out, "0s", 3);
            }
            return;
        } else if (u < 1000) {
            prec = 0;
            w--;
            buf[w] = 'n';
        } else if (u < 1000000) {
            prec = 3;
            /* U+00B5 'µ' */
            w--;
            buf[w] = '\xb5';
            w--;
            buf[w] = '\xc2';
        } else {
            prec = 6;
            w--;
            buf[w] = 'm';
        }
        w = fmt_frac(buf, w, u, prec, &u);
        w = fmt_int(buf, w, u);
    } else {
        w--;
        buf[w] = 's';
        w = fmt_frac(buf, w, u, 9, &u);
        w = fmt_int(buf, w, u % 60);
        u /= 60;
        if (u > 0) {
            w--;
            buf[w] = 'm';
            w = fmt_int(buf, w, u % 60);
            u /= 60;
            if (u > 0) {
                w--;
                buf[w] = 'h';
                w = fmt_int(buf, w, u);
            }
        }
    }
    if (neg) {
        w--;
        buf[w] = '-';
    }

    size_t len = sizeof(buf) - w;
    if (len + 1 <= out_len) {
        memcpy(out, buf + w, len);
        out[len] = '\0';
    } else if (out_len > 0) {
        out[0] = '\0';
    }
}
