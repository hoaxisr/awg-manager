#include "firc/models.h"

#include <stdint.h>
#include <stdlib.h>
#include <string.h>

#include "firc/devices.h"
#include "firc/ifacename.h"
#include "firc/ipset_to_link.h"
#include "firc/utf8.h"

firc_err_t firc_strset(char **dst, const char *src)
{
    char *copy = NULL;
    if (src != NULL) {
        copy = strdup(src);
        if (copy == NULL) {
            return FIRC_ERR_NOMEM;
        }
    }
    free(*dst);
    *dst = copy;
    return FIRC_OK;
}

firc_rule_t *firc_rule_new(void)
{
    return calloc(1, sizeof(firc_rule_t));
}

void firc_rule_free(firc_rule_t *r)
{
    if (r == NULL) {
        return;
    }
    free(r->type);
    free(r->rule);
    free(r->proto);
    free(r->ports);
    free(r->comment);
    free(r->autolist);
    free(r);
}

#define COMMENT_MAX_STR_(n) #n
#define COMMENT_MAX_STR(n) COMMENT_MAX_STR_(n)

static const char *comment_trimmed(const char *s, size_t *n)
{
    if (s == NULL) {
        s = "";
    }
    while (*s == ' ') {
        s++;
    }
    *n = strlen(s);
    while (*n > 0 && s[*n - 1] == ' ') {
        (*n)--;
    }
    return s;
}

const char *firc_rule_comment_why(const char *s)
{
    size_t n;
    s = comment_trimmed(s, &n);
    if (!firc_utf8_valid(s)) {
        return "comment is not valid UTF-8";
    }
    if (firc_text_has_control(s)) {
        return "comment holds a control character";
    }
    if (strstr(s, "\xe2\x80\xa8") != NULL || strstr(s, "\xe2\x80\xa9") != NULL) {
        return "comment holds a line or paragraph separator";
    }
    if (n > FIRC_RULE_COMMENT_MAX) {
        return "comment is longer than " COMMENT_MAX_STR(FIRC_RULE_COMMENT_MAX) " bytes";
    }
    return NULL;
}

firc_err_t firc_rule_set_comment(firc_rule_t *r, const char *s, const char **why)
{
    *why = firc_rule_comment_why(s);
    if (*why != NULL) {
        return FIRC_ERR_INVAL;
    }
    size_t n;
    s = comment_trimmed(s, &n);
    char *copy = NULL;
    if (n > 0) {
        copy = malloc(n + 1);
        if (copy == NULL) {
            return FIRC_ERR_NOMEM;
        }
        memcpy(copy, s, n);
        copy[n] = '\0';
    }
    free(r->comment);
    r->comment = copy;
    return FIRC_OK;
}

firc_group_t *firc_group_new(void)
{
    firc_group_t *g = calloc(1, sizeof(firc_group_t));
    if (g != NULL) { g->resolve.tunnel = true; } /* on unless said off */
    return g;
}

void firc_devsel_spec_clear(firc_devsel_spec_t *s)
{
    for (size_t i = 0; i < s->n_allow; i++) { free(s->allow[i]); }
    for (size_t i = 0; i < s->n_deny; i++) { free(s->deny[i]); }
    free(s->allow);
    free(s->deny);
    memset(s, 0, sizeof(*s));
}

static firc_err_t strings_copy(char ***dst, size_t *dst_n, char *const *src, size_t n)
{
    *dst = NULL;
    *dst_n = 0;
    if (n == 0) { return FIRC_OK; }
    char **out = calloc(n, sizeof(*out));
    if (out == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n; i++) {
        out[i] = strdup(src[i] != NULL ? src[i] : "");
        if (out[i] == NULL) {
            for (size_t j = 0; j < i; j++) { free(out[j]); }
            free(out);
            return FIRC_ERR_NOMEM;
        }
    }
    *dst = out;
    *dst_n = n;
    return FIRC_OK;
}

