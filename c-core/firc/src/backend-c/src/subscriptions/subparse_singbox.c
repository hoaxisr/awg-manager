#include <cjson/cJSON.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "firc/models.h"
#include "firc/subnet.h"
#include "subparse_internal.h"

/* ponytail: streaming parser if a list outgrows the fetch cap's share of a 256 MB router */

/* bounds cJSON's tree; measured: a value-dense body peaks cJSON near 40x its size, enough to OOM-kill the daemon */
#define FIRC_SUB_SINGBOX_MAX_VALUES 400000
/* two-step stringize: one step alone would paste the macro's NAME, not its value, into the refusal text */
#define FIRC_SUB_SINGBOX_STR_(x) #x
#define FIRC_SUB_SINGBOX_STR(x) FIRC_SUB_SINGBOX_STR_(x)

static const char *const k_known[] = {"domain", "domain_suffix", "domain_keyword", "domain_regex",
                                      "ip_cidr", "network", "port", "port_range", "type", "invert", NULL};

/* a field is one value or an array of them; calls fn per value, a wrong-kind value fails the field */
typedef bool (*value_fn)(const cJSON *v, void *ud);
static bool each_value(const cJSON *f, value_fn fn, void *ud) {
    if (!cJSON_IsArray(f)) { return fn(f, ud); }
    const cJSON *v;
    cJSON_ArrayForEach(v, f) {
        if (!fn(v, ud)) { return false; }
    }
    return true;
}

static bool object_is_honourable(const cJSON *o) {
    /* cJSON returns the FIRST repeated key, sing-box keeps the LAST; reading the first could widen a rule */
    int seen[sizeof(k_known) / sizeof(k_known[0]) - 1] = {0};
    const cJSON *f;
    cJSON_ArrayForEach(f, o) {
        bool known = false;
        for (size_t i = 0; k_known[i] != NULL; i++) {
            if (strcmp(f->string, k_known[i]) == 0) {
                known = true;
                if (++seen[i] > 1) { return false; }
            }
        }
        if (!known) { return false; }
    }
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(o, "type");
    if (type != NULL && !(cJSON_IsString(type) && strcmp(type->valuestring, "default") == 0)) { return false; }
    const cJSON *inv = cJSON_GetObjectItemCaseSensitive(o, "invert");
    if (inv != NULL && !cJSON_IsFalse(inv)) { return false; }
    return true;
}

bool firc_sub_net_add(int *net, const char *s) {
    if (strcmp(s, "tcp") == 0) { *net |= FIRC_SUB_NET_TCP; return true; }
    if (strcmp(s, "udp") == 0) { *net |= FIRC_SUB_NET_UDP; return true; }
    return false;
}

static bool net_value(const cJSON *v, void *ud) {
    return cJSON_IsString(v) && firc_sub_net_add(ud, v->valuestring);
}

bool firc_sub_ports_add(firc_sub_ports_t *p, long a, long b) {
    if (a < 1 || b > 65535 || a > b || p->n == FIRC_SUB_PORT_ITEMS) { return false; }
    /* recast to int: -Wformat-truncation sizes "%ld" against long's range regardless of the bounds check above */
    int ai = (int)a, bi = (int)b;
    if (ai == bi) { snprintf(p->item[p->n], sizeof(p->item[0]), "%d", ai); p->cost[p->n] = 1; }
    else { snprintf(p->item[p->n], sizeof(p->item[0]), "%d-%d", ai, bi); p->cost[p->n] = 2; }
    p->n++;
    return true;
}

static bool port_value(const cJSON *v, void *ud) {
    /* Range first, cast after: a double out of long's range is UB to cast. */
    if (!cJSON_IsNumber(v) || v->valuedouble < 1 || v->valuedouble > 65535 ||
        v->valuedouble != (double)(long)v->valuedouble) {
        return false;
    }
    return firc_sub_ports_add(ud, (long)v->valuedouble, (long)v->valuedouble);
}

/* "a:b", "a:" (to 65535), ":b" (from 1). */
bool firc_sub_ports_add_range(firc_sub_ports_t *p, const char *s) {
    const char *colon = strchr(s, ':');
    if (colon == NULL || strchr(colon + 1, ':') != NULL) { return false; }
    char *end;
    long a = 1, b = 65535;
    if (colon != s) {
        a = strtol(s, &end, 10);
        if (end != colon) { return false; }
    }
    if (colon[1] != '\0') {
        b = strtol(colon + 1, &end, 10);
        if (*end != '\0') { return false; }
    }
    return firc_sub_ports_add(p, a, b);
}

static bool range_value(const cJSON *v, void *ud) {
    return cJSON_IsString(v) && firc_sub_ports_add_range(ud, v->valuestring);
}

