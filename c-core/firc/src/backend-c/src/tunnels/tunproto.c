#include "firc/tunproto.h"

#include <arpa/inet.h>
#include <cjson/cJSON.h>
#include <stdio.h>
#include <string.h>

size_t firc_tun_lines_feed(firc_tun_lines_t *l, const char *data, size_t n, firc_tun_line_fn fn, void *ud)
{
    size_t dropped = 0;
    for (size_t i = 0; i < n; i++) {
        char c = data[i];
        if (l->dropping) {
            if (c == '\n')
                l->dropping = false;
            continue;
        }
        if (c == '\n') {
            l->buf[l->len] = '\0';
            fn(l->buf, l->len, ud);
            l->len = 0;
        } else if (l->len == FIRC_TUN_LINE_MAX - 1) {
            l->dropping = true;
            l->len = 0;
            dropped++;
        } else {
            l->buf[l->len++] = c;
        }
    }
    return dropped;
}

firc_log_level_t firc_tun_err_level(const char *line, size_t len, size_t *skip)
{
    static const char head[] = "tunvless[";
    static const struct {
        const char *name;
        firc_log_level_t level;
    } tags[] = {
        {"debug]", FIRC_LOG_DEBUG},
        {"info]", FIRC_LOG_INFO},
        {"warn]", FIRC_LOG_WARN},
        {"error]", FIRC_LOG_ERROR},
    };
    *skip = 0;
    size_t hl = sizeof head - 1;
    if (len <= hl || memcmp(line, head, hl) != 0) {
        return FIRC_LOG_INFO;
    }
    for (size_t i = 0; i < sizeof tags / sizeof tags[0]; i++) {
        size_t at = hl + strlen(tags[i].name);
        if (len <= at || memcmp(line + hl, tags[i].name, at - hl) != 0 ||
            (line[at] != ':' && line[at] != ' ')) {
            continue;
        }
        if (line[at] == ':') {
            at++;
        }
        while (at < len && line[at] == ' ') {
            at++;
        }
        *skip = at;
        return tags[i].level;
    }
    return FIRC_LOG_INFO;
}

static bool copy_str(const cJSON *obj, const char *key, char *dst, size_t cap)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    if (!cJSON_IsString(v) || !v->valuestring)
        return false;
    snprintf(dst, cap, "%s", v->valuestring);
    return true;
}

static bool count_of(const cJSON *v, int *out)
{
    if (!cJSON_IsNumber(v) || !(v->valuedouble >= 0) || v->valuedouble > 1e9)
        return false;
    *out = (int)v->valuedouble;
    return true;
}

static bool opt_index(const cJSON *root, int *out)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(root, "index");
    return v == NULL || count_of(v, out);
}

static bool parse_active(const cJSON *root, firc_tev_t *out)
{
    const cJSON *arr = cJSON_GetObjectItemCaseSensitive(root, "nodes");
    if (!cJSON_IsArray(arr) || cJSON_GetArraySize(arr) > 8)
        return false;
    const cJSON *it;
    cJSON_ArrayForEach(it, arr)
    {
        if (!cJSON_IsString(it) || !it->valuestring)
            return false;
        snprintf(out->active[out->n_active++], sizeof(out->active[0]), "%s", it->valuestring);
    }
    const cJSON *v6 = cJSON_GetObjectItemCaseSensitive(root, "v6");
    if (cJSON_IsArray(v6) && (size_t)cJSON_GetArraySize(v6) == out->n_active) {
        bool flags[8] = {false};
        size_t j = 0;
        bool all_bool = true;
        cJSON_ArrayForEach(it, v6)
        {
            all_bool = all_bool && cJSON_IsBool(it);
            flags[j++] = cJSON_IsTrue(it);
        }
        if (all_bool)
            memcpy(out->active_v6, flags, sizeof flags);
    }
    const cJSON *ix = cJSON_GetObjectItemCaseSensitive(root, "index");
    if (ix == NULL)
        return true;
    if (!cJSON_IsArray(ix) || (size_t)cJSON_GetArraySize(ix) != out->n_active)
        return false;
    size_t k = 0;
    cJSON_ArrayForEach(it, ix)
    {
        if (!count_of(it, &out->active_index[k++]))
            return false;
    }
    return true;
}

static bool opt_port(const cJSON *root, uint16_t *out)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(root, "tcpPort");
    if (v == NULL)
        return true;
    if (!cJSON_IsNumber(v) || !(v->valuedouble >= 0) || v->valuedouble > 65535 ||
        v->valuedouble != (double)(long)v->valuedouble)
        return false;
    *out = (uint16_t)v->valuedouble;
    return true;
}

static bool parse_fake(const cJSON *root, uint8_t out[4])
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(root, "fake");
    return cJSON_IsString(v) && v->valuestring && inet_pton(AF_INET, v->valuestring, out) == 1;
}

static bool u32_of(const cJSON *v, uint32_t *out)
{
    if (!cJSON_IsNumber(v) || !(v->valuedouble >= 0) || v->valuedouble > 4294967295.0 ||
        (double)(uint32_t)v->valuedouble != v->valuedouble)
        return false;
    *out = (uint32_t)v->valuedouble;
    return true;
}

