#include <errno.h>
#include <inttypes.h>
#include <sys/stat.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <yaml.h>

#include "firc/atomic_write.h"
#include "firc/yamlio.h"
#include "yaml_scalar.h"

typedef struct emit_buf {
    char *data;
    size_t len;
    size_t cap;
} emit_buf_t;

static int emit_buf_write(void *ext, unsigned char *buffer, size_t size)
{
    emit_buf_t *b = ext;
    if (b->len + size + 1 > b->cap) {
        size_t cap = b->cap == 0 ? 4096 : b->cap;
        while (b->len + size + 1 > cap) {
            cap *= 2;
        }
        char *grown = realloc(b->data, cap);
        if (grown == NULL) {
            return 0;
        }
        b->data = grown;
        b->cap = cap;
    }
    memcpy(b->data + b->len, buffer, size);
    b->len += size;
    b->data[b->len] = '\0';
    return 1;
}

typedef struct emitter_ctx {
    yaml_emitter_t emitter;
    bool failed;
} emitter_ctx_t;

static void emit_event(emitter_ctx_t *ctx, yaml_event_t *ev)
{
    if (ctx->failed) {
        yaml_event_delete(ev);
        return;
    }
    if (!yaml_emitter_emit(&ctx->emitter, ev)) {
        ctx->failed = true;
    }
}

static void emit_scalar_styled(emitter_ctx_t *ctx, const char *value,
                               yaml_scalar_style_t style)
{
    yaml_event_t ev;
    if (!yaml_scalar_event_initialize(&ev, NULL, NULL,
                                      (yaml_char_t *)value,
                                      (int)strlen(value), 1, 1, style)) {
        ctx->failed = true;
        return;
    }
    emit_event(ctx, &ev);
}

/* picks plain/literal/double-quoted style for a string value */
static void emit_string(emitter_ctx_t *ctx, const char *value)
{
    yaml_scalar_style_t style = YAML_PLAIN_SCALAR_STYLE;
    if (strchr(value, '\n') != NULL) {
        style = YAML_LITERAL_SCALAR_STYLE;
    } else if (firc_yaml_string_needs_quote(value)) {
        style = YAML_DOUBLE_QUOTED_SCALAR_STYLE;
    }
    emit_scalar_styled(ctx, value, style);
}

static void emit_plain(emitter_ctx_t *ctx, const char *value)
{
    emit_scalar_styled(ctx, value, YAML_PLAIN_SCALAR_STYLE);
}

static void emit_bool(emitter_ctx_t *ctx, bool v)
{
    emit_plain(ctx, v ? "true" : "false");
}

static void emit_u64(emitter_ctx_t *ctx, uint64_t v)
{
    char buf[24];
    snprintf(buf, sizeof(buf), "%" PRIu64, v);
    emit_plain(ctx, buf);
}

static void emit_duration(emitter_ctx_t *ctx, firc_duration_t d)
{
    char buf[40];
    firc_duration_format(d, buf, sizeof(buf));
    /* a duration string never resolves as a number/bool -> plain */
    emit_plain(ctx, buf);
}

static void emit_id(emitter_ctx_t *ctx, firc_id_t id)
{
    char buf[FIRC_ID_STR_LEN];
    firc_id_format(id, buf);
    emit_string(ctx, buf); /* "12345678"/"666e0000" must be quoted */
}

static void map_start(emitter_ctx_t *ctx)
{
    yaml_event_t ev;
    if (!yaml_mapping_start_event_initialize(&ev, NULL, NULL, 1,
                                             YAML_BLOCK_MAPPING_STYLE)) {
        ctx->failed = true;
        return;
    }
    emit_event(ctx, &ev);
}

static void map_end(emitter_ctx_t *ctx)
{
    yaml_event_t ev;
    if (!yaml_mapping_end_event_initialize(&ev)) {
        ctx->failed = true;
        return;
    }
    emit_event(ctx, &ev);
}

static void seq_start(emitter_ctx_t *ctx, bool empty)
{
    yaml_event_t ev;
    if (!yaml_sequence_start_event_initialize(
            &ev, NULL, NULL, 1,
            empty ? YAML_FLOW_SEQUENCE_STYLE : YAML_BLOCK_SEQUENCE_STYLE)) {
        ctx->failed = true;
        return;
    }
    emit_event(ctx, &ev);
}

