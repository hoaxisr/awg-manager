#include "firc/groups.h"
#include "firc/match.h"

#include <stdlib.h>
#include <string.h>

#include <cjson/cJSON.h>

#include "firc/events.h"
#include "firc/id.h"
#include "firc/ifacename.h"
#include "firc/log.h"
#include "firc/json.h"
#include "firc/utf8.h"
#include "firc/resolve_check.h"
#include "firc/resolver_addr.h"
#include "firc/sub_fetch.h"

#include "groups_internal.h"

/* absent/null is unset, a string is the value, anything else sets *bad; get_string would silently widen the rule */
static const char *get_opt_string(const cJSON *obj, const char *key, bool *bad) {
    cJSON *item = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (item == NULL || cJSON_IsNull(item)) { return ""; }
    if (!cJSON_IsString(item)) { *bad = true; return ""; }
    return item->valuestring;
}

static const char why_not_strings[] = "proto and ports are strings";

static const char *comment_from_req(const cJSON *req, const char **why) {
    const cJSON *item = cJSON_GetObjectItemCaseSensitive(req, "comment");
    *why = NULL;
    if (item == NULL || cJSON_IsNull(item)) { return NULL; }
    if (!cJSON_IsString(item)) {
        *why = "comment is a string";
        return NULL;
    }
    *why = firc_rule_comment_why(item->valuestring);
    return item->valuestring;
}

/* list is config only here; the app's carry_list gate moves a live list's arena/sync state in when the url matches */

/* absent list keeps the group's own, as an absent devices/rules does; NULL on OOM */
static firc_group_list_t *list_config_copy(const firc_group_list_t *src) {
    firc_group_list_t *list = firc_group_list_new();
    if (list == NULL) { return NULL; }
    if (firc_strset(&list->url, src->url ? src->url : "") != FIRC_OK) {
        firc_group_list_free(list);
        return NULL;
    }
    list->interval = src->interval;
    list->last_update = src->last_update;
    for (size_t i = 0; i < src->n_overrides; i++) {
        const firc_sub_override_t *o = src->overrides[i];
        firc_sub_rule_key_t key = {o->rule, o->list_type, o->proto, o->ports};
        if (firc_group_list_set_override(list, &key, o->type,
                                         o->has_enable ? &o->enable : NULL) != FIRC_OK) {
            firc_group_list_free(list);
            return NULL;
        }
    }
    return list;
}

/* url is required; interval absent keeps the live list's own; overrides always come from existing, never the body */
static firc_err_t list_from_req(const cJSON *list_j, const firc_group_list_t *existing,
                               firc_group_list_t **out, const char **err_msg) {
    const char *url = get_string(list_j, "url");
    if (url[0] == '\0' || !firc_sub_url_is_supported(url)) {
        *err_msg = "invalid list";
        return FIRC_ERR_INVAL;
    }
    firc_group_list_t *list = firc_group_list_new();
    if (list == NULL) { return FIRC_ERR_NOMEM; }
    firc_err_t err = firc_strset(&list->url, url);
    if (err != FIRC_OK) {
        firc_group_list_free(list);
        return err;
    }
    cJSON *interval_j = cJSON_GetObjectItemCaseSensitive(list_j, "interval");
    if (cJSON_IsNumber(interval_j)) {
        uint64_t interval;
        if (!firc_json_whole(interval_j, 4294967295.0, &interval)) {
            firc_group_list_free(list);
            *err_msg = "list interval must be a whole number of seconds, 0 to 4294967295";
            return FIRC_ERR_INVAL;
        }
        list->interval = (uint32_t)interval;
    } else if (existing != NULL) {
        list->interval = existing->interval;
    }
    if (existing != NULL) {
        list->last_update = existing->last_update;
        for (size_t i = 0; i < existing->n_overrides; i++) {
            const firc_sub_override_t *o = existing->overrides[i];
            firc_sub_rule_key_t key = {o->rule, o->list_type, o->proto, o->ports};
            if (firc_group_list_set_override(list, &key, o->type,
                                             o->has_enable ? &o->enable : NULL) != FIRC_OK) {
                firc_group_list_free(list);
                return FIRC_ERR_NOMEM;
            }
        }
    }
    *out = list;
    return FIRC_OK;
}

#define FIRC_RULE_WHERE_MAX 384

static const char *locate_rule(char *buf, size_t len, const char *group_name, const char *type,
                               const char *pattern, int index, const char *why) {
    char num[16]; /* empty pattern identifies nothing, so the 1-based index is used instead */
    bool by_pattern = (pattern != NULL && *pattern != '\0');
    snprintf(num, sizeof(num), "%d", index + 1);
    const char *q = by_pattern ? "\"" : "";
    const char *which = by_pattern ? pattern : num;
    int wn = firc_utf8_clamp(which, 120); /* bounded so the whole message fits FIRC_RULE_WHERE_MAX */
    const char *cut = (by_pattern && which[wn] != '\0') ? "..." : ""; /* a truncated pattern must not read as one that exists */
    const char *t = (type != NULL && *type != '\0') ? type : "?";
    int tn = firc_utf8_clamp(t, 20); /* client-supplied too; must stay UTF-8-clamped like the pattern */
    if (group_name != NULL && *group_name != '\0') {
        snprintf(buf, len, "group \"%.*s\", rule %s%.*s%s%s (type \"%.*s\"): %.90s",
                 firc_utf8_clamp(group_name, 60), group_name, q, wn, which, cut, q, tn, t, why);
    } else {
        snprintf(buf, len, "rule %s%.*s%s%s (type \"%.*s\"): %.90s", q, wn, which, cut, q, tn, t,
                 why);
    }
    return buf;
}