firc_err_t firc_devsel_spec_copy(firc_devsel_spec_t *dst, const firc_devsel_spec_t *src)
{
    firc_devsel_spec_t tmp = {0};
    firc_err_t err = strings_copy(&tmp.allow, &tmp.n_allow, src->allow, src->n_allow);
    if (err == FIRC_OK) { err = strings_copy(&tmp.deny, &tmp.n_deny, src->deny, src->n_deny); }
    if (err != FIRC_OK) {
        firc_devsel_spec_clear(&tmp);
        return err;
    }
    firc_devsel_spec_clear(dst);
    *dst = tmp;
    return FIRC_OK;
}

firc_err_t firc_devsel_spec_check_canon(firc_devsel_spec_t *ds, bool *in_allow, const char **entry,
                                        const char **why)
{
    for (size_t k = 0; k < ds->n_allow + ds->n_deny; k++) {
        bool allow = k < ds->n_allow;
        char *e = allow ? ds->allow[k] : ds->deny[k - ds->n_allow];
        const char *w = firc_devsel_entry_why(e);
        if (w != NULL) {
            *in_allow = allow;
            *entry = e;
            *why = w;
            return FIRC_ERR_INVAL;
        }
        firc_devsel_entry_canon(e);
    }
    return FIRC_OK;
}

static bool devsel_listed(char *const *list, size_t n, const char *entry)
{
    for (size_t i = 0; i < n; i++) {
        if (strcmp(list[i], entry) == 0) { return true; }
    }
    return false;
}

bool firc_devsel_spec_narrows(const firc_devsel_spec_t *was, const firc_devsel_spec_t *now)
{
    for (size_t i = 0; i < now->n_deny; i++) {
        if (!devsel_listed(was->deny, was->n_deny, now->deny[i])) { return true; }
    }
    if (now->n_allow == 0) { return false; } /* every device it does not deny */
    if (was->n_allow == 0) { return true; }
    for (size_t i = 0; i < was->n_allow; i++) {
        if (!devsel_listed(now->allow, now->n_allow, was->allow[i])) { return true; }
    }
    return false;
}

void firc_group_free(firc_group_t *g)
{
    if (g == NULL) {
        return;
    }
    for (size_t i = 0; i < g->n_rules; i++) {
        firc_rule_free(g->rules[i]);
    }
    free(g->rules);
    firc_devsel_spec_clear(&g->devices);
    free(g->resolve.server);
    firc_group_list_free(g->list);
    free(g->name);
    free(g->iface);
    for (size_t i = 0; i < g->n_iface_rest; i++) { free(g->iface_rest[i]); }
    free(g);
}

static firc_err_t grow_array(void ***arr, size_t n)
{
    /* grow by doubling starting at 8 */
    if ((n & (n - 1)) == 0 && n >= 8) {
        void **na = realloc(*arr, n * 2 * sizeof(void *));
        if (na == NULL) {
            return FIRC_ERR_NOMEM;
        }
        *arr = na;
    } else if (n < 8) {
        if (*arr == NULL) {
            void **na = calloc(8, sizeof(void *));
            if (na == NULL) {
                return FIRC_ERR_NOMEM;
            }
            *arr = na;
        }
    }
    return FIRC_OK;
}

firc_err_t firc_rule_dup(const firc_rule_t *r, firc_rule_t **out)
{
    firc_rule_t *d = firc_rule_new();
    if (d == NULL) {
        return FIRC_ERR_NOMEM;
    }
    d->id = r->id;
    d->enable = r->enable;
    d->autolist_by_domain = r->autolist_by_domain;
    firc_err_t err = firc_strset(&d->type, r->type);
    if (err == FIRC_OK) { err = firc_strset(&d->rule, r->rule); }
    if (err == FIRC_OK) { err = firc_strset(&d->proto, r->proto); }
    if (err == FIRC_OK) { err = firc_strset(&d->ports, r->ports); }
    if (err == FIRC_OK) { err = firc_strset(&d->comment, r->comment); }
    if (err == FIRC_OK) { err = firc_strset(&d->autolist, r->autolist); }
    if (err != FIRC_OK) {
        firc_rule_free(d);
        return err;
    }
    *out = d;
    return FIRC_OK;
}