static void seq_end(emitter_ctx_t *ctx)
{
    yaml_event_t ev;
    if (!yaml_sequence_end_event_initialize(&ev)) {
        ctx->failed = true;
        return;
    }
    emit_event(ctx, &ev);
}

static void emit_rule(emitter_ctx_t *ctx, const firc_rule_t *r)
{
    map_start(ctx);
    emit_plain(ctx, "id");
    emit_id(ctx, r->id);
    emit_plain(ctx, "type");
    emit_string(ctx, r->type != NULL ? r->type : "");
    emit_plain(ctx, "rule");
    emit_string(ctx, r->rule != NULL ? r->rule : "");
    emit_plain(ctx, "enable");
    emit_bool(ctx, r->enable);
    /* Only when set: a rule without them reads back unchanged. */
    if (r->proto != NULL && *r->proto != '\0') {
        emit_plain(ctx, "proto");
        emit_string(ctx, r->proto);
    }
    if (r->ports != NULL && *r->ports != '\0') {
        emit_plain(ctx, "ports");
        emit_string(ctx, r->ports);
    }
    if (r->comment != NULL && *r->comment != '\0') {
        emit_plain(ctx, "comment");
        emit_string(ctx, r->comment);
    }
    if (r->autolist != NULL && *r->autolist != '\0') {
        emit_plain(ctx, "autolist");
        emit_string(ctx, r->autolist);
        if (r->autolist_by_domain) {
            emit_plain(ctx, "autolistByDomain");
            emit_bool(ctx, true);
        }
    }
    map_end(ctx);
}

/* only when non-empty: an empty selector is the default and is not written */
static void emit_devices(emitter_ctx_t *ctx, const firc_devsel_spec_t *ds)
{
    if (ds->n_allow == 0 && ds->n_deny == 0) {
        return;
    }
    emit_plain(ctx, "devices");
    map_start(ctx);
    if (ds->n_allow > 0) {
        emit_plain(ctx, "allow");
        seq_start(ctx, false);
        for (size_t i = 0; i < ds->n_allow; i++) { emit_string(ctx, ds->allow[i]); }
        seq_end(ctx);
    }
    if (ds->n_deny > 0) {
        emit_plain(ctx, "deny");
        seq_start(ctx, false);
        for (size_t i = 0; i < ds->n_deny; i++) { emit_string(ctx, ds->deny[i]); }
        seq_end(ctx);
    }
    map_end(ctx);
}

/* only when not the default, so a file without the key reads back unchanged */
static void emit_resolve(emitter_ctx_t *ctx, const firc_group_resolve_t *r)
{
    bool has_server = r->server != NULL && r->server[0] != '\0';
    if (r->tunnel && !has_server) {
        return;
    }
    emit_plain(ctx, "resolve");
    map_start(ctx);
    emit_plain(ctx, "tunnel");
    emit_bool(ctx, r->tunnel);
    if (has_server) {
        emit_plain(ctx, "server");
        emit_string(ctx, r->server);
    }
    map_end(ctx);
}

/* a list's own rules are never written; only what a person changed about one */
static void emit_sub_override(emitter_ctx_t *ctx, const firc_sub_override_t *o)
{
    map_start(ctx);
    emit_plain(ctx, "rule");
    emit_string(ctx, o->rule != NULL ? o->rule : "");
    if (o->list_type != NULL) {
        emit_plain(ctx, "list_type");
        emit_string(ctx, o->list_type);
    }
    if (o->proto != NULL) {
        emit_plain(ctx, "proto");
        emit_string(ctx, o->proto);
    }
    if (o->ports != NULL) {
        emit_plain(ctx, "ports");
        emit_string(ctx, o->ports);
    }
    if (o->type != NULL) {
        emit_plain(ctx, "type");
        emit_string(ctx, o->type);
    }
    if (o->has_enable) {
        emit_plain(ctx, "enable");
        emit_bool(ctx, o->enable);
    }
    map_end(ctx);
}