static firc_err_t tick(firc_sub_walk_t *w) {
    w->values++;
    if (w->progress != NULL && w->values % FIRC_SUB_PARSE_PROGRESS_LINES == 0 && !w->progress(w->ud, w->values)) {
        return FIRC_ERR_CANCELED;
    }
    return FIRC_OK;
}

/* One value into the sink; a value the sink refuses is one drop. */
static firc_err_t take(firc_sub_walk_t *w, const char *text, const char *type, const char *proto,
                       const char *ports, bool counts_unconstrained) {
    w->text += strlen(text) + 1;
    if (w->text_cap != 0 && w->text > w->text_cap) {
        w->st->why = "sing-box rule-set too large: its rules hold more text than a list may";
        return FIRC_ERR_INVAL;
    }
    char *copy = strdup(text);
    if (copy == NULL) { return FIRC_ERR_NOMEM; }
    bool took = false;
    firc_err_t err = firc_sub_sink_take(w->sink, copy, type, proto, ports, &took);
    free(copy);
    if (err != FIRC_OK) { return err; }
    if (!took) { w->st->dropped++; }
    else if (counts_unconstrained) { w->st->unconstrained++; }
    return tick(w);
}

/* a dropped object ticks too, so a list of nothing but junk can still be cancelled */
firc_err_t firc_sub_walk_drop(firc_sub_walk_t *w) {
    w->st->dropped++;
    return tick(w);
}

firc_err_t firc_sub_walk_name(firc_sub_walk_t *w, const char *s, const char *type, bool constrained) {
    char buf[300];
    if (strcmp(type, FIRC_RULE_NAMESPACE) == 0 && s[0] == '.') { s++; }
    if (strcmp(type, FIRC_RULE_WILDCARD) == 0) {
        if (!firc_sub_keyword_wildcard(s, buf, sizeof(buf))) { return firc_sub_walk_drop(w); }
        s = buf;
    }
    return take(w, s, type, NULL, NULL, constrained);
}

typedef struct names_ud { firc_sub_walk_t *w; const char *type; bool constrained; firc_err_t err; } names_ud_t;

static bool name_value(const cJSON *v, void *ud) {
    names_ud_t *n = ud;
    n->err = cJSON_IsString(v) ? firc_sub_walk_name(n->w, v->valuestring, n->type, n->constrained)
                               : firc_sub_walk_drop(n->w);
    return n->err == FIRC_OK;
}

void firc_sub_cidr_plan(firc_sub_cidr_plan_t *c, int net, const firc_sub_ports_t *ports) {
    c->n_protos = 0;
    c->n_chunks = 0;
    bool both = net == FIRC_SUB_NET_ANY || net == (FIRC_SUB_NET_TCP | FIRC_SUB_NET_UDP);
    if (ports->n == 0) {
        c->protos[c->n_protos++] = both ? NULL : (net == FIRC_SUB_NET_TCP ? "tcp" : "udp");
        c->chunks[c->n_chunks++] = NULL;
        return;
    }
    if (both || net == FIRC_SUB_NET_TCP) { c->protos[c->n_protos++] = "tcp"; }
    if (both || net == FIRC_SUB_NET_UDP) { c->protos[c->n_protos++] = "udp"; }
    /* at most FIRC_PORTS_MAX_SLOTS slots per rule */
    for (size_t i = 0, slots = 0; i < ports->n; i++) {
        if (c->n_chunks == 0 || slots + (size_t)ports->cost[i] > FIRC_PORTS_MAX_SLOTS) {
            size_t k = c->n_chunks++;
            c->bufs[k][0] = '\0';
            c->chunks[k] = c->bufs[k];
            slots = 0;
        }
        char *b = c->bufs[c->n_chunks - 1];
        size_t len = strlen(b);
        snprintf(b + len, FIRC_PORTS_STR_MAX - len, "%s%s", len ? "," : "", ports->item[i]);
        slots += (size_t)ports->cost[i];
    }
}

firc_err_t firc_sub_walk_cidr(firc_sub_walk_t *w, const firc_sub_cidr_plan_t *c, const char *text) {
    const char *type = strchr(text, ':') != NULL ? FIRC_RULE_SUBNET6 : FIRC_RULE_SUBNET;
    for (size_t p = 0; p < c->n_protos; p++) {
        for (size_t k = 0; k < c->n_chunks; k++) {
            firc_err_t err = take(w, text, type, c->protos[p], c->chunks[k], false);
            if (err != FIRC_OK) { return err; }
        }
    }
    return FIRC_OK;
}

typedef struct cidr_ud { firc_sub_walk_t *w; const firc_sub_cidr_plan_t *plan; firc_err_t err; } cidr_ud_t;