firc_err_t firc_group_add_rule(firc_group_t *g, firc_rule_t *r)
{
    firc_err_t err = grow_array((void ***)&g->rules, g->n_rules);
    if (err != FIRC_OK) {
        return err;
    }
    g->rules[g->n_rules++] = r;
    return FIRC_OK;
}

firc_err_t firc_group_rules_with(const firc_group_t *g, size_t at, firc_rule_t *put, firc_rule_t *append,
                                 firc_group_t *out)
{
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; err == FIRC_OK && i < g->n_rules; i++) {
        firc_rule_t *r = i == at ? put : g->rules[i];
        if (r != NULL) { err = firc_group_add_rule(out, r); }
    }
    if (err == FIRC_OK && append != NULL) { err = firc_group_add_rule(out, append); }
    if (err != FIRC_OK) {
        free(out->rules);
        out->rules = NULL;
        out->n_rules = 0;
    }
    return err;
}

firc_err_t firc_group_check_rule_ids(const firc_group_t *g)
{
    firc_id_set_t seen = {0};
    bool zero = false;
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; err == FIRC_OK && i < g->n_rules; i++) {
        firc_id_t id = g->rules[i]->id;
        if (firc_id_is_zero(id)) {
            if (zero) { err = FIRC_ERR_EXIST; }
            zero = true;
        } else if (firc_id_set_contains(&seen, id)) {
            err = FIRC_ERR_EXIST;
        } else {
            err = firc_id_set_add(&seen, id);
        }
    }
    firc_id_set_free(&seen);
    return err;
}

/* pointer equality across TUs isn't guaranteed; callers compare against these or use strcmp */
static const char *const rule_type_names[] = {"",
                                              FIRC_RULE_DOMAIN,
                                              FIRC_RULE_NAMESPACE,
                                              FIRC_RULE_WILDCARD,
                                              FIRC_RULE_REGEX,
                                              FIRC_RULE_SUBNET,
                                              FIRC_RULE_SUBNET6};
#define RULE_TYPE_COUNT (sizeof(rule_type_names) / sizeof(rule_type_names[0]))

uint8_t firc_rule_type_index(const char *type)
{
    if (type != NULL) {
        for (size_t i = 1; i < RULE_TYPE_COUNT; i++) {
            if (strcmp(rule_type_names[i], type) == 0) {
                return (uint8_t)i;
            }
        }
    }
    return 0;
}

const char *firc_rule_type_name(uint8_t index)
{
    return rule_type_names[index < RULE_TYPE_COUNT ? index : 0];
}

const char *firc_rule_type_intern(const char *type)
{
    return rule_type_names[firc_rule_type_index(type)];
}

const char *firc_sub_sync_state_name(firc_sub_sync_state_t s)
{
    switch (s) {
    case FIRC_SUB_SYNC_IDLE:
        return "idle";
    case FIRC_SUB_SYNC_QUEUED:
        return "queued";
    case FIRC_SUB_SYNC_FETCHING:
        return "fetching";
    case FIRC_SUB_SYNC_ERROR:
        return "error";
    }
    return "idle";
}

firc_group_list_t *firc_group_list_new(void)
{
    /* calloc: zero is "nothing has happened yet" for sync state, error text, and progress */
    firc_group_list_t *l = calloc(1, sizeof(firc_group_list_t));
    if (l == NULL) {
        return NULL;
    }
    if (firc_strset(&l->url, "") != FIRC_OK) {
        free(l);
        return NULL;
    }
    return l;
}

void firc_group_list_free(firc_group_list_t *l)
{
    if (l == NULL) {
        return;
    }
    firc_sub_rules_free(&l->rules);
    for (size_t i = 0; i < l->n_overrides; i++) {
        firc_sub_override_free(l->overrides[i]);
    }
    free(l->overrides);
    free(l->url);
    free(l);
}

void firc_sub_rules_init(firc_sub_rules_t *rs)
{
    memset(rs, 0, sizeof(*rs));
}

