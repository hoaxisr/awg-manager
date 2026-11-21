#define PCRE2_CODE_UNIT_WIDTH 8

#include "firc/tunnels.h"
#include "vless.h"

#include <arpa/inet.h>
#include <errno.h>
#include <pcre2.h>
#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <yaml.h>

#include "firc/atomic_write.h"
#include "firc/duration.h"
#include "firc/ifacename.h"
#include "firc/log.h"
#include "firc/hash.h"
#include "firc/tunvless_name.h"
#include "firc/utf8.h"

#define FILE_CAP (4u * 1024u * 1024u)

typedef yaml_node_t node_t;

typedef struct {
    yaml_document_t *doc;
    firc_tun_err_t *err;
} ctx_t;

static firc_err_t fail(ctx_t *c, const char *why, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

static firc_err_t fail(ctx_t *c, const char *why, const char *fmt, ...)
{
    if (c->err != NULL) {
        va_list ap;
        va_start(ap, fmt);
        vsnprintf(c->err->where, sizeof c->err->where, fmt, ap);
        va_end(ap);
        snprintf(c->err->why, sizeof c->err->why, "%s", why);
    }
    return FIRC_ERR_INVAL;
}

static node_t *at(ctx_t *c, yaml_node_item_t i)
{
    return yaml_document_get_node(c->doc, i);
}

static const char *text(const node_t *n)
{
    return (const char *)n->data.scalar.value;
}

static bool is_scalar(const node_t *n)
{
    return n != NULL && n->type == YAML_SCALAR_NODE;
}

static size_t seq_len(const node_t *n)
{
    return (size_t)(n->data.sequence.items.top - n->data.sequence.items.start);
}

static bool parse_bool(const node_t *n, bool *out)
{
    if (!is_scalar(n)) {
        return false;
    }
    if (strcmp(text(n), "true") == 0) {
        *out = true;
        return true;
    }
    if (strcmp(text(n), "false") == 0) {
        *out = false;
        return true;
    }
    return false;
}

static bool parse_int(const node_t *n, int *out)
{
    if (!is_scalar(n) || text(n)[0] == '\0') {
        return false;
    }
    char *end = NULL;
    errno = 0;
    long v = strtol(text(n), &end, 10);
    if (errno != 0 || *end != '\0' || v < INT32_MIN || v > INT32_MAX) {
        return false;
    }
    *out = (int)v;
    return true;
}

static bool parse_secs(const node_t *n, int *out, int lo, int hi)
{
    firc_duration_t d;
    if (!is_scalar(n) || firc_duration_parse(text(n), &d) != FIRC_OK || d % FIRC_DURATION_SEC != 0) {
        return false;
    }
    int64_t s = d / FIRC_DURATION_SEC;
    if (s < lo || s > hi) {
        return false;
    }
    *out = (int)s;
    return true;
}

static bool copy_str(const node_t *n, char *dst, size_t cap)
{
    if (!is_scalar(n) || strlen(text(n)) >= cap) {
        return false;
    }
    strcpy(dst, text(n));
    return true;
}

static bool id_ok(const char *s)
{
    if (s[0] == '\0') {
        return false;
    }
    for (; *s != '\0'; s++) {
        unsigned char ch = (unsigned char)*s;
        bool ok = (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') ||
                  ch == '_' || ch == '-';
        if (!ok) {
            return false;
        }
    }
    return true;
}

static bool key_echoable(const char *k)
{
    return strlen(k) <= 32 && id_ok(k);
}

static firc_err_t bad_key(ctx_t *c, const node_t *k, size_t ti, const char *section, const char *why)
{
    if (is_scalar(k) && key_echoable(text(k))) {
        return fail(c, why, "tunnels[%zu]%s.%s", ti, section, text(k));
    }
    return fail(c, "unknown key", "tunnels[%zu]%s", ti, section);
}

static bool key_seen(ctx_t *c, const node_t *m, const yaml_node_pair_t *p)
{
    const node_t *k = at(c, p->key);
    for (const yaml_node_pair_t *q = m->data.mapping.pairs.start; q < p; q++) {
        const node_t *o = at(c, q->key);
        if (is_scalar(o) && is_scalar(k) && strcmp(text(o), text(k)) == 0) {
            return true;
        }
    }
    return false;
}

static bool link_ok(const char *s)
{
    size_t scheme = vless_link_scheme(s);
    if (scheme == 0 || s[scheme] == '\0') {
        return false;
    }
    for (; *s != '\0'; s++) {
        unsigned char ch = (unsigned char)*s;
        if (ch <= 0x20 || ch == 0x7f) {
            return false;
        }
    }
    return true;
}

void firc_tunnels_free(firc_tunnels_t *t)
{
    if (t == NULL) {
        return;
    }
    for (size_t i = 0; i < t->n; i++) {
        firc_tunnel_t *x = &t->t[i];
        for (size_t j = 0; j < x->n_src; j++) {
            free(x->src[j].link);
            free(x->src[j].sub.url);
            for (size_t k = 0; k < x->src[j].sub.n_headers; k++) {
                free(x->src[j].sub.headers[k].name);
                free(x->src[j].sub.headers[k].value);
            }
            free(x->src[j].sub.headers);
        }
        free(x->src);
        free(x->filter);
        free(x->filter_exclude);
        for (size_t j = 0; j < x->n_order; j++) {
            free(x->order[j]);
        }
        free(x->order);
        for (size_t j = 0; j < x->n_exclude; j++) {
            free(x->exclude[j]);
        }
        free(x->exclude);
    }
    free(t->t);
    memset(t, 0, sizeof *t);
}

static bool hex8_ok(const char *s)
{
    if (strlen(s) != 8) {
        return false;
    }
    for (; *s != '\0'; s++) {
        if (!((*s >= '0' && *s <= '9') || (*s >= 'a' && *s <= 'f'))) {
            return false;
        }
    }
    return true;
}

static bool url_ok(const char *s)
{
    size_t skip = strncmp(s, "https://", 8) == 0 ? 8 : strncmp(s, "http://", 7) == 0 ? 7 : 0;
    if (skip == 0 || s[skip] == '\0' || strlen(s) > 2048) {
        return false;
    }
    for (; *s != '\0'; s++) {
        unsigned char ch = (unsigned char)*s;
        if (ch <= 0x20 || ch == 0x7f) {
            return false;
        }
    }
    return true;
}

static bool sub_name_ok(const char *s)
{
    size_t n = strlen(s);
    if (n == 0 || n > 63) {
        return false;
    }
    for (; *s != '\0'; s++) {
        unsigned char ch = (unsigned char)*s;
        bool ok = (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == ' ' ||
                  ch == '.' || ch == '_' || ch == '-';
        if (!ok) {
            return false;
        }
    }
    return true;
}

static firc_err_t parse_headers(ctx_t *c, node_t *m, size_t ti, const char *section, firc_tun_sub_t *sub)
{
    if (m == NULL || m->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping of header names to values", "tunnels[%zu]%s.headers", ti, section);
    }
    size_t n = (size_t)(m->data.mapping.pairs.top - m->data.mapping.pairs.start);
    if (n > FIRC_SUBHDR_OWN_MAX) {
        return fail(c, "at most 16 headers", "tunnels[%zu]%s.headers", ti, section);
    }
    if (n == 0) {
        return FIRC_OK;
    }
    sub->headers = calloc(n, sizeof *sub->headers);
    if (sub->headers == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (yaml_node_pair_t *p = m->data.mapping.pairs.start; p < m->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        const char *name = is_scalar(k) ? text(k) : "";
        const char *why = firc_subhdr_check(name, is_scalar(v) ? text(v) : NULL);
        for (size_t j = 0; why == NULL && j < sub->n_headers; j++) {
            if (sub->headers[j].name != NULL && strcasecmp(sub->headers[j].name, name) == 0) {
                why = "given twice";
            }
        }
        if (why != NULL) {
            char said[160];
            if (firc_subhdr_name_ok(name)) {
                snprintf(said, sizeof said, "%.64s: %s", name, why);
            } else {
                snprintf(said, sizeof said, "%s", why);
            }
            return fail(c, said, "tunnels[%zu]%s.headers", ti, section);
        }
        firc_subhdr_pair_t *h = &sub->headers[sub->n_headers++];
        h->name = strdup(name);
        h->value = strdup(text(v));
        if (h->name == NULL || h->value == NULL) {
            return FIRC_ERR_NOMEM;
        }
    }
    return FIRC_OK;
}

static firc_err_t parse_sub(ctx_t *c, node_t *m, size_t ti, size_t i, firc_tun_sub_t *sub)
{
    char section[48];
    snprintf(section, sizeof section, ".sources[%zu].subscription", i);
    if (m->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping", "tunnels[%zu]%s", ti, section);
    }
    sub->interval_s = 21600;
    bool have_name = false;
    for (yaml_node_pair_t *p = m->data.mapping.pairs.start; p < m->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        if (!is_scalar(k)) {
            return bad_key(c, k, ti, section, "unknown key");
        }
        if (key_seen(c, m, p)) {
            return bad_key(c, k, ti, section, "key given twice");
        }
        if (strcmp(text(k), "name") == 0) {
            if (!copy_str(v, sub->name, sizeof sub->name) || !sub_name_ok(sub->name)) {
                return fail(c, "1..63 letters, digits, spaces, '.', '_' or '-'", "tunnels[%zu]%s.name", ti, section);
            }
            have_name = true;
        } else if (strcmp(text(k), "url") == 0) {
            if (!is_scalar(v) || !url_ok(text(v))) {
                return fail(c, "an http:// or https:// URL without whitespace, up to 2048 bytes", "tunnels[%zu]%s.url", ti,
                            section);
            }
            sub->url = strdup(text(v));
            if (sub->url == NULL) {
                return FIRC_ERR_NOMEM;
            }
        } else if (strcmp(text(k), "interval") == 0) {
            if (!parse_secs(v, &sub->interval_s, 0, 604800) || (sub->interval_s != 0 && sub->interval_s < 600)) {
                return fail(c, "0s (by hand only) or whole seconds, 10m..168h", "tunnels[%zu]%s.interval", ti, section);
            }
        } else if (strcmp(text(k), "headers") == 0) {
            firc_err_t r = parse_headers(c, v, ti, section, sub);
            if (r != FIRC_OK) {
                return r;
            }
        } else {
            return bad_key(c, k, ti, section, "unknown key");
        }
    }
    if (!have_name) {
        return fail(c, "required", "tunnels[%zu]%s.name", ti, section);
    }
    if (sub->url == NULL) {
        return fail(c, "required", "tunnels[%zu]%s.url", ti, section);
    }
    return FIRC_OK;
}

static firc_err_t parse_source(ctx_t *c, node_t *item, size_t ti, size_t i, firc_tun_src_t *src)
{
    static const char *const shape = "a source is one link: or subscription: entry";
    if (item->type != YAML_MAPPING_NODE) {
        return fail(c, shape, "tunnels[%zu].sources[%zu]", ti, i);
    }
    node_t *link = NULL;
    node_t *sub = NULL;
    for (yaml_node_pair_t *p = item->data.mapping.pairs.start; p < item->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        if (!is_scalar(k)) {
            return fail(c, shape, "tunnels[%zu].sources[%zu]", ti, i);
        }
        if (key_seen(c, item, p)) {
            return fail(c, "key given twice", "tunnels[%zu].sources[%zu]", ti, i);
        }
        if (strcmp(text(k), "id") == 0) {
            if (is_scalar(v) && text(v)[0] == '\0') {
                src->id[0] = '\0';
            } else if (!copy_str(v, src->id, sizeof src->id) || !hex8_ok(src->id)) {
                return fail(c, "8 hex digits, 0-9 and a-f", "tunnels[%zu].sources[%zu].id", ti, i);
            }
        } else if (strcmp(text(k), "link") == 0) {
            link = v;
        } else if (strcmp(text(k), "subscription") == 0) {
            sub = v;
        } else if (strcmp(text(k), "kind") == 0 || strcmp(text(k), "name") == 0 ||
                   strcmp(text(k), "enabled") == 0) {
            continue;
        } else {
            return fail(c, shape, "tunnels[%zu].sources[%zu]", ti, i);
        }
    }
    if ((link == NULL) == (sub == NULL)) {
        return fail(c, shape, "tunnels[%zu].sources[%zu]", ti, i);
    }
    if (sub != NULL) {
        src->kind = FIRC_TUN_SRC_SUB;
        return parse_sub(c, sub, ti, i, &src->sub);
    }
    if (!is_scalar(link) || !link_ok(text(link))) {
        return fail(c, "must be one proxy link without whitespace", "tunnels[%zu].sources[%zu]", ti, i);
    }
    src->kind = FIRC_TUN_SRC_LINK;
    src->link = strdup(text(link));
    return src->link == NULL ? FIRC_ERR_NOMEM : FIRC_OK;
}

static bool src_id_taken(const firc_tunnel_t *t, const char *id)
{
    for (size_t j = 0; j < t->n_src; j++) {
        if (strcmp(t->src[j].id, id) == 0) {
            return true;
        }
    }
    return false;
}

static void derived_id(const firc_tun_src_t *src, unsigned k, char id[9])
{
    firc_sha256_ctx_t h;
    firc_sha256_init(&h);
    const char *kind = src->kind == FIRC_TUN_SRC_LINK ? "link" : "sub";
    const char *text = src->kind == FIRC_TUN_SRC_LINK ? src->link : src->sub.url;
    firc_sha256_update(&h, (const uint8_t *)kind, strlen(kind) + 1);
    firc_sha256_update(&h, (const uint8_t *)text, strlen(text));
    if (k > 0) {
        char n[16];
        int l = snprintf(n, sizeof n, "%u", k);
        firc_sha256_update(&h, (const uint8_t *)"", 1);
        firc_sha256_update(&h, (const uint8_t *)n, (size_t)l);
    }
    uint8_t d[FIRC_SHA256_DIGEST_LEN];
    firc_sha256_final(&h, d);
    snprintf(id, 9, "%02x%02x%02x%02x", d[0], d[1], d[2], d[3]);
}

static void give_ids(firc_tunnel_t *t)
{
    for (size_t i = 0; i < t->n_src; i++) {
        for (unsigned k = 0; t->src[i].id[0] == '\0'; k++) {
            char id[9];
            derived_id(&t->src[i], k, id);
            if (!src_id_taken(t, id)) {
                memcpy(t->src[i].id, id, sizeof id);
            }
        }
    }
}

static firc_err_t parse_sources(ctx_t *c, node_t *seq, size_t ti, firc_tunnel_t *t)
{
    if (seq->type != YAML_SEQUENCE_NODE) {
        return fail(c, "must be a list", "tunnels[%zu].sources", ti);
    }
    size_t n = seq_len(seq);
    if (n > FIRC_TUN_MAX_SOURCES) {
        return fail(c, "at most 256 sources", "tunnels[%zu].sources", ti);
    }
    if (n == 0) {
        return FIRC_OK;
    }
    t->src = calloc(n, sizeof *t->src);
    if (t->src == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < n; i++) {
        t->n_src = i + 1;
        firc_err_t r = parse_source(c, at(c, seq->data.sequence.items.start[i]), ti, i, &t->src[i]);
        if (r != FIRC_OK) {
            return r;
        }
        for (size_t j = 0; j < i; j++) {
            if (t->src[i].id[0] != '\0' && strcmp(t->src[j].id, t->src[i].id) == 0) {
                return fail(c, "used twice", "tunnels[%zu].sources[%zu].id", ti, i);
            }
            if (t->src[i].kind == FIRC_TUN_SRC_SUB && t->src[j].kind == FIRC_TUN_SRC_SUB &&
                strcmp(t->src[j].sub.name, t->src[i].sub.name) == 0) {
                return fail(c, "used twice", "tunnels[%zu].sources[%zu].subscription.name", ti, i);
            }
        }
    }
    give_ids(t);
    return FIRC_OK;
}

static firc_err_t parse_filter_regex(ctx_t *c, node_t *v, size_t ti, const char *field, char **dst)
{
    static const char *const why = "a regular expression under 1024 bytes";
    if (!is_scalar(v) || strlen(text(v)) >= 1024) {
        return fail(c, why, "tunnels[%zu].%s", ti, field);
    }
    if (text(v)[0] == '\0') {
        return FIRC_OK;
    }
    int errcode = 0;
    PCRE2_SIZE erroff = 0;
    pcre2_code *re = pcre2_compile((PCRE2_SPTR)text(v), PCRE2_ZERO_TERMINATED,
                                   PCRE2_CASELESS | PCRE2_UTF | PCRE2_UCP,
                                   &errcode, &erroff, NULL);
    if (re == NULL) {
        return fail(c, why, "tunnels[%zu].%s", ti, field);
    }
    pcre2_code_free(re);
    *dst = strdup(text(v));
    return *dst == NULL ? FIRC_ERR_NOMEM : FIRC_OK;
}

static bool node_key_ok(const char *s)
{
    size_t n = strlen(s);
    if (n == 0 || n > FIRC_TUN_KEY_MAX) {
        return false;
    }
    for (; *s != '\0'; s++) {
        unsigned char ch = (unsigned char)*s;
        if (ch < 0x20 || ch == 0x7f) {
            return false;
        }
    }
    return true;
}

static firc_err_t parse_keys(ctx_t *c, node_t *seq, size_t ti, const char *name, char ***out, size_t *n_out)
{
    if (seq->type != YAML_SEQUENCE_NODE) {
        return fail(c, "must be a list of node keys", "tunnels[%zu].%s", ti, name);
    }
    size_t n = seq_len(seq);
    if (n > FIRC_TUN_MAX_KEYS) {
        return fail(c, "at most 1024 node keys", "tunnels[%zu].%s", ti, name);
    }
    if (n == 0) {
        return FIRC_OK;
    }
    *out = calloc(n, sizeof **out);
    if (*out == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < n; i++) {
        node_t *v = at(c, seq->data.sequence.items.start[i]);
        if (!is_scalar(v) || !node_key_ok(text(v))) {
            return fail(c, "a node key of 1..191 bytes", "tunnels[%zu].%s[%zu]", ti, name, i);
        }
        (*out)[i] = strdup(text(v));
        if ((*out)[i] == NULL) {
            return FIRC_ERR_NOMEM;
        }
        *n_out = i + 1;
    }
    return FIRC_OK;
}

static firc_err_t parse_inbound(ctx_t *c, node_t *m, size_t ti, size_t i, firc_tun_inbound_t *b)
{
    char section[40];
    snprintf(section, sizeof section, ".inbounds[%zu]", i);
    if (m->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping", "tunnels[%zu]%s", ti, section);
    }
    bool have_port = false;
    for (yaml_node_pair_t *p = m->data.mapping.pairs.start; p < m->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        if (!is_scalar(k)) {
            return bad_key(c, k, ti, section, "unknown key");
        }
        if (key_seen(c, m, p)) {
            return bad_key(c, k, ti, section, "key given twice");
        }
        const char *key = text(k);
        if (strcmp(key, "port") == 0) {
            int port;
            if (!parse_int(v, &port) || port < 1 || port > 65535) {
                return fail(c, "a port 1..65535", "tunnels[%zu]%s.port", ti, section);
            }
            b->port = (uint16_t)port;
            have_port = true;
        } else if (strcmp(key, "listen") == 0) {
            if (!copy_str(v, b->listen, sizeof b->listen)) {
                return fail(c, "an interface name", "tunnels[%zu]%s.listen", ti, section);
            }
        } else if (strcmp(key, "node") == 0) {
            if (!copy_str(v, b->node, sizeof b->node)) {
                return fail(c, "a node key of 1..191 bytes", "tunnels[%zu]%s.node", ti, section);
            }
        } else if (strcmp(key, "user") == 0) {
            if (!copy_str(v, b->user, sizeof b->user)) {
                return fail(c, "1..255 bytes", "tunnels[%zu]%s.user", ti, section);
            }
        } else if (strcmp(key, "password") == 0) {
            if (!copy_str(v, b->password, sizeof b->password)) {
                return fail(c, "1..255 bytes", "tunnels[%zu]%s.password", ti, section);
            }
        } else {
            return bad_key(c, k, ti, section, "unknown key");
        }
    }
    if (!have_port) {
        return fail(c, "a port 1..65535", "tunnels[%zu]%s.port", ti, section);
    }
    return FIRC_OK;
}

static firc_err_t parse_inbounds(ctx_t *c, node_t *seq, size_t ti, firc_tunnel_t *t)
{
    if (seq->type != YAML_SEQUENCE_NODE) {
        return fail(c, "must be a list", "tunnels[%zu].inbounds", ti);
    }
    size_t n = seq_len(seq);
    if (n > FIRC_TUN_MAX_INBOUNDS) {
        return fail(c, "at most 16 inbounds", "tunnels[%zu].inbounds", ti);
    }
    for (size_t i = 0; i < n; i++) {
        t->n_inb = i + 1;
        firc_err_t r = parse_inbound(c, at(c, seq->data.sequence.items.start[i]), ti, i, &t->inb[i]);
        if (r != FIRC_OK) {
            return r;
        }
    }
    return FIRC_OK;
}

static firc_err_t parse_advanced(ctx_t *c, node_t *m, size_t ti, firc_tunnel_t *t)
{
    if (m->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping", "tunnels[%zu].advanced", ti);
    }
    for (yaml_node_pair_t *p = m->data.mapping.pairs.start; p < m->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        if (!is_scalar(k)) {
            return bad_key(c, k, ti, ".advanced", "unknown key");
        }
        if (key_seen(c, m, p)) {
            return bad_key(c, k, ti, ".advanced", "key given twice");
        }
        if (strcmp(text(k), "timeout") == 0) {
            if (!parse_secs(v, &t->timeout_s, 1, 600)) {
                return fail(c, "whole seconds, 1s..600s", "tunnels[%zu].advanced.timeout", ti);
            }
        } else if (strcmp(text(k), "insecure") == 0) {
            if (!parse_bool(v, &t->insecure)) {
                return fail(c, "must be true or false", "tunnels[%zu].advanced.insecure", ti);
            }
        } else if (strcmp(text(k), "kernelTcp") == 0) {
            if (!parse_bool(v, &t->kernel_tcp)) {
                return fail(c, "must be true or false", "tunnels[%zu].advanced.kernelTcp", ti);
            }
        } else if (strcmp(text(k), "ca") == 0) {
            if (!copy_str(v, t->ca, sizeof t->ca)) {
                return fail(c, "a path under 256 bytes", "tunnels[%zu].advanced.ca", ti);
            }
        } else if (strcmp(text(k), "fingerprint") == 0) {
            if (!copy_str(v, t->fingerprint, sizeof t->fingerprint) ||
                (t->fingerprint[0] != 0 && strcmp(t->fingerprint, "chrome") != 0 &&
                 strcmp(t->fingerprint, "firefox") != 0)) {
                return fail(c, "chrome or firefox, or empty for the link's", "tunnels[%zu].advanced.fingerprint", ti);
            }
        } else {
            return bad_key(c, k, ti, ".advanced", "unknown key");
        }
    }
    return FIRC_OK;
}

static firc_err_t parse_uplink(ctx_t *c, node_t *v, size_t ti, firc_tunnel_t *t)
{
    char buf[40];
    if (!copy_str(v, buf, sizeof buf)) {
        return fail(c, "auto, iface:<name> or tunnel:<id>", "tunnels[%zu].uplink", ti);
    }
    t->uplink_ref[0] = '\0';
    if (strcmp(buf, "auto") == 0) {
        t->uplink = FIRC_UPLINK_AUTO;
    } else if (strncmp(buf, "iface:", 6) == 0 && firc_is_interface_name(buf + 6)) {
        t->uplink = FIRC_UPLINK_IFACE;
        strcpy(t->uplink_ref, buf + 6);
    } else if (strncmp(buf, "tunnel:", 7) == 0 && id_ok(buf + 7) && strlen(buf + 7) < sizeof t->id) {
        t->uplink = FIRC_UPLINK_TUNNEL;
        strcpy(t->uplink_ref, buf + 7);
    } else {
        return fail(c, "auto, iface:<name> or tunnel:<id>", "tunnels[%zu].uplink", ti);
    }
    return FIRC_OK;
}

static void tunnel_defaults(firc_tunnel_t *t)
{
    memset(t, 0, sizeof *t);
    t->enable = true;
    t->uplink = FIRC_UPLINK_AUTO;
    t->active = 1;
    strcpy(t->by, "connection");
    t->interval_s = 60;
    t->silence_s = 20;
    t->timeout_s = 8;
}

static bool key_has_source(const firc_tunnel_t *t, const char *key)
{
    const char *colon = strchr(key, ':');
    for (size_t i = 0; colon != NULL && i < t->n_src; i++) {
        if (strlen(t->src[i].id) == (size_t)(colon - key) && strncmp(t->src[i].id, key, (size_t)(colon - key)) == 0) {
            return true;
        }
    }
    return false;
}

static void drop_orphan_keys(const firc_tunnel_t *t, char **keys, size_t *n)
{
    size_t kept = 0;
    for (size_t i = 0; i < *n; i++) {
        if (key_has_source(t, keys[i])) {
            keys[kept++] = keys[i];
        } else {
            free(keys[i]);
        }
    }
    *n = kept;
}

static firc_err_t parse_tunnel(ctx_t *c, node_t *m, size_t ti, firc_tunnel_t *t)
{
    tunnel_defaults(t);
    if (m->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping", "tunnels[%zu]", ti);
    }
    bool have_id = false;
    for (yaml_node_pair_t *p = m->data.mapping.pairs.start; p < m->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        node_t *v = at(c, p->value);
        if (!is_scalar(k)) {
            return bad_key(c, k, ti, "", "unknown key");
        }
        if (key_seen(c, m, p)) {
            return bad_key(c, k, ti, "", "key given twice");
        }
        const char *key = text(k);
        firc_err_t r = FIRC_OK;
        if (strcmp(key, "id") == 0) {
            if (!copy_str(v, t->id, sizeof t->id) || !id_ok(t->id)) {
                return fail(c, "letters, digits, - and _, under 16 bytes", "tunnels[%zu].id", ti);
            }
            have_id = true;
        } else if (strcmp(key, "device") == 0) {
            if (!copy_str(v, t->device, sizeof t->device)) {
                return fail(c, "must start with tunvless or opkgtun", "tunnels[%zu].device", ti);
            }
        } else if (strcmp(key, "description") == 0) {
            if (!copy_str(v, t->description, sizeof t->description) || firc_text_has_control(t->description)) {
                return fail(c, "up to 63 bytes, no control characters", "tunnels[%zu].description", ti);
            }
        } else if (strcmp(key, "enable") == 0) {
            if (!parse_bool(v, &t->enable)) {
                return fail(c, "must be true or false", "tunnels[%zu].enable", ti);
            }
        } else if (strcmp(key, "routerPolicies") == 0) {
            if (!parse_bool(v, &t->router_policies)) {
                return fail(c, "must be true or false", "tunnels[%zu].routerPolicies", ti);
            }
        } else if (strcmp(key, "uplink") == 0) {
            r = parse_uplink(c, v, ti, t);
        } else if (strcmp(key, "sources") == 0) {
            r = parse_sources(c, v, ti, t);
        } else if (strcmp(key, "active") == 0) {
            if (!parse_int(v, &t->active)) {
                return fail(c, "an integer 1..8", "tunnels[%zu].active", ti);
            }
        } else if (strcmp(key, "by") == 0) {
            if (!copy_str(v, t->by, sizeof t->by)) {
                return fail(c, "connection, site or site-client", "tunnels[%zu].by", ti);
            }
        } else if (strcmp(key, "interval") == 0) {
            if (!parse_secs(v, &t->interval_s, 5, 86400)) {
                return fail(c, "whole seconds, 5s..24h", "tunnels[%zu].interval", ti);
            }
        } else if (strcmp(key, "silence") == 0) {
            if (!parse_secs(v, &t->silence_s, 0, 3600)) {
                return fail(c, "whole seconds, 0s..1h", "tunnels[%zu].silence", ti);
            }
        } else if (strcmp(key, "advanced") == 0) {
            r = parse_advanced(c, v, ti, t);
        } else if (strcmp(key, "inbounds") == 0) {
            r = parse_inbounds(c, v, ti, t);
        } else if (strcmp(key, "filter") == 0) {
            r = parse_filter_regex(c, v, ti, "filter", &t->filter);
        } else if (strcmp(key, "filterExclude") == 0) {
            r = parse_filter_regex(c, v, ti, "filterExclude", &t->filter_exclude);
        } else if (strcmp(key, "order") == 0) {
            r = parse_keys(c, v, ti, "order", &t->order, &t->n_order);
        } else if (strcmp(key, "exclude") == 0) {
            r = parse_keys(c, v, ti, "exclude", &t->exclude, &t->n_exclude);
        } else {
            return bad_key(c, k, ti, "", "unknown key");
        }
        if (r != FIRC_OK) {
            return r;
        }
    }
    if (!have_id) {
        return fail(c, "required", "tunnels[%zu].id", ti);
    }
    drop_orphan_keys(t, t->order, &t->n_order);
    drop_orphan_keys(t, t->exclude, &t->n_exclude);
    if (t->enable && t->n_src == 0) {
        return fail(c, "needs at least one source", "tunnels[%zu].sources", ti);
    }
    return FIRC_OK;
}

static const char *device_problem(const char *d)
{
    if (firc_tundev_name_index(d) >= 0 && firc_is_interface_name(d)) {
        return NULL;
    }
    if (firc_tundev_is_opkg(d)) {
        return "opkgtun<N> needs N 0..16, the router's limit for OpkgTun";
    }
    if (strncmp(d, "tunvless", 8) == 0) {
        return "tunvless<N> needs N 0..99";
    }
    return "must start with tunvless or opkgtun";
}

static const firc_tunnel_t *find_id(const firc_tunnels_t *ts, const char *id)
{
    for (size_t i = 0; i < ts->n; i++) {
        if (strcmp(ts->t[i].id, id) == 0) {
            return &ts->t[i];
        }
    }
    return NULL;
}

static firc_err_t validate_inbounds(ctx_t *c, const firc_tunnels_t *ts, size_t ti)
{
    const firc_tunnel_t *t = &ts->t[ti];
    for (size_t i = 0; i < t->n_inb; i++) {
        const firc_tun_inbound_t *b = &t->inb[i];
        unsigned char ip[sizeof(struct in6_addr)];
        if (!firc_is_interface_name(b->listen) || inet_pton(AF_INET, b->listen, ip) == 1 ||
            inet_pton(AF_INET6, b->listen, ip) == 1) {
            return fail(c, "an interface name", "tunnels[%zu].inbounds[%zu].listen", ti, i);
        }
        if (b->port >= FIRC_TUNNEL_TCP_PORT_BASE && b->port < FIRC_TUNNEL_TCP_PORT_BASE + 100) {
            return fail(c, "3600..3699 is firc's own", "tunnels[%zu].inbounds[%zu].port", ti, i);
        }
        for (size_t j = 0; j < i; j++) {
            if (t->inb[j].port == b->port && strcmp(t->inb[j].listen, b->listen) != 0) {
                return fail(c, "used twice in this tunnel", "tunnels[%zu].inbounds[%zu].port", ti, i);
            }
        }
        for (size_t tj = 0; tj <= ti; tj++) {
            const firc_tunnel_t *o = &ts->t[tj];
            size_t lim = tj == ti ? i : o->n_inb;
            for (size_t j = 0; j < lim; j++) {
                if (o->inb[j].port == b->port && strcmp(o->inb[j].listen, b->listen) == 0) {
                    return fail(c, "port used twice on this interface", "tunnels[%zu].inbounds[%zu].port", ti, i);
                }
            }
        }
        if (b->node[0] == '\0') {
            return fail(c, "required", "tunnels[%zu].inbounds[%zu].node", ti, i);
        }
        if (!node_key_ok(b->node)) {
            return fail(c, "a node key of 1..191 bytes", "tunnels[%zu].inbounds[%zu].node", ti, i);
        }
        if ((b->user[0] == '\0') != (b->password[0] == '\0')) {
            return fail(c, "user and password: both or neither", "tunnels[%zu].inbounds[%zu].user", ti, i);
        }
        if (firc_text_has_control(b->user)) {
            return fail(c, "no control characters", "tunnels[%zu].inbounds[%zu].user", ti, i);
        }
        if (firc_text_has_control(b->password)) {
            return fail(c, "no control characters", "tunnels[%zu].inbounds[%zu].password", ti, i);
        }
        if (strchr(b->user, ':') != NULL) {
            return fail(c, "no ':' in a user", "tunnels[%zu].inbounds[%zu].user", ti, i);
        }
    }
    return FIRC_OK;
}

static firc_err_t validate(ctx_t *c, const firc_tunnels_t *ts)
{
    for (size_t i = 0; i < ts->n; i++) {
        const firc_tunnel_t *t = &ts->t[i];
        for (size_t j = 0; j < i; j++) {
            if (strcmp(ts->t[j].id, t->id) == 0) {
                return fail(c, "id used twice", "tunnels[%zu].id", i);
            }
        }
        const char *why = device_problem(t->device);
        if (why != NULL) {
            return fail(c, why, "tunnels[%zu].device", i);
        }
        if (t->router_policies && !firc_tundev_is_opkg(t->device)) {
            return fail(c, "routerPolicies needs an opkgtun<N> device", "tunnels[%zu].routerPolicies", i);
        }
        for (size_t j = 0; j < i; j++) {
            if (firc_tunnel_index(&ts->t[j]) == firc_tunnel_index(t)) {
                return fail(c, "device used twice", "tunnels[%zu].device", i);
            }
        }
    }
    for (size_t i = 0; i < ts->n; i++) {
        const firc_tunnel_t *t = &ts->t[i];
        if (t->uplink == FIRC_UPLINK_IFACE && firc_tunnels_by_device(ts, t->uplink_ref) != NULL) {
            return fail(c, "use tunnel:<id> to chain tunnels", "tunnels[%zu].uplink", i);
        }
        if (t->uplink == FIRC_UPLINK_TUNNEL) {
            if (strcmp(t->uplink_ref, t->id) == 0) {
                return fail(c, "a tunnel cannot use itself as its uplink", "tunnels[%zu].uplink", i);
            }
            const firc_tunnel_t *cur = find_id(ts, t->uplink_ref);
            if (cur == NULL) {
                return fail(c, "no tunnel with this id", "tunnels[%zu].uplink", i);
            }
            bool latest = true;
            for (size_t step = 0; step < ts->n && cur != NULL && cur->uplink == FIRC_UPLINK_TUNNEL; step++) {
                if (cur > t) {
                    latest = false;
                }
                if (strcmp(cur->uplink_ref, t->id) == 0) {
                    if (latest) {
                        return fail(c, "uplink cycle", "tunnels[%zu].uplink", i);
                    }
                    break;
                }
                cur = find_id(ts, cur->uplink_ref);
            }
        }
        if (t->active < 1 || t->active > FIRC_TUN_MAX_ACTIVE) {
            return fail(c, "active must be 1..8", "tunnels[%zu].active", i);
        }
        if (strcmp(t->by, "connection") != 0 && strcmp(t->by, "site") != 0 && strcmp(t->by, "site-client") != 0) {
            return fail(c, "must be connection, site or site-client", "tunnels[%zu].by", i);
        }
        firc_err_t r = validate_inbounds(c, ts, i);
        if (r != FIRC_OK) {
            return r;
        }
    }
    return FIRC_OK;
}

static firc_err_t load_doc(ctx_t *c, firc_tunnels_t *out)
{
    node_t *root = yaml_document_get_root_node(c->doc);
    if (root == NULL) {
        return FIRC_OK;
    }
    if (root->type != YAML_MAPPING_NODE) {
        return fail(c, "must be a mapping", "tunnels.yaml");
    }
    node_t *list = NULL;
    for (yaml_node_pair_t *p = root->data.mapping.pairs.start; p < root->data.mapping.pairs.top; p++) {
        node_t *k = at(c, p->key);
        if (is_scalar(k) && strcmp(text(k), "configVersion") == 0) {
            continue;
        }
        if (!is_scalar(k) || strcmp(text(k), "tunnels") != 0) {
            return fail(c, "unknown key", "tunnels.yaml");
        }
        if (key_seen(c, root, p)) {
            return fail(c, "key given twice", "tunnels.yaml");
        }
        list = at(c, p->value);
    }
    if (list == NULL || (is_scalar(list) && (text(list)[0] == '\0' || strcmp(text(list), "~") == 0 ||
                                              strcmp(text(list), "null") == 0))) {
        return FIRC_OK;
    }
    if (list->type != YAML_SEQUENCE_NODE) {
        return fail(c, "must be a list", "tunnels");
    }
    size_t n = seq_len(list);
    if (n > FIRC_TUN_MAX) {
        return fail(c, "at most 16 tunnels", "tunnels[%d]", FIRC_TUN_MAX);
    }
    if (n == 0) {
        return FIRC_OK;
    }
    out->t = calloc(n, sizeof *out->t);
    if (out->t == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < n; i++) {
        out->n = i + 1;
        firc_err_t r = parse_tunnel(c, at(c, list->data.sequence.items.start[i]), i, &out->t[i]);
        if (r != FIRC_OK) {
            return r;
        }
    }
    return validate(c, out);
}

firc_err_t firc_tunnels_load_buffer(firc_tunnels_t *out, const char *buf, size_t len,
                                    firc_tun_err_t *err)
{
    memset(out, 0, sizeof *out);
    if (err != NULL) {
        memset(err, 0, sizeof *err);
    }
    yaml_parser_t parser;
    yaml_document_t doc;
    if (!yaml_parser_initialize(&parser)) {
        return FIRC_ERR_NOMEM;
    }
    yaml_parser_set_input_string(&parser, (const unsigned char *)buf, len);
    if (!yaml_parser_load(&parser, &doc)) {
        ctx_t c = {NULL, err};
        firc_err_t r = fail(&c, parser.problem != NULL ? parser.problem : "syntax error", "tunnels.yaml");
        yaml_parser_delete(&parser);
        return r;
    }
    yaml_parser_delete(&parser);
    ctx_t c = {&doc, err};
    firc_err_t r = load_doc(&c, out);
    yaml_document_delete(&doc);
    if (r != FIRC_OK) {
        firc_tunnels_free(out);
    }
    return r;
}

static firc_err_t read_file(const char *path, char **buf, size_t *len, bool warn_mode)
{
    *buf = NULL;
    *len = 0;
    FILE *f = fopen(path, "rbe");
    if (f == NULL) {
        return errno == ENOENT ? FIRC_OK : firc_err_from_errno(errno);
    }
    struct stat st;
    if (warn_mode && fstat(fileno(f), &st) == 0 && (st.st_mode & 077) != 0) {
        FIRC_WARN("%s can be read by others (mode %03o) and holds the tunnel links; chmod 600 it", path,
                  (unsigned)(st.st_mode & 0777));
    }
    *buf = malloc(FILE_CAP + 1);
    if (*buf == NULL) {
        fclose(f);
        return FIRC_ERR_NOMEM;
    }
    *len = fread(*buf, 1, FILE_CAP + 1, f);
    bool bad = ferror(f) != 0 || *len > FILE_CAP;
    fclose(f);
    if (bad) {
        free(*buf);
        *buf = NULL;
        return FIRC_ERR_IO;
    }
    return FIRC_OK;
}

firc_err_t firc_tunnels_load_file(firc_tunnels_t *out, const char *path, firc_tun_err_t *err)
{
    memset(out, 0, sizeof *out);
    char *buf = NULL;
    size_t len = 0;
    firc_err_t r = read_file(path, &buf, &len, true);
    if (r != FIRC_OK) {
        return r;
    }
    if (buf != NULL) {
        r = firc_tunnels_load_buffer(out, buf, len, err);
    }
    if (r == FIRC_OK) {
        firc_sha256((const uint8_t *)(buf != NULL ? buf : ""), len, out->file_sha);
        out->file_known = true;
    }
    free(buf);
    return r;
}

bool firc_tunnels_file_unchanged(const firc_tunnels_t *t, const char *path)
{
    char *buf = NULL;
    size_t len = 0;
    if (!t->file_known || read_file(path, &buf, &len, false) != FIRC_OK) {
        return false;
    }
    uint8_t sha[32];
    firc_sha256((const uint8_t *)(buf != NULL ? buf : ""), len, sha);
    free(buf);
    return memcmp(sha, t->file_sha, sizeof sha) == 0;
}

typedef struct {
    char *data;
    size_t len, cap;
} obuf_t;

static int obuf_write(void *ext, unsigned char *p, size_t n)
{
    obuf_t *b = ext;
    if (b->len + n > b->cap) {
        size_t cap = b->cap == 0 ? 4096 : b->cap;
        while (b->len + n > cap) {
            cap *= 2;
        }
        char *g = realloc(b->data, cap);
        if (g == NULL) {
            return 0;
        }
        b->data = g;
        b->cap = cap;
    }
    memcpy(b->data + b->len, p, n);
    b->len += n;
    return 1;
}

typedef struct {
    yaml_emitter_t em;
    bool ok;
} em_t;

static void ev(em_t *e, yaml_event_t *event)
{
    if (e->ok && !yaml_emitter_emit(&e->em, event)) {
        e->ok = false;
    }
}

static void str(em_t *e, const char *s, bool quoted)
{
    yaml_event_t x;
    if (!yaml_scalar_event_initialize(&x, NULL, NULL, (const yaml_char_t *)s, (int)strlen(s), quoted ? 0 : 1,
                                      1, quoted ? YAML_DOUBLE_QUOTED_SCALAR_STYLE : YAML_PLAIN_SCALAR_STYLE)) {
        e->ok = false;
        return;
    }
    ev(e, &x);
}

static void map_start(em_t *e)
{
    yaml_event_t x;
    if (yaml_mapping_start_event_initialize(&x, NULL, NULL, 1, YAML_BLOCK_MAPPING_STYLE)) {
        ev(e, &x);
    } else {
        e->ok = false;
    }
}

static void map_end(em_t *e)
{
    yaml_event_t x;
    if (yaml_mapping_end_event_initialize(&x)) {
        ev(e, &x);
    } else {
        e->ok = false;
    }
}

static void seq_start(em_t *e, bool flow)
{
    yaml_event_t x;
    if (yaml_sequence_start_event_initialize(&x, NULL, NULL, 1,
                                             flow ? YAML_FLOW_SEQUENCE_STYLE : YAML_BLOCK_SEQUENCE_STYLE)) {
        ev(e, &x);
    } else {
        e->ok = false;
    }
}

static void seq_end(em_t *e)
{
    yaml_event_t x;
    if (yaml_sequence_end_event_initialize(&x)) {
        ev(e, &x);
    } else {
        e->ok = false;
    }
}

static void kv(em_t *e, const char *k, const char *v, bool quoted)
{
    str(e, k, false);
    str(e, v, quoted);
}

static void kv_secs(em_t *e, const char *k, int s)
{
    char b[24];
    snprintf(b, sizeof b, "%ds", s);
    kv(e, k, b, false);
}

static void emit_keys(em_t *e, const char *name, char *const *v, size_t n)
{
    if (n == 0) {
        return;
    }
    str(e, name, false);
    seq_start(e, false);
    for (size_t i = 0; i < n; i++) {
        str(e, v[i], true);
    }
    seq_end(e);
}

static void emit_tunnel(em_t *e, const firc_tunnel_t *t)
{
    char b[48];
    char pbuf[8];
    map_start(e);
    kv(e, "id", t->id, true);
    kv(e, "device", t->device, true);
    if (t->description[0] != '\0') {
        kv(e, "description", t->description, true);
    }
    kv(e, "enable", t->enable ? "true" : "false", false);
    if (t->router_policies) {
        kv(e, "routerPolicies", "true", false);
    }
    if (t->uplink == FIRC_UPLINK_AUTO) {
        snprintf(b, sizeof b, "auto");
    } else {
        snprintf(b, sizeof b, "%s:%s", t->uplink == FIRC_UPLINK_IFACE ? "iface" : "tunnel", t->uplink_ref);
    }
    kv(e, "uplink", b, true);
    str(e, "sources", false);
    seq_start(e, t->n_src == 0);
    for (size_t i = 0; i < t->n_src; i++) {
        const firc_tun_src_t *src = &t->src[i];
        map_start(e);
        kv(e, "id", src->id, true);
        if (src->kind == FIRC_TUN_SRC_LINK) {
            kv(e, "link", src->link, true);
        } else {
            str(e, "subscription", false);
            map_start(e);
            kv(e, "name", src->sub.name, true);
            kv(e, "url", src->sub.url, true);
            kv_secs(e, "interval", src->sub.interval_s);
            if (src->sub.n_headers > 0) {
                str(e, "headers", false);
                map_start(e);
                for (size_t j = 0; j < src->sub.n_headers; j++) {
                    str(e, src->sub.headers[j].name, true);
                    str(e, src->sub.headers[j].value, true);
                }
                map_end(e);
            }
            map_end(e);
        }
        map_end(e);
    }
    seq_end(e);
    if (t->filter != NULL) {
        kv(e, "filter", t->filter, true);
    }
    if (t->filter_exclude != NULL) {
        kv(e, "filterExclude", t->filter_exclude, true);
    }
    emit_keys(e, "order", t->order, t->n_order);
    emit_keys(e, "exclude", t->exclude, t->n_exclude);
    snprintf(b, sizeof b, "%d", t->active);
    kv(e, "active", b, false);
    kv(e, "by", t->by, true);
    kv_secs(e, "interval", t->interval_s);
    kv_secs(e, "silence", t->silence_s);
    str(e, "advanced", false);
    map_start(e);
    kv_secs(e, "timeout", t->timeout_s);
    kv(e, "insecure", t->insecure ? "true" : "false", false);
    kv(e, "ca", t->ca, true);
    kv(e, "fingerprint", t->fingerprint, true);
    kv(e, "kernelTcp", t->kernel_tcp ? "true" : "false", false);
    map_end(e);
    if (t->n_inb > 0) {
        str(e, "inbounds", false);
        seq_start(e, false);
        for (size_t i = 0; i < t->n_inb; i++) {
            const firc_tun_inbound_t *ib = &t->inb[i];
            map_start(e);
            snprintf(pbuf, sizeof pbuf, "%u", (unsigned)ib->port);
            kv(e, "port", pbuf, false);
            kv(e, "listen", ib->listen, true);
            kv(e, "node", ib->node, true);
            if (ib->user[0] != '\0') {
                kv(e, "user", ib->user, true);
                kv(e, "password", ib->password, true);
            }
            map_end(e);
        }
        seq_end(e);
    }
    map_end(e);
}

firc_err_t firc_tunnels_save_file(firc_tunnels_t *in, const char *path)
{
    obuf_t out = {0};
    em_t e = {.ok = true};
    if (!yaml_emitter_initialize(&e.em)) {
        return FIRC_ERR_NOMEM;
    }
    yaml_emitter_set_output(&e.em, obuf_write, &out);
    yaml_emitter_set_indent(&e.em, 2);
    yaml_emitter_set_width(&e.em, -1);
    yaml_emitter_set_unicode(&e.em, 1);
    yaml_event_t x;
    if (yaml_stream_start_event_initialize(&x, YAML_UTF8_ENCODING)) {
        ev(&e, &x);
    }
    if (yaml_document_start_event_initialize(&x, NULL, NULL, NULL, 1)) {
        ev(&e, &x);
    }
    map_start(&e);
    str(&e, "tunnels", false);
    seq_start(&e, in->n == 0);
    for (size_t i = 0; i < in->n; i++) {
        emit_tunnel(&e, &in->t[i]);
    }
    seq_end(&e);
    map_end(&e);
    if (yaml_document_end_event_initialize(&x, 1)) {
        ev(&e, &x);
    }
    if (yaml_stream_end_event_initialize(&x)) {
        ev(&e, &x);
    }
    yaml_emitter_delete(&e.em);
    if (!e.ok) {
        free(out.data);
        return FIRC_ERR_SYS;
    }
    firc_err_t r = firc_atomic_write(path, out.data, out.len);
    if (r == FIRC_OK) {
        firc_sha256((const uint8_t *)(out.data != NULL ? out.data : ""), out.len, in->file_sha);
        in->file_known = true;
    }
    free(out.data);
    return r;
}

static bool str_eq(const char *a, const char *b)
{
    return (a == NULL || b == NULL) ? a == b : strcmp(a, b) == 0;
}

bool firc_tunnel_same_argv(const firc_tunnel_t *a, const firc_tunnel_t *b)
{
    return strcmp(a->id, b->id) == 0 && strcmp(a->device, b->device) == 0 && a->enable == b->enable &&
           a->uplink == b->uplink && strcmp(a->uplink_ref, b->uplink_ref) == 0 && a->active == b->active &&
           strcmp(a->by, b->by) == 0 && a->interval_s == b->interval_s && a->silence_s == b->silence_s &&
           a->timeout_s == b->timeout_s && a->insecure == b->insecure && strcmp(a->ca, b->ca) == 0 &&
           strcmp(a->fingerprint, b->fingerprint) == 0 &&
           a->kernel_tcp == b->kernel_tcp;
}

bool firc_tunnel_same_nodes(const firc_tunnel_t *a, const firc_tunnel_t *b)
{
    if (a->n_src != b->n_src) {
        return false;
    }
    for (size_t i = 0; i < a->n_src; i++) {
        const firc_tun_src_t *x = &a->src[i];
        const firc_tun_src_t *y = &b->src[i];
        if (x->kind != y->kind || !str_eq(x->link, y->link) || !str_eq(x->sub.url, y->sub.url)) {
            return false;
        }
    }
    return true;
}

int firc_tunnel_index(const firc_tunnel_t *t)
{
    return firc_tundev_name_index(t->device);
}

void firc_tunnels_names(const firc_tunnels_t *ts, char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP])
{
    memset(names, 0, sizeof(char[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP]));
    for (size_t i = 0; ts != NULL && i < ts->n; i++) {
        int n = firc_tunnel_index(&ts->t[i]);
        if (n >= 0 && n < FIRC_KTCP_DEVICES) {
            snprintf(names[n], FIRC_KTCP_NAME_CAP, "%s", ts->t[i].device);
        }
    }
}

const firc_tunnel_t *firc_tunnels_by_device(const firc_tunnels_t *ts, const char *dev)
{
    for (size_t i = 0; ts != NULL && dev != NULL && i < ts->n; i++) {
        if (strcmp(ts->t[i].device, dev) == 0) {
            return &ts->t[i];
        }
    }
    return NULL;
}

uint16_t firc_tunnel_tcp_port(const firc_tunnel_t *t)
{
    int n = firc_tunnel_index(t);
    if (!t->enable || !t->kernel_tcp || n < 0) {
        return 0;
    }
    return (uint16_t)(FIRC_TUNNEL_TCP_PORT_BASE + n);
}

uint64_t firc_tun_sub_headers_sig(const firc_tun_sub_t *sub)
{
    if (sub->n_headers == 0) {
        return 0;
    }
    firc_sha256_ctx_t h;
    firc_sha256_init(&h);
    for (size_t i = 0; i < sub->n_headers; i++) {
        firc_sha256_update(&h, (const uint8_t *)sub->headers[i].name, strlen(sub->headers[i].name) + 1);
        firc_sha256_update(&h, (const uint8_t *)sub->headers[i].value, strlen(sub->headers[i].value) + 1);
    }
    uint8_t d[FIRC_SHA256_DIGEST_LEN];
    firc_sha256_final(&h, d);
    uint64_t v = 0;
    for (size_t i = 0; i < 8; i++) {
        v = (v << 8) | d[i];
    }
    return v != 0 ? v : 1;
}