/* why (a static string) is set only when the rule is refused as unusable; the caller turns that into a 400 */
static firc_err_t fill_rule_fields(firc_rule_t *rule, const cJSON *req, const char **why) {
    if (why != NULL) { *why = NULL; }
    if (firc_strset(&rule->type, get_string(req, "type")) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    if (firc_strset(&rule->rule, get_string(req, "rule")) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    bool bad = false;
    const char *proto = get_opt_string(req, "proto", &bad);
    const char *ports = get_opt_string(req, "ports", &bad);
    if (bad) {
        if (why != NULL) { *why = why_not_strings; }
        return FIRC_ERR_INVAL;
    }
    if (firc_strset(&rule->proto, proto) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    if (firc_strset(&rule->ports, ports) != FIRC_OK) { return FIRC_ERR_NOMEM; }
    if (!firc_rule_spec_is_usable(rule->type, rule->rule, rule->proto, rule->ports, why)) {
        return FIRC_ERR_INVAL;
    }
    const char *comment_why = NULL;
    const char *comment = comment_from_req(req, &comment_why);
    if (comment_why == NULL && firc_rule_set_comment(rule, comment, &comment_why) == FIRC_ERR_NOMEM) {
        return FIRC_ERR_NOMEM;
    }
    if (comment_why != NULL) {
        if (why != NULL) { *why = comment_why; }
        return FIRC_ERR_INVAL;
    }
    cJSON *enable_j = cJSON_GetObjectItemCaseSensitive(req, "enable"); /* absent/wrong-type/null all read as false */
    rule->enable = cJSON_IsBool(enable_j) && cJSON_IsTrue(enable_j);
    return FIRC_OK;
}

typedef struct {
    firc_id_set_t ids;
    bool zero;
} rule_ids_t;

static firc_err_t rule_ids_of(firc_rule_t **rules, size_t n, rule_ids_t *out) {
    *out = (rule_ids_t){0};
    firc_err_t err = FIRC_OK;
    for (size_t i = 0; err == FIRC_OK && i < n; i++) {
        if (firc_id_is_zero(rules[i]->id)) {
            out->zero = true;
        } else {
            err = firc_id_set_add(&out->ids, rules[i]->id);
        }
    }
    if (err != FIRC_OK) { firc_id_set_free(&out->ids); }
    return err;
}

static bool rule_ids_has(const rule_ids_t *k, firc_id_t id) {
    return firc_id_is_zero(id) ? k->zero : firc_id_set_contains(&k->ids, id);
}

/* an id in known keeps that identity; any other case silently gets a fresh random id */
static bool same_text(const char *a, const char *b) {
    return strcmp(a != NULL ? a : "", b != NULL ? b : "") == 0;
}

static firc_err_t carry_marker(firc_rule_t *built, const firc_group_t *stored) {
    if (stored == NULL) { return FIRC_OK; }
    for (size_t i = 0; i < stored->n_rules; i++) {
        const firc_rule_t *old = stored->rules[i];
        if (!firc_id_equal(old->id, built->id)) { continue; }
        if (old->autolist == NULL || old->enable != built->enable || !same_text(old->type, built->type) ||
            !same_text(old->rule, built->rule)) {
            return FIRC_OK;
        }
        built->autolist_by_domain = old->autolist_by_domain;
        return firc_strset(&built->autolist, old->autolist);
    }
    return FIRC_OK;
}

static firc_err_t rule_from_req(const cJSON *req, const rule_ids_t *known, const firc_group_t *stored,
                                firc_rule_t **out, const char **why) {
    firc_id_t id;
    bool has_id;
    bool found = false;
    if (parse_optional_id(req, "id", &id, &has_id) == FIRC_OK && has_id) { found = rule_ids_has(known, id); }
    if (why != NULL) { *why = NULL; }
    if (!found) {
        firc_err_t err = firc_id_random(&id);
        if (err != FIRC_OK) { return err; }
    }
    firc_rule_t *rule = firc_rule_new();
    if (!rule) { return FIRC_ERR_NOMEM; }
    rule->id = id;
    firc_err_t err = fill_rule_fields(rule, req, why);
    if (err == FIRC_OK) { err = carry_marker(rule, stored); }
    if (err != FIRC_OK) {
        firc_rule_free(rule);
        return err;
    }
    *out = rule;
    return FIRC_OK;
}

/* unlike rule_from_req, an explicit id not in known is FIRC_ERR_NOENT */
static firc_err_t rule_from_req_strict(const cJSON *req, const rule_ids_t *known, const firc_group_t *stored,
                                       firc_rule_t **out, const char **why) {
    firc_id_t id;
    bool has_id;
    if (parse_optional_id(req, "id", &id, &has_id) != FIRC_OK) { return FIRC_ERR_INVAL; }
    firc_id_t final_id;
    if (!has_id) {
        firc_err_t err = firc_id_random(&final_id);
        if (err != FIRC_OK) { return err; }
    } else {
        if (!rule_ids_has(known, id)) { return FIRC_ERR_NOENT; }
        final_id = id;
    }
    firc_rule_t *rule = firc_rule_new();
    if (!rule) { return FIRC_ERR_NOMEM; }
    rule->id = final_id;
    firc_err_t err = fill_rule_fields(rule, req, why);
    if (err == FIRC_OK) { err = carry_marker(rule, stored); }
    if (err != FIRC_OK) {
        firc_rule_free(rule);
        return err;
    }
    *out = rule;
    return FIRC_OK;
}

/* absent keeps the group's own (new group: tunnel on, no server); null is the default; an object replaces it */
static firc_err_t resolve_from_req(const cJSON *req, const firc_group_t *existing, const firc_fakeip_t *pool,
                                   firc_group_resolve_t *out, const char **err_msg, const char **err_field,
                                   char *msgbuf, size_t msgbuf_len) {
    const cJSON *r = cJSON_GetObjectItemCaseSensitive(req, "resolve");
    out->tunnel = true;
    if (r == NULL) {
        if (existing == NULL) { return FIRC_OK; }
        out->tunnel = existing->resolve.tunnel;
        return firc_strset(&out->server, existing->resolve.server);
    }
    if (cJSON_IsNull(r)) { return FIRC_OK; }
    if (!cJSON_IsObject(r)) {
        *err_msg = "resolve must be an object of tunnel and server";
        *err_field = "resolve";
        return FIRC_ERR_INVAL;
    }
    const cJSON *t = cJSON_GetObjectItemCaseSensitive(r, "tunnel");
    if (t != NULL && !cJSON_IsNull(t)) {
        if (!cJSON_IsBool(t)) {
            *err_msg = "resolve.tunnel must be true or false";
            *err_field = "resolve.tunnel";
            return FIRC_ERR_INVAL;
        }
        out->tunnel = cJSON_IsTrue(t);
    }
    const cJSON *s = cJSON_GetObjectItemCaseSensitive(r, "server");
    if (s == NULL || cJSON_IsNull(s)) { return FIRC_OK; }
    if (!cJSON_IsString(s)) {
        *err_msg = "resolve.server must be a string";
        *err_field = "resolve.server";
        return FIRC_ERR_INVAL;
    }
    if (s->valuestring[0] == '\0') { return FIRC_OK; }
    firc_resolver_list_t l;
    char why[256];
    if (firc_resolver_list_parse(s->valuestring, &l, why, sizeof(why)) != FIRC_RESOLVER_LIST_OK) {
        snprintf(msgbuf, msgbuf_len, "resolve.server %s", why);
        *err_msg = msgbuf;
        *err_field = "resolve.server";
        return FIRC_ERR_INVAL;
    }
    char hit[FIRC_RESOLVER_ADDR_STRLEN];
    if (firc_resolve_server_in_pool(s->valuestring, pool, hit, sizeof(hit))) {
        snprintf(msgbuf, msgbuf_len, "resolve.server \"%s\" is inside firc's address pool, where no resolver can be",
                 hit);
        *err_msg = msgbuf;
        *err_field = "resolve.server";
        return FIRC_ERR_INVAL;
    }
    return firc_strset(&out->server, s->valuestring);
}

static firc_err_t block_from_req(const cJSON *req, const firc_group_t *existing, firc_group_t *group,
                                 const char **err_msg, const char **err_field) {
    if (!firc_group_is_block(group)) { return FIRC_OK; }
    const cJSON *b = cJSON_GetObjectItemCaseSensitive(req, "block");
    if (b == NULL) {
        if (existing != NULL) { group->block = existing->block; }
        return FIRC_OK;
    }
    if (cJSON_IsNull(b)) { return FIRC_OK; }
    if (!cJSON_IsString(b) || !firc_block_answer_parse(b->valuestring, &group->block)) {
        *err_msg = "block must be null, nxdomain or refused";
        *err_field = "block";
        return FIRC_ERR_INVAL;
    }
    return FIRC_OK;
}

/* group_id is "" for a POST with no "id": a dialog for an id the client was never given would be wrong to mark */
static void write_group_error(firc_http_res_t *res, const char *msg, const char *field, const char *group_id) {
    if (field == NULL) {
        firc_http_res_write_error(res, 400, msg);
        return;
    }
    cJSON *o = cJSON_CreateObject();
    if (o == NULL) {
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    cJSON_AddStringToObject(o, "error", msg);
    cJSON_AddStringToObject(o, "field", field);
    cJSON_AddStringToObject(o, "group", group_id);
    firc_http_res_write_json(res, 400, o);
}

static firc_err_t ifaces_from_req(const cJSON *req, firc_group_t *group, const char **err_msg, const char **err_field,
                                  char *msgbuf, size_t msgbuf_len) {
    const cJSON *j = cJSON_GetObjectItemCaseSensitive(req, "interface");
    if (!cJSON_IsArray(j)) {
        const char *iface = cJSON_IsString(j) ? j->valuestring : "";
        if (iface[0] != '\0' && !firc_is_interface_name(iface)) {
            *err_msg = "interface is not an interface name";
            return FIRC_ERR_INVAL;
        }
        return firc_strset(&group->iface, iface);
    }
    int n = cJSON_GetArraySize(j);
    const char *names[FIRC_GROUP_IFACES_MAX];
    size_t bad = 0;
    const char *why = NULL;
    if (n < 1 || n > FIRC_GROUP_IFACES_MAX) {
        snprintf(msgbuf, msgbuf_len, "interface: a group names 1 to 4 interfaces");
    } else {
        for (int i = 0; why == NULL && i < n; i++) {
            const cJSON *it = cJSON_GetArrayItem(j, i);
            if (!cJSON_IsString(it)) {
                bad = (size_t)i;
                why = "not an interface name";
            } else {
                names[i] = it->valuestring;
            }
        }
        if (why == NULL) { why = firc_group_ifaces_check(names, (size_t)n, &bad); }
        if (why == NULL) { return firc_group_set_ifaces(group, names, (size_t)n); }
        snprintf(msgbuf, msgbuf_len, "interface %zu: %s", bad + 1, why);
    }
    *err_msg = msgbuf;
    *err_field = "interface";
    return FIRC_ERR_INVAL;
}

static cJSON *ifaces_json(const firc_group_t *g) {
    if (g->n_iface_rest == 0) { return cJSON_CreateString(firc_group_iface_at(g, 0)); }
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && i < firc_group_n_ifaces(g); i++) {
        cJSON *s = cJSON_CreateString(firc_group_iface_at(g, i));
        if (s == NULL || !cJSON_AddItemToArray(a, s)) {
            cJSON_Delete(s);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

/* builds a brand-new, independent firc_group_t; existing supplies the id to validate against and baseline rules
 * for lenient nested rule-id reuse; the caller transplants the result onto the live group itself */
static firc_err_t group_from_req(const cJSON *req, const firc_group_t *existing, firc_group_t **out,
                               const char **err_msg, const char **err_field, char *err_group,
                               const firc_fakeip_t *pool, char *msgbuf, size_t msgbuf_len) {
    firc_id_t req_id;
    bool has_req_id;
    if (parse_optional_id(req, "id", &req_id, &has_req_id) != FIRC_OK) {
        *err_msg = "invalid group id";
        return FIRC_ERR_INVAL;
    }
    if (existing && has_req_id && !firc_id_equal(existing->id, req_id)) {
        *err_msg = "group ID mismatch";
        return FIRC_ERR_INVAL;
    }

    if (existing == NULL && !has_req_id) {
        firc_err_t err = firc_id_random(&req_id);
        if (err != FIRC_OK) { return err; }
    }
    firc_group_t *group = firc_group_new();
    if (!group) { return FIRC_ERR_NOMEM; }
    group->id = existing ? existing->id : req_id;

    firc_err_t err = firc_strset(&group->name, get_string(req, "name"));
    if (err == FIRC_OK) { err = ifaces_from_req(req, group, err_msg, err_field, msgbuf, msgbuf_len); }
    if (err != FIRC_OK) {
        firc_group_free(group);
        return err;
    }

    bool enable_present, enable_val;
    get_optional_bool(req, "enable", &enable_val, &enable_present);
    group->enable = enable_present ? enable_val : true;

    if (existing != NULL || has_req_id) { /* "" when the id is fresh and random (see write_group_error) */
        firc_id_format(group->id, err_group);
    } else {
        err_group[0] = '\0';
    }

    err = firc_api_devices_from_req(req, existing != NULL ? &existing->devices : NULL, &group->devices,
                                    err_msg, err_field, msgbuf, msgbuf_len);
    if (err != FIRC_OK) {
        firc_group_free(group);
        return err;
    }

    err = resolve_from_req(req, existing, pool, &group->resolve, err_msg, err_field, msgbuf, msgbuf_len);
    if (err == FIRC_OK) { err = block_from_req(req, existing, group, err_msg, err_field); }
    if (err != FIRC_OK) {
        firc_group_free(group);
        return err;
    }

    /* absent keeps the live list as a config copy; null removes it; an object replaces it; anything else is refused */
    cJSON *list_j = cJSON_GetObjectItemCaseSensitive(req, "list");
    if (list_j == NULL) {
        if (existing != NULL && existing->list != NULL) {
            group->list = list_config_copy(existing->list);
            if (group->list == NULL) {
                firc_group_free(group);
                return FIRC_ERR_NOMEM;
            }
        }
    } else if (cJSON_IsNull(list_j)) {
        /* group->list is already NULL */
    } else if (cJSON_IsObject(list_j)) {
        err = list_from_req(list_j, existing != NULL ? existing->list : NULL, &group->list, err_msg);
        if (err != FIRC_OK) {
            firc_group_free(group);
            return err;
        }
    } else {
        firc_group_free(group);
        *err_msg = "invalid list";
        return FIRC_ERR_INVAL;
    }

    cJSON *rules_j = cJSON_GetObjectItemCaseSensitive(req, "rules");
    if (rules_j && !cJSON_IsNull(rules_j)) {
        if (!cJSON_IsArray(rules_j)) {
            firc_group_free(group);
            *err_msg = "invalid rules";
            return FIRC_ERR_INVAL;
        }
        rule_ids_t baseline;
        err = rule_ids_of(existing ? existing->rules : NULL, existing ? existing->n_rules : 0, &baseline);
        if (err != FIRC_OK) {
            firc_group_free(group);
            return err;
        }
        int n = cJSON_GetArraySize(rules_j);
        for (int i = 0; i < n; i++) {
            const char *rule_why = NULL;
            cJSON *rule_j = cJSON_GetArrayItem(rules_j, i);
            firc_rule_t *r = NULL;
            err = rule_from_req(rule_j, &baseline, existing, &r, &rule_why);
            if (err == FIRC_ERR_INVAL && rule_why != NULL) {
                /* falls back to existing->name when the PUT left "name" out, which is the one the operator is looking at */
                const char *gname = (group->name != NULL && group->name[0] != '\0')
                                        ? group->name
                                        : (existing != NULL ? existing->name : NULL);
                *err_msg = locate_rule(msgbuf, msgbuf_len, gname, get_string(rule_j, "type"),
                                       get_string(rule_j, "rule"), i, rule_why);
                firc_id_set_free(&baseline.ids);
                firc_group_free(group);
                return FIRC_ERR_INVAL;
            }
            if (err == FIRC_OK) { err = firc_group_add_rule(group, r); }
            if (err != FIRC_OK) {
                firc_id_set_free(&baseline.ids);
                firc_rule_free(r);
                firc_group_free(group);
                return err;
            }
        }
        firc_id_set_free(&baseline.ids);
    } else if (existing) {
        /* this returns an independent object, so existing's rules are deep-copied rather than aliased */
        for (size_t i = 0; i < existing->n_rules; i++) {
            firc_rule_t *r = firc_rule_new();
            firc_err_t e = r ? FIRC_OK : FIRC_ERR_NOMEM;
            if (e == FIRC_OK) { r->id = existing->rules[i]->id; }
            if (e == FIRC_OK) { e = firc_strset(&r->type, existing->rules[i]->type); }
            if (e == FIRC_OK) { e = firc_strset(&r->rule, existing->rules[i]->rule); }
            if (e == FIRC_OK) { e = firc_strset(&r->proto, existing->rules[i]->proto); }
            if (e == FIRC_OK) { e = firc_strset(&r->ports, existing->rules[i]->ports); }
            if (e == FIRC_OK) { e = firc_strset(&r->comment, existing->rules[i]->comment); }
            if (e == FIRC_OK) { e = firc_strset(&r->autolist, existing->rules[i]->autolist); }
            if (e == FIRC_OK) { r->autolist_by_domain = existing->rules[i]->autolist_by_domain; }
            if (e == FIRC_OK) { r->enable = existing->rules[i]->enable; }
            if (e != FIRC_OK || firc_group_add_rule(group, r) != FIRC_OK) {
                firc_rule_free(r);
                firc_group_free(group);
                return e != FIRC_OK ? e : FIRC_ERR_NOMEM;
            }
        }
    }
    *out = group;
    return FIRC_OK;
}

static cJSON *rule_to_json(const firc_rule_t *r) {
    char id_buf[FIRC_ID_STR_LEN];
    firc_id_format(r->id, id_buf);
    cJSON *obj = cJSON_CreateObject();
    cJSON_AddStringToObject(obj, "id", id_buf);
    cJSON_AddStringToObject(obj, "type", r->type ? r->type : "");
    cJSON_AddStringToObject(obj, "rule", r->rule ? r->rule : "");
    cJSON_AddBoolToObject(obj, "enable", r->enable);
    /* only when set, so an unset rule has neither key */
    if (r->proto != NULL && *r->proto != '\0') { cJSON_AddStringToObject(obj, "proto", r->proto); }
    if (r->ports != NULL && *r->ports != '\0') { cJSON_AddStringToObject(obj, "ports", r->ports); }
    if (r->comment != NULL && *r->comment != '\0') { cJSON_AddStringToObject(obj, "comment", r->comment); }
    if (r->autolist != NULL && *r->autolist != '\0') {
        cJSON_AddStringToObject(obj, "autolist", r->autolist);
        if (r->autolist_by_domain) { cJSON_AddTrueToObject(obj, "autolistByDomain"); }
    }
    return obj;
}

static cJSON *rules_to_json_array(firc_rule_t **rules, size_t n) {
    cJSON *arr = cJSON_CreateArray();
    for (size_t i = 0; i < n; i++) { cJSON_AddItemToArray(arr, rule_to_json(rules[i])); }
    return arr;
}

static cJSON *wrap_rules(firc_rule_t **rules, size_t n) {
    cJSON *out = cJSON_CreateObject();
    cJSON_AddItemToObject(out, "rules", rules_to_json_array(rules, n));
    return out;
}

/* never the rules themselves, only their count; sync.progress is null outside QUEUED/FETCHING; NULL on OOM */
static cJSON *list_to_json(const firc_group_list_t *l) {
    cJSON *obj = cJSON_CreateObject();
    if (obj == NULL) { return NULL; }
    if (cJSON_AddStringToObject(obj, "url", l->url ? l->url : "") == NULL ||
        cJSON_AddNumberToObject(obj, "interval", l->interval) == NULL ||
        cJSON_AddNumberToObject(obj, "lastUpdate", l->last_update) == NULL ||
        cJSON_AddNumberToObject(obj, "rulesTotal", (double)l->rules.n) == NULL) {
        cJSON_Delete(obj);
        return NULL;
    }
    cJSON *sync = firc_api_list_sync_json(l);
    if (sync == NULL) {
        cJSON_Delete(obj);
        return NULL;
    }
    bool running = l->sync_state == FIRC_SUB_SYNC_QUEUED || l->sync_state == FIRC_SUB_SYNC_FETCHING;
    cJSON *progress = running ? firc_api_list_progress_json(l) : cJSON_CreateNull();
    if (progress == NULL) {
        cJSON_Delete(sync);
        cJSON_Delete(obj);
        return NULL;
    }
    cJSON_AddItemToObject(sync, "progress", progress);
    cJSON_AddItemToObject(obj, "sync", sync);
    return obj;
}

static bool add_iface_states(firc_app_t *app, const firc_group_t *g, cJSON *obj) {
    cJSON *ifs = cJSON_AddArrayToObject(obj, "interfaces");
    if (ifs == NULL) { return false; }
    for (size_t i = 0; i < firc_group_n_ifaces(g); i++) {
        const char *name = firc_group_iface_at(g, i);
        firc_if_view_t v;
        if (!firc_app_iface_view(app, name, &v)) { memset(&v, 0, sizeof(v)); }
        cJSON *e = cJSON_CreateObject();
        if (e == NULL || !cJSON_AddItemToArray(ifs, e)) {
            cJSON_Delete(e);
            return false;
        }
        if (cJSON_AddStringToObject(e, "name", name) == NULL ||
            cJSON_AddStringToObject(e, "state", firc_if_state_name(v.state)) == NULL ||
            cJSON_AddNumberToObject(e, "since", (double)v.since_unix) == NULL ||
            cJSON_AddStringToObject(e, "error", v.error) == NULL) {
            return false;
        }
    }
    return true;
}

/* fails whole (NULL) rather than coming back with devices or list silently missing; app nullable omits resolver */
cJSON *firc_groups_group_json(firc_app_t *app, const firc_group_t *g, bool with_rules) {
    char id_buf[FIRC_ID_STR_LEN];
    firc_id_format(g->id, id_buf);
    cJSON *obj = cJSON_CreateObject();
    if (obj == NULL) { return NULL; }
    cJSON_AddStringToObject(obj, "id", id_buf);
    cJSON_AddStringToObject(obj, "name", g->name ? g->name : "");
    cJSON *ifs = ifaces_json(g);
    if (ifs == NULL || !cJSON_AddItemToObject(obj, "interface", ifs)) {
        cJSON_Delete(ifs);
        cJSON_Delete(obj);
        return NULL;
    }
    cJSON_AddBoolToObject(obj, "enable", g->enable);
    if (firc_group_is_block(g)) { cJSON_AddStringToObject(obj, "block", firc_block_answer_name(g->block)); }
    if (app != NULL) {
        const char *reason = NULL;
        bool live = firc_app_group_live(app, g->id, &reason);
        cJSON_AddBoolToObject(obj, "live", live);
        if (!live) { cJSON_AddStringToObject(obj, "liveReason", reason); }
        firc_ruleset_t *rs = firc_app_find_group_by_id(app, g->id);
        cJSON_AddStringToObject(obj, "active", rs != NULL ? firc_ruleset_active_iface(rs) : firc_group_iface_at(g, 0));
        if (firc_group_n_ifaces(g) > 1 && !add_iface_states(app, g, obj)) {
            cJSON_Delete(obj);
            return NULL;
        }
    }
    cJSON *devices = firc_api_devices_to_json(&g->devices);
    if (devices == NULL) {
        cJSON_Delete(obj);
        return NULL;
    }
    cJSON_AddItemToObject(obj, "devices", devices);
    cJSON *resolve = cJSON_AddObjectToObject(obj, "resolve");
    if (resolve == NULL) {
        cJSON_Delete(obj);
        return NULL;
    }
    cJSON_AddBoolToObject(resolve, "tunnel", g->resolve.tunnel);
    cJSON_AddStringToObject(resolve, "server", g->resolve.server != NULL ? g->resolve.server : "");
    if (app != NULL) {
        firc_app_resolver_info_t info;
        firc_app_group_resolver(app, g, &info);
        cJSON *rv = cJSON_AddObjectToObject(obj, "resolver");
        cJSON *servers = rv != NULL ? cJSON_AddArrayToObject(rv, "servers") : NULL;
        if (servers == NULL) {
            cJSON_Delete(obj);
            return NULL;
        }
        cJSON_AddStringToObject(rv, "source", firc_resolve_source_name(info.source));
        for (size_t i = 0; i < info.n_servers; i++) {
            char s[FIRC_RESOLVER_ADDR_STRLEN];
            if (firc_resolver_addr_format(&info.servers[i], s, sizeof(s)) > 0) {
                cJSON_AddItemToArray(servers, cJSON_CreateString(s));
            }
        }
        const firc_dnsproxy_health_t *hl = &info.health;
        cJSON *health = cJSON_AddObjectToObject(rv, "health");
        if (health == NULL) {
            cJSON_Delete(obj);
            return NULL;
        }
        cJSON_AddNumberToObject(health, "window", FIRC_DNSPROXY_HEALTH_WINDOW_S);
        cJSON_AddNumberToObject(health, "total", (double)hl->total);
        cJSON_AddNumberToObject(health, "fallbacks", (double)hl->fallbacks);
        cJSON_AddNumberToObject(health, "last",
                                hl->has_last ? (double)(info.wall_s - (int64_t)(hl->last_ago_ms / 1000u)) : 0);
        cJSON_AddStringToObject(health, "reason", hl->has_last ? firc_dns_resolver_name(hl->reason) : "");
        cJSON_AddBoolToObject(health, "resting", hl->resting);
        cJSON_AddNumberToObject(health, "since",
                                hl->resting ? (double)(info.wall_s - (int64_t)(hl->since_ago_ms / 1000u)) : 0);
    }
    /* "rules" key omitted otherwise */
    if (with_rules) { cJSON_AddItemToObject(obj, "rules", rules_to_json_array(g->rules, g->n_rules)); }
    if (g->list != NULL) {
        cJSON *list = list_to_json(g->list);
        if (list == NULL) {
            cJSON_Delete(obj);
            return NULL;
        }
        cJSON_AddItemToObject(obj, "list", list);
    }
    return obj;
}

bool firc_groups_maybe_save(firc_groups_ctx_t *ctx, firc_http_req_t *req, firc_http_res_t *res) {
    if (!firc_http_req_query_is_true(req, "save") || !ctx->config_path) { return true; }
    firc_err_t err = firc_app_save_groups(ctx->app, ctx->config_path, ctx->config_version ? ctx->config_version : "");
    if (err == FIRC_OK) { return true; }
    FIRC_ERROR("failed to save config file: %s", firc_err_str(err));
    char msg[160];
    snprintf(msg, sizeof(msg), "applied, but groups.yaml was not saved: %s", firc_err_str(err));
    firc_http_res_write_error(res, 500, msg);
    return false;
}

static bool hand_rules_set(firc_groups_ctx_t *ctx, firc_ruleset_t *rs, firc_group_t *next, firc_http_req_t *req,
                           firc_http_res_t *res) {
    bool unsaved = false;
    const char *path = firc_http_req_query_is_true(req, "save") ? ctx->config_path : NULL;
    firc_err_t err = firc_app_set_hand_rules(ctx->app, rs, next, path, ctx->config_version, &unsaved);
    if (err == FIRC_OK) { return true; }
    if (!unsaved) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return false;
    }
    FIRC_ERROR("failed to save config file: %s", firc_err_str(err));
    char msg[160];
    snprintf(msg, sizeof(msg), "applied, but groups.yaml was not saved: %s", firc_err_str(err));
    firc_http_res_write_error(res, 500, msg);
    return false;
}

bool firc_groups_resolve(firc_groups_ctx_t *ctx, firc_http_req_t *req, firc_http_res_t *res,
                         firc_ruleset_t **out) {
    const char *id_str = firc_http_req_param(req, "groupID");
    firc_id_t id;
    if (!id_str || firc_id_parse(id_str, &id) != FIRC_OK) {
        firc_http_res_write_error(res, 400, "invalid group id");
        return false;
    }
    firc_ruleset_t *rs = firc_app_find_group_by_id(ctx->app, id);
    if (!rs) {
        firc_http_res_write_error(res, 404, "group not exist");
        return false;
    }
    *out = rs;
    return true;
}

static bool resolve_rule(firc_ruleset_t *rs, firc_http_req_t *req, firc_http_res_t *res, size_t *out_idx) {
    const char *id_str = firc_http_req_param(req, "ruleID");
    firc_id_t id;
    if (!id_str || firc_id_parse(id_str, &id) != FIRC_OK) {
        firc_http_res_write_error(res, 400, "invalid rule id");
        return false;
    }
    const firc_group_t *g = firc_ruleset_group(rs);
    for (size_t i = 0; i < g->n_rules; i++) {
        if (firc_id_equal(g->rules[i]->id, id)) {
            *out_idx = i;
            return true;
        }
    }
    firc_http_res_write_error(res, 404, "rule not exist");
    return false;
}

static void handle_get_groups(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    bool with_rules = firc_http_req_query_is_true(req, "with_rules");
    cJSON *out = cJSON_CreateObject();
    cJSON *arr = cJSON_AddArrayToObject(out, "groups");
    size_t n = firc_app_user_group_count(ctx->app);
    for (size_t i = 0; i < n; i++) {
        const firc_group_t *g = firc_ruleset_group(firc_app_user_group_at(ctx->app, i));
        cJSON *gj = firc_groups_group_json(ctx->app, g, with_rules);
        if (gj == NULL) {
            cJSON_Delete(out);
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        cJSON_AddItemToArray(arr, gj);
    }
    firc_http_res_write_json(res, 200, out);
}

static void handle_put_groups(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    cJSON *groups_j = cJSON_GetObjectItemCaseSensitive(json, "groups");
    if (!groups_j || cJSON_IsNull(groups_j) || !cJSON_IsArray(groups_j)) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "no groups in request");
        return;
    }

    /* parsed against the CURRENT groups first; the swap below disables and replaces them under one lock acquisition */
    int n_req = cJSON_GetArraySize(groups_j);
    firc_group_t **new_groups = n_req > 0 ? calloc((size_t)n_req, sizeof(*new_groups)) : NULL;
    if (n_req > 0 && !new_groups) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    for (int i = 0; i < n_req; i++) {
        cJSON *group_req = cJSON_GetArrayItem(groups_j, i);
        firc_id_t wanted_id;
        bool has_id;
        const firc_group_t *existing = NULL;
        if (parse_optional_id(group_req, "id", &wanted_id, &has_id) == FIRC_OK && has_id) {
            firc_ruleset_t *rs = firc_app_find_group_by_id(ctx->app, wanted_id);
            if (rs) { existing = firc_ruleset_group(rs); }
        }
        const char *err_msg = "invalid group";
        const char *err_field = NULL;
        char err_group[FIRC_ID_STR_LEN] = "";
        char msgbuf[FIRC_RULE_WHERE_MAX];
        firc_err_t err = group_from_req(group_req, existing, &new_groups[i], &err_msg, &err_field, err_group,
                                        firc_app_pool(ctx->app), msgbuf, sizeof(msgbuf));
        if (err != FIRC_OK && err != FIRC_ERR_INVAL) { /* not the client's fault -- see handle_create_group */
            cJSON_Delete(json);
            for (int j = 0; j < i; j++) { firc_group_free(new_groups[j]); }
            free(new_groups);
            firc_http_res_write_error(res, 500, firc_err_str(err));
            return;
        }
        if (err != FIRC_OK) {
            cJSON_Delete(json);
            for (int j = 0; j < i; j++) { firc_group_free(new_groups[j]); }
            free(new_groups);
            write_group_error(res, err_msg, err_field, err_group);
            return;
        }
    }
    cJSON_Delete(json);

    /* built AFTER the swap, from the live groups: the swap is what moves a carried list's arena in */
    firc_err_t err = firc_app_replace_groups(ctx->app, new_groups, (size_t)n_req);
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    if (!firc_groups_maybe_save(ctx, req, res)) { return; }
    cJSON *out = cJSON_CreateObject();
    cJSON *arr = cJSON_AddArrayToObject(out, "groups");
    size_t n_live = firc_app_user_group_count(ctx->app);
    for (size_t i = 0; i < n_live; i++) {
        cJSON *gj = firc_groups_group_json(ctx->app, firc_ruleset_group(firc_app_user_group_at(ctx->app, i)), true);
        if (gj == NULL) {
            cJSON_Delete(out);
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        cJSON_AddItemToArray(arr, gj);
    }
    firc_http_res_write_json(res, 200, out);
}

static void handle_create_group(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    firc_group_t *group = NULL;
    const char *err_msg = "invalid group";
    const char *err_field = NULL;
    char err_group[FIRC_ID_STR_LEN] = "";
    char msgbuf[FIRC_RULE_WHERE_MAX];
    firc_err_t err = group_from_req(json, NULL, &group, &err_msg, &err_field, err_group,
                                    firc_app_pool(ctx->app), msgbuf, sizeof(msgbuf));
    cJSON_Delete(json);
    if (err != FIRC_OK && err != FIRC_ERR_INVAL) { /* not the client's fault: a 400 would tell the operator to fix a body that was fine */
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    if (err != FIRC_OK) {
        write_group_error(res, err_msg, err_field, err_group);
        return;
    }

    char why[FIRC_APP_WHY_MAX] = "";
    err = firc_app_add_group_why(ctx->app, group, why, sizeof(why)); /* always takes ownership */
    if (err != FIRC_OK) {
        int status = err == FIRC_ERR_EXIST ? 409 : 500; /* a group that did not come up says which step failed */
        firc_http_res_write_error(res, status, why[0] != '\0' ? why : firc_err_str(err));
        return;
    }
    if (!firc_groups_maybe_save(ctx, req, res)) { return; }
    firc_http_res_write_json(res, 200, firc_groups_group_json(ctx->app, group, true)); /* still valid: no free on success */
}

static void handle_get_group(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    bool with_rules = firc_http_req_query_is_true(req, "with_rules");
    firc_http_res_write_json(res, 200, firc_groups_group_json(ctx->app, firc_ruleset_group(rs), with_rules));
}

static void handle_put_group(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }

    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }

    /* built and validated before the group leaves the kernel: a 400 must leave it exactly as it was */
    firc_group_t *live = firc_ruleset_group_mut(rs);
    firc_group_t *built = NULL;
    const char *err_msg = "invalid group";
    const char *err_field = NULL;
    char err_group[FIRC_ID_STR_LEN] = "";
    char msgbuf[FIRC_RULE_WHERE_MAX];
    firc_err_t err = group_from_req(json, live, &built, &err_msg, &err_field, err_group,
                                    firc_app_pool(ctx->app), msgbuf, sizeof(msgbuf));
    cJSON_Delete(json);
    if (err != FIRC_OK && err != FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    if (err != FIRC_OK) {
        write_group_error(res, err_msg, err_field, err_group);
        return;
    }

    err = firc_app_update_group(ctx->app, live->id, built); /* takes built either way; live keeps its address */
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }

    if (!firc_groups_maybe_save(ctx, req, res)) { return; }
    firc_http_res_write_json(res, 200, firc_groups_group_json(ctx->app, live, true));
}

static void handle_delete_group(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    /* a teardown error is logged by the app and keeps nothing, so this answers 200 regardless */
    firc_app_remove_group_by_id(ctx->app, firc_ruleset_group(rs)->id);
    if (!firc_groups_maybe_save(ctx, req, res)) { return; }
    firc_http_res_write(res, 200, NULL, NULL, 0);
}

static void handle_move_up(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    const firc_group_t *g = firc_ruleset_group(rs);
    firc_err_t err = firc_app_move_up(ctx->app, g->id);
    if (err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 400, "the group names one interface");
        return;
    }
    if (err == FIRC_ERR_STATE) {
        char msg[64];
        snprintf(msg, sizeof(msg), "%s is not alive", firc_group_iface_at(g, 0));
        firc_http_res_write_error(res, 409, msg);
        return;
    }
    if (err == FIRC_ERR_AGAIN) {
        firc_http_res_write_error(res, 409, "the group is not routed");
        return;
    }
    if (err == FIRC_ERR_NOSYS) {
        firc_http_res_write_error(res, 409, "no check runs for the group");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    firc_http_res_write_json(res, 200, firc_groups_group_json(ctx->app, g, false));
}

static void handle_get_rules(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    const firc_group_t *g = firc_ruleset_group(rs);
    firc_http_res_write_json(res, 200, wrap_rules(g->rules, g->n_rules));
}

static void handle_put_rules(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }

    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    cJSON *rules_j = cJSON_GetObjectItemCaseSensitive(json, "rules");
    if (!rules_j || cJSON_IsNull(rules_j) || !cJSON_IsArray(rules_j)) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, "no rules in request");
        return;
    }

    firc_group_t *group = firc_ruleset_group_mut(rs);
    rule_ids_t known;
    if (rule_ids_of(group->rules, group->n_rules, &known) != FIRC_OK) {
        cJSON_Delete(json);
        firc_http_res_write_error(res, 500, firc_err_str(FIRC_ERR_NOMEM));
        return;
    }
    int n = cJSON_GetArraySize(rules_j);
    firc_group_t built = {0};
    for (int i = 0; i < n; i++) {
        const char *why = NULL;
        cJSON *rule_j = cJSON_GetArrayItem(rules_j, i);
        firc_rule_t *rule = NULL;
        firc_err_t err = rule_from_req_strict(rule_j, &known, group, &rule, &why);
        if (err == FIRC_OK) {
            err = firc_group_add_rule(&built, rule);
            if (err != FIRC_OK) { firc_rule_free(rule); }
        }
        if (err != FIRC_OK) {
            firc_id_set_free(&known.ids);
            for (size_t j = 0; j < built.n_rules; j++) { firc_rule_free(built.rules[j]); }
            free(built.rules);
            if (err == FIRC_ERR_INVAL && why != NULL) {
                char msgbuf[FIRC_RULE_WHERE_MAX]; /* built before json is freed: the pattern points into it */
                const char *msg = locate_rule(msgbuf, sizeof(msgbuf), group->name,
                                              get_string(rule_j, "type"),
                                              get_string(rule_j, "rule"), i, why);
                cJSON_Delete(json);
                firc_http_res_write_error(res, 400, msg);
                return;
            }
            cJSON_Delete(json);
            if (err == FIRC_ERR_NOENT) {
                firc_http_res_write_error(res, 404, "rule not found");
            } else if (err == FIRC_ERR_INVAL) {
                firc_http_res_write_error(res, 400, "invalid rule");
            } else {
                firc_http_res_write_error(res, 500, firc_err_str(err));
            }
            return;
        }
    }
    firc_id_set_free(&known.ids);
    cJSON_Delete(json);

    if (!hand_rules_set(ctx, rs, &built, req, res)) { return; }
    firc_http_res_write_json(res, 200, wrap_rules(group->rules, group->n_rules));
}

static void handle_create_rule(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }

    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    firc_group_t *group = firc_ruleset_group_mut(rs);
    const char *why = NULL;
    firc_rule_t *rule = NULL;
    rule_ids_t known;
    firc_err_t err = rule_ids_of(group->rules, group->n_rules, &known);
    if (err == FIRC_OK) {
        err = rule_from_req(json, &known, NULL, &rule, &why);
        firc_id_set_free(&known.ids);
    }
    if (err == FIRC_ERR_INVAL && why != NULL) {
        char msgbuf[FIRC_RULE_WHERE_MAX];
        const char *msg = locate_rule(msgbuf, sizeof(msgbuf), NULL, get_string(json, "type"),
                                      get_string(json, "rule"), (int)group->n_rules, why);
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, msg);
        return;
    }
    cJSON_Delete(json);
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, firc_err_str(err));
        return;
    }
    firc_group_t next = {0};
    if (firc_group_rules_with(group, SIZE_MAX, NULL, rule, &next) != FIRC_OK) {
        firc_rule_free(rule);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (!hand_rules_set(ctx, rs, &next, req, res)) { return; }
    firc_http_res_write_json(res, 200, rule_to_json(rule));
}

