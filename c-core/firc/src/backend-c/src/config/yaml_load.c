#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <yaml.h>

#include "firc/log.h"
#include "firc/match.h"
#include <stddef.h>

#include "firc/fakeip.h"
#include "firc/regdomain.h"
#include "firc/resolver_addr.h"
#include "firc/settings.h"
#include "firc/yamlio.h"
#include "yaml_scalar.h"

typedef yaml_node_t node_t;

static node_t *doc_root(yaml_document_t *doc)
{
    return yaml_document_get_root_node(doc);
}

static node_t *node_of(yaml_document_t *doc, yaml_node_item_t idx)
{
    return yaml_document_get_node(doc, idx);
}

static const char *scalar_text(const node_t *n)
{
    return (const char *)n->data.scalar.value;
}

static bool scalar_is_plain(const node_t *n)
{
    return n->data.scalar.style == YAML_PLAIN_SCALAR_STYLE;
}

/* a scalar counts as "null" (absent) when it is plain and resolves to null */
static bool node_is_null(const node_t *n)
{
    return n->type == YAML_SCALAR_NODE && scalar_is_plain(n) &&
           firc_yaml_resolve_plain(scalar_text(n)).kind == FIRC_SCALAR_NULL;
}

/* map lookup by key (scalar keys only) */
static node_t *map_get(yaml_document_t *doc, node_t *map, const char *key)
{
    if (map->type != YAML_MAPPING_NODE) {
        return NULL;
    }
    for (yaml_node_pair_t *p = map->data.mapping.pairs.start;
         p < map->data.mapping.pairs.top; p++) {
        node_t *k = node_of(doc, p->key);
        if (k != NULL && k->type == YAML_SCALAR_NODE &&
            strcmp(scalar_text(k), key) == 0) {
            return node_of(doc, p->value);
        }
    }
    return NULL;
}

/* warns once with up to three urls so the operator can recreate them before the key is dropped on save */
enum { SUBS_URLS_NAMED = 3, SUBS_URL_CLIP = 200 };

static void warn_ignored_subscriptions(yaml_document_t *doc, node_t *subs)
{
    static const char head[] = "config: the \"subscriptions\" key is no longer read -- a list "
                               "is part of a group now";
    if (subs->type != YAML_SEQUENCE_NODE) {
        if (node_is_null(subs)) {
            FIRC_WARN("%s; it holds no entries, nothing is lost", head);
        } else {
            FIRC_WARN("%s; its value is not a list and is ignored", head);
        }
        return;
    }
    size_t n = (size_t)(subs->data.sequence.items.top - subs->data.sequence.items.start);
    if (n == 0) {
        FIRC_WARN("%s; it holds no entries, nothing is lost", head);
        return;
    }
    const char *urls[SUBS_URLS_NAMED] = {"?", "?", "?"};
    size_t named = n < SUBS_URLS_NAMED ? n : SUBS_URLS_NAMED;
    for (size_t i = 0; i < named; i++) {
        node_t *item = node_of(doc, subs->data.sequence.items.start[i]);
        node_t *url = item != NULL ? map_get(doc, item, "url") : NULL;
        if (url != NULL && url->type == YAML_SCALAR_NODE) { urls[i] = scalar_text(url); }
    }
    char rest[48] = "";
    if (n > named) { snprintf(rest, sizeof(rest), " and %zu more", n - named); }
    FIRC_WARN("%s; its %zu %s ignored, recreate %s as a group with a list before the next Save "
              "drops the key: %.*s%s%.*s%s%.*s%s",
              head, n, n == 1 ? "entry is" : "entries are", n == 1 ? "it" : "them",
              SUBS_URL_CLIP, urls[0], named > 1 ? ", " : "", SUBS_URL_CLIP,
              named > 1 ? urls[1] : "", named > 2 ? ", " : "", SUBS_URL_CLIP,
              named > 2 ? urls[2] : "", rest);
}

/* every getter: node NULL/null -> absent, type mismatch -> FIRC_ERR_INVAL */
typedef enum getter_res {
    GET_ABSENT = 0,
    GET_OK,
    GET_ERR,
} getter_res_t;

static getter_res_t get_string(node_t *n, char **dst, firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SCALAR_NODE) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    /* any scalar coerces to its literal text */
    if (firc_strset(dst, scalar_text(n)) != FIRC_OK) {
        *err = FIRC_ERR_NOMEM;
        return GET_ERR;
    }
    return GET_OK;
}

static getter_res_t get_bool(node_t *n, bool *dst, firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SCALAR_NODE || !scalar_is_plain(n)) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    firc_scalar_value_t v = firc_yaml_resolve_plain(scalar_text(n));
    if (v.kind != FIRC_SCALAR_BOOL) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    *dst = v.b;
    return GET_OK;
}