static bool cidr_value(const cJSON *v, void *ud) {
    cidr_ud_t *c = ud;
    c->err = cJSON_IsString(v) ? firc_sub_walk_cidr(c->w, c->plan, v->valuestring) : firc_sub_walk_drop(c->w);
    return c->err == FIRC_OK;
}

static firc_err_t walk_object(firc_sub_walk_t *w, const cJSON *o) {
    if (!cJSON_IsObject(o) || !object_is_honourable(o)) { return firc_sub_walk_drop(w); }
    int net = FIRC_SUB_NET_ANY;
    firc_sub_ports_t ports = {.n = 0};
    const cJSON *f;
    if ((f = cJSON_GetObjectItemCaseSensitive(o, "network")) != NULL && !each_value(f, net_value, &net)) {
        return firc_sub_walk_drop(w);
    }
    if (((f = cJSON_GetObjectItemCaseSensitive(o, "port")) != NULL && !each_value(f, port_value, &ports)) ||
        ((f = cJSON_GetObjectItemCaseSensitive(o, "port_range")) != NULL && !each_value(f, range_value, &ports))) {
        return firc_sub_walk_drop(w); /* includes more than 64 port items */
    }
    bool constrained = net != FIRC_SUB_NET_ANY || ports.n > 0;

    static const struct { const char *key; const char *type; } names[] = {
        {"domain", FIRC_RULE_DOMAIN}, {"domain_suffix", FIRC_RULE_NAMESPACE},
        {"domain_keyword", FIRC_RULE_WILDCARD}, {"domain_regex", FIRC_RULE_REGEX}};
    bool had_field = false;
    for (size_t i = 0; i < sizeof(names) / sizeof(names[0]); i++) {
        if ((f = cJSON_GetObjectItemCaseSensitive(o, names[i].key)) == NULL) { continue; }
        had_field = true;
        names_ud_t n = {w, names[i].type, constrained, FIRC_OK};
        (void)each_value(f, name_value, &n);
        if (n.err != FIRC_OK) { return n.err; }
    }

    if ((f = cJSON_GetObjectItemCaseSensitive(o, "ip_cidr")) == NULL) {
        /* honourable but with no name and no ip_cidr (e.g. {} or {"network":"udp"}) routes nothing */
        return had_field ? FIRC_OK : firc_sub_walk_drop(w);
    }
    firc_sub_cidr_plan_t plan;
    firc_sub_cidr_plan(&plan, net, &ports);
    cidr_ud_t c = {w, &plan, FIRC_OK};
    (void)each_value(f, cidr_value, &c);
    return c.err;
}

/* a cheap upper bound on JSON values, counted without building the tree; backslash-escapes are tracked inside strings */
static size_t count_json_values(const char *body) {
    size_t n = 0;
    bool in_string = false, escaped = false;
    for (const unsigned char *p = (const unsigned char *)body; *p != '\0'; p++) {
        if (in_string) {
            if (escaped) { escaped = false; }
            else if (*p == '\\') { escaped = true; }
            else if (*p == '"') { in_string = false; }
            continue;
        }
        if (*p == '"') { in_string = true; continue; }
        if (*p == '{' || *p == '[' || *p == ',') { n++; }
    }
    return n;
}

firc_err_t firc_sub_parse_singbox(const char *body, firc_sub_sink_t *sink, firc_sub_parse_stats_t *st,
                                  firc_sub_parse_progress_fn progress, void *ud)
{
    if (count_json_values(body) > FIRC_SUB_SINGBOX_MAX_VALUES) {
        st->why = "sing-box list too large: more than " FIRC_SUB_SINGBOX_STR(
            FIRC_SUB_SINGBOX_MAX_VALUES) " JSON values";
        return FIRC_ERR_INVAL;
    }
    /* require_null_terminated: cJSON_Parse would otherwise stop at the first value and ignore trailing garbage */
    cJSON *root = cJSON_ParseWithOpts(body, NULL, 1);
    const cJSON *rules = cJSON_IsObject(root) ? cJSON_GetObjectItemCaseSensitive(root, "rules") : NULL;
    if (!cJSON_IsArray(rules)) {
        cJSON_Delete(root);
        st->why = "not a sing-box rule-set: a JSON list needs a \"rules\" array";
        return FIRC_ERR_INVAL;
    }
    firc_sub_walk_t w = {sink, st, progress, ud, 0, 0, 0};
    firc_err_t err = FIRC_OK;
    const cJSON *o;
    cJSON_ArrayForEach(o, rules) {
        if ((err = walk_object(&w, o)) != FIRC_OK) { break; }
    }
    cJSON_Delete(root);
    return err;
}