static void handle_get_rule(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    size_t idx;
    if (!resolve_rule(rs, req, res, &idx)) { return; }
    firc_http_res_write_json(res, 200, rule_to_json(firc_ruleset_group(rs)->rules[idx]));
}

static void handle_put_rule(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    size_t idx;
    if (!resolve_rule(rs, req, res, &idx)) { return; }

    cJSON *json = parse_body_json(req);
    if (!json) {
        firc_http_res_write_error(res, 400, "failed to parse request");
        return;
    }
    /* the rule's identity always stays the path-resolved one; the body's own "id" is never read */
    firc_rule_t *rule = firc_ruleset_group_mut(rs)->rules[idx];
    /* checked before a single field is written: a refusal partway would leave the rule half-replaced */
    const char *why = NULL;
    bool bad = false;
    const char *proto = get_opt_string(json, "proto", &bad);
    const char *ports = get_opt_string(json, "ports", &bad);
    if (bad) { why = why_not_strings; }
    bool refused = bad || !firc_rule_spec_is_usable(get_string(json, "type"), get_string(json, "rule"), proto, ports, &why);
    if (!refused) {
        (void)comment_from_req(json, &why);
        refused = why != NULL;
    }
    if (refused) {
        char msgbuf[FIRC_RULE_WHERE_MAX];
        const char *msg =
            locate_rule(msgbuf, sizeof(msgbuf), NULL, get_string(json, "type"),
                        get_string(json, "rule"), (int)idx, why);
        cJSON_Delete(json);
        firc_http_res_write_error(res, 400, msg);
        return;
    }
    firc_rule_t *fresh = firc_rule_new();
    firc_err_t err = fresh != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
    if (err == FIRC_OK) {
        fresh->id = rule->id;
        err = fill_rule_fields(fresh, json, NULL);
    }
    if (err == FIRC_OK) { err = carry_marker(fresh, firc_ruleset_group_mut(rs)); }
    cJSON_Delete(json);
    firc_group_t next = {0};
    if (err == FIRC_OK) { err = firc_group_rules_with(firc_ruleset_group(rs), idx, fresh, NULL, &next); }
    if (err != FIRC_OK) { firc_rule_free(fresh); }
    if (err == FIRC_ERR_INVAL) { /* unreachable while the pre-check above stands */
        firc_http_res_write_error(res, 400, why != NULL ? why : "the rule is unusable");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (!hand_rules_set(ctx, rs, &next, req, res)) { return; }
    firc_http_res_write_json(res, 200, rule_to_json(fresh));
}

static void handle_delete_rule(firc_http_req_t *req, firc_http_res_t *res, void *ud) {
    firc_groups_ctx_t *ctx = ud;
    firc_ruleset_t *rs;
    if (!firc_groups_resolve(ctx, req, res, &rs)) { return; }
    size_t idx;
    if (!resolve_rule(rs, req, res, &idx)) { return; }

    firc_group_t next = {0};
    if (firc_group_rules_with(firc_ruleset_group(rs), idx, NULL, NULL, &next) != FIRC_OK) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (!hand_rules_set(ctx, rs, &next, req, res)) { return; }
    firc_http_res_write(res, 200, NULL, NULL, 0);
}

void firc_groups_register_routes(firc_httpd_t *h, firc_groups_ctx_t *ctx) {
    firc_group_lists_register_routes(h, ctx);
    must_route(h, "GET", "/api/v1/groups", handle_get_groups, ctx);
    must_route(h, "PUT", "/api/v1/groups", handle_put_groups, ctx);
    must_route(h, "POST", "/api/v1/groups", handle_create_group, ctx);
    must_route(h, "GET", "/api/v1/groups/{groupID}", handle_get_group, ctx);
    must_route(h, "PUT", "/api/v1/groups/{groupID}", handle_put_group, ctx);
    must_route(h, "DELETE", "/api/v1/groups/{groupID}", handle_delete_group, ctx);
    must_route(h, "POST", "/api/v1/groups/{groupID}/move-up", handle_move_up, ctx);
    must_route(h, "GET", "/api/v1/groups/{groupID}/rules", handle_get_rules, ctx);
    must_route(h, "PUT", "/api/v1/groups/{groupID}/rules", handle_put_rules, ctx);
    must_route(h, "POST", "/api/v1/groups/{groupID}/rules", handle_create_rule, ctx);
    must_route(h, "GET", "/api/v1/groups/{groupID}/rules/{ruleID}", handle_get_rule, ctx);
    must_route(h, "PUT", "/api/v1/groups/{groupID}/rules/{ruleID}", handle_put_rule, ctx);
    must_route(h, "DELETE", "/api/v1/groups/{groupID}/rules/{ruleID}", handle_delete_rule, ctx);
}