void firc_sub_rules_free(firc_sub_rules_t *rs)
{
    if (rs == NULL) {
        return;
    }
    free(rs->text);
    free(rs->v);
    memset(rs, 0, sizeof(*rs));
}

/* proto/ports non-empty (NULL and "" both mean none) selects the spec form */
static firc_err_t push_parts(firc_sub_rules_t *rs, const char *text, const char *type, bool enable,
                             firc_id_t id, const char *proto, const char *ports)
{
    bool spec = (proto != NULL && proto[0] != '\0') || (ports != NULL && ports[0] != '\0');
    size_t tl = strlen(text) + 1;
    size_t pl = spec ? strlen(proto != NULL ? proto : "") + 1 : 0;
    size_t ql = spec ? strlen(ports != NULL ? ports : "") + 1 : 0;
    size_t len = tl + pl + ql;
    if (rs->text_len + len > rs->text_cap) {
        size_t cap = rs->text_cap == 0 ? 4096 : rs->text_cap;
        while (cap < rs->text_len + len) {
            cap *= 2;
        }
        char *grown = realloc(rs->text, cap);
        if (grown == NULL) {
            return FIRC_ERR_NOMEM;
        }
        rs->text = grown;
        rs->text_cap = cap;
    }
    if (rs->n == rs->cap) {
        size_t cap = rs->cap == 0 ? 64 : rs->cap * 2;
        firc_sub_rule_ref_t *grown = realloc(rs->v, cap * sizeof(*grown));
        if (grown == NULL) {
            return FIRC_ERR_NOMEM;
        }
        rs->v = grown;
        rs->cap = cap;
    }
    if (rs->text_len > UINT32_MAX - len) {
        return FIRC_ERR_LIMIT;
    }
    char *at = rs->text + rs->text_len;
    memcpy(at, text, tl);
    if (spec) {
        memcpy(at + tl, proto != NULL ? proto : "", pl);
        memcpy(at + tl + pl, ports != NULL ? ports : "", ql);
    }
    firc_sub_rule_ref_t *r = &rs->v[rs->n];
    r->off = (uint32_t)rs->text_len;
    r->id = id;
    r->type = r->list_type = firc_rule_type_index(type);
    r->enable = enable;
    r->flags = spec ? FIRC_SUB_RULE_HAS_SPEC : 0;
    rs->text_len += len;
    rs->n++;
    return FIRC_OK;
}

firc_err_t firc_sub_rules_push(firc_sub_rules_t *rs, const char *text, const char *type, bool enable,
                               firc_id_t id)
{
    return push_parts(rs, text, type, enable, id, NULL, NULL);
}

firc_err_t firc_sub_rules_push_spec(firc_sub_rules_t *rs, const char *text, const char *type, bool enable,
                                    firc_id_t id, const char *proto, const char *ports)
{
    return push_parts(rs, text, type, enable, id, proto, ports);
}

void firc_sub_rules_shrink(firc_sub_rules_t *rs)
{
    /* a shrink that fails to realloc just keeps the slack; not an error */
    if (rs->text_len > 0 && rs->text_cap > rs->text_len) {
        char *t = realloc(rs->text, rs->text_len);
        if (t != NULL) {
            rs->text = t;
            rs->text_cap = rs->text_len;
        }
    }
    if (rs->n > 0 && rs->cap > rs->n) {
        firc_sub_rule_ref_t *v = realloc(rs->v, rs->n * sizeof(*v));
        if (v != NULL) {
            rs->v = v;
            rs->cap = rs->n;
        }
    }
}

void firc_sub_rules_move(firc_sub_rules_t *dst, firc_sub_rules_t *src)
{
    firc_sub_rules_free(dst);
    *dst = *src;
    memset(src, 0, sizeof(*src));
}

