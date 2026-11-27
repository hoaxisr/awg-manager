#include "firc/tunnels_api.h"

#include <errno.h>
#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/inbound_check.h"
#include "firc/json.h"
#include "firc/log.h"
#include "firc/ruleset.h"
#include "groups_internal.h"

#define PROBE_TIMEOUT_S 3
#define PROBE_JOBS 8

struct tun_probe_call {
    firc_tunnels_ctx_t *ctx;
    firc_tunprobe_t *probe;
    firc_http_deferred_t *pending;
    char (*keys)[192];
    char **links;
    size_t n;
    char *saved;
};

static int64_t mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static int64_t wall_s_of(int64_t at_mono_ms)
{
    if (at_mono_ms <= 0) {
        return 0;
    }
    return (int64_t)time(NULL) - (mono_ms() - at_mono_ms) / 1000;
}

static void write_tun_error(firc_http_res_t *res, int status, const char *why, const char *field, const char *tunnel)
{
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddStringToObject(o, "error", why) == NULL ||
        (field != NULL ? cJSON_AddStringToObject(o, "field", field) : cJSON_AddNullToObject(o, "field")) == NULL ||
        (tunnel != NULL ? cJSON_AddStringToObject(o, "tunnel", tunnel) : cJSON_AddNullToObject(o, "tunnel")) == NULL) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, status, o);
}

