/* awgm-warpscout: tiny JSON DOM parser (enough for the stdin config). */
#include "json.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
    const char *p, *end;
    const char *err;
    int depth;
} jp;

static void ws(jp *s) {
    while (s->p < s->end && (*s->p == ' ' || *s->p == '\t' || *s->p == '\n' || *s->p == '\r')) s->p++;
}

static int parse_value(jp *s, jval *v);

static int hex4(const char *p, unsigned *out) {
    unsigned v = 0;
    for (int i = 0; i < 4; i++) {
        char c = p[i];
        v <<= 4;
        if (c >= '0' && c <= '9') v |= (unsigned)(c - '0');
        else if (c >= 'a' && c <= 'f') v |= (unsigned)(c - 'a' + 10);
        else if (c >= 'A' && c <= 'F') v |= (unsigned)(c - 'A' + 10);
        else return -1;
    }
    *out = v;
    return 0;
}

static char *parse_string(jp *s) {
    if (s->p >= s->end || *s->p != '"') { s->err = "expected string"; return NULL; }
    s->p++;
    size_t cap = 16, n = 0;
    char *o = (char *)malloc(cap);
    if (!o) { s->err = "oom"; return NULL; }
    while (s->p < s->end && *s->p != '"') {
        unsigned c = (unsigned char)*s->p++;
        if (c < 0x20) { s->err = "control char in string"; free(o); return NULL; }
        if (n + 5 >= cap) {
            cap *= 2;
            char *no = (char *)realloc(o, cap);
            if (!no) { free(o); s->err = "oom"; return NULL; }
            o = no;
        }
        if (c != '\\') { o[n++] = (char)c; continue; }
        if (s->p >= s->end) break;
        c = (unsigned char)*s->p++;
        switch (c) {
        case '"': o[n++] = '"'; break;
        case '\\': o[n++] = '\\'; break;
        case '/': o[n++] = '/'; break;
        case 'b': o[n++] = '\b'; break;
        case 'f': o[n++] = '\f'; break;
        case 'n': o[n++] = '\n'; break;
        case 'r': o[n++] = '\r'; break;
        case 't': o[n++] = '\t'; break;
        case 'u': {
            unsigned u;
            if (s->end - s->p < 4 || hex4(s->p, &u) != 0) { s->err = "bad \\u escape"; free(o); return NULL; }
            s->p += 4;
            /* строки DOM NUL-терминированы: U+0000 молча обрезал бы значение */
            if (u == 0) { s->err = "\\u0000 not supported"; free(o); return NULL; }
            if (u >= 0xD800 && u <= 0xDBFF && s->end - s->p >= 6 && s->p[0] == '\\' && s->p[1] == 'u') {
                unsigned lo;
                if (hex4(s->p + 2, &lo) == 0 && lo >= 0xDC00 && lo <= 0xDFFF) {
                    u = 0x10000 + ((u - 0xD800) << 10) + (lo - 0xDC00);
                    s->p += 6;
                }
            }
            if (u < 0x80) o[n++] = (char)u;
            else if (u < 0x800) { o[n++] = (char)(0xC0 | (u >> 6)); o[n++] = (char)(0x80 | (u & 63)); }
            else if (u < 0x10000) {
                o[n++] = (char)(0xE0 | (u >> 12)); o[n++] = (char)(0x80 | ((u >> 6) & 63));
                o[n++] = (char)(0x80 | (u & 63));
            } else {
                o[n++] = (char)(0xF0 | (u >> 18)); o[n++] = (char)(0x80 | ((u >> 12) & 63));
                o[n++] = (char)(0x80 | ((u >> 6) & 63)); o[n++] = (char)(0x80 | (u & 63));
            }
            break;
        }
        default: s->err = "bad escape"; free(o); return NULL;
        }
    }
    if (s->p >= s->end) { s->err = "unterminated string"; free(o); return NULL; }
    s->p++;
    o[n] = 0;
    return o;
}

static int push(jval *v, int *cap) {
    if (v->n == *cap) {
        int nc = *cap ? *cap * 2 : 4;
        jval *ni = (jval *)realloc(v->items, (size_t)nc * sizeof(jval));
        if (!ni) return -1;
        v->items = ni;
        if (v->t == J_OBJ) {
            char **nk = (char **)realloc(v->keys, (size_t)nc * sizeof(char *));
            if (!nk) return -1;
            v->keys = nk;
        }
        *cap = nc;
    }
    memset(&v->items[v->n], 0, sizeof(jval));
    return 0;
}