static getter_res_t get_uint64(node_t *n, uint64_t max, uint64_t *dst,
                               firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SCALAR_NODE || !scalar_is_plain(n)) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    firc_scalar_value_t v = firc_yaml_resolve_plain(scalar_text(n));
    uint64_t value;
    if (v.kind == FIRC_SCALAR_INT) {
        if (v.is_uint) {
            value = v.u;
        } else if (v.i < 0) {
            *err = FIRC_ERR_INVAL;
            return GET_ERR;
        } else {
            value = (uint64_t)v.i;
        }
    } else if (v.kind == FIRC_SCALAR_FLOAT && v.f >= 0 &&
               v.f == (double)(uint64_t)v.f) {
        /* an integral float (1.0) is accepted into a uint field */
        value = (uint64_t)v.f;
    } else {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    if (value > max) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    *dst = value;
    return GET_OK;
}

/* both address families are read identically; only the node name and fields differ */
static getter_res_t load_fakeip_family(yaml_document_t *doc, node_t *fip, const char *key,
                                       char **pool, uint8_t *chunk, firc_err_t *err)
{
    node_t *fam = map_get(doc, fip, key);
    if (fam == NULL || node_is_null(fam)) {
        return GET_ABSENT;
    }
    /* a scalar here (e.g. "v4: 198.18.0.0/15") would read as "no keys set" and silently lose intent */
    if (fam->type != YAML_MAPPING_NODE) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    if (get_string(map_get(doc, fam, "pool"), pool, err) == GET_ERR) {
        return GET_ERR;
    }
    uint64_t n;
    getter_res_t r = get_uint64(map_get(doc, fam, "chunk"), 128, &n, err);
    if (r == GET_ERR) {
        return GET_ERR;
    }
    if (r == GET_OK) {
        *chunk = (uint8_t)n;
    }
    return GET_OK;
}

static getter_res_t get_duration(node_t *n, firc_duration_t *dst, firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SCALAR_NODE) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    if (scalar_is_plain(n)) {
        firc_scalar_value_t v = firc_yaml_resolve_plain(scalar_text(n));
        if (v.kind == FIRC_SCALAR_INT) {
            *dst = v.is_uint ? (firc_duration_t)v.u : (firc_duration_t)v.i;
            return GET_OK;
        }
        if (v.kind != FIRC_SCALAR_STR) {
            *err = FIRC_ERR_INVAL;
            return GET_ERR;
        }
    }
    /* string (plain or quoted) */
    if (firc_duration_parse(scalar_text(n), dst) != FIRC_OK) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    return GET_OK;
}

static getter_res_t get_id(node_t *n, firc_id_t *dst, firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SCALAR_NODE) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    if (firc_id_parse(scalar_text(n), dst) != FIRC_OK) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    return GET_OK;
}

static getter_res_t get_string_list(yaml_document_t *doc, node_t *n,
                                    char ***dst, size_t *dst_n,
                                    firc_err_t *err)
{
    if (n == NULL || node_is_null(n)) {
        return GET_ABSENT;
    }
    if (n->type != YAML_SEQUENCE_NODE) {
        *err = FIRC_ERR_INVAL;
        return GET_ERR;
    }
    size_t count = (size_t)(n->data.sequence.items.top -
                            n->data.sequence.items.start);
    char **list = calloc(count > 0 ? count : 1, sizeof(char *));
    if (list == NULL) {
        *err = FIRC_ERR_NOMEM;
        return GET_ERR;
    }
    size_t idx = 0;
    for (yaml_node_item_t *it = n->data.sequence.items.start;
         it < n->data.sequence.items.top; it++) {
        node_t *item = node_of(doc, *it);
        if (item == NULL || item->type != YAML_SCALAR_NODE) {
            for (size_t i = 0; i < idx; i++) {
                free(list[i]);
            }
            free(list);
            *err = FIRC_ERR_INVAL;
            return GET_ERR;
        }
        list[idx] = strdup(scalar_text(item));
        if (list[idx] == NULL) {
            for (size_t i = 0; i < idx; i++) {
                free(list[i]);
            }
            free(list);
            *err = FIRC_ERR_NOMEM;
            return GET_ERR;
        }
        idx++;
    }
    for (size_t i = 0; i < *dst_n; i++) {
        free((*dst)[i]);
    }
    free(*dst);
    *dst = list;
    *dst_n = idx;
    return GET_OK;
}