static cJSON *strings_json(char *const *v, size_t n)
{
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && i < n; i++) {
        cJSON *s = cJSON_CreateString(v[i]);
        if (s == NULL || !cJSON_AddItemToArray(a, s)) {
            cJSON_Delete(s);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static bool put_item(cJSON *o, const char *key, cJSON *v)
{
    if (v == NULL) {
        return false;
    }
    if (!cJSON_AddItemToObject(o, key, v)) {
        cJSON_Delete(v);
        return false;
    }
    return true;
}

static const char *uplink_kind(firc_uplink_kind_t k)
{
    return k == FIRC_UPLINK_IFACE ? "iface" : k == FIRC_UPLINK_TUNNEL ? "tunnel" : "auto";
}

static cJSON *source_json(const firc_tun_src_t *src)
{
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddStringToObject(o, "id", src->id) == NULL) {
        cJSON_Delete(o);
        return NULL;
    }
    bool ok;
    if (src->kind == FIRC_TUN_SRC_LINK) {
        ok = cJSON_AddStringToObject(o, "kind", "link") != NULL &&
             cJSON_AddStringToObject(o, "link", src->link != NULL ? src->link : "") != NULL;
    } else {
        ok = cJSON_AddStringToObject(o, "kind", "subscription") != NULL &&
             cJSON_AddStringToObject(o, "name", src->sub.name) != NULL &&
             cJSON_AddStringToObject(o, "url", src->sub.url != NULL ? src->sub.url : "") != NULL &&
             cJSON_AddNumberToObject(o, "interval", src->sub.interval_s) != NULL;
        if (ok && src->sub.n_headers > 0) {
            cJSON *h = cJSON_CreateObject();
            for (size_t i = 0; h != NULL && i < src->sub.n_headers; i++) {
                if (cJSON_AddStringToObject(h, src->sub.headers[i].name, src->sub.headers[i].value) == NULL) {
                    cJSON_Delete(h);
                    h = NULL;
                }
            }
            ok = put_item(o, "headers", h);
        }
    }
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static cJSON *inbounds_json(const firc_tunnel_t *t)
{
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && i < t->n_inb; i++) {
        const firc_tun_inbound_t *b = &t->inb[i];
        cJSON *o = cJSON_CreateObject();
        bool ok = o != NULL && cJSON_AddNumberToObject(o, "port", b->port) != NULL &&
                  cJSON_AddStringToObject(o, "listen", b->listen) != NULL &&
                  cJSON_AddStringToObject(o, "node", b->node) != NULL &&
                  cJSON_AddStringToObject(o, "user", b->user) != NULL &&
                  cJSON_AddStringToObject(o, "password", b->password) != NULL && cJSON_AddItemToArray(a, o);
        if (!ok) {
            cJSON_Delete(o);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *tunnel_json(const firc_tunnel_t *t)
{
    cJSON *o = cJSON_CreateObject();
    cJSON *up = cJSON_CreateObject();
    cJSON *src = cJSON_CreateArray();
    cJSON *adv = cJSON_CreateObject();
    bool ok = o != NULL && up != NULL && src != NULL && adv != NULL;
    ok = ok && cJSON_AddStringToObject(up, "kind", uplink_kind(t->uplink)) != NULL &&
         cJSON_AddStringToObject(up, "ref", t->uplink_ref) != NULL;
    for (size_t i = 0; ok && i < t->n_src; i++) {
        cJSON *s = source_json(&t->src[i]);
        ok = s != NULL && cJSON_AddItemToArray(src, s);
        if (!ok) {
            cJSON_Delete(s);
        }
    }
    ok = ok && cJSON_AddNumberToObject(adv, "timeout", t->timeout_s) != NULL &&
         cJSON_AddBoolToObject(adv, "insecure", t->insecure) != NULL &&
         cJSON_AddStringToObject(adv, "ca", t->ca) != NULL &&
         cJSON_AddStringToObject(adv, "fingerprint", t->fingerprint) != NULL &&
         cJSON_AddBoolToObject(adv, "kernelTcp", t->kernel_tcp) != NULL;
    ok = ok && cJSON_AddStringToObject(o, "id", t->id) != NULL &&
         cJSON_AddStringToObject(o, "device", t->device) != NULL &&
         cJSON_AddStringToObject(o, "description", t->description) != NULL &&
         cJSON_AddBoolToObject(o, "enable", t->enable) != NULL &&
         cJSON_AddBoolToObject(o, "routerPolicies", t->router_policies) != NULL;
    if (ok) {
        ok = put_item(o, "uplink", up);
        up = NULL;
    }
    if (ok) {
        ok = put_item(o, "sources", src);
        src = NULL;
    }
    ok = ok && cJSON_AddStringToObject(o, "filter", t->filter != NULL ? t->filter : "") != NULL;
    if (ok && t->filter_exclude != NULL && t->filter_exclude[0] != '\0') {
        ok = cJSON_AddStringToObject(o, "filterExclude", t->filter_exclude) != NULL;
    }
    ok = ok && put_item(o, "order", strings_json(t->order, t->n_order)) &&
         put_item(o, "exclude", strings_json(t->exclude, t->n_exclude)) &&
         put_item(o, "inbounds", inbounds_json(t)) && cJSON_AddNumberToObject(o, "active", t->active) != NULL &&
         cJSON_AddStringToObject(o, "by", t->by) != NULL &&
         cJSON_AddNumberToObject(o, "interval", t->interval_s) != NULL &&
         cJSON_AddNumberToObject(o, "silence", t->silence_s) != NULL;
    if (ok) {
        ok = put_item(o, "advanced", adv);
        adv = NULL;
    }
    cJSON_Delete(up);
    cJSON_Delete(src);
    cJSON_Delete(adv);
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static cJSON *tunnels_json(const firc_tunnels_t *all)
{
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && i < all->n; i++) {
        cJSON *t = tunnel_json(&all->t[i]);
        if (t == NULL || !cJSON_AddItemToArray(a, t)) {
            cJSON_Delete(t);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *secs_value(const cJSON *v)
{
    if (!cJSON_IsNumber(v)) {
        return cJSON_Duplicate(v, true);
    }
    char b[64];
    double d = v->valuedouble;
    if (d > -1e15 && d < 1e15 && d == (double)(long long)d) {
        snprintf(b, sizeof b, "%llds", (long long)d);
    } else {
        snprintf(b, sizeof b, "%gs", d);
    }
    return cJSON_CreateString(b);
}

static bool add_shaped(cJSON *o, const char *key, cJSON *v)
{
    return put_item(o, key, v);
}

static cJSON *shape_uplink(const cJSON *v)
{
    if (!cJSON_IsObject(v)) {
        return cJSON_Duplicate(v, true);
    }
    const cJSON *kind = cJSON_GetObjectItemCaseSensitive(v, "kind");
    const cJSON *ref = cJSON_GetObjectItemCaseSensitive(v, "ref");
    const char *k = cJSON_IsString(kind) ? kind->valuestring : "";
    if (strcmp(k, "auto") == 0) {
        return cJSON_CreateString("auto");
    }
    if (strcmp(k, "iface") != 0 && strcmp(k, "tunnel") != 0) {
        return cJSON_CreateString("");
    }
    const char *r = cJSON_IsString(ref) ? ref->valuestring : "";
    size_t len = strlen(k) + strlen(r) + 2;
    char *b = malloc(len);
    if (b == NULL) {
        return NULL;
    }
    snprintf(b, len, "%s:%s", k, r);
    cJSON *s = cJSON_CreateString(b);
    free(b);
    return s;
}

static cJSON *shape_source(const cJSON *v)
{
    if (!cJSON_IsObject(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *o = cJSON_CreateObject();
    cJSON *sub = NULL;
    const cJSON *kind = cJSON_GetObjectItemCaseSensitive(v, "kind");
    const cJSON *link_item = cJSON_GetObjectItemCaseSensitive(v, "link");
    const cJSON *url_item = cJSON_GetObjectItemCaseSensitive(v, "url");
    bool is_sub = (cJSON_IsString(kind) && strcmp(kind->valuestring, "subscription") == 0) ||
                  (kind == NULL && cJSON_IsString(url_item) && url_item->valuestring[0] != '\0');
    bool is_link = (cJSON_IsString(kind) && strcmp(kind->valuestring, "link") == 0) ||
                   (!is_sub && cJSON_IsString(link_item) && link_item->valuestring[0] != '\0');
    if (is_sub) {
        sub = cJSON_CreateObject();
    }
    bool ok = o != NULL && (!is_sub || sub != NULL);
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        if (!ok) {
            break;
        }
        if (cJSON_IsNull(it) || strcmp(it->string, "kind") == 0) {
            continue;
        }
        if (strcmp(it->string, "id") == 0 && cJSON_IsString(it) && it->valuestring[0] == '\0') {
            continue;
        }
        if (is_link && (strcmp(it->string, "name") == 0 || strcmp(it->string, "url") == 0 ||
                        strcmp(it->string, "interval") == 0 || strcmp(it->string, "headers") == 0)) {
            continue;
        }
        if (is_sub && strcmp(it->string, "link") == 0) {
            continue;
        }
        bool sub_field = is_sub && (strcmp(it->string, "name") == 0 || strcmp(it->string, "url") == 0 ||
                                    strcmp(it->string, "interval") == 0 ||
                                    strcmp(it->string, "headers") == 0);
        if (!sub_field) {
            ok = add_shaped(o, it->string, cJSON_Duplicate(it, true));
        } else if (strcmp(it->string, "interval") == 0) {
            ok = add_shaped(sub, "interval", secs_value(it));
        } else {
            ok = add_shaped(sub, it->string, cJSON_Duplicate(it, true));
        }
    }
    if (ok && is_sub) {
        ok = add_shaped(o, "subscription", sub);
        sub = NULL;
    } else if (ok && !is_link && kind != NULL) {
        ok = add_shaped(o, "kind", cJSON_Duplicate(kind, true));
    }
    cJSON_Delete(sub);
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static cJSON *shape_sources(const cJSON *v)
{
    if (!cJSON_IsArray(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *a = cJSON_CreateArray();
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        cJSON *s = a != NULL ? shape_source(it) : NULL;
        if (s == NULL || !cJSON_AddItemToArray(a, s)) {
            cJSON_Delete(s);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *shape_advanced(const cJSON *v)
{
    if (!cJSON_IsObject(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *o = cJSON_CreateObject();
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        if (o == NULL || cJSON_IsNull(it)) {
            continue;
        }
        cJSON *x = strcmp(it->string, "timeout") == 0 ? secs_value(it) : cJSON_Duplicate(it, true);
        if (!add_shaped(o, it->string, x)) {
            cJSON_Delete(o);
            return NULL;
        }
    }
    return o;
}

static cJSON *shape_inbound(const cJSON *v)
{
    if (!cJSON_IsObject(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *o = cJSON_CreateObject();
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        bool empty_auth = cJSON_IsString(it) && it->valuestring[0] == 0 &&
                          (strcmp(it->string, "user") == 0 || strcmp(it->string, "password") == 0);
        if (o == NULL || cJSON_IsNull(it) || empty_auth) {
            continue;
        }
        if (!add_shaped(o, it->string, cJSON_Duplicate(it, true))) {
            cJSON_Delete(o);
            return NULL;
        }
    }
    return o;
}

static cJSON *shape_inbounds(const cJSON *v)
{
    if (!cJSON_IsArray(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *a = cJSON_CreateArray();
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        cJSON *x = a != NULL ? shape_inbound(it) : NULL;
        if (x == NULL || !cJSON_AddItemToArray(a, x)) {
            cJSON_Delete(x);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *shape_tunnel(const cJSON *v)
{
    if (!cJSON_IsObject(v)) {
        return cJSON_Duplicate(v, true);
    }
    cJSON *o = cJSON_CreateObject();
    const cJSON *it;
    cJSON_ArrayForEach(it, v)
    {
        if (o == NULL || cJSON_IsNull(it)) {
            continue;
        }
        const char *k = it->string;
        cJSON *x;
        if (strcmp(k, "uplink") == 0) {
            x = shape_uplink(it);
        } else if (strcmp(k, "sources") == 0) {
            x = shape_sources(it);
        } else if (strcmp(k, "interval") == 0 || strcmp(k, "silence") == 0) {
            x = secs_value(it);
        } else if (strcmp(k, "advanced") == 0) {
            x = shape_advanced(it);
        } else if (strcmp(k, "inbounds") == 0) {
            x = shape_inbounds(it);
        } else {
            x = cJSON_Duplicate(it, true);
        }
        if (!add_shaped(o, k, x)) {
            cJSON_Delete(o);
            return NULL;
        }
    }
    return o;
}

typedef struct {
    firc_tun_err_t err;
    char field[96];
    char tunnel[64];
    bool has_field, has_tunnel;
} load_err_t;

static void map_where(const cJSON *arr, load_err_t *le)
{
    const char *w = le->err.where;
    le->has_field = true;
    snprintf(le->field, sizeof le->field, "tunnels");
    if (strncmp(w, "tunnels[", 8) != 0) {
        return;
    }
    char *end = NULL;
    unsigned long i = strtoul(w + 8, &end, 10);
    if (end == w + 8 || *end != ']') {
        return;
    }
    const cJSON *t = cJSON_GetArrayItem(arr, (int)i);
    const cJSON *id = cJSON_GetObjectItemCaseSensitive(t, "id");
    if (cJSON_IsString(id)) {
        le->has_tunnel = true;
        snprintf(le->tunnel, sizeof le->tunnel, "%.63s", id->valuestring);
    }
    const char *rest = end + 1;
    if (*rest != '.') {
        le->has_field = false;
        return;
    }
    char f[96];
    snprintf(f, sizeof f, "%s", rest + 1);
    char *sub = strstr(f, ".subscription");
    if (sub != NULL) {
        memmove(sub, sub + 13, strlen(sub + 13) + 1);
    }
    snprintf(le->field, sizeof le->field, "%s", f);
}

typedef enum { T_STR, T_BOOL, T_NUM, T_ARR, T_OBJ } jtype_t;

typedef struct {
    const char *key;
    jtype_t type;
} jkey_t;

static const jkey_t TUNNEL_KEYS[] = {
    {"id", T_STR},     {"device", T_STR},  {"enable", T_BOOL},  {"uplink", T_OBJ},
    {"sources", T_ARR}, {"filter", T_STR},  {"filterExclude", T_STR}, {"order", T_ARR},
    {"exclude", T_ARR}, {"active", T_NUM},  {"by", T_STR},      {"interval", T_NUM},
    {"silence", T_NUM}, {"advanced", T_OBJ}, {"description", T_STR}, {"inbounds", T_ARR},
    {"routerPolicies", T_BOOL},
};
static const jkey_t INBOUND_KEYS[] = {
    {"port", T_NUM}, {"listen", T_STR}, {"node", T_STR}, {"user", T_STR}, {"password", T_STR}};
static const jkey_t UPLINK_KEYS[] = {{"kind", T_STR}, {"ref", T_STR}};
static const jkey_t ADVANCED_KEYS[] = {
    {"timeout", T_NUM}, {"insecure", T_BOOL}, {"ca", T_STR}, {"fingerprint", T_STR}, {"kernelTcp", T_BOOL}};
static const jkey_t SOURCE_KEYS[] = {{"id", T_STR},   {"kind", T_STR}, {"link", T_STR},
                                     {"name", T_STR}, {"url", T_STR},  {"interval", T_NUM}, {"headers", T_OBJ}};

static bool type_is(const cJSON *v, jtype_t t)
{
    switch (t) {
    case T_STR:
        return cJSON_IsString(v);
    case T_BOOL:
        return cJSON_IsBool(v);
    case T_NUM:
        return cJSON_IsNumber(v);
    case T_ARR:
        return cJSON_IsArray(v);
    case T_OBJ:
    default:
        return cJSON_IsObject(v);
    }
}

static const char *type_why(jtype_t t)
{
    switch (t) {
    case T_STR:
        return "must be a string";
    case T_BOOL:
        return "must be true or false";
    case T_NUM:
        return "must be a number";
    case T_ARR:
        return "must be a list";
    case T_OBJ:
    default:
        return "must be an object";
    }
}

static bool refuse_type(load_err_t *le, const char *why, const char *field)
{
    snprintf(le->err.why, sizeof le->err.why, "%s", why);
    snprintf(le->field, sizeof le->field, "%s", field);
    le->has_field = true;
    return false;
}

static bool keys_typed(const cJSON *o, const jkey_t *keys, size_t n, const char *prefix, load_err_t *le)
{
    for (size_t i = 0; i < n; i++) {
        const cJSON *v = cJSON_GetObjectItemCaseSensitive(o, keys[i].key);
        if (v != NULL && !cJSON_IsNull(v) && !type_is(v, keys[i].type)) {
            char f[96];
            snprintf(f, sizeof f, "%s%s", prefix, keys[i].key);
            return refuse_type(le, type_why(keys[i].type), f);
        }
    }
    return true;
}

static bool strings_typed(const cJSON *a, const char *name, load_err_t *le)
{
    int i = 0;
    const cJSON *v;
    cJSON_ArrayForEach(v, a)
    {
        if (!cJSON_IsString(v)) {
            char f[96];
            snprintf(f, sizeof f, "%s[%d]", name, i);
            return refuse_type(le, "must be a string", f);
        }
        i++;
    }
    return true;
}

static bool tunnel_typed(const cJSON *t, load_err_t *le)
{
    if (!cJSON_IsObject(t)) {
        snprintf(le->err.why, sizeof le->err.why, "must be a tunnel");
        return false;
    }
    const cJSON *id = cJSON_GetObjectItemCaseSensitive(t, "id");
    if (cJSON_IsString(id)) {
        le->has_tunnel = true;
        snprintf(le->tunnel, sizeof le->tunnel, "%.63s", id->valuestring);
    }
    if (!keys_typed(t, TUNNEL_KEYS, sizeof TUNNEL_KEYS / sizeof TUNNEL_KEYS[0], "", le)) {
        return false;
    }
    const cJSON *up = cJSON_GetObjectItemCaseSensitive(t, "uplink");
    const cJSON *adv = cJSON_GetObjectItemCaseSensitive(t, "advanced");
    if ((cJSON_IsObject(up) && !keys_typed(up, UPLINK_KEYS, 2, "uplink.", le)) ||
        (cJSON_IsObject(adv) && !keys_typed(adv, ADVANCED_KEYS, 5, "advanced.", le)) ||
        !strings_typed(cJSON_GetObjectItemCaseSensitive(t, "order"), "order", le) ||
        !strings_typed(cJSON_GetObjectItemCaseSensitive(t, "exclude"), "exclude", le)) {
        return false;
    }
    int ib = 0;
    const cJSON *inb;
    cJSON_ArrayForEach(inb, cJSON_GetObjectItemCaseSensitive(t, "inbounds"))
    {
        char prefix[40];
        snprintf(prefix, sizeof prefix, "inbounds[%d]", ib);
        if (!cJSON_IsObject(inb)) {
            return refuse_type(le, "must be an object", prefix);
        }
        snprintf(prefix, sizeof prefix, "inbounds[%d].", ib++);
        if (!keys_typed(inb, INBOUND_KEYS, sizeof INBOUND_KEYS / sizeof INBOUND_KEYS[0], prefix, le)) {
            return false;
        }
    }
    int i = 0;
    const cJSON *src;
    cJSON_ArrayForEach(src, cJSON_GetObjectItemCaseSensitive(t, "sources"))
    {
        char prefix[32];
        snprintf(prefix, sizeof prefix, "sources[%d]", i);
        if (!cJSON_IsObject(src)) {
            return refuse_type(le, "must be an object", prefix);
        }
        snprintf(prefix, sizeof prefix, "sources[%d].", i);
        if (!keys_typed(src, SOURCE_KEYS, sizeof SOURCE_KEYS / sizeof SOURCE_KEYS[0], prefix, le)) {
            return false;
        }
        const cJSON *hv;
        cJSON_ArrayForEach(hv, cJSON_GetObjectItemCaseSensitive(src, "headers"))
        {
            if (!cJSON_IsString(hv)) {
                snprintf(prefix, sizeof prefix, "sources[%d].headers", i);
                return refuse_type(le, "must be a string", prefix);
            }
        }
        i++;
    }
    return true;
}

static bool set_typed(const cJSON *arr, load_err_t *le)
{
    const cJSON *t;
    cJSON_ArrayForEach(t, arr)
    {
        load_err_t one;
        memset(&one, 0, sizeof one);
        if (!tunnel_typed(t, &one)) {
            *le = one;
            return false;
        }
    }
    return true;
}

static firc_err_t load_json(const cJSON *arr, firc_tunnels_t *out, load_err_t *le)
{
    memset(le, 0, sizeof *le);
    memset(out, 0, sizeof *out);
    if (!set_typed(arr, le)) {
        return FIRC_ERR_INVAL;
    }
    cJSON *root = cJSON_CreateObject();
    cJSON *list = cJSON_CreateArray();
    bool ok = root != NULL && list != NULL;
    const cJSON *it;
    cJSON_ArrayForEach(it, arr)
    {
        cJSON *t = ok ? shape_tunnel(it) : NULL;
        ok = t != NULL && cJSON_AddItemToArray(list, t);
        if (!ok) {
            cJSON_Delete(t);
        }
    }
    if (ok) {
        ok = put_item(root, "tunnels", list);
        list = NULL;
    }
    char *text = ok ? cJSON_PrintUnformatted(root) : NULL;
    cJSON_Delete(list);
    cJSON_Delete(root);
    if (text == NULL) {
        return FIRC_ERR_NOMEM;
    }
    firc_err_t err = firc_tunnels_load_buffer(out, text, strlen(text), &le->err);
    free(text);
    if (err == FIRC_ERR_INVAL) {
        map_where(arr, le);
    }
    return err;
}

static void write_load_error(firc_http_res_t *res, firc_err_t err, const load_err_t *le)
{
    if (err == FIRC_ERR_INVAL) {
        write_tun_error(res, 400, le->err.why, le->has_field ? le->field : NULL, le->has_tunnel ? le->tunnel : NULL);
    } else {
        firc_http_res_write_error(res, 500, firc_err_str(err));
    }
}

static bool require_run(firc_tunnels_ctx_t *ctx, firc_http_res_t *res)
{
    if (ctx->run == NULL) {
        firc_http_res_write_error(res, 503, "tunnels are not running");
        return false;
    }
    return true;
}

static void handle_get(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    (void)req;
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || !put_item(o, "tunnels", tunnels_json(firc_tunrun_config(ctx->run)))) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static const firc_tunnel_t *saved_tunnel(firc_tunnels_ctx_t *ctx, const char *id)
{
    const firc_tunnels_t *cur = firc_tunrun_config(ctx->run);
    for (size_t i = 0; id != NULL && i < cur->n; i++) {
        if (strcmp(cur->t[i].id, id) == 0) {
            return &cur->t[i];
        }
    }
    return NULL;
}

static void prune_held(firc_tunnels_ctx_t *ctx, firc_tunnels_t *set)
{
    for (size_t i = 0; i < set->n; i++) {
        firc_tunnel_t *t = &set->t[i];
        firc_tun_body_t *b = calloc(t->n_src + 1, sizeof *b);
        if (b == NULL) {
            continue;
        }
        const firc_tunnel_t *saved = saved_tunnel(ctx, t->id);
        size_t nb = firc_tunsubs_held(ctx->subs, saved != NULL ? saved : t, t, b, t->n_src);
        size_t dropped = 0;
        (void)firc_tun_nodes_prune(t, 0, b, nb, &dropped);
        free(b);
    }
}

static void handle_put(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    cJSON *body = parse_body_json(req);
    const cJSON *arr = cJSON_GetObjectItemCaseSensitive(body, "tunnels");
    if (!cJSON_IsArray(arr)) {
        cJSON_Delete(body);
        write_tun_error(res, 400, "must be a list", "tunnels", NULL);
        return;
    }
    firc_tunnels_t set;
    load_err_t le;
    firc_err_t err = load_json(arr, &set, &le);
    bool checked = err == FIRC_OK && ctx->app != NULL;
    if (checked) {
        err = firc_inbounds_check(&set, ctx->run, firc_app_running_settings(ctx->app), &le.err);
        if (err == FIRC_ERR_INVAL) {
            map_where(arr, &le);
        }
    }
    cJSON_Delete(body);
    if (err != FIRC_OK) {
        if (checked) {
            firc_tunnels_free(&set);
        }
        write_load_error(res, err, &le);
        return;
    }
    prune_held(ctx, &set);
    err = firc_tunnels_save_file(&set, ctx->path);
    if (err != FIRC_OK) {
        firc_tunnels_free(&set);
        FIRC_ERROR("tunnels: %s not written: %s", ctx->path, firc_err_str(err));
        firc_http_res_write_error(res, 500, "tunnels.yaml not written");
        return;
    }
    uint64_t seq = firc_tunrun_start_seq(ctx->run);
    err = firc_tunrun_apply(ctx->run, &set);
    bool taken = set.t == NULL;
    firc_tunnels_free(&set);
    if (!taken) {
        FIRC_ERROR("tunnels saved, not applied: %s; next reload applies them", firc_err_str(err));
        firc_http_res_write_error(res, 500, "saved, but not applied");
        return;
    }
    if (err != FIRC_OK) {
        FIRC_WARN("tunnels: applied with errors: %s", firc_err_str(err));
    }
    const firc_tunnels_t *cur = firc_tunrun_config(ctx->run);
    cJSON *o = cJSON_CreateObject();
    cJSON *restarted = cJSON_CreateArray();
    cJSON *updated = cJSON_CreateArray();
    bool ok = o != NULL && restarted != NULL && updated != NULL;
    for (size_t i = 0; ok && i < cur->n; i++) {
        bool started = firc_tunrun_started(ctx->run, cur->t[i].id) > seq;
        cJSON *list = started ? restarted : firc_tunrun_updated(ctx->run, cur->t[i].id) > seq ? updated : NULL;
        if (list != NULL) {
            cJSON *s = cJSON_CreateString(cur->t[i].device);
            ok = s != NULL && cJSON_AddItemToArray(list, s);
            if (!ok) {
                cJSON_Delete(s);
            }
        }
    }
    ok = ok && put_item(o, "tunnels", tunnels_json(cur));
    if (ok) {
        ok = put_item(o, "restarted", restarted);
        restarted = NULL;
    }
    if (ok) {
        ok = put_item(o, "updated", updated);
        updated = NULL;
    }
    cJSON_Delete(restarted);
    cJSON_Delete(updated);
    if (!ok) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static const char *status_name(firc_tun_status_t s)
{
    switch (s) {
    case FIRC_TUN_ST_STARTING:
        return "starting";
    case FIRC_TUN_ST_UP:
        return "up";
    case FIRC_TUN_ST_NO_NODE:
        return "no_node";
    case FIRC_TUN_ST_BACKOFF:
        return "backoff";
    case FIRC_TUN_ST_BAD_CONFIG:
        return "bad_config";
    case FIRC_TUN_ST_WAITING:
        return "waiting";
    case FIRC_TUN_ST_UPLINK_DOWN:
        return "uplink_down";
    case FIRC_TUN_ST_ROUTER:
        return "router";
    case FIRC_TUN_ST_OFF:
    default:
        return "off";
    }
}

static bool read_counter(const char *root, const char *dev, const char *which, uint64_t *out)
{
    char path[256];
    snprintf(path, sizeof path, "%s/%s/statistics/%s", root, dev, which);
    FILE *f = fopen(path, "re");
    if (f == NULL) {
        return false;
    }
    char line[32];
    bool ok = fgets(line, sizeof line, f) != NULL;
    fclose(f);
    if (!ok) {
        return false;
    }
    char *end = NULL;
    errno = 0;
    unsigned long long v = strtoull(line, &end, 10);
    if (errno != 0 || end == line || (*end != '\n' && *end != 0)) {
        return false;
    }
    *out = (uint64_t)v;
    return true;
}

static void rate_of_key(firc_tunnels_ctx_t *ctx, const char *dev, uint64_t rx, uint64_t tx, uint64_t *rx_bps,
                        uint64_t *tx_bps)
{
    int64_t now = mono_ms();
    size_t n = sizeof ctx->rates / sizeof ctx->rates[0];
    firc_tun_rate_t *slot = NULL;
    for (size_t i = 0; i < n && slot == NULL; i++) {
        if (strcmp(ctx->rates[i].dev, dev) == 0) {
            slot = &ctx->rates[i];
        }
    }
    if (slot != NULL) {
        int64_t dt = now - slot->at_ms;
        if (dt <= 0) {
            return;
        }
        if (rx >= slot->rx && tx >= slot->tx) {
            *rx_bps = (rx - slot->rx) * 1000u / (uint64_t)dt;
            *tx_bps = (tx - slot->tx) * 1000u / (uint64_t)dt;
        }
    } else {
        slot = &ctx->rates[0];
        for (size_t i = 0; i < n; i++) {
            if (ctx->rates[i].dev[0] == 0 || ctx->rates[i].at_ms < slot->at_ms) {
                slot = &ctx->rates[i];
                if (slot->dev[0] == 0) {
                    break;
                }
            }
        }
        snprintf(slot->dev, sizeof slot->dev, "%s", dev);
    }
    slot->rx = rx;
    slot->tx = tx;
    slot->at_ms = now;
}

static void rates_of(firc_tunnels_ctx_t *ctx, const char *dev, uint64_t *rx_bps, uint64_t *tx_bps)
{
    *rx_bps = 0;
    *tx_bps = 0;
    const char *root = ctx->sysfs_net != NULL ? ctx->sysfs_net : "/sys/class/net";
    uint64_t rx = 0, tx = 0;
    if (!read_counter(root, dev, "rx_bytes", &rx) || !read_counter(root, dev, "tx_bytes", &tx)) {
        return;
    }
    uint64_t krx = 0, ktx = 0;
    (void)firc_tunrun_ktcp_bytes(ctx->run, dev, &krx, &ktx);
    rate_of_key(ctx, dev, rx + krx, tx + ktx, rx_bps, tx_bps);
}

static cJSON *node_state_json(firc_tunnels_ctx_t *ctx, const firc_tun_state_t *st, const firc_tun_node_t *row)
{
    const char *state = "reserve";
    const char *skip = "";
    int64_t since = 0;
    bool v6 = false;
    if (row->excluded) {
        state = "excluded";
    } else if (row->over_cap || row->skip_reason[0] != 0) {
        state = "skipped";
        skip = row->over_cap ? "over the 256-node limit" : row->skip_reason;
    } else if (st->n_active > 0 && firc_tunrun_node_active(ctx->run, st->id, row->key)) {
        state = "active";
        v6 = firc_tunrun_node_v6(ctx->run, st->id, row->key);
    } else if (firc_tunrun_node_down(ctx->run, st->id, row->key, &since)) {
        state = "down";
    }
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddStringToObject(o, "key", row->key) == NULL ||
        cJSON_AddStringToObject(o, "name", row->name) == NULL ||
        cJSON_AddStringToObject(o, "source", row->source) == NULL ||
        cJSON_AddStringToObject(o, "state", state) == NULL ||
        cJSON_AddNumberToObject(o, "since", (double)wall_s_of(since)) == NULL ||
        cJSON_AddStringToObject(o, "skipReason", skip) == NULL || (v6 && cJSON_AddTrueToObject(o, "v6") == NULL)) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static cJSON *sub_state_json(firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t, const firc_tun_src_t *src, uint32_t mark)
{
    firc_tun_sub_state_t ss;
    memset(&ss, 0, sizeof ss);
    bool has_state = t->uplink == FIRC_UPLINK_AUTO || mark != 0;
    if (!has_state || ctx->subs == NULL ||
        firc_tunsubs_state(ctx->subs, src->sub.url, mark, firc_tun_sub_headers_sig(&src->sub), &ss) == 0) {
        memset(&ss, 0, sizeof ss);
    }
    int64_t ok_s = ss.last_ok_ms / 1000;
    int64_t try_s = ss.last_try_ms / 1000;
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddStringToObject(o, "name", src->sub.name) == NULL ||
        cJSON_AddNumberToObject(o, "nodes", (double)ss.nodes) == NULL ||
        cJSON_AddNumberToObject(o, "lastOk", (double)ok_s) == NULL ||
        cJSON_AddNumberToObject(o, "lastTry", (double)try_s) == NULL ||
        cJSON_AddStringToObject(o, "error", ss.error) == NULL ||
        cJSON_AddBoolToObject(o, "fetching", ss.fetching) == NULL) {
        cJSON_Delete(o);
        return NULL;
    }
    cJSON *usage = ss.info.have ? cJSON_CreateObject() : cJSON_CreateNull();
    bool ok = usage != NULL &&
              (!ss.info.have ||
               (cJSON_AddNumberToObject(usage, "used", (double)(ss.info.upload + ss.info.download)) != NULL &&
                cJSON_AddNumberToObject(usage, "total", (double)ss.info.total) != NULL &&
                cJSON_AddNumberToObject(usage, "expire", (double)ss.info.expire) != NULL));
    if (!ok || !cJSON_AddItemToObject(o, "usage", usage)) {
        cJSON_Delete(usage);
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static cJSON *groups_of(firc_tunnels_ctx_t *ctx, const char *device)
{
    cJSON *a = cJSON_CreateArray();
    size_t n = ctx->app != NULL ? firc_app_user_group_count(ctx->app) : 0;
    for (size_t i = 0; a != NULL && i < n; i++) {
        const firc_group_t *g = firc_ruleset_group(firc_app_user_group_at(ctx->app, i));
        if (g == NULL || !firc_group_has_iface(g, device)) {
            continue;
        }
        cJSON *s = cJSON_CreateString(g->name != NULL ? g->name : "");
        if (s == NULL || !cJSON_AddItemToArray(a, s)) {
            cJSON_Delete(s);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *skipped_json(firc_tunnels_ctx_t *ctx, uint16_t port)
{
    cJSON *a = cJSON_CreateArray();
    const firc_xt_acl_t *acl = ctx->app != NULL ? firc_app_ktcp_skipped(ctx->app) : NULL;
    if (a == NULL || port == 0 || acl == NULL) {
        return a;
    }
    for (size_t i = 0; i < acl->n; i++) {
        cJSON *s = cJSON_CreateString(acl->iface[i]);
        if (s == NULL || !cJSON_AddItemToArray(a, s)) {
            cJSON_Delete(s);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static const char *off_word(firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t)
{
    if (firc_tunnel_tcp_port(t) == 0 || ctx->app == NULL) {
        return "";
    }
    switch (firc_app_ktcp_off(ctx->app)) {
    case FIRC_KTCP_OFF_ACL:
        return "acl";
    case FIRC_KTCP_OFF_KERNEL:
        return "kernel";
    case FIRC_KTCP_OFF_REFUSED:
        return "refused";
    case FIRC_KTCP_OFF_NONE:
        break;
    }
    return "";
}

static cJSON *inbounds_state_json(firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t, const firc_tun_state_t *st)
{
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && i < st->n_ib && i < FIRC_TUN_MAX_INBOUNDS; i++) {
        const firc_tun_ib_state_t *ib = &st->ib[i];
        char key[sizeof ctx->rates[0].dev];
        snprintf(key, sizeof key, "%s:%u", t->device, (unsigned)ib->port);
        uint64_t rx_bps = 0, tx_bps = 0;
        rate_of_key(ctx, key, ib->rx, ib->tx, &rx_bps, &tx_bps);
        cJSON *o = cJSON_CreateObject();
        bool ok = o != NULL && cJSON_AddNumberToObject(o, "port", ib->port) != NULL &&
                  cJSON_AddStringToObject(o, "state", ib->state) != NULL &&
                  cJSON_AddStringToObject(o, "why", ib->why) != NULL &&
                  cJSON_AddNumberToObject(o, "rxBps", (double)rx_bps) != NULL &&
                  cJSON_AddNumberToObject(o, "txBps", (double)tx_bps) != NULL && cJSON_AddItemToArray(a, o);
        if (!ok) {
            cJSON_Delete(o);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static const char *opkg_state_name(firc_opkg_state_t s)
{
    switch (s) {
    case FIRC_OPKG_READY:
        return "ready";
    case FIRC_OPKG_FAILED:
        return "failed";
    case FIRC_OPKG_FOREIGN:
        return "foreign";
    case FIRC_OPKG_PENDING:
        break;
    }
    return "pending";
}

static const char *pol_state_name(firc_opkg_pol_t p)
{
    switch (p) {
    case FIRC_OPKG_POL_ON:
        return "on";
    case FIRC_OPKG_POL_FAILED:
        return "failed";
    case FIRC_OPKG_POL_OFF:
        break;
    }
    return "off";
}

static bool put_router(cJSON *o, firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t)
{
    firc_opkgtun_seen_t r;
    if (!firc_tunrun_router_of(ctx->run, t->id, &r)) {
        return true;
    }
    cJSON *x = cJSON_CreateObject();
    cJSON *p = cJSON_CreateObject();
    bool ok = x != NULL && p != NULL && cJSON_AddStringToObject(x, "name", r.name) != NULL &&
              cJSON_AddStringToObject(x, "state", opkg_state_name(r.state)) != NULL &&
              cJSON_AddBoolToObject(x, "saved", r.saved) != NULL &&
              cJSON_AddStringToObject(x, "error", r.error) != NULL &&
              cJSON_AddStringToObject(p, "state", pol_state_name(r.policies)) != NULL &&
              cJSON_AddStringToObject(p, "error", r.pol_error) != NULL;
    if (ok) {
        ok = put_item(x, "policies", p);
        p = NULL;
    }
    cJSON_Delete(p);
    if (!ok) {
        cJSON_Delete(x);
        return false;
    }
    return put_item(o, "router", x);
}

static cJSON *filtered_nodes_json(const firc_tun_nodes_t *rows)
{
    cJSON *a = cJSON_CreateArray();
    for (size_t i = 0; a != NULL && rows != NULL && i < rows->n_filtered; i++) {
        const firc_tun_filtered_t *f = &rows->filtered[i];
        cJSON *x = cJSON_CreateObject();
        bool ok = x != NULL && cJSON_AddStringToObject(x, "key", f->key) != NULL &&
                  cJSON_AddStringToObject(x, "name", f->name) != NULL &&
                  cJSON_AddStringToObject(x, "source", f->source) != NULL &&
                  cJSON_AddItemToArray(a, x);
        if (!ok) {
            cJSON_Delete(x);
            cJSON_Delete(a);
            return NULL;
        }
    }
    return a;
}

static cJSON *tunnel_state_json(firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t, const firc_tun_state_t *st)
{
    cJSON *o = cJSON_CreateObject();
    cJSON *active = cJSON_CreateArray();
    cJSON *nodes = cJSON_CreateArray();
    cJSON *subs = cJSON_CreateArray();
    bool ok = o != NULL && active != NULL && nodes != NULL && subs != NULL;
    for (size_t i = 0; ok && i < st->n_active && i < 8; i++) {
        cJSON *s = cJSON_CreateString(st->active[i]);
        ok = s != NULL && cJSON_AddItemToArray(active, s);
        if (!ok) {
            cJSON_Delete(s);
        }
    }
    const firc_tun_nodes_t *rows = firc_tunrun_nodes(ctx->run, t->id);
    for (size_t i = 0; ok && rows != NULL && i < rows->n; i++) {
        cJSON *n = node_state_json(ctx, st, &rows->v[i]);
        ok = n != NULL && cJSON_AddItemToArray(nodes, n);
        if (!ok) {
            cJSON_Delete(n);
        }
    }
    uint32_t mark = 0;
    bool held = false;
    (void)firc_tunrun_mark_of(ctx->run, t->id, &mark, &held);
    for (size_t i = 0; ok && i < t->n_src; i++) {
        if (t->src[i].kind != FIRC_TUN_SRC_SUB) {
            continue;
        }
        cJSON *s = sub_state_json(ctx, t, &t->src[i], mark);
        ok = s != NULL && cJSON_AddItemToArray(subs, s);
        if (!ok) {
            cJSON_Delete(s);
        }
    }
    uint64_t rx = 0, tx = 0;
    rates_of(ctx, t->device, &rx, &tx);
    ok = ok && cJSON_AddStringToObject(o, "id", t->id) != NULL &&
         cJSON_AddStringToObject(o, "device", t->device) != NULL &&
         cJSON_AddStringToObject(o, "status", status_name(st->status)) != NULL &&
         cJSON_AddStringToObject(o, "why", st->why) != NULL && put_router(o, ctx, t);
    if (ok) {
        ok = put_item(o, "active", active);
        active = NULL;
    }
    if (ok) {
        ok = put_item(o, "nodes", nodes);
        nodes = NULL;
    }
    if (ok && rows != NULL && rows->n_filtered > 0) {
        ok = put_item(o, "filteredNodes", filtered_nodes_json(rows));
    }
    ok = ok && cJSON_AddNumberToObject(o, "since", (double)wall_s_of(st->since_ms)) != NULL &&
         cJSON_AddNumberToObject(o, "backoffS", st->backoff_s) != NULL &&
         cJSON_AddNumberToObject(o, "lastExit", st->last_exit) != NULL &&
         cJSON_AddBoolToObject(o, "uplinkOk", st->uplink_ok) != NULL &&
         cJSON_AddNumberToObject(o, "tcpPort", st->tcp_port) != NULL &&
         put_item(o, "tcpSkipped", skipped_json(ctx, st->tcp_port)) &&
         cJSON_AddStringToObject(o, "tcpOff", off_word(ctx, t)) != NULL &&
         cJSON_AddNumberToObject(o, "rxBps", (double)rx) != NULL &&
         cJSON_AddNumberToObject(o, "txBps", (double)tx) != NULL &&
         put_item(o, "groups", groups_of(ctx, t->device)) && put_item(o, "inbounds", inbounds_state_json(ctx, t, st));
    if (ok) {
        ok = put_item(o, "subscriptions", subs);
        subs = NULL;
    }
    cJSON_Delete(active);
    cJSON_Delete(nodes);
    cJSON_Delete(subs);
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static void handle_state(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    (void)req;
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    const firc_tunnels_t *cur = firc_tunrun_config(ctx->run);
    firc_tun_state_t *st = calloc(FIRC_TUN_MAX, sizeof(*st));
    if (st == NULL) {
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    size_t n = firc_tunrun_states(ctx->run, st, FIRC_TUN_MAX);
    cJSON *o = cJSON_CreateObject();
    cJSON *a = cJSON_CreateArray();
    bool ok = o != NULL && a != NULL;
    for (size_t i = 0; ok && i < n && i < FIRC_TUN_MAX && i < cur->n; i++) {
        cJSON *t = tunnel_state_json(ctx, &cur->t[i], &st[i]);
        ok = t != NULL && cJSON_AddItemToArray(a, t);
        if (!ok) {
            cJSON_Delete(t);
        }
    }
    free(st);
    if (ok) {
        ok = put_item(o, "tunnels", a);
        a = NULL;
    }
    cJSON_Delete(a);
    if (!ok) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static void handle_refresh(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    const char *id = firc_http_req_param(req, "id");
    if (saved_tunnel(ctx, id) == NULL) {
        firc_http_res_write_error(res, 404, "no tunnel with this id");
        return;
    }
    bool queued = ctx->subs != NULL && firc_tunsubs_refresh(ctx->subs, id) == FIRC_OK;
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddBoolToObject(o, "queued", queued) == NULL) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 202, o);
}

static void handle_restart(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    firc_err_t err = firc_tunrun_restart(ctx->run, firc_http_req_param(req, "id"));
    if (err == FIRC_ERR_NOENT) {
        firc_http_res_write_error(res, 404, "no tunnel with this id");
        return;
    }
    if (err != FIRC_OK) {
        firc_http_res_write_error(res, 409, "the tunnel has no process to restart");
        return;
    }
    cJSON *o = cJSON_CreateObject();
    if (o == NULL || cJSON_AddBoolToObject(o, "queued", true) == NULL) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 202, o);
}

static bool replace_item(cJSON *o, const char *key, cJSON *v)
{
    if (v == NULL) {
        return false;
    }
    if (cJSON_GetObjectItemCaseSensitive(o, key) != NULL) {
        return cJSON_ReplaceItemInObjectCaseSensitive(o, key, v) != 0;
    }
    return put_item(o, key, v);
}

static bool load_draft(firc_tunnels_ctx_t *ctx, const cJSON *draft, firc_http_res_t *res, firc_tunnels_t *set,
                       size_t *at)
{
    if (!cJSON_IsObject(draft)) {
        write_tun_error(res, 400, "must be a tunnel", "tunnel", NULL);
        return false;
    }
    const cJSON *did = cJSON_GetObjectItemCaseSensitive(draft, "id");
    cJSON *arr = tunnels_json(firc_tunrun_config(ctx->run));
    cJSON *copy = cJSON_Duplicate(draft, true);
    bool ok = arr != NULL && copy != NULL && replace_item(copy, "enable", cJSON_CreateFalse());
    int pos = -1;
    for (int i = 0; ok && i < cJSON_GetArraySize(arr); i++) {
        const cJSON *id = cJSON_GetObjectItemCaseSensitive(cJSON_GetArrayItem(arr, i), "id");
        if (cJSON_IsString(did) && cJSON_IsString(id) && strcmp(id->valuestring, did->valuestring) == 0) {
            pos = i;
        }
    }
    if (ok && pos >= 0) {
        ok = cJSON_ReplaceItemInArray(arr, pos, copy) != 0;
    } else if (ok) {
        pos = cJSON_GetArraySize(arr);
        ok = cJSON_AddItemToArray(arr, copy) != 0;
    }
    if (!ok) {
        cJSON_Delete(copy);
        cJSON_Delete(arr);
        firc_http_res_write_error(res, 500, "out of memory");
        return false;
    }
    load_err_t le;
    firc_err_t err = load_json(arr, set, &le);
    cJSON_Delete(arr);
    if (err != FIRC_OK) {
        write_load_error(res, err, &le);
        return false;
    }
    *at = (size_t)pos;
    return true;
}

static bool same_uplink(const firc_tunnel_t *a, const firc_tunnel_t *b)
{
    return a->uplink == b->uplink && strcmp(a->uplink_ref, b->uplink_ref) == 0;
}

static bool usable_mark(firc_tunnels_ctx_t *ctx, const firc_tunnel_t *t, uint32_t *mark)
{
    *mark = 0;
    if (t->uplink == FIRC_UPLINK_AUTO) {
        return true;
    }
    const firc_tunnel_t *saved = saved_tunnel(ctx, t->id);
    bool held = false;
    if (saved == NULL || !same_uplink(saved, t) || !firc_tunrun_mark_of(ctx->run, t->id, mark, &held)) {
        return false;
    }
    return *mark != 0 && held;
}

static const firc_tun_body_t *bodies_of(firc_tunnels_ctx_t *ctx, size_t *n)
{
    *n = 0;
    return ctx->subs != NULL ? firc_tunsubs_bodies(ctx->subs, n) : NULL;
}

static bool body_cached(const firc_tun_body_t *b, size_t n, const char *url, uint32_t mark, uint64_t hsig)
{
    for (size_t i = 0; i < n; i++) {
        if (b[i].mark == mark && b[i].hsig == hsig && b[i].url != NULL && strcmp(b[i].url, url) == 0) {
            return true;
        }
    }
    return false;
}

static cJSON *preview_json(const firc_tunnel_t *t, const firc_tun_nodes_t *nodes, const firc_tun_body_t *b,
                           size_t nb, uint32_t mark)
{
    cJSON *o = cJSON_CreateObject();
    cJSON *rows = cJSON_CreateArray();
    cJSON *subs = cJSON_CreateArray();
    bool ok = o != NULL && rows != NULL && subs != NULL;
    for (size_t i = 0; ok && i < nodes->n; i++) {
        const firc_tun_node_t *r = &nodes->v[i];
        cJSON *x = cJSON_CreateObject();
        ok = x != NULL && cJSON_AddStringToObject(x, "key", r->key) != NULL &&
             cJSON_AddStringToObject(x, "name", r->name) != NULL &&
             cJSON_AddStringToObject(x, "source", r->source) != NULL &&
             cJSON_AddBoolToObject(x, "isNew", r->is_new) != NULL &&
             cJSON_AddBoolToObject(x, "excluded", r->excluded) != NULL &&
             cJSON_AddBoolToObject(x, "overCap", r->over_cap) != NULL &&
             cJSON_AddStringToObject(x, "skipReason", r->skip_reason) != NULL && cJSON_AddItemToArray(rows, x);
        if (!ok) {
            cJSON_Delete(x);
        }
    }
    for (size_t i = 0; ok && i < t->n_src; i++) {
        const firc_tun_src_t *src = &t->src[i];
        if (src->kind != FIRC_TUN_SRC_SUB) {
            continue;
        }
        cJSON *x = cJSON_CreateObject();
        ok = x != NULL && cJSON_AddStringToObject(x, "name", src->sub.name) != NULL &&
             cJSON_AddBoolToObject(x, "cached", b != NULL && body_cached(b, nb, src->sub.url, mark, firc_tun_sub_headers_sig(&src->sub))) != NULL &&
             cJSON_AddItemToArray(subs, x);
        if (!ok) {
            cJSON_Delete(x);
        }
    }
    if (ok) {
        ok = put_item(o, "nodes", rows);
        rows = NULL;
    }
    if (ok && nodes->n_filtered > 0) {
        ok = put_item(o, "filteredNodes", filtered_nodes_json(nodes));
    }
    ok = ok && cJSON_AddNumberToObject(o, "matched", (double)nodes->matched) != NULL &&
         cJSON_AddNumberToObject(o, "total", (double)nodes->total) != NULL;
    if (ok) {
        ok = put_item(o, "subscriptions", subs);
        subs = NULL;
    }
    cJSON_Delete(rows);
    cJSON_Delete(subs);
    if (!ok) {
        cJSON_Delete(o);
        return NULL;
    }
    return o;
}

static void write_nodes_build_err(firc_http_res_t *res, firc_err_t err)
{
    if (err == FIRC_ERR_LIMIT) {
        firc_http_res_write_error(res, 400, "filter regex exceeded match limit");
        return;
    }
    if (err == FIRC_ERR_INVAL) {
        firc_http_res_write_error(res, 400, "invalid filter regex");
        return;
    }
    firc_http_res_write_error(res, 500, err == FIRC_OK ? "out of memory" : firc_err_str(err));
}

static void handle_preview(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    cJSON *body = parse_body_json(req);
    firc_tunnels_t set;
    size_t at = 0;
    bool ok = load_draft(ctx, cJSON_GetObjectItemCaseSensitive(body, "tunnel"), res, &set, &at);
    cJSON_Delete(body);
    if (!ok) {
        return;
    }
    const firc_tunnel_t *t = &set.t[at];
    const firc_tunnel_t *saved = saved_tunnel(ctx, t->id);
    firc_tun_body_t *b = t->n_src > 0 ? calloc(t->n_src, sizeof *b) : NULL;
    if (t->n_src > 0 && b == NULL) {
        firc_tunnels_free(&set);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    size_t nb = firc_tunsubs_held(ctx->subs, saved != NULL ? saved : t, t, b, t->n_src);
    firc_tun_nodes_t nodes;
    firc_err_t err = firc_tun_nodes_build(t, 0, b, nb, &nodes);
    cJSON *o = err == FIRC_OK ? preview_json(t, &nodes, b, nb, 0) : NULL;
    if (err == FIRC_OK) {
        firc_tun_nodes_free(&nodes);
    }
    free(b);
    firc_tunnels_free(&set);
    if (o == NULL) {
        write_nodes_build_err(res, err);
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static void call_free(tun_probe_call_t *c)
{
    for (size_t i = 0; c->links != NULL && i < c->n; i++) {
        free(c->links[i]);
    }
    free(c->links);
    free(c->saved);
    free(c->keys);
    free(c);
}

static bool add_probe_row(cJSON *a, const char *key, const firc_tunprobe_row_t *r)
{
    cJSON *x = cJSON_CreateObject();
    bool ok = x != NULL && cJSON_AddStringToObject(x, "key", key) != NULL &&
              cJSON_AddBoolToObject(x, "ok", r->have && r->ok) != NULL &&
              (r->have && r->ok ? cJSON_AddNumberToObject(x, "handshakeMs", r->handshake_ms)
                                : cJSON_AddNullToObject(x, "handshakeMs")) != NULL &&
              (r->have && r->ok ? cJSON_AddNumberToObject(x, "firstByteMs", r->first_byte_ms)
                                : cJSON_AddNullToObject(x, "firstByteMs")) != NULL &&
              cJSON_AddStringToObject(x, "why", !r->have ? "no answer" : r->why) != NULL &&
              cJSON_AddItemToArray(a, x);
    if (!ok) {
        cJSON_Delete(x);
    }
    return ok;
}

static void probe_done(const firc_tunprobe_row_t *rows, size_t n, void *ud)
{
    tun_probe_call_t *c = ud;
    c->ctx->probe = NULL;
    cJSON *o = cJSON_CreateObject();
    cJSON *a = cJSON_CreateArray();
    bool ok = o != NULL && a != NULL;
    for (size_t i = 0; ok && i < n && i < c->n; i++) {
        const firc_tunprobe_row_t *r = &rows[i];
        if (c->saved != NULL && r->have && r->ok && c->ctx->run != NULL) {
            firc_tunrun_node_answered(c->ctx->run, c->saved, c->keys[i]);
        }
        ok = add_probe_row(a, c->keys[i], r);
    }
    if (ok) {
        ok = put_item(o, "nodes", a);
        a = NULL;
    }
    cJSON_Delete(a);
    size_t m = 0;
    for (size_t i = 0; c->saved != NULL && i < n && i < c->n; i++) {
        if (rows[i].have && rows[i].ok) {
            char *x = c->links[m];
            c->links[m++] = c->links[i];
            c->links[i] = x;
        }
    }
    if (m > 0 && c->ctx->run != NULL) {
        firc_tunrun_wake(c->ctx->run, c->saved, (const char *const *)c->links, m);
    }
    char *text = ok ? firc_json_dump(o) : NULL;
    cJSON_Delete(o);
    if (text == NULL) {
        static const char fail[] = "{\"error\":\"out of memory\"}";
        firc_http_deferred_finish(c->pending, 500, "application/json; charset=utf-8", (uint8_t *)strdup(fail),
                                  sizeof fail - 1);
    } else {
        firc_http_deferred_finish(c->pending, 200, "application/json; charset=utf-8", (uint8_t *)text,
                                  strlen(text));
    }
    call_free(c);
}

static void probe_client_left(void *ud)
{
    tun_probe_call_t *c = ud;
    c->ctx->probe = NULL;
    firc_tunprobe_cancel(c->probe);
    firc_http_deferred_drop(c->pending);
    call_free(c);
}

static bool probe_target(firc_tunnels_ctx_t *ctx, const cJSON *body, firc_http_res_t *res, firc_tunnels_t *set,
                         const firc_tunnel_t **t)
{
    const cJSON *id = cJSON_GetObjectItemCaseSensitive(body, "id");
    if (cJSON_IsString(id)) {
        *t = saved_tunnel(ctx, id->valuestring);
        if (*t == NULL) {
            firc_http_res_write_error(res, 404, "no tunnel with this id");
            return false;
        }
        return true;
    }
    size_t at = 0;
    if (!load_draft(ctx, cJSON_GetObjectItemCaseSensitive(body, "tunnel"), res, set, &at)) {
        return false;
    }
    *t = &set->t[at];
    return true;
}

static bool key_chosen(const cJSON *keys, const char *key)
{
    if (keys == NULL) {
        return true;
    }
    const cJSON *k = NULL;
    cJSON_ArrayForEach(k, keys)
    {
        if (cJSON_IsString(k) && strcmp(k->valuestring, key) == 0) {
            return true;
        }
    }
    return false;
}

static bool collect_probe_links(tun_probe_call_t *c, firc_tun_nodes_t *nodes, const firc_tunnel_t *t, uint32_t mark,
                                const firc_tun_body_t *b, size_t nb, const cJSON *keys)
{
    const char **capped = calloc(nodes->n + 1, sizeof *capped);
    if (capped == NULL) {
        return false;
    }
    size_t nc = 0;
    for (size_t i = 0; i < nodes->n; i++) {
        if ((nodes->v[i].link != NULL || nodes->v[i].over_cap) && key_chosen(keys, nodes->v[i].key)) {
            c->links[c->n] = nodes->v[i].link;
            nodes->v[i].link = NULL;
            snprintf(c->keys[c->n], sizeof c->keys[c->n], "%s", nodes->v[i].key);
            if (c->links[c->n] == NULL) {
                capped[nc++] = c->keys[c->n];
            }
            c->n++;
        }
    }
    char **got = calloc(nc + 1, sizeof *got);
    bool ok = got != NULL && (nc == 0 || firc_tun_nodes_links_of(t, mark, b, nb, capped, nc, got) == FIRC_OK);
    size_t m = 0;
    size_t k = 0;
    for (size_t i = 0; i < c->n; i++) {
        if (c->links[i] == NULL) {
            c->links[i] = ok ? got[k] : NULL;
            k++;
        }
        if (c->links[i] != NULL) {
            c->links[m] = c->links[i];
            if (m != i) {
                c->links[i] = NULL;
                memcpy(c->keys[m], c->keys[i], sizeof c->keys[m]);
            }
            m++;
        }
    }
    c->n = m;
    free(got);
    free(capped);
    return ok;
}

static void start_probe(firc_tunnels_ctx_t *ctx, firc_http_res_t *res, const firc_tunnel_t *t, uint32_t mark,
                        bool saved, const cJSON *keys)
{
    size_t nb = 0;
    const firc_tun_body_t *b = bodies_of(ctx, &nb);
    firc_tunnel_t all = *t;
    all.exclude = NULL;
    all.n_exclude = 0;
    firc_tun_nodes_t nodes;
    firc_err_t err = firc_tun_nodes_build(&all, mark, b, nb, &nodes);
    if (err != FIRC_OK) {
        write_nodes_build_err(res, err);
        return;
    }
    tun_probe_call_t *c = calloc(1, sizeof *c);
    if (c != NULL) {
        c->links = calloc(nodes.n + 1, sizeof *c->links);
        c->keys = calloc(nodes.n + 1, sizeof *c->keys);
        c->saved = saved ? strdup(t->id) : NULL;
    }
    if (c == NULL || c->links == NULL || c->keys == NULL || (saved && c->saved == NULL)) {
        if (c != NULL) {
            call_free(c);
        }
        firc_tun_nodes_free(&nodes);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (!collect_probe_links(c, &nodes, &all, mark, b, nb, keys)) {
        call_free(c);
        firc_tun_nodes_free(&nodes);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    if (c->n == 0) {
        call_free(c);
        firc_tun_nodes_free(&nodes);
        cJSON *o = cJSON_CreateObject();
        if (o == NULL || cJSON_AddArrayToObject(o, "nodes") == NULL) {
            cJSON_Delete(o);
            firc_http_res_write_error(res, 500, "out of memory");
            return;
        }
        firc_http_res_write_json(res, 200, o);
        return;
    }
    firc_tunprobe_opts_t o = {.binary = ctx->binary,
                              .mark = mark,
                              .insecure = t->insecure,
                              .ca = t->ca,
                              .fingerprint = t->fingerprint,
                              .timeout_s = PROBE_TIMEOUT_S,
                              .jobs = PROBE_JOBS,
                              .kill_after_ms = firc_tunprobe_deadline_ms(c->n, PROBE_JOBS)};
    c->ctx = ctx;
    err = firc_tunprobe_start(firc_http_res_loop(res), &o, c->links, c->n, probe_done, c, &c->probe);
    firc_tun_nodes_free(&nodes);
    if (err != FIRC_OK) {
        call_free(c);
        FIRC_WARN("tunnels: probe not started: %s", firc_err_str(err));
        firc_http_res_write_error(res, 500, "probe not started");
        return;
    }
    if (!saved) {
        for (size_t i = 0; i < c->n; i++) {
            free(c->links[i]);
            c->links[i] = NULL;
        }
    }
    c->pending = firc_http_res_defer(res, probe_client_left, c);
    if (c->pending == NULL) {
        firc_tunprobe_cancel(c->probe);
        call_free(c);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    ctx->probe = c;
}

static void handle_probe(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    firc_tunnels_ctx_t *ctx = ud;
    if (!require_run(ctx, res)) {
        return;
    }
    if (ctx->probe != NULL) {
        firc_http_res_write_error(res, 409, "a probe is already running");
        return;
    }
    cJSON *body = parse_body_json(req);
    const cJSON *keys = cJSON_GetObjectItemCaseSensitive(body, "keys");
    bool keys_ok = keys == NULL || cJSON_IsArray(keys);
    for (const cJSON *k = keys_ok && keys != NULL ? keys->child : NULL; k != NULL; k = k->next) {
        keys_ok = keys_ok && cJSON_IsString(k);
    }
    if (!keys_ok) {
        cJSON_Delete(body);
        firc_http_res_write_error(res, 400, "keys must be an array of strings");
        return;
    }
    firc_tunnels_t set = {0};
    const firc_tunnel_t *t = NULL;
    bool ok = probe_target(ctx, body, res, &set, &t);
    if (ok) {
        uint32_t mark = 0;
        if (!usable_mark(ctx, t, &mark)) {
            firc_http_res_write_error(res, 409, "save first");
        } else {
            start_probe(ctx, res, t, mark, set.n == 0, keys);
        }
    }
    cJSON_Delete(body);
    firc_tunnels_free(&set);
}

static void handle_probe_rows(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    (void)req;
    firc_tunnels_ctx_t *ctx = ud;
    tun_probe_call_t *c = ctx->probe;
    const firc_tunprobe_row_t *rows = c != NULL ? firc_tunprobe_rows(c->probe) : NULL;
    cJSON *o = cJSON_CreateObject();
    cJSON *a = cJSON_CreateArray();
    bool ok = o != NULL && a != NULL && cJSON_AddNumberToObject(o, "total", c != NULL ? (double)c->n : 0) != NULL;
    for (size_t i = 0; ok && rows != NULL && i < c->n; i++) {
        if (rows[i].have) {
            ok = add_probe_row(a, c->keys[i], &rows[i]);
        }
    }
    if (ok) {
        ok = put_item(o, "nodes", a);
        a = NULL;
    }
    cJSON_Delete(a);
    if (!ok) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

static void handle_headers(firc_http_req_t *req, firc_http_res_t *res, void *ud)
{
    (void)req;
    firc_tunnels_ctx_t *ctx = ud;
    const firc_subhdr_set_t *d = ctx->subs != NULL ? firc_tunsubs_defaults(ctx->subs) : NULL;
    cJSON *o = cJSON_CreateObject();
    cJSON *a = cJSON_CreateArray();
    bool ok = o != NULL && a != NULL;
    for (size_t i = 0; ok && d != NULL && i < d->n; i++) {
        cJSON *x = cJSON_CreateObject();
        ok = x != NULL && cJSON_AddStringToObject(x, "name", d->h[i].name) != NULL &&
             cJSON_AddStringToObject(x, "value", d->h[i].value) != NULL && cJSON_AddItemToArray(a, x);
        if (!ok) {
            cJSON_Delete(x);
        }
    }
    if (ok) {
        ok = put_item(o, "headers", a);
        a = NULL;
    }
    cJSON_Delete(a);
    if (!ok) {
        cJSON_Delete(o);
        firc_http_res_write_error(res, 500, "out of memory");
        return;
    }
    firc_http_res_write_json(res, 200, o);
}

void firc_tunnels_register_routes(firc_httpd_t *h, firc_tunnels_ctx_t *ctx)
{
    must_route(h, "GET", "/api/v1/tunnels", handle_get, ctx);
    must_route(h, "PUT", "/api/v1/tunnels", handle_put, ctx);
    must_route(h, "GET", "/api/v1/tunnels/state", handle_state, ctx);
    must_route(h, "GET", "/api/v1/tunnels/headers", handle_headers, ctx);
    must_route(h, "POST", "/api/v1/tunnels/preview", handle_preview, ctx);
    must_route(h, "POST", "/api/v1/tunnels/probe", handle_probe, ctx);
    must_route(h, "GET", "/api/v1/tunnels/probe", handle_probe_rows, ctx);
    must_route(h, "POST", "/api/v1/tunnels/{id}/refresh", handle_refresh, ctx);
    must_route(h, "POST", "/api/v1/tunnels/{id}/restart", handle_restart, ctx);
}

void firc_tunnels_api_close(firc_tunnels_ctx_t *ctx)
{
    if (ctx == NULL || ctx->probe == NULL) {
        return;
    }
    tun_probe_call_t *c = ctx->probe;
    ctx->probe = NULL;
    firc_tunprobe_cancel(c->probe);
    firc_http_deferred_drop(c->pending);
    call_free(c);
}