firc_err_t firc_sub_rules_copy(firc_sub_rules_t *dst, const firc_sub_rules_t *src)
{
    firc_sub_rules_t out;
    firc_sub_rules_init(&out);
    if (src->n > 0) {
        out.text = malloc(src->text_len);
        out.v = malloc(src->n * sizeof(*out.v));
        if (out.text == NULL || out.v == NULL) {
            firc_sub_rules_free(&out);
            return FIRC_ERR_NOMEM;
        }
        memcpy(out.text, src->text, src->text_len);
        memcpy(out.v, src->v, src->n * sizeof(*out.v));
        out.text_len = out.text_cap = src->text_len;
        out.n = out.cap = src->n;
    }
    firc_sub_rules_move(dst, &out);
    return FIRC_OK;
}

bool firc_sub_rules_find_id(const firc_sub_rules_t *rs, firc_id_t id, size_t *out_idx)
{
    for (size_t i = 0; i < rs->n; i++) {
        if (firc_id_equal(rs->v[i].id, id)) {
            *out_idx = i;
            return true;
        }
    }
    return false;
}

firc_sub_override_t *firc_sub_override_new(void)
{
    return calloc(1, sizeof(firc_sub_override_t));
}

void firc_sub_override_free(firc_sub_override_t *o)
{
    if (o == NULL) {
        return;
    }
    free(o->rule);
    free(o->list_type);
    free(o->proto);
    free(o->ports);
    free(o->type);
    free(o);
}

static void override_remove_at(firc_group_list_t *l, size_t at)
{
    firc_sub_override_free(l->overrides[at]);
    for (size_t i = at + 1; i < l->n_overrides; i++) {
        l->overrides[i - 1] = l->overrides[i];
    }
    l->n_overrides--;
}

/* NULL and "" are the same spec everywhere a key is compared */
static bool spec_same(const char *a, const char *b)
{
    return strcmp(a != NULL ? a : "", b != NULL ? b : "") == 0;
}

/* text/proto/ports match exactly; list_type is the caller's to compare */
static bool override_spec_equal(const firc_sub_override_t *o, const firc_sub_rule_key_t *k)
{
    return strcmp(o->rule, k->text) == 0 && spec_same(o->proto, k->proto) &&
           spec_same(o->ports, k->ports);
}

/* exact key match including list_type: naming a type stores its own entry, never widens another's */
static bool override_key_equal(const firc_sub_override_t *o, const firc_sub_rule_key_t *k)
{
    return override_spec_equal(o, k) && spec_same(o->list_type, k->list_type);
}

/* an entry with no list_type reaches every type of its text (legacy hand-written form); also the fallback match */
static bool override_matches_wildcard(const firc_sub_override_t *o, const firc_sub_rule_key_t *k)
{
    return o->list_type == NULL && override_spec_equal(o, k);
}

/* first-pass match: an entry naming this exact type */
static bool override_matches_exact_type(const firc_sub_override_t *o, const firc_sub_rule_key_t *k)
{
    return o->list_type != NULL && spec_same(o->list_type, k->list_type) && override_spec_equal(o, k);
}

const firc_sub_override_t *firc_group_list_find_override(const firc_group_list_t *l,
                                                         const firc_sub_rule_key_t *key)
{
    if (l == NULL || key == NULL || key->text == NULL) {
        return NULL;
    }
    /* two passes: an exact-type entry wins over a wildcard one regardless of array order */
    for (size_t i = 0; i < l->n_overrides; i++) {
        if (override_matches_exact_type(l->overrides[i], key)) {
            return l->overrides[i];
        }
    }
    for (size_t i = 0; i < l->n_overrides; i++) {
        if (override_matches_wildcard(l->overrides[i], key)) {
            return l->overrides[i];
        }
    }
    return NULL;
}