/* nested under list so it sits beside the group's hand-written rules; written only when g->list is set */
static void emit_group_list(emitter_ctx_t *ctx, const firc_group_list_t *l)
{
    emit_plain(ctx, "list");
    map_start(ctx);
    emit_plain(ctx, "url");
    emit_string(ctx, l->url != NULL ? l->url : "");
    emit_plain(ctx, "interval");
    emit_u64(ctx, l->interval);
    emit_plain(ctx, "last_update");
    emit_u64(ctx, l->last_update);
    emit_plain(ctx, "overrides");
    seq_start(ctx, l->n_overrides == 0);
    for (size_t i = 0; i < l->n_overrides; i++) {
        emit_sub_override(ctx, l->overrides[i]);
    }
    seq_end(ctx);
    map_end(ctx);
}

static void emit_group(emitter_ctx_t *ctx, const firc_group_t *g)
{
    map_start(ctx);
    emit_plain(ctx, "id");
    emit_id(ctx, g->id);
    emit_plain(ctx, "name");
    emit_string(ctx, g->name != NULL ? g->name : "");
    emit_plain(ctx, "interface");
    if (g->n_iface_rest == 0) {
        emit_string(ctx, g->iface != NULL ? g->iface : "");
    } else {
        seq_start(ctx, false);
        for (size_t i = 0; i < firc_group_n_ifaces(g); i++) { emit_string(ctx, firc_group_iface_at(g, i)); }
        seq_end(ctx);
    }
    emit_plain(ctx, "enable");
    emit_bool(ctx, g->enable);
    emit_devices(ctx, &g->devices);
    emit_resolve(ctx, &g->resolve);
    if (firc_group_is_block(g) && g->block != FIRC_BLOCK_NULL) {
        emit_plain(ctx, "block");
        emit_string(ctx, firc_block_answer_name(g->block));
    }
    emit_plain(ctx, "rules");
    seq_start(ctx, g->n_rules == 0);
    for (size_t i = 0; i < g->n_rules; i++) {
        emit_rule(ctx, g->rules[i]);
    }
    seq_end(ctx);
    if (g->list != NULL) {
        emit_group_list(ctx, g->list);
    }
    map_end(ctx);
}