#define GET_OR_FAIL(expr)          \
    do {                           \
        if ((expr) == GET_ERR) {   \
            return err;            \
        }                          \
    } while (0)

static firc_err_t load_addr_port(yaml_document_t *doc, node_t *n, char **addr,
                               uint16_t *port)
{
    firc_err_t err = FIRC_OK;
    if (n == NULL || node_is_null(n)) {
        return FIRC_OK;
    }
    if (n->type != YAML_MAPPING_NODE) {
        return FIRC_ERR_INVAL;
    }
    GET_OR_FAIL(get_string(map_get(doc, n, "address"), addr, &err));
    uint64_t p;
    getter_res_t r = get_uint64(map_get(doc, n, "port"), UINT16_MAX, &p, &err);
    if (r == GET_ERR) {
        return err;
    }
    if (r == GET_OK) {
        *port = (uint16_t)p;
    }
    return FIRC_OK;
}

static firc_err_t load_app(yaml_document_t *doc, node_t *app,
                         firc_app_config_t *c)
{
    firc_err_t err = FIRC_OK;
    if (app->type != YAML_MAPPING_NODE) {
        return FIRC_ERR_INVAL;
    }

    /* no skin key: the WebUI is always skins/default; an old file with it just loads as an unknown key */
    node_t *hw = map_get(doc, app, "httpWeb");
    if (hw != NULL && !node_is_null(hw)) {
        if (hw->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        GET_OR_FAIL(get_bool(map_get(doc, hw, "enabled"),
                             &c->http_web.enabled, &err));
        err = load_addr_port(doc, map_get(doc, hw, "host"),
                             &c->http_web.host.address,
                             &c->http_web.host.port);
        if (err != FIRC_OK) {
            return err;
        }
    }

    node_t *dp = map_get(doc, app, "dnsProxy");
    if (dp != NULL && !node_is_null(dp)) {
        if (dp->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        err = load_addr_port(doc, map_get(doc, dp, "upstream"),
                             &c->dns_proxy.upstream.address,
                             &c->dns_proxy.upstream.port);
        if (err != FIRC_OK) {
            return err;
        }
        err = load_addr_port(doc, map_get(doc, dp, "host"),
                             &c->dns_proxy.host.address,
                             &c->dns_proxy.host.port);
        if (err != FIRC_OK) {
            return err;
        }
        GET_OR_FAIL(get_bool(map_get(doc, dp, "disableRemap53"),
                             &c->dns_proxy.disable_remap53, &err));
        GET_OR_FAIL(get_bool(map_get(doc, dp, "disableDropAAAA"),
                             &c->dns_proxy.disable_drop_aaaa, &err));
        GET_OR_FAIL(get_bool(map_get(doc, dp, "disableStats"),
                             &c->dns_proxy.disable_stats, &err));
        GET_OR_FAIL(get_duration(map_get(doc, dp, "unmatchedTtl"),
                                 &c->dns_proxy.unmatched_ttl, &err));
        GET_OR_FAIL(get_uint64(map_get(doc, dp, "maxIdleConns"), UINT64_MAX,
                               &c->dns_proxy.max_idle_conns, &err));
        GET_OR_FAIL(get_uint64(map_get(doc, dp, "maxConcurrent"), UINT64_MAX,
                               &c->dns_proxy.max_concurrent, &err));
        firc_duration_t t;
        getter_res_t r = get_duration(map_get(doc, dp, "timeout"), &t, &err);
        if (r == GET_ERR) {
            return err;
        }
        if (r == GET_OK) {
            /* legacy: a value below 1ms is reinterpreted as milliseconds */
            if (t < FIRC_DURATION_MS) {
                t *= FIRC_DURATION_MS;
            }
            c->dns_proxy.timeout = t;
        }
    }

    node_t *nf = map_get(doc, app, "netfilter");
    if (nf != NULL && !node_is_null(nf)) {
        if (nf->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        node_t *ipt = map_get(doc, nf, "iptables");
        if (ipt != NULL && !node_is_null(ipt)) {
            if (ipt->type != YAML_MAPPING_NODE) {
                return FIRC_ERR_INVAL;
            }
            GET_OR_FAIL(get_string(map_get(doc, ipt, "chainPrefix"),
                                   &c->netfilter.iptables.chain_prefix,
                                   &err));
        }
        /* netfilter.ipset keys (tablePrefix, additionalTTL) are gone; an old file with them loads as unknown keys */
        GET_OR_FAIL(get_bool(map_get(doc, nf, "disableIPv4"),
                             &c->netfilter.disable_ipv4, &err));
        GET_OR_FAIL(get_bool(map_get(doc, nf, "disableIPv6"),
                             &c->netfilter.disable_ipv6, &err));
        uint64_t idx;
        getter_res_t r = get_uint64(map_get(doc, nf, "startMarkTableIndex"),
                                    UINT32_MAX, &idx, &err);
        if (r == GET_ERR) {
            return err;
        }
        if (r == GET_OK) {
            c->netfilter.start_mark_table_index = (uint32_t)idx;
        }
    }

    node_t *fip = map_get(doc, app, "addressPool");
    if (fip != NULL && !node_is_null(fip) && fip->type != YAML_MAPPING_NODE) {
        /* a scalar here would read as "no keys set" and get silently overwritten with defaults */
        return FIRC_ERR_INVAL;
    }
    if (fip != NULL) {
        GET_OR_FAIL(load_fakeip_family(doc, fip, "v4", &c->fakeip.v4.pool,
                                       &c->fakeip.v4.chunk, &err));
        GET_OR_FAIL(load_fakeip_family(doc, fip, "v6", &c->fakeip.v6.pool,
                                       &c->fakeip.v6.chunk, &err));
        GET_OR_FAIL(get_duration(map_get(doc, fip, "ttlClamp"), &c->fakeip.ttl_clamp, &err));
        uint64_t names;
        getter_res_t rn = get_uint64(map_get(doc, fip, "maxNames"), UINT32_MAX, &names, &err);
        if (rn == GET_ERR) {
            return err;
        }
        if (rn == GET_OK) {
            c->fakeip.max_names = (uint32_t)names;
        }

    }

    GET_OR_FAIL(get_string_list(doc, map_get(doc, app, "link"), &c->link,
                                &c->n_link, &err));
    node_t *upd = map_get(doc, app, "update");
    if (upd != NULL && !node_is_null(upd)) {
        if (upd->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        GET_OR_FAIL(get_bool(map_get(doc, upd, "check"), &c->update.check, &err));
    }
    node_t *cn = map_get(doc, app, "check");
    if (cn != NULL && !node_is_null(cn)) {
        if (cn->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        GET_OR_FAIL(get_string(map_get(doc, cn, "url"), &c->check.url, &err));
        GET_OR_FAIL(get_string(map_get(doc, cn, "wanUrl"), &c->check.wan_url, &err));
        GET_OR_FAIL(get_duration(map_get(doc, cn, "interval"), &c->check.interval, &err));
        GET_OR_FAIL(get_duration(map_get(doc, cn, "timeout"), &c->check.timeout, &err));
    }
    node_t *al = map_get(doc, app, "autolist");
    if (al != NULL && !node_is_null(al)) {
        if (al->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        GET_OR_FAIL(get_bool(map_get(doc, al, "enabled"), &c->autolist.enabled, &err));
        GET_OR_FAIL(get_string(map_get(doc, al, "group"), &c->autolist.group, &err));
        GET_OR_FAIL(get_string(map_get(doc, al, "mode"), &c->autolist.mode, &err));
        uint64_t v;
        getter_res_t r = get_uint64(map_get(doc, al, "failThreshold"), UINT32_MAX, &v, &err);
        if (r == GET_ERR) {
            return err;
        }
        if (r == GET_OK) {
            c->autolist.fail_threshold = (uint32_t)v;
        }
        GET_OR_FAIL(get_duration(map_get(doc, al, "failTime"), &c->autolist.fail_time, &err));
        r = get_uint64(map_get(doc, al, "retransThreshold"), UINT32_MAX, &v, &err);
        if (r == GET_ERR) {
            return err;
        }
        if (r == GET_OK) {
            c->autolist.retrans_threshold = (uint32_t)v;
        }
        GET_OR_FAIL(get_string_list(doc, map_get(doc, al, "exclude"), &c->autolist.exclude,
                                    &c->autolist.n_exclude, &err));
        GET_OR_FAIL(get_bool(map_get(doc, al, "recheck"), &c->autolist.recheck, &err));
        GET_OR_FAIL(get_duration(map_get(doc, al, "recheckInterval"), &c->autolist.recheck_interval, &err));
        GET_OR_FAIL(get_string(map_get(doc, al, "checkInterface"), &c->autolist.check_iface, &err));
    }
    /* runs unconditionally: a later overlay that omits a block must not leave an earlier value unchecked */
    const char *field = NULL, *why = NULL;
    firc_err_t chk = firc_app_config_check(c, &field, &why);
    if (chk != FIRC_OK) { FIRC_ERROR("config: %s: %s", field, why); }
    return chk;
}

static firc_err_t load_rule(yaml_document_t *doc, node_t *n, firc_rule_t *r)
{
    firc_err_t err = FIRC_OK;
    if (n->type != YAML_MAPPING_NODE) {
        return FIRC_ERR_INVAL;
    }
    GET_OR_FAIL(get_id(map_get(doc, n, "id"), &r->id, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "type"), &r->type, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "rule"), &r->rule, &err));
    GET_OR_FAIL(get_bool(map_get(doc, n, "enable"), &r->enable, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "proto"), &r->proto, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "ports"), &r->ports, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "comment"), &r->comment, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "autolist"), &r->autolist, &err));
    if (r->autolist != NULL && !firc_autolist_exclude_ok(r->autolist)) {
        free(r->autolist);
        r->autolist = NULL;
    }
    GET_OR_FAIL(get_bool(map_get(doc, n, "autolistByDomain"), &r->autolist_by_domain, &err));
    if (r->autolist == NULL) {
        r->autolist_by_domain = false;
    }
    if (r->type == NULL && (err = firc_strset(&r->type, "")) != FIRC_OK) {
        return err;
    }
    if (r->rule == NULL && (err = firc_strset(&r->rule, "")) != FIRC_OK) {
        return err;
    }
    return FIRC_OK;
}