firc_err_t firc_group_list_set_override(firc_group_list_t *l, const firc_sub_rule_key_t *key,
                                        const char *type, const bool *enable)
{
    if (l == NULL || key == NULL || key->text == NULL || key->text[0] == '\0') {
        return FIRC_ERR_INVAL;
    }

    size_t at = 0;
    firc_sub_override_t *o = NULL;
    for (size_t i = 0; i < l->n_overrides; i++) {
        if (override_key_equal(l->overrides[i], key)) {
            o = l->overrides[i];
            at = i;
            break;
        }
    }
    bool has_list_type = key->list_type != NULL && key->list_type[0] != '\0';
    if (o == NULL && has_list_type) {
        /* a legacy entry (no list_type) adopts this type in place rather than risk a duplicate shadowing it */
        for (size_t i = 0; i < l->n_overrides; i++) {
            if (override_matches_wildcard(l->overrides[i], key)) {
                o = l->overrides[i];
                at = i;
                break;
            }
        }
        if (o != NULL && firc_strset(&o->list_type, key->list_type) != FIRC_OK) {
            return FIRC_ERR_NOMEM;
        }
    }
    if (o == NULL) {
        o = firc_sub_override_new();
        if (o == NULL) {
            return FIRC_ERR_NOMEM;
        }
        bool has_proto = key->proto != NULL && key->proto[0] != '\0';
        bool has_ports = key->ports != NULL && key->ports[0] != '\0';
        if (firc_strset(&o->rule, key->text) != FIRC_OK ||
            (has_list_type && firc_strset(&o->list_type, key->list_type) != FIRC_OK) ||
            (has_proto && firc_strset(&o->proto, key->proto) != FIRC_OK) ||
            (has_ports && firc_strset(&o->ports, key->ports) != FIRC_OK)) {
            firc_sub_override_free(o);
            return FIRC_ERR_NOMEM;
        }
        if (grow_array((void ***)&l->overrides, l->n_overrides) != FIRC_OK) {
            firc_sub_override_free(o);
            return FIRC_ERR_NOMEM;
        }
        at = l->n_overrides;
        l->overrides[l->n_overrides++] = o;
    }

    if (type != NULL) {
        if (type[0] == '\0') {
            free(o->type);
            o->type = NULL;
        } else if (firc_strset(&o->type, type) != FIRC_OK) {
            return FIRC_ERR_NOMEM;
        }
    }
    if (enable != NULL) {
        o->enable = *enable;
        o->has_enable = true;
    }

    /* an entry that says nothing new (no type, enabled) is dropped rather than kept as a no-op */
    if (o->type == NULL && (!o->has_enable || o->enable)) {
        override_remove_at(l, at);
    }
    return FIRC_OK;
}

void firc_sub_apply_overrides(const firc_group_list_t *l, firc_sub_rules_t *rules)
{
    if (l == NULL || rules == NULL || l->n_overrides == 0) {
        return;
    }
    /* linear in both rules and overrides: the override set is small, a lookup structure would cost more to build */
    for (size_t i = 0; i < rules->n; i++) {
        firc_sub_rule_key_t key = {.text = firc_sub_rules_text(rules, i),
                                   .list_type = firc_sub_rules_list_type(rules, i),
                                   .proto = firc_sub_rules_proto(rules, i),
                                   .ports = firc_sub_rules_ports(rules, i)};
        const firc_sub_override_t *o = firc_group_list_find_override(l, &key);
        if (o == NULL) {
            continue;
        }
        if (o->type != NULL) {
            firc_sub_rules_set_type(rules, i, o->type);
        }
        if (o->has_enable) {
            rules->v[i].enable = o->enable;
        }
    }
}