/* its own function so the saver can emit one half without the other */
static void emit_app(emitter_ctx_t *ctx, const firc_app_config_t *a)
{
    emit_plain(ctx, "app");
    map_start(ctx);

    emit_plain(ctx, "httpWeb");
    map_start(ctx);
    emit_plain(ctx, "enabled");
    emit_bool(ctx, a->http_web.enabled);
    emit_plain(ctx, "host");
    map_start(ctx);
    emit_plain(ctx, "address");
    emit_string(ctx, a->http_web.host.address);
    emit_plain(ctx, "port");
    emit_u64(ctx, a->http_web.host.port);
    map_end(ctx);
    map_end(ctx);

    emit_plain(ctx, "dnsProxy");
    map_start(ctx);
    emit_plain(ctx, "host");
    map_start(ctx);
    emit_plain(ctx, "address");
    emit_string(ctx, a->dns_proxy.host.address);
    emit_plain(ctx, "port");
    emit_u64(ctx, a->dns_proxy.host.port);
    map_end(ctx);
    emit_plain(ctx, "upstream");
    map_start(ctx);
    emit_plain(ctx, "address");
    emit_string(ctx, a->dns_proxy.upstream.address);
    emit_plain(ctx, "port");
    emit_u64(ctx, a->dns_proxy.upstream.port);
    map_end(ctx);
    emit_plain(ctx, "disableRemap53");
    emit_bool(ctx, a->dns_proxy.disable_remap53);
    emit_plain(ctx, "disableDropAAAA");
    emit_bool(ctx, a->dns_proxy.disable_drop_aaaa);
    emit_plain(ctx, "disableStats");
    emit_bool(ctx, a->dns_proxy.disable_stats);
    emit_plain(ctx, "unmatchedTtl");
    emit_duration(ctx, a->dns_proxy.unmatched_ttl);
    emit_plain(ctx, "maxIdleConns");
    emit_u64(ctx, a->dns_proxy.max_idle_conns);
    emit_plain(ctx, "maxConcurrent");
    emit_u64(ctx, a->dns_proxy.max_concurrent);
    emit_plain(ctx, "timeout");
    emit_duration(ctx, a->dns_proxy.timeout);
    map_end(ctx);

    emit_plain(ctx, "netfilter");
    map_start(ctx);
    emit_plain(ctx, "iptables");
    map_start(ctx);
    emit_plain(ctx, "chainPrefix");
    emit_string(ctx, a->netfilter.iptables.chain_prefix);
    map_end(ctx);
    emit_plain(ctx, "disableIPv4");
    emit_bool(ctx, a->netfilter.disable_ipv4);
    emit_plain(ctx, "disableIPv6");
    emit_bool(ctx, a->netfilter.disable_ipv6);
    emit_plain(ctx, "startMarkTableIndex");
    emit_u64(ctx, a->netfilter.start_mark_table_index);
    map_end(ctx);

    emit_plain(ctx, "addressPool");
    map_start(ctx);
    emit_plain(ctx, "v4");
    map_start(ctx);
    emit_plain(ctx, "pool");
    emit_string(ctx, a->fakeip.v4.pool);
    emit_plain(ctx, "chunk");
    emit_u64(ctx, a->fakeip.v4.chunk);
    map_end(ctx);
    emit_plain(ctx, "v6");
    map_start(ctx);
    emit_plain(ctx, "pool");
    emit_string(ctx, a->fakeip.v6.pool);
    emit_plain(ctx, "chunk");
    emit_u64(ctx, a->fakeip.v6.chunk);
    map_end(ctx);
    emit_plain(ctx, "ttlClamp");
    emit_duration(ctx, a->fakeip.ttl_clamp);
    emit_plain(ctx, "maxNames");
    emit_u64(ctx, a->fakeip.max_names);
    map_end(ctx);

    emit_plain(ctx, "link");
    seq_start(ctx, a->n_link == 0);
    for (size_t i = 0; i < a->n_link; i++) {
        emit_string(ctx, a->link[i]);
    }
    seq_end(ctx);
    emit_plain(ctx, "update");
    map_start(ctx);
    emit_plain(ctx, "check");
    emit_bool(ctx, a->update.check);
    map_end(ctx);
    emit_plain(ctx, "check");
    map_start(ctx);
    emit_plain(ctx, "url");
    emit_string(ctx, a->check.url != NULL ? a->check.url : "");
    emit_plain(ctx, "wanUrl");
    emit_string(ctx, a->check.wan_url != NULL ? a->check.wan_url : "");
    emit_plain(ctx, "interval");
    emit_duration(ctx, a->check.interval);
    emit_plain(ctx, "timeout");
    emit_duration(ctx, a->check.timeout);
    map_end(ctx);
    emit_plain(ctx, "autolist");
    map_start(ctx);
    emit_plain(ctx, "enabled");
    emit_bool(ctx, a->autolist.enabled);
    emit_plain(ctx, "group");
    emit_string(ctx, a->autolist.group != NULL ? a->autolist.group : "");
    emit_plain(ctx, "mode");
    emit_string(ctx, a->autolist.mode != NULL ? a->autolist.mode : "candidates");
    emit_plain(ctx, "failThreshold");
    emit_u64(ctx, a->autolist.fail_threshold);
    emit_plain(ctx, "failTime");
    emit_duration(ctx, a->autolist.fail_time);
    emit_plain(ctx, "retransThreshold");
    emit_u64(ctx, a->autolist.retrans_threshold);
    emit_plain(ctx, "exclude");
    seq_start(ctx, a->autolist.n_exclude == 0);
    for (size_t i = 0; i < a->autolist.n_exclude; i++) {
        emit_string(ctx, a->autolist.exclude[i]);
    }
    seq_end(ctx);
    emit_plain(ctx, "recheck");
    emit_bool(ctx, a->autolist.recheck);
    emit_plain(ctx, "recheckInterval");
    emit_duration(ctx, a->autolist.recheck_interval);
    emit_plain(ctx, "checkInterface");
    emit_string(ctx, a->autolist.check_iface != NULL ? a->autolist.check_iface : "");
    map_end(ctx);
    map_end(ctx); /* app */
}