static int parse_value(jp *s, jval *v) {
    memset(v, 0, sizeof(*v));
    ws(s);
    if (s->p >= s->end) { s->err = "unexpected end"; return -1; }
    if (++s->depth > 32) { s->err = "nesting too deep"; return -1; }
    char c = *s->p;
    int rc = 0;
    if (c == '{' || c == '[') {
        char close = c == '{' ? '}' : ']';
        int cap = 0;
        v->t = c == '{' ? J_OBJ : J_ARR;
        s->p++;
        ws(s);
        if (s->p < s->end && *s->p == close) { s->p++; goto out; }
        for (;;) {
            char *key = NULL;
            ws(s);
            if (v->t == J_OBJ) {
                key = parse_string(s);
                if (!key) { rc = -1; goto out; }
                ws(s);
                if (s->p >= s->end || *s->p != ':') { free(key); s->err = "expected ':'"; rc = -1; goto out; }
                s->p++;
            }
            if (push(v, &cap) != 0) { free(key); s->err = "oom"; rc = -1; goto out; }
            if (v->t == J_OBJ) v->keys[v->n] = key;
            v->n++;
            if (parse_value(s, &v->items[v->n - 1]) != 0) { rc = -1; goto out; }
            ws(s);
            if (s->p < s->end && *s->p == ',') { s->p++; continue; }
            if (s->p < s->end && *s->p == close) { s->p++; break; }
            s->err = "expected ',' or closing bracket";
            rc = -1;
            goto out;
        }
    } else if (c == '"') {
        v->t = J_STR;
        v->str = parse_string(s);
        if (!v->str) rc = -1;
    } else if (c == 't' && s->end - s->p >= 4 && !memcmp(s->p, "true", 4)) {
        v->t = J_BOOL; v->b = 1; s->p += 4;
    } else if (c == 'f' && s->end - s->p >= 5 && !memcmp(s->p, "false", 5)) {
        v->t = J_BOOL; v->b = 0; s->p += 5;
    } else if (c == 'n' && s->end - s->p >= 4 && !memcmp(s->p, "null", 4)) {
        v->t = J_NULL; s->p += 4;
    } else if (c == '-' || (c >= '0' && c <= '9')) {
        char buf[64];
        size_t n = 0;
        while (s->p < s->end && n < sizeof(buf) - 1 &&
               ((*s->p && strchr("+-.eE", *s->p)) || (*s->p >= '0' && *s->p <= '9')))
            buf[n++] = *s->p++;
        buf[n] = 0;
        char *e;
        v->t = J_NUM;
        v->num = strtod(buf, &e);
        if (*e) { s->err = "bad number"; rc = -1; }
    } else {
        s->err = "unexpected character";
        rc = -1;
    }
out:
    s->depth--;
    return rc;
}

int json_parse(const char *text, size_t len, jval *out, char *err, size_t errlen) {
    jp s = {text, text + len, NULL, 0};
    if (len >= 3 && (unsigned char)text[0] == 0xEF && (unsigned char)text[1] == 0xBB && (unsigned char)text[2] == 0xBF)
        s.p += 3;
    if (parse_value(&s, out) != 0) {
        snprintf(err, errlen, "json: %s at offset %ld", s.err ? s.err : "error", (long)(s.p - text));
        json_free(out);
        return -1;
    }
    ws(&s);
    if (s.p != s.end) {
        snprintf(err, errlen, "json: trailing data at offset %ld", (long)(s.p - text));
        json_free(out);
        return -1;
    }
    return 0;
}

void json_free(jval *v) {
    if (!v) return;
    for (int i = 0; i < v->n; i++) {
        json_free(&v->items[i]);
        if (v->keys) free(v->keys[i]);
    }
    free(v->items);
    free(v->keys);
    free(v->str);
    memset(v, 0, sizeof(*v));
}

const jval *json_get(const jval *obj, const char *key) {
    if (!obj || obj->t != J_OBJ) return NULL;
    for (int i = 0; i < obj->n; i++)
        if (obj->keys[i] && strcmp(obj->keys[i], key) == 0) return &obj->items[i];
    return NULL;
}