firc_err_t firc_app_config_init_defaults(firc_app_config_t *c)
{
    memset(c, 0, sizeof(*c));
    c->http_web.enabled = true;
    c->http_web.host.port = 666;
    c->dns_proxy.host.port = 3553;
    c->dns_proxy.upstream.port = 53;
    c->dns_proxy.disable_remap53 = false;
    c->dns_proxy.disable_drop_aaaa = false;
    c->dns_proxy.disable_stats = false;
    c->dns_proxy.max_idle_conns = 10;
    c->dns_proxy.max_concurrent = 100;
    c->dns_proxy.timeout = 5000 * FIRC_DURATION_MS;
    c->dns_proxy.unmatched_ttl = 60 * FIRC_DURATION_SEC;
    c->netfilter.disable_ipv4 = false;
    c->netfilter.disable_ipv6 = false;
    c->netfilter.start_mark_table_index = UINT32_C(0x66697263); /* "firc" */
    c->update.check = false;
    c->check.interval = 30 * FIRC_DURATION_SEC;
    c->check.timeout = 5 * FIRC_DURATION_SEC;
    c->autolist.enabled = false;
    c->autolist.fail_threshold = 3;
    c->autolist.fail_time = 60 * FIRC_DURATION_SEC;
    c->autolist.retrans_threshold = 3;
    c->autolist.recheck = false;
    c->autolist.recheck_interval = 6 * 3600 * FIRC_DURATION_SEC;

    firc_err_t err;
    if ((err = firc_strset(&c->http_web.host.address, "[::]")) != FIRC_OK ||
        (err = firc_strset(&c->dns_proxy.host.address, "[::]")) != FIRC_OK ||
        (err = firc_strset(&c->dns_proxy.upstream.address, "127.0.0.1")) !=
            FIRC_OK ||
        (err = firc_strset(&c->netfilter.iptables.chain_prefix, "FIRC_")) !=
            FIRC_OK ||
        (err = firc_strset(&c->fakeip.v4.pool, "198.18.0.0/15")) != FIRC_OK ||
        (err = firc_strset(&c->fakeip.v6.pool, "")) != FIRC_OK ||
        (err = firc_strset(&c->check.url, "http://cp.cloudflare.com/generate_204")) != FIRC_OK ||
        (err = firc_strset(&c->check.wan_url, "")) != FIRC_OK ||
        (err = firc_strset(&c->autolist.group, "")) != FIRC_OK ||
        (err = firc_strset(&c->autolist.mode, "candidates")) != FIRC_OK ||
        (err = firc_strset(&c->autolist.check_iface, "")) != FIRC_OK) {
        firc_app_config_clear(c);
        return err;
    }

    c->link = calloc(1, sizeof(char *));
    if (c->link == NULL) {
        firc_app_config_clear(c);
        return FIRC_ERR_NOMEM;
    }
    c->link[0] = strdup("br0");
    if (c->link[0] == NULL) {
        firc_app_config_clear(c);
        return FIRC_ERR_NOMEM;
    }
    c->n_link = 1;

    c->fakeip.v4.chunk = 24;
    c->fakeip.v6.chunk = 64;
    c->fakeip.ttl_clamp = 300 * FIRC_DURATION_SEC;
    c->fakeip.max_names = 65536;
    return FIRC_OK;
}

void firc_app_config_clear(firc_app_config_t *c)
{
    free(c->http_web.host.address);
    free(c->dns_proxy.host.address);
    free(c->dns_proxy.upstream.address);
    free(c->fakeip.v4.pool);
    free(c->fakeip.v6.pool);
    free(c->netfilter.iptables.chain_prefix);
    free(c->check.url);
    free(c->check.wan_url);
    for (size_t i = 0; i < c->n_link; i++) {
        free(c->link[i]);
    }
    free(c->link);
    free(c->autolist.group);
    free(c->autolist.check_iface);
    free(c->autolist.mode);
    for (size_t i = 0; i < c->autolist.n_exclude; i++) {
        free(c->autolist.exclude[i]);
    }
    free(c->autolist.exclude);
    memset(c, 0, sizeof(*c));
}

firc_err_t firc_config_init_defaults(firc_config_t *c)
{
    memset(c, 0, sizeof(*c));
    return firc_app_config_init_defaults(&c->app);
}

void firc_config_clear(firc_config_t *c)
{
    firc_app_config_clear(&c->app);
    for (size_t i = 0; i < c->n_groups; i++) {
        firc_group_free(c->groups[i]);
    }
    free(c->groups);
    memset(c, 0, sizeof(*c));
}

firc_err_t firc_config_add_group(firc_config_t *c, firc_group_t *g)
{
    firc_err_t err = grow_array((void ***)&c->groups, c->n_groups);
    if (err != FIRC_OK) {
        return err;
    }
    c->groups[c->n_groups++] = g;
    return FIRC_OK;
}