static bool req_port(const cJSON *root, uint16_t *out)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(root, "port");
    if (!cJSON_IsNumber(v) || !(v->valuedouble >= 1) || v->valuedouble > 65535 ||
        v->valuedouble != (double)(long)v->valuedouble)
        return false;
    *out = (uint16_t)v->valuedouble;
    return true;
}

static bool parse_inbound(const cJSON *root, firc_tev_t *out)
{
    if (!req_port(root, &out->ib_port) || !copy_str(root, "state", out->istate, sizeof(out->istate)))
        return false;
    if (strcmp(out->istate, "up") != 0 && strcmp(out->istate, "busy") != 0 && strcmp(out->istate, "no-node") != 0)
        return false;
    const cJSON *w = cJSON_GetObjectItemCaseSensitive(root, "why");
    return w == NULL || copy_str(root, "why", out->why, sizeof(out->why));
}

static bool parse_fields(const cJSON *root, const char *type, firc_tev_t *out)
{
    if (strcmp(type, "ready") == 0) {
        out->kind = FIRC_TEV_READY;
        return copy_str(root, "dev", out->dev, sizeof(out->dev)) && opt_port(root, &out->tcp_port);
    }
    if (strcmp(type, "active") == 0) {
        out->kind = FIRC_TEV_ACTIVE;
        return parse_active(root, out);
    }
    if (strcmp(type, "node_down") == 0) {
        out->kind = FIRC_TEV_NODE_DOWN;
        return copy_str(root, "node", out->node, sizeof(out->node)) &&
               copy_str(root, "why", out->why, sizeof(out->why)) && opt_index(root, &out->index);
    }
    if (strcmp(type, "node_up") == 0) {
        out->kind = FIRC_TEV_NODE_UP;
        return copy_str(root, "node", out->node, sizeof(out->node)) && opt_index(root, &out->index);
    }
    if (strcmp(type, "nodes") == 0) {
        out->kind = FIRC_TEV_NODES;
        return count_of(cJSON_GetObjectItemCaseSensitive(root, "count"), &out->count);
    }
    if (strcmp(type, "map") == 0 || strcmp(type, "close") == 0) {
        out->kind = type[0] == 'm' ? FIRC_TEV_MAP : FIRC_TEV_CLOSE;
        return count_of(cJSON_GetObjectItemCaseSensitive(root, "count"), &out->count);
    }
    if (strcmp(type, "miss") == 0) {
        out->kind = FIRC_TEV_MISS;
        return parse_fake(root, out->fake);
    }
    if (strcmp(type, "bytes") == 0) {
        out->kind = FIRC_TEV_BYTES;
        return u32_of(cJSON_GetObjectItemCaseSensitive(root, "rx"), &out->rx) &&
               u32_of(cJSON_GetObjectItemCaseSensitive(root, "tx"), &out->tx);
    }
    if (strcmp(type, "inbound") == 0) {
        out->kind = FIRC_TEV_INBOUND;
        return parse_inbound(root, out);
    }
    if (strcmp(type, "ibytes") == 0) {
        out->kind = FIRC_TEV_IBYTES;
        return req_port(root, &out->ib_port) &&
               u32_of(cJSON_GetObjectItemCaseSensitive(root, "rx"), &out->rx) &&
               u32_of(cJSON_GetObjectItemCaseSensitive(root, "tx"), &out->tx);
    }
    if (strcmp(type, "inbounds") == 0) {
        out->kind = FIRC_TEV_INBOUNDS;
        return count_of(cJSON_GetObjectItemCaseSensitive(root, "count"), &out->count);
    }
    if (strcmp(type, "pins_full") == 0) {
        out->kind = FIRC_TEV_PINS_FULL;
        return true;
    }
    if (strcmp(type, "no_node") == 0) {
        out->kind = FIRC_TEV_NO_NODE;
        const cJSON *r = cJSON_GetObjectItemCaseSensitive(root, "retry");
        if (!cJSON_IsNumber(r) || !(r->valuedouble >= 0) || r->valuedouble > 86400)
            return false;
        out->retry_s = (int)r->valuedouble;
        return true;
    }
    return false;
}

static void unset_indexes(firc_tev_t *out)
{
    out->index = -1;
    out->pos = -1;
    for (size_t i = 0; i < 8; i++) {
        out->active_index[i] = -1;
        out->active_pos[i] = -1;
    }
}

void firc_tun_event_parse(const char *line, size_t len, firc_tev_t *out)
{
    memset(out, 0, sizeof(*out));
    unset_indexes(out);
    out->kind = FIRC_TEV_BAD;
    const char *end = NULL;
    cJSON *root = cJSON_ParseWithLengthOpts(line, len, &end, 0);
    if (!root)
        return;
    while (end && end < line + len && (*end == ' ' || *end == '\t' || *end == '\r'))
        end++;
    if (!end || end != line + len) {
        cJSON_Delete(root);
        return;
    }
    const cJSON *type = cJSON_GetObjectItemCaseSensitive(root, "type");
    bool ok = cJSON_IsObject(root) && cJSON_IsString(type) && type->valuestring &&
              parse_fields(root, type->valuestring, out);
    cJSON_Delete(root);
    if (!ok) {
        memset(out, 0, sizeof(*out));
        unset_indexes(out);
        out->kind = FIRC_TEV_BAD;
    }
}