/* absent/null devices is the empty selector; a bad entry is refused here, naming the group/key/entry */
static firc_err_t load_devices(yaml_document_t *doc, node_t *n, firc_devsel_spec_t *ds, const char *group_name)
{
    firc_err_t err = FIRC_OK;
    const char *group = group_name != NULL ? group_name : "";
    node_t *devices = map_get(doc, n, "devices");
    if (devices == NULL || node_is_null(devices)) {
        return FIRC_OK;
    }
    if (devices->type != YAML_MAPPING_NODE) {
        FIRC_ERROR("config: group \"%s\": devices is not a mapping of allow and deny", group);
        return FIRC_ERR_INVAL;
    }
    if (get_string_list(doc, map_get(doc, devices, "allow"), &ds->allow, &ds->n_allow, &err) == GET_ERR) {
        FIRC_ERROR("config: group \"%s\": devices.allow is not a list of device names", group);
        return err;
    }
    if (get_string_list(doc, map_get(doc, devices, "deny"), &ds->deny, &ds->n_deny, &err) == GET_ERR) {
        FIRC_ERROR("config: group \"%s\": devices.deny is not a list of device names", group);
        return err;
    }
    bool in_allow;
    const char *bad_entry = NULL, *why = NULL;
    if (firc_devsel_spec_check_canon(ds, &in_allow, &bad_entry, &why) != FIRC_OK) {
        FIRC_ERROR("config: group \"%s\": devices.%s entry \"%.64s\" %s", group, in_allow ? "allow" : "deny",
                   bad_entry != NULL ? bad_entry : "", why);
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

/* absent/null resolve is {tunnel:true, server:""}; a server list that doesn't parse is refused here */
static firc_err_t load_resolve(yaml_document_t *doc, node_t *n, firc_group_resolve_t *r,
                               const char *group_name)
{
    firc_err_t err = FIRC_OK;
    node_t *res = map_get(doc, n, "resolve");
    if (res == NULL || node_is_null(res)) { return FIRC_OK; }
    if (res->type != YAML_MAPPING_NODE) {
        FIRC_ERROR("config: group \"%s\": resolve is not a mapping of tunnel and server",
                   group_name != NULL ? group_name : "");
        return FIRC_ERR_INVAL;
    }
    GET_OR_FAIL(get_bool(map_get(doc, res, "tunnel"), &r->tunnel, &err));
    GET_OR_FAIL(get_string(map_get(doc, res, "server"), &r->server, &err));
    if (r->server != NULL && r->server[0] != '\0') {
        firc_resolver_list_t l;
        char why[256];
        if (firc_resolver_list_parse(r->server, &l, why, sizeof(why)) != FIRC_RESOLVER_LIST_OK) {
            FIRC_ERROR("config: group \"%s\": resolve.server %s", group_name != NULL ? group_name : "", why);
            return FIRC_ERR_INVAL;
        }
    }
    return FIRC_OK;
}

static firc_err_t load_block(yaml_document_t *doc, node_t *n, firc_group_t *g)
{
    node_t *b = map_get(doc, n, "block");
    if (b == NULL) { return FIRC_OK; }
    const char *name = g->name != NULL ? g->name : "";
    if (!firc_group_is_block(g)) {
        FIRC_ERROR("config: group \"%s\": block is only for a group whose interface is block", name);
        return FIRC_ERR_INVAL;
    }
    if (node_is_null(b)) { return FIRC_OK; }
    if (b->type != YAML_SCALAR_NODE || !firc_block_answer_parse(scalar_text(b), &g->block)) {
        FIRC_ERROR("config: group \"%s\": block must be null, nxdomain or refused", name);
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

/* url is required: a list with nothing to fetch is refused at load, naming the group */
static firc_err_t load_group_list(yaml_document_t *doc, node_t *n, firc_group_list_t *l,
                                  const char *group_name)
{
    firc_err_t err = FIRC_OK;
    GET_OR_FAIL(get_string(map_get(doc, n, "url"), &l->url, &err));
    if (l->url == NULL || l->url[0] == '\0') {
        FIRC_ERROR("config: group \"%s\" has a list with no url, which is nothing to fetch",
                   group_name != NULL ? group_name : "");
        return FIRC_ERR_INVAL;
    }
    uint64_t v;
    getter_res_t r = get_uint64(map_get(doc, n, "interval"), UINT32_MAX, &v, &err);
    if (r == GET_ERR) {
        return err;
    }
    if (r == GET_OK) {
        l->interval = (uint32_t)v;
    }
    r = get_uint64(map_get(doc, n, "last_update"), UINT32_MAX, &v, &err);
    if (r == GET_ERR) {
        return err;
    }
    if (r == GET_OK) {
        l->last_update = (uint32_t)v;
    }

    /* keyed by the rule's text, the type the list gave it, and its proto/ports */
    node_t *ovr = map_get(doc, n, "overrides");
    if (ovr != NULL && !node_is_null(ovr)) {
        if (ovr->type != YAML_SEQUENCE_NODE) {
            return FIRC_ERR_INVAL;
        }
        for (yaml_node_item_t *it = ovr->data.sequence.items.start;
             it < ovr->data.sequence.items.top; it++) {
            node_t *item = node_of(doc, *it);
            if (item == NULL || item->type != YAML_MAPPING_NODE) {
                return FIRC_ERR_INVAL;
            }
            char *text = NULL, *type = NULL, *list_type = NULL, *proto = NULL, *ports = NULL;
            bool on = true, has_on = false;
            err = FIRC_OK;
            if (get_string(map_get(doc, item, "rule"), &text, &err) == GET_ERR ||
                get_string(map_get(doc, item, "type"), &type, &err) == GET_ERR ||
                get_string(map_get(doc, item, "list_type"), &list_type, &err) == GET_ERR ||
                get_string(map_get(doc, item, "proto"), &proto, &err) == GET_ERR ||
                get_string(map_get(doc, item, "ports"), &ports, &err) == GET_ERR) {
                free(text);
                free(type);
                free(list_type);
                free(proto);
                free(ports);
                return err;
            }
            node_t *en = map_get(doc, item, "enable");
            if (en != NULL && !node_is_null(en)) {
                if (get_bool(en, &on, &err) == GET_ERR) {
                    free(text);
                    free(type);
                    free(list_type);
                    free(proto);
                    free(ports);
                    return err;
                }
                has_on = true;
            }
            if (type != NULL && type[0] != '\0') {
                const char *why = NULL;
                /* the spec check, not the bare one: a type must also fit the key's own proto/ports */
                if (!firc_rule_spec_is_usable(type, text != NULL ? text : "", proto, ports, &why)) {
                    if (why == firc_rule_why_nomem) {
                        free(text);
                        free(type);
                        free(list_type);
                        free(proto);
                        free(ports);
                        return FIRC_ERR_NOMEM;
                    }
                    FIRC_WARN("config: group \"%s\" list override for \"%s\" has a type the "
                              "daemon cannot use (%s); the type is dropped and the list's own "
                              "stands",
                              group_name != NULL ? group_name : "",
                              text != NULL ? text : "", why != NULL ? why : "unusable");
                    free(type);
                    type = NULL;
                }
            }
            /* folded unless it looks like a regex (metacharacter or saved list_type: regex) */
            bool key_is_regex = list_type != NULL && strcmp(list_type, FIRC_RULE_REGEX) == 0;
            if (text != NULL && !key_is_regex && strpbrk(text, "\\^$()[]{}|+") == NULL) {
                firc_rule_fold_name(text);
            }
            firc_sub_rule_key_t key = {.text = text, .list_type = list_type, .proto = proto, .ports = ports};
            err = firc_group_list_set_override(l, &key, type, has_on ? &on : NULL);
            free(text);
            free(type);
            free(list_type);
            free(proto);
            free(ports);
            if (err != FIRC_OK && err != FIRC_ERR_INVAL) {
                return err;
            }
        }
    }
    return FIRC_OK;
}

static firc_err_t load_ifaces(yaml_document_t *doc, node_t *n, firc_group_t *g)
{
    if (n == NULL || node_is_null(n)) { return firc_strset(&g->iface, ""); }
    if (n->type == YAML_SCALAR_NODE) { return firc_strset(&g->iface, scalar_text(n)); }
    if (n->type != YAML_SEQUENCE_NODE) { return FIRC_ERR_INVAL; }
    size_t count = (size_t)(n->data.sequence.items.top - n->data.sequence.items.start);
    const char *names[FIRC_GROUP_IFACES_MAX];
    size_t bad = 0;
    const char *why = count < 1 || count > FIRC_GROUP_IFACES_MAX ? "a group names 1 to 4 interfaces" : NULL;
    for (size_t i = 0; why == NULL && i < count; i++) {
        node_t *item = node_of(doc, n->data.sequence.items.start[i]);
        if (item == NULL || item->type != YAML_SCALAR_NODE) {
            bad = i;
            why = "not an interface name";
            break;
        }
        names[i] = scalar_text(item);
    }
    if (why == NULL) { why = firc_group_ifaces_check(names, count, &bad); }
    if (why != NULL) {
        FIRC_ERROR("config: group \"%s\": interface %zu: %s", g->name != NULL ? g->name : "", bad + 1, why);
        return FIRC_ERR_INVAL;
    }
    return firc_group_set_ifaces(g, names, count);
}

static firc_err_t load_group(yaml_document_t *doc, node_t *n, firc_group_t *g)
{
    firc_err_t err = FIRC_OK;
    if (n->type != YAML_MAPPING_NODE) {
        return FIRC_ERR_INVAL;
    }
    GET_OR_FAIL(get_id(map_get(doc, n, "id"), &g->id, &err));
    GET_OR_FAIL(get_string(map_get(doc, n, "name"), &g->name, &err));
    GET_OR_FAIL(get_bool(map_get(doc, n, "enable"), &g->enable, &err));
    if (g->name == NULL && (err = firc_strset(&g->name, "")) != FIRC_OK) {
        return err;
    }
    err = load_ifaces(doc, map_get(doc, n, "interface"), g);
    if (err != FIRC_OK) {
        return err;
    }

    err = load_devices(doc, n, &g->devices, g->name);
    if (err != FIRC_OK) {
        return err;
    }

    err = load_resolve(doc, n, &g->resolve, g->name);
    if (err != FIRC_OK) {
        return err;
    }

    err = load_block(doc, n, g);
    if (err != FIRC_OK) {
        return err;
    }

    node_t *rules = map_get(doc, n, "rules");
    if (rules != NULL && !node_is_null(rules)) {
        if (rules->type != YAML_SEQUENCE_NODE) {
            return FIRC_ERR_INVAL;
        }
        for (yaml_node_item_t *it = rules->data.sequence.items.start;
             it < rules->data.sequence.items.top; it++) {
            node_t *item = node_of(doc, *it);
            if (item == NULL) {
                return FIRC_ERR_INVAL;
            }
            firc_rule_t *rule = firc_rule_new();
            if (rule == NULL) {
                return FIRC_ERR_NOMEM;
            }
            err = load_rule(doc, item, rule);
            if (err == FIRC_OK) {
                /* WARNed, not refused: a config the daemon won't start for is worse than a dead rule */
                const char *why = NULL;
                if (!firc_rule_spec_is_usable(rule->type, rule->rule, rule->proto, rule->ports, &why)) {
                    FIRC_WARN("config: group \"%s\" has a rule the daemon cannot use and that routes "
                              "nothing -- %s (type \"%s\", rule \"%s\")",
                              g->name ? g->name : "", why ? why : "unusable",
                              rule->type ? rule->type : "", rule->rule ? rule->rule : "");
                }
                char *raw = rule->comment;
                rule->comment = NULL;
                err = firc_rule_set_comment(rule, raw, &why);
                free(raw);
                if (err == FIRC_ERR_INVAL) {
                    FIRC_WARN("config: group \"%s\", rule \"%s\" (type \"%s\"): %s; the comment is dropped",
                              g->name ? g->name : "", rule->rule, rule->type, why);
                    err = FIRC_OK;
                }
            }
            if (err == FIRC_OK) {
                err = firc_group_add_rule(g, rule);
            }
            if (err != FIRC_OK) {
                firc_rule_free(rule);
                return err;
            }
        }
    }

    node_t *list = map_get(doc, n, "list");
    if (list != NULL && !node_is_null(list)) {
        if (list->type != YAML_MAPPING_NODE) {
            return FIRC_ERR_INVAL;
        }
        firc_group_list_t *l = firc_group_list_new();
        if (l == NULL) {
            return FIRC_ERR_NOMEM;
        }
        err = load_group_list(doc, list, l, g->name);
        if (err != FIRC_OK) {
            firc_group_list_free(l);
            return err;
        }
        g->list = l;
    }
    return FIRC_OK;
}

static bool group_id_taken(firc_config_t *cfg, firc_group_t *g)
{
    for (size_t i = 0; i < cfg->n_groups; i++) {
        if (firc_id_equal(cfg->groups[i]->id, g->id)) {
            return true;
        }
    }
    return false;
}

firc_err_t firc_config_load_buffer(firc_config_t *cfg, const char *buf, size_t len)
{
    yaml_parser_t parser;
    yaml_document_t doc;
    if (!yaml_parser_initialize(&parser)) {
        return FIRC_ERR_NOMEM;
    }
    yaml_parser_set_input_string(&parser, (const unsigned char *)buf, len);
    if (!yaml_parser_load(&parser, &doc)) {
        yaml_parser_delete(&parser);
        return FIRC_ERR_PROTO; /* malformed YAML */
    }
    yaml_parser_delete(&parser);

    firc_err_t err = FIRC_OK;
    node_t *root = doc_root(&doc);

    if (root == NULL) {
        err = FIRC_ERR_STATE;
        goto out;
    }
    if (root->type != YAML_MAPPING_NODE) {
        err = FIRC_ERR_INVAL;
        goto out;
    }

    node_t *app = root != NULL ? map_get(&doc, root, "app") : NULL;
    if (app != NULL && !node_is_null(app)) {
        err = load_app(&doc, app, &cfg->app);
        if (err != FIRC_OK) {
            goto out;
        }
    }

    node_t *groups = root != NULL ? map_get(&doc, root, "groups") : NULL;
    if (groups != NULL && !node_is_null(groups)) {
        if (groups->type != YAML_SEQUENCE_NODE) {
            err = FIRC_ERR_INVAL;
            goto out;
        }
        /* replace existing groups */
        for (size_t i = 0; i < cfg->n_groups; i++) {
            firc_group_free(cfg->groups[i]);
        }
        cfg->n_groups = 0;
        cfg->groups_present = true;
        for (yaml_node_item_t *it = groups->data.sequence.items.start;
             it < groups->data.sequence.items.top; it++) {
            node_t *item = node_of(&doc, *it);
            if (item == NULL) {
                err = FIRC_ERR_INVAL;
                goto out;
            }
            firc_group_t *g = firc_group_new();
            if (g == NULL) {
                err = FIRC_ERR_NOMEM;
                goto out;
            }
            err = load_group(&doc, item, g);
            if (err == FIRC_OK && group_id_taken(cfg, g)) {
                err = FIRC_ERR_EXIST;
            }
            if (err == FIRC_OK) {
                err = firc_group_check_rule_ids(g);
            }
            if (err == FIRC_OK) {
                err = firc_config_add_group(cfg, g);
            }
            if (err != FIRC_OK) {
                firc_group_free(g);
                goto out;
            }
        }
    }

    /* the old subscriptions key has no loader (no migration); warned once so nothing is silently lost */
    node_t *subs = root != NULL ? map_get(&doc, root, "subscriptions") : NULL;
    if (subs != NULL) {
        warn_ignored_subscriptions(&doc, subs);
    }

out:
    yaml_document_delete(&doc);
    return err;
}

firc_err_t firc_config_load_file(firc_config_t *cfg, const char *path)
{
    FILE *f = fopen(path, "rbe");
    if (f == NULL) {
        /* NOENT only for a literal missing file; every other errno is a file that exists but couldn't be read */
        return errno == ENOENT ? FIRC_ERR_NOENT : firc_err_from_errno(errno);
    }
    if (fseek(f, 0, SEEK_END) != 0) {
        fclose(f);
        return FIRC_ERR_IO;
    }
    long size = ftell(f);
    if (size < 0) {
        fclose(f);
        return FIRC_ERR_IO;
    }
    if (fseek(f, 0, SEEK_SET) != 0) {
        fclose(f);
        return FIRC_ERR_IO;
    }
    char *buf = malloc((size_t)size + 1);
    if (buf == NULL) {
        fclose(f);
        return FIRC_ERR_NOMEM;
    }
    size_t got = fread(buf, 1, (size_t)size, f);
    fclose(f);
    if (got != (size_t)size) {
        free(buf);
        return FIRC_ERR_IO;
    }
    buf[size] = '\0';
    firc_err_t err = firc_config_load_buffer(cfg, buf, (size_t)size);
    free(buf);
    return err;
}