firc_err_t firc_config_save_buffer_part(const firc_config_t *cfg, const char *version,
                                        unsigned parts, char **out, size_t *out_len)
{
    emitter_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    emit_buf_t buf = {NULL, 0, 0};

    if (!yaml_emitter_initialize(&ctx.emitter)) {
        return FIRC_ERR_NOMEM;
    }
    yaml_emitter_set_output(&ctx.emitter, emit_buf_write, &buf);
    yaml_emitter_set_indent(&ctx.emitter, 2);
    yaml_emitter_set_width(&ctx.emitter, -1);
    yaml_emitter_set_unicode(&ctx.emitter, 1);

    yaml_event_t ev;
    if (!yaml_stream_start_event_initialize(&ev, YAML_UTF8_ENCODING)) {
        ctx.failed = true;
    } else {
        emit_event(&ctx, &ev);
    }
    if (!yaml_document_start_event_initialize(&ev, NULL, NULL, NULL, 1)) {
        ctx.failed = true;
    } else {
        emit_event(&ctx, &ev);
    }

    const firc_app_config_t *a = &cfg->app;

    map_start(&ctx);
    emit_plain(&ctx, "configVersion");
    emit_string(&ctx, version);

    if (parts & FIRC_CFG_SETTINGS) { emit_app(&ctx, a); }

    if (parts & FIRC_CFG_GROUPS) {
        emit_plain(&ctx, "groups");
        seq_start(&ctx, cfg->n_groups == 0);
        for (size_t i = 0; i < cfg->n_groups; i++) {
            emit_group(&ctx, cfg->groups[i]);
        }
        seq_end(&ctx);
    }

    map_end(&ctx);

    if (!yaml_document_end_event_initialize(&ev, 1)) {
        ctx.failed = true;
    } else {
        emit_event(&ctx, &ev);
    }
    if (!yaml_stream_end_event_initialize(&ev)) {
        ctx.failed = true;
    } else {
        emit_event(&ctx, &ev);
    }
    yaml_emitter_delete(&ctx.emitter);

    if (ctx.failed) {
        free(buf.data);
        return FIRC_ERR_SYS;
    }
    *out = buf.data != NULL ? buf.data : calloc(1, 1);
    *out_len = buf.len;
    return *out != NULL ? FIRC_OK : FIRC_ERR_NOMEM;
}

firc_err_t firc_config_save_buffer(const firc_config_t *cfg, const char *version,
                                   char **out, size_t *out_len)
{
    return firc_config_save_buffer_part(cfg, version, FIRC_CFG_SETTINGS | FIRC_CFG_GROUPS, out,
                                        out_len);
}

firc_err_t firc_config_save_part_file(const firc_config_t *cfg, const char *version,
                                      unsigned parts, const char *path)
{
    char *data = NULL;
    size_t len = 0;
    firc_err_t err = firc_config_save_buffer_part(cfg, version, parts, &data, &len);
    if (err != FIRC_OK) {
        return err;
    }

    err = firc_atomic_write(path, data, len);
    free(data);
    return err;
}

firc_err_t firc_config_save_file(const firc_config_t *cfg, const char *version, const char *path)
{
    return firc_config_save_part_file(cfg, version, FIRC_CFG_SETTINGS | FIRC_CFG_GROUPS, path);
}

firc_err_t firc_config_groups_path(const char *conf_path, char *out, size_t cap)
{
    if (conf_path == NULL || out == NULL || cap == 0) { return FIRC_ERR_INVAL; }
    static const char name[] = "groups.yaml";
    const char *slash = strrchr(conf_path, '/');
    /* no directory in conf_path means the sibling is in the working directory, not filesystem root */
    size_t dir_len = slash != NULL ? (size_t)(slash - conf_path) + 1 : 0;
    /* refused rather than truncated: half a path names a different file */
    if (dir_len + sizeof(name) > cap) { return FIRC_ERR_INVAL; }
    memcpy(out, conf_path, dir_len);
    memcpy(out + dir_len, name, sizeof(name));
    return FIRC_OK;
}