void firc_config_remove_group_by_index(firc_config_t *c, size_t idx)
{
    firc_group_free(c->groups[idx]);
    for (size_t i = idx; i + 1 < c->n_groups; i++) {
        c->groups[i] = c->groups[i + 1];
    }
    c->n_groups--;
}

void firc_config_clear_groups(firc_config_t *c)
{
    for (size_t i = 0; i < c->n_groups; i++) {
        firc_group_free(c->groups[i]);
    }
    free(c->groups);
    c->groups = NULL;
    c->n_groups = 0;
}

bool firc_group_has_iface(const firc_group_t *g, const char *name)
{
    if (name == NULL) { return false; }
    for (size_t i = 0; i < firc_group_n_ifaces(g); i++) {
        if (strcmp(firc_group_iface_at(g, i), name) == 0) { return true; }
    }
    return false;
}

bool firc_group_is_block(const firc_group_t *g)
{
    return g->n_iface_rest == 0 && g->iface != NULL && strcmp(g->iface, FIRC_IFACE_BLOCK) == 0;
}

const char *firc_block_answer_name(firc_block_answer_t a)
{
    return a == FIRC_BLOCK_NXDOMAIN ? "nxdomain" : a == FIRC_BLOCK_REFUSED ? "refused" : "null";
}

bool firc_block_answer_parse(const char *s, firc_block_answer_t *out)
{
    static const firc_block_answer_t all[] = {FIRC_BLOCK_NULL, FIRC_BLOCK_NXDOMAIN, FIRC_BLOCK_REFUSED};
    if (s == NULL) { return false; }
    for (size_t i = 0; i < sizeof(all) / sizeof(all[0]); i++) {
        if (strcmp(s, firc_block_answer_name(all[i])) == 0) {
            *out = all[i];
            return true;
        }
    }
    return false;
}

const char *firc_group_ifaces_check(const char *const *names, size_t n, size_t *bad)
{
    *bad = 0;
    if (n < 1 || n > FIRC_GROUP_IFACES_MAX) { return "a group names 1 to 4 interfaces"; }
    for (size_t i = 0; i < n; i++) {
        *bad = i;
        if (names[i] == NULL || !firc_is_interface_name(names[i])) { return "not an interface name"; }
        if (n > 1 && strcmp(names[i], FIRC_IPSET_TO_LINK_BLACKHOLE) == 0) { return "blackhole cannot be one of several"; }
        if (n > 1 && strcmp(names[i], FIRC_IFACE_BLOCK) == 0) { return "block cannot be one of several"; }
        for (size_t k = 0; k < i; k++) {
            if (strcmp(names[k], names[i]) == 0) { return "named twice"; }
        }
    }
    return NULL;
}

firc_err_t firc_group_set_ifaces(firc_group_t *g, const char *const *names, size_t n)
{
    if (n < 1 || n > FIRC_GROUP_IFACES_MAX) { return FIRC_ERR_INVAL; }
    char *copy[FIRC_GROUP_IFACES_MAX] = {NULL};
    for (size_t i = 0; i < n; i++) {
        copy[i] = strdup(names[i]);
        if (copy[i] == NULL) {
            for (size_t k = 0; k < i; k++) { free(copy[k]); }
            return FIRC_ERR_NOMEM;
        }
    }
    free(g->iface);
    for (size_t i = 0; i < g->n_iface_rest; i++) { free(g->iface_rest[i]); }
    g->iface = copy[0];
    for (size_t i = 1; i < FIRC_GROUP_IFACES_MAX; i++) { g->iface_rest[i - 1] = copy[i]; }
    g->n_iface_rest = n - 1;
    return FIRC_OK;
}

bool firc_config_selects_devices(const firc_config_t *c)
{
    for (size_t i = 0; i < c->n_groups; i++) {
        const firc_group_t *g = c->groups[i];
        if (g->enable && (g->devices.n_allow > 0 || g->devices.n_deny > 0)) { return true; }
    }
    return false;
}
