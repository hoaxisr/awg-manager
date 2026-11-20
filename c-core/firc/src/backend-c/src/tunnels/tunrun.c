#include "firc/tunrun.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/types.h>
#include <time.h>

#include "firc/events.h"
#include "firc/logrep.h"
#include "firc/mark.h"
#include "firc/tunnet.h"
#include "firc/tunnodes.h"
#include "firc/tunvless_name.h"
#include "firc/tunwatch.h"
#include "vlink.h"

#define WATCH_MS 15000
#define GUARD_MS 1000
#define GUARD_BOUND_MS 5000
#define GUARD_OTHER_MS 5000
#define GUARD_CLEAN_MS 5000
#define FLAP_WINDOW_MS 600000
#define FLAP_MAX 3
#define GUARD_OIFS 32
#define OPKG_N (FIRC_OPKGTUN_MAX + 1)
#define TRIP_TEXT "router policies: the router made %s its main default; its default route is removed"
#define LATCH_TEXT "the router keeps making %s its main default; router policies are off for it until the next edit"
#define BOUND_TEXT "router policies: the default route could not be removed; the tunnel is stopped"

typedef struct {
    bool named;
    bool removing;
    bool gone;
    firc_opkgtun_want_t w;
    uint32_t recheck;
    bool have_seen;
    firc_opkgtun_seen_t seen;
    bool waiting;
    char wait_id[16];
    uint32_t wait_gen;
    bool route_ok;
    bool live;
    int64_t live_since;
    uint32_t checked;
    bool clean;
    int64_t clean_since;
    bool tripped;
    int64_t trip_at;
    uint32_t trip_reads;
    bool cut;
    bool other;
    int64_t other_since;
    int64_t flaps[FLAP_MAX];
    size_t n_flaps;
    bool latched;
} orec_t;

typedef struct {
    char id[16];
    char device[16];
    firc_tunnet_uplink_t u;
} drain_t;

typedef struct {
    firc_tun_src_t *src;
    size_t *row_of;
    size_t n;
    bool have;
    firc_tun_nodes_t rows;
} sent_t;

typedef struct {
    char key[FIRC_TUN_KEY_MAX + 1];
    int64_t since;
} down_t;

typedef struct {
    down_t *v;
    size_t n;
} keys_t;

typedef enum { IB_OK, IB_GONE, IB_NOT_LAN, IB_NO_ADDR } ib_verdict_t;

typedef struct {
    char state[16];
    char why[96];
    uint64_t rx, tx;
    ib_verdict_t verdict;
    bool lined;
} ib_t;

typedef struct {
    firc_tun_status_t syn;
    int64_t syn_since;
    keys_t downs;
    keys_t acts;
    keys_t v6s;
    firc_tunwatch_t watch;
    firc_logrep_t no_node;
    ib_t ib[FIRC_TUN_MAX_INBOUNDS];
    char *sent_in;
} ext_t;

struct firc_tunrun {
    firc_loop_t *loop;
    firc_rtnl_t *r;
    uint32_t start;
    firc_tunsup_t *sup;
    firc_tunnels_t cur;
    firc_tunnet_uplink_t *up;
    bool *ok;
    sent_t *sent;
    bool *waiting;
    ext_t *ext;
    drain_t *drains;
    size_t n_drains;
    firc_tunsubs_t *subs;
    const char *path;
    size_t list_max;
    bool stopped;
    bool stopping;
    bool pushing;
    char **link;
    bool *wan;
    size_t n_link;
    firc_tunrun_wan_fn wan_of;
    void *wan_ud;
    bool has_k;
    firc_tunrun_ktcp_t k;
    uint16_t ports[FIRC_KTCP_DEVICES];
    char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP];
    uint8_t drifts[FIRC_KTCP_DEVICES];
    uint64_t kb_rx[FIRC_KTCP_DEVICES], kb_tx[FIRC_KTCP_DEVICES];
    bool ktcp_unusable;
    int watch_timer;
    int guard_timer;
    int64_t (*now)(void);
    bool has_router;
    firc_tunrun_router_t router;
    orec_t orec[OPKG_N];
};

bool firc_tun_journal_line(const firc_tev_t *ev, char *buf, size_t cap, firc_log_level_t *level)
{
    switch (ev->kind) {
    case FIRC_TEV_ACTIVE: {
        if (ev->n_active == 0) {
            return false;
        }
        *level = FIRC_LOG_INFO;
        int off = snprintf(buf, cap, "active ");
        for (size_t i = 0; i < ev->n_active && off >= 0 && (size_t)off < cap; i++) {
            off += snprintf(buf + off, cap - (size_t)off, "%s%s", i > 0 ? ", " : "", ev->active[i]);
        }
        return true;
    }
    case FIRC_TEV_NODE_DOWN:
        *level = FIRC_LOG_WARN;
        if (ev->why[0] != 0) {
            snprintf(buf, cap, "%s does not answer (%s)", ev->node, ev->why);
        } else {
            snprintf(buf, cap, "%s does not answer", ev->node);
        }
        return true;
    case FIRC_TEV_NODE_UP:
        *level = FIRC_LOG_INFO;
        snprintf(buf, cap, "%s answers again", ev->node);
        return true;
    case FIRC_TEV_NO_NODE:
        *level = FIRC_LOG_WARN;
        if (ev->retry_s > 0) {
            snprintf(buf, cap, "no node answers, retry in %d s", ev->retry_s);
        } else {
            snprintf(buf, cap, "no node answers");
        }
        return true;
    case FIRC_TEV_READY:
    case FIRC_TEV_BAD:
    default:
        return false;
    }
}

static int64_t mono_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}


static bool needs_uplink(const firc_tunnel_t *tun)
{
    return tun->enable && tun->uplink != FIRC_UPLINK_AUTO;
}

static void show_uplink(firc_tunrun_t *t, size_t i)
{
    firc_tunsup_set_uplink_ok(t->sup, t->cur.t[i].id, !needs_uplink(&t->cur.t[i]) || t->up[i].routed);
}

static void refresh(firc_tunrun_t *t, size_t i)
{
    firc_err_t err = firc_tunnet_uplink_refresh(t->r, &t->cur.t[i], &t->cur, &t->up[i]);
    if (err != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, t->cur.t[i].device, "uplink route not rewritten: %s", firc_err_str(err));
    }
    show_uplink(t, i);
}

static ssize_t find(const firc_tunnels_t *all, const char *id);

static const down_t *key_find(const keys_t *k, const char *key)
{
    for (size_t i = 0; i < k->n; i++) {
        if (strcmp(k->v[i].key, key) == 0) {
            return &k->v[i];
        }
    }
    return NULL;
}

static void key_add(keys_t *k, const char *key)
{
    if (key_find(k, key) != NULL || k->n >= FIRC_TUN_MAX_NODES) {
        return;
    }
    down_t *g = realloc(k->v, (k->n + 1) * sizeof *g);
    if (g == NULL) {
        return;
    }
    k->v = g;
    snprintf(g[k->n].key, sizeof g[k->n].key, "%s", key);
    g[k->n].since = mono_ms();
    k->n++;
}

static void key_del_at(keys_t *k, size_t i)
{
    memmove(&k->v[i], &k->v[i + 1], (k->n - i - 1) * sizeof *k->v);
    k->n--;
}

static void key_del(keys_t *k, const char *key)
{
    for (size_t i = 0; i < k->n; i++) {
        if (strcmp(k->v[i].key, key) == 0) {
            key_del_at(k, i);
            return;
        }
    }
}

static void each_key(const sent_t *x, int pos, const char *name, keys_t *k, void (*fn)(keys_t *, const char *))
{
    if (pos >= 0 && (size_t)pos < x->n && x->row_of != NULL) {
        const firc_tun_node_t *row = &x->rows.v[x->row_of[pos]];
        if (strncmp(row->name, name, strlen(name)) == 0) {
            fn(k, row->key);
            return;
        }
    }
    for (size_t r = 0; r < x->rows.n; r++) {
        if (strcmp(x->rows.v[r].name, name) == 0) {
            fn(k, x->rows.v[r].key);
        }
    }
}

static bool sent_key(const sent_t *x, const char *key)
{
    for (size_t p = 0; p < x->n && x->row_of != NULL; p++) {
        if (strcmp(x->rows.v[x->row_of[p]].key, key) == 0) {
            return true;
        }
    }
    return false;
}

static void prune(keys_t *k, const sent_t *x)
{
    for (size_t i = 0; i < k->n;) {
        if (sent_key(x, k->v[i].key)) {
            i++;
        } else {
            key_del_at(k, i);
        }
    }
}

static void track_nodes(firc_tunrun_t *t, const firc_tun_state_t *st, const firc_tev_t *ev)
{
    ssize_t i = find(&t->cur, st->id);
    if (i < 0 || t->ext == NULL) {
        return;
    }
    ext_t *x = &t->ext[i];
    const sent_t *sent = &t->sent[i];
    if (ev == NULL) {
        x->acts.n = 0;
        x->v6s.n = 0;
        if (st->status == FIRC_TUN_ST_STARTING || st->status == FIRC_TUN_ST_OFF) {
            x->downs.n = 0;
        }
        return;
    }
    switch (ev->kind) {
    case FIRC_TEV_NODE_DOWN:
        each_key(sent, ev->pos, ev->node, &x->downs, key_add);
        break;
    case FIRC_TEV_NODE_UP:
        each_key(sent, ev->pos, ev->node, &x->downs, key_del);
        break;
    case FIRC_TEV_ACTIVE:
        x->acts.n = 0;
        x->v6s.n = 0;
        for (size_t k = 0; k < ev->n_active && k < 8; k++) {
            each_key(sent, ev->active_pos[k], ev->active[k], &x->acts, key_add);
            if (ev->active_v6[k]) {
                each_key(sent, ev->active_pos[k], ev->active[k], &x->v6s, key_add);
            }
        }
        break;
    case FIRC_TEV_NO_NODE:
        x->acts.n = 0;
        x->v6s.n = 0;
        break;
    default:
        break;
    }
}

static int ktcp_dev(const firc_tunnel_t *tun)
{
    uint16_t port = firc_tunnel_tcp_port(tun);
    return port == 0 ? -1 : port - FIRC_TUNNEL_TCP_PORT_BASE;
}

static void publish(const firc_tunrun_t *t)
{
    if (t->has_k && t->k.tunnels != NULL) {
        t->k.tunnels(t->ports, (const char(*)[FIRC_KTCP_NAME_CAP])t->names, t->k.ud);
    }
}

static void set_port(firc_tunrun_t *t, int dev, uint16_t port)
{
    if (t->ports[dev] != port) {
        t->ports[dev] = port;
        publish(t);
    }
}

static int send_cmds(firc_tunrun_t *t, const firc_tunnel_t *tun, firc_tun_cmd_t kind, const firc_ktcp_cmds_t *c)
{
    if (c->n_cmds == 0) {
        return 0;
    }
    firc_err_t err = firc_tunsup_command(t->sup, tun->id, kind, c->text, c->len, c->counts, c->n_cmds);
    if (err != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, tun->device, "command not sent: %s", firc_err_str(err));
        return 0;
    }
    return (int)c->n_cmds;
}

static void send_mapall(firc_tunrun_t *t, const firc_tunnel_t *tun, int dev, const firc_ktcp_image_t *im)
{
    static const firc_ktcp_image_t none = {0};
    firc_ktcp_cmds_t c = {0};
    if (firc_ktcp_map_cmds(im != NULL ? im : &none, (uint8_t)dev, true, &c) == FIRC_OK) {
        send_cmds(t, tun, FIRC_TUN_CMD_MAP, &c);
    } else {
        firc_log_from(FIRC_LOG_WARN, tun->device, "out of memory, the address map is not sent");
    }
    firc_ktcp_cmds_free(&c);
}

static const firc_ktcp_image_t *image_now(const firc_tunrun_t *t)
{
    return t->has_k && t->k.image != NULL ? t->k.image(t->k.ud) : NULL;
}

static void ready_port(firc_tunrun_t *t, const firc_tunnel_t *tun, int dev, uint16_t got)
{
    uint16_t want = firc_tunnel_tcp_port(tun);
    t->drifts[dev] = 0;
    if (want != 0 && got == 0) {
        firc_log_from(FIRC_LOG_WARN, tun->device, "port %u is taken, TCP stays on the TUN", (unsigned)want);
    } else if (want != 0 && got != want) {
        firc_log_from(FIRC_LOG_WARN, tun->device, "tunvless reported port %u, not %u; TCP stays on the TUN",
                      (unsigned)got, (unsigned)want);
    }
    set_port(t, dev, got == want ? want : 0);
    if (t->ports[dev] != 0) {
        send_mapall(t, tun, dev, image_now(t));
    }
}

static void map_drift(firc_tunrun_t *t, const firc_tunnel_t *tun, int dev, const firc_tev_t *ev)
{
    if (t->drifts[dev] >= 2) {
        return;
    }
    if (++t->drifts[dev] == 1) {
        send_mapall(t, tun, dev, image_now(t));
        return;
    }
    firc_log_from(FIRC_LOG_WARN, tun->device, "tunvless took %d of %d map lines, its map is no longer resent",
                  ev->count, ev->expect);
}

static void answer_miss(firc_tunrun_t *t, const firc_tunnel_t *tun, int dev, const uint8_t fake[4])
{
    firc_ip_t real = {{0}, 0};
    bool known = t->has_k && t->k.real_of != NULL && t->k.real_of((uint8_t)dev, fake, &real, t->k.ud);
    firc_ktcp_cmds_t c = {0};
    if (firc_ktcp_line(&c, fake, known ? &real : NULL) == FIRC_OK) {
        send_cmds(t, tun, FIRC_TUN_CMD_MAP, &c);
    }
    firc_ktcp_cmds_free(&c);
}

static int configured_index(const firc_tunrun_t *t, const char *device)
{
    const firc_tunnel_t *tun = firc_tunnels_by_device(&t->cur, device);
    return tun != NULL ? firc_tunnel_index(tun) : -1;
}

static void add_bytes(firc_tunrun_t *t, int n, uint32_t rx, uint32_t tx)
{
    if (n < 0 || n >= FIRC_KTCP_DEVICES) {
        return;
    }
    t->kb_rx[n] += rx;
    t->kb_tx[n] += tx;
}

static void ktcp_event(firc_tunrun_t *t, const firc_tun_state_t *st, const firc_tev_t *ev)
{
    if (ev == NULL) {
        return;
    }
    ssize_t i = find(&t->cur, st->id);
    if (ev->kind == FIRC_TEV_BYTES) {
        if (i >= 0) {
            add_bytes(t, firc_tunnel_index(&t->cur.t[i]), ev->rx, ev->tx);
        }
        return;
    }
    int dev = i < 0 ? -1 : ktcp_dev(&t->cur.t[i]);
    if (dev < 0 || !firc_tunsup_ready(t->sup, st->id)) {
        return;
    }
    const firc_tunnel_t *tun = &t->cur.t[i];
    switch (ev->kind) {
    case FIRC_TEV_READY:
        if (t->k.ready != NULL) {
            t->k.ready((uint8_t)dev, t->k.ud);
        }
        ready_port(t, tun, dev, ev->tcp_port);
        break;
    case FIRC_TEV_MAP:
        if (ev->drift && t->ports[dev] != 0) {
            map_drift(t, tun, dev, ev);
        }
        break;
    case FIRC_TEV_MISS:
        if (t->ports[dev] != 0) {
            answer_miss(t, tun, dev, ev->fake);
        }
        break;
    case FIRC_TEV_CLOSE:
        firc_log_from(FIRC_LOG_DEBUG, tun->device, "tunvless closed %d connections", ev->count);
        if (t->k.closed != NULL) {
            t->k.closed((uint8_t)dev, t->k.ud);
        }
        break;
    default:
        break;
    }
}

static void watch(firc_tunrun_t *t)
{
    if (t->stopped || t->ext == NULL) {
        return;
    }
    firc_tun_state_t *st = calloc(FIRC_TUN_MAX, sizeof(*st));
    if (st == NULL) {
        return;
    }
    size_t n = firc_tunrun_states(t, st, FIRC_TUN_MAX);
    for (size_t i = 0; i < n && i < FIRC_TUN_MAX; i++) {
        firc_tunwatch_see(&t->ext[i].watch, st[i].device, st[i].status, t->now());
    }
    free(st);
}

static void on_watch(firc_loop_t *loop, void *ud)
{
    (void)loop;
    watch(ud);
}

static void set_ib(ib_t *s, const char *state, const char *why)
{
    snprintf(s->state, sizeof s->state, "%.15s", state);
    snprintf(s->why, sizeof s->why, "%.95s", why);
}

static void judge(const firc_tunnel_t *tun, const firc_tun_inbound_t *b, ib_t *s, ib_verdict_t v)
{
    if (v == s->verdict) {
        return;
    }
    unsigned port = b->port;
    switch (v) {
    case IB_GONE:
        firc_log_from(FIRC_LOG_WARN, tun->device, "inbound :%u: node %s is gone", port, b->node);
        break;
    case IB_NOT_LAN:
        firc_log_from(FIRC_LOG_WARN, tun->device, "inbound :%u: %s is not a LAN interface of app.link", port, b->listen);
        break;
    case IB_NO_ADDR:
        firc_log_from(FIRC_LOG_WARN, tun->device, "inbound :%u: %s has no IPv4 address", port, b->listen);
        break;
    case IB_OK:
        if (s->verdict == IB_GONE) {
            firc_log_from(FIRC_LOG_INFO, tun->device, "inbound :%u: node %s is back", port, b->node);
        }
        break;
    }
    s->verdict = v;
}

static bool lan_ok(firc_tunrun_t *t, const char *name)
{
    if (t->wan_of != NULL) {
        firc_tunrun_wan_fn f = t->wan_of;
        t->wan_of = NULL;
        (void)f((const char *const *)t->link, t->n_link, t->wan, t->wan_ud);
    }
    for (size_t i = 0; i < t->n_link; i++) {
        if (strcmp(t->link[i], name) == 0) {
            return !t->wan[i];
        }
    }
    /* AWGM: private diagnostics do not participate in LAN interception. */
    return strcmp(name, "lo") == 0;
}

bool firc_tunrun_lan_ok(firc_tunrun_t *t, const char *name)
{
    return lan_ok(t, name);
}

static void put_field(FILE *fp, const char *s)
{
    if (s[0] == 0) {
        fputc('-', fp);
        return;
    }
    if (strcmp(s, "-") == 0) {
        fputs("%2D", fp);
        return;
    }
    for (const unsigned char *p = (const unsigned char *)s; *p != 0; p++) {
        if (*p <= 0x20 || *p == '%' || *p == 0x7f) {
            fprintf(fp, "%%%02X", *p);
        } else {
            fputc(*p, fp);
        }
    }
}

static bool put_line(firc_tunrun_t *t, size_t i, size_t k, const char *link, FILE *fp)
{
    const firc_tunnel_t *tun = &t->cur.t[i];
    const firc_tun_inbound_t *b = &tun->inb[k];
    ib_t *s = &t->ext[i].ib[k];
    char addr[INET_ADDRSTRLEN];
    if (!lan_ok(t, b->listen)) {
        judge(tun, b, s, IB_NOT_LAN);
        set_ib(s, "not-lan", "");
        return false;
    }
    if (firc_iface_ipv4(b->listen, addr, sizeof addr) != 0) {
        judge(tun, b, s, IB_NO_ADDR);
        set_ib(s, "no-address", "");
        return false;
    }
    if (strcmp(b->node, FIRC_TUN_NODE_POOL) == 0) {
        link = FIRC_TUN_NODE_POOL;
    }
    judge(tun, b, s, link != NULL ? IB_OK : IB_GONE);
    fprintf(fp, "%s:%u ", addr, (unsigned)b->port);
    put_field(fp, b->user);
    fputc(' ', fp);
    put_field(fp, b->password);
    fprintf(fp, " %s\n", link != NULL ? link : "-");
    if (link == NULL) {
        set_ib(s, "no-node", "");
        return false;
    }
    return true;
}

static void refresh_inbounds(firc_tunrun_t *t, size_t i)
{
    const firc_tunnel_t *tun = &t->cur.t[i];
    ext_t *x = &t->ext[i];
    if (t->stopping) {
        return;
    }
    if (!tun->enable) {
        for (size_t k = 0; k < tun->n_inb; k++) {
            set_ib(&x->ib[k], "off", "");
        }
        return;
    }
    if (tun->n_inb == 0 && x->sent_in == NULL) {
        return;
    }
    const char *keys[FIRC_TUN_MAX_INBOUNDS];
    char *links[FIRC_TUN_MAX_INBOUNDS] = {NULL};
    for (size_t k = 0; k < tun->n_inb; k++) {
        keys[k] = tun->inb[k].node;
    }
    size_t n_bodies = 0;
    const firc_tun_body_t *bodies = firc_tunsubs_bodies(t->subs, &n_bodies);
    if (tun->n_inb > 0 &&
        firc_tun_nodes_links_of(tun, t->up[i].mark, bodies, n_bodies, keys, tun->n_inb, links) != FIRC_OK) {
        return;
    }
    char *text = NULL;
    size_t len = 0;
    FILE *fp = open_memstream(&text, &len);
    bool live[FIRC_TUN_MAX_INBOUNDS] = {false};
    for (size_t k = 0; fp != NULL && k < tun->n_inb; k++) {
        live[k] = put_line(t, i, k, links[k], fp);
    }
    for (size_t k = 0; k < tun->n_inb; k++) {
        free(links[k]);
    }
    if (fp == NULL || fclose(fp) != 0 || text == NULL) {
        free(text);
        firc_log_from(FIRC_LOG_WARN, tun->device, "out of memory, inbounds not sent");
        return;
    }
    bool ready = firc_tunsup_ready(t->sup, tun->id);
    bool changed = x->sent_in == NULL ? len > 0 : strcmp(x->sent_in, text) != 0;
    bool due = changed;
    for (size_t k = 0; k < tun->n_inb && !due; k++) {
        due = x->ib[k].lined && strcmp(x->ib[k].state, "busy") == 0;
    }
    bool sent = false;
    if (ready && due) {
        firc_err_t err = firc_tunsup_command(t->sup, tun->id, FIRC_TUN_CMD_INBOUNDS, text, len, NULL, 0);
        if (err == FIRC_OK) {
            free(x->sent_in);
            x->sent_in = text;
            text = NULL;
            sent = true;
            for (size_t k = 0; k < tun->n_inb; k++) {
                x->ib[k].lined = live[k];
            }
        } else {
            firc_log_from(FIRC_LOG_WARN, tun->device, "inbounds not sent: %s", firc_err_str(err));
        }
    }
    free(text);
    for (size_t k = 0; k < tun->n_inb; k++) {
        if (live[k] && (sent || !ready) && (changed || strcmp(x->ib[k].state, "busy") != 0)) {
            set_ib(&x->ib[k], "starting", "");
        }
    }
}

static ssize_t ib_at(const firc_tunnel_t *tun, const ext_t *x, uint16_t port)
{
    for (size_t k = 0; k < tun->n_inb; k++) {
        if (x->ib[k].lined && tun->inb[k].port == port) {
            return (ssize_t)k;
        }
    }
    return -1;
}

static void inbound_event(firc_tunrun_t *t, const firc_tun_state_t *st, const firc_tev_t *ev)
{
    ssize_t i = find(&t->cur, st->id);
    if (i < 0 || t->ext == NULL) {
        return;
    }
    ext_t *x = &t->ext[i];
    const firc_tunnel_t *tun = &t->cur.t[i];
    if (ev == NULL) {
        if (!t->pushing) {
            refresh_inbounds(t, (size_t)i);
        }
        return;
    }
    if (ev->kind != FIRC_TEV_INBOUND && ev->kind != FIRC_TEV_IBYTES) {
        return;
    }
    ssize_t k = ib_at(tun, x, ev->ib_port);
    if (k < 0) {
        return;
    }
    ib_t *s = &x->ib[k];
    if (ev->kind == FIRC_TEV_IBYTES) {
        s->rx += ev->rx;
        s->tx += ev->tx;
        return;
    }
    if (strcmp(ev->istate, "busy") == 0 && strcmp(s->state, "busy") != 0) {
        firc_log_from(FIRC_LOG_ERROR, tun->device, "inbound :%u on %s: port is busy", (unsigned)ev->ib_port,
                      tun->inb[k].listen);
    }
    set_ib(s, ev->istate, ev->why);
}

static void on_event(const firc_tun_state_t *st, const firc_tev_t *ev, void *ud)
{
    firc_tunrun_t *t = ud;
    track_nodes(t, st, ev);
    ktcp_event(t, st, ev);
    ssize_t at = find(&t->cur, st->id);
    firc_logrep_t *no_node = at >= 0 && t->ext != NULL ? &t->ext[at].no_node : NULL;
    char line[FIRC_EVENT_TEXT];
    firc_log_level_t level = FIRC_LOG_INFO;
    if (ev != NULL && firc_tun_journal_line(ev, line, sizeof line, &level)) {
        if (ev->kind == FIRC_TEV_NO_NODE && no_node != NULL) {
            firc_logrep_fail(no_node, level, st->device, "%s", line);
        } else {
            firc_log_from(level, st->device, "%s", line);
        }
    }
    if (ev != NULL && ev->kind == FIRC_TEV_ACTIVE && ev->n_active > 0 && no_node != NULL) {
        (void)firc_logrep_ok(no_node, st->device, "nodes");
    }
    watch(t);
    inbound_event(t, st, ev);
    if (ev == NULL || ev->kind != FIRC_TEV_READY) {
        return;
    }
    for (size_t i = 0; i < t->cur.n; i++) {
        if (t->cur.t[i].uplink == FIRC_UPLINK_TUNNEL && strcmp(t->cur.t[i].uplink_ref, st->id) == 0) {
            refresh(t, i);
        }
    }
    if (at >= 0 && t->ext != NULL) {
        free(t->ext[at].sent_in);
        t->ext[at].sent_in = NULL;
        refresh_inbounds(t, (size_t)at);
    }
}

static void forget(firc_tunrun_t *t, const char *id)
{
    char owner[24];
    snprintf(owner, sizeof owner, FIRC_MARK_TUNNEL_OWNER "%s", id);
    firc_rtnl_forget_mark_field(t->r, owner);
}

static void remove_uplink(firc_tunrun_t *t, const char *device, firc_tunnet_uplink_t *u)
{
    firc_err_t err = firc_tunnet_uplink_remove(t->r, u);
    if (err != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, device, "uplink not fully removed: %s", firc_err_str(err));
    }
    memset(u, 0, sizeof *u);
}

static ssize_t find_drain(const firc_tunrun_t *t, const char *id)
{
    for (size_t i = 0; i < t->n_drains; i++) {
        if (strcmp(t->drains[i].id, id) == 0) {
            return (ssize_t)i;
        }
    }
    return -1;
}

static void drop_drain(firc_tunrun_t *t, size_t i)
{
    memmove(&t->drains[i], &t->drains[i + 1], (t->n_drains - i - 1) * sizeof *t->drains);
    t->n_drains--;
}

static void release(firc_tunrun_t *t, size_t i)
{
    remove_uplink(t, t->drains[i].device, &t->drains[i].u);
    forget(t, t->drains[i].id);
    drop_drain(t, i);
}

static void drain(firc_tunrun_t *t, const firc_tunnel_t *tun, firc_tunnet_uplink_t *u)
{
    if (!firc_tunsup_holds(t->sup, tun->id, u->mark)) {
        remove_uplink(t, tun->device, u);
        forget(t, tun->id);
        return;
    }
    drain_t *grown = realloc(t->drains, (t->n_drains + 1) * sizeof *grown);
    if (grown == NULL) {
        firc_log_from(FIRC_LOG_WARN, tun->device, "out of memory, its uplink rule stays until the next start");
        memset(u, 0, sizeof *u);
        return;
    }
    t->drains = grown;
    drain_t *d = &t->drains[t->n_drains++];
    snprintf(d->id, sizeof d->id, "%s", tun->id);
    snprintf(d->device, sizeof d->device, "%s", tun->device);
    d->u = *u;
    memset(u, 0, sizeof *u);
}

static int opkg_n(const char *device)
{
    int n = firc_tundev_is_opkg(device) ? firc_tundev_name_index(device) : -1;
    return n >= 0 && n < OPKG_N ? n : -1;
}

static void send_wants(firc_tunrun_t *t)
{
    if (!t->has_router) {
        return;
    }
    firc_opkgtun_want_t w[OPKG_N];
    size_t k = 0;
    for (int n = 0; n < OPKG_N; n++) {
        const orec_t *o = &t->orec[n];
        if (o->named) {
            w[k] = o->w;
            w[k].route = o->route_ok;
            w[k].keep = !(o->have_seen && o->seen.route_reads > 0) && o->w.policies;
            w[k].checked = o->checked;
            k++;
        } else if (o->removing && firc_tunsup_holds_device(t->sup, o->w.device)) {
            w[k++] = o->w;
        } else if (o->removing) {
            memset(&w[k], 0, sizeof w[k]);
            snprintf(w[k].name, sizeof w[k].name, "OpkgTun%u", (unsigned)n % OPKG_N);
            snprintf(w[k].device, sizeof w[k].device, "opkgtun%u", (unsigned)n % OPKG_N);
            w[k].n = n;
            k++;
        }
    }
    t->router.want(w, k, t->router.ud);
}

static void on_reap(const char *id, const char *device, pid_t pid, void *ud)
{
    (void)pid;
    firc_tunrun_t *t = ud;
    ssize_t d = find_drain(t, id);
    if (d >= 0 && !firc_tunsup_holds(t->sup, id, t->drains[d].u.mark)) {
        release(t, (size_t)d);
    }
    int n = opkg_n(device);
    if (n >= 0 && !t->stopping && t->orec[n].removing && !firc_tunsup_holds_device(t->sup, device)) {
        send_wants(t);
    }
}

static void hold(orec_t *o, const char *id, uint32_t gen)
{
    o->waiting = true;
    snprintf(o->wait_id, sizeof o->wait_id, "%s", id);
    o->wait_gen = gen;
}

static firc_tun_gate_t gate(const char *id, const char *device, int n, uint32_t gen, void *ud)
{
    firc_tunrun_t *t = ud;
    if (!t->has_router) {
        return firc_tundev_is_opkg(device) ? FIRC_TUN_GATE_WAIT : FIRC_TUN_GATE_GO;
    }
    if (n < 0 || n >= OPKG_N) {
        return FIRC_TUN_GATE_GO;
    }
    orec_t *o = &t->orec[n];
    if (o->removing) {
        hold(o, id, gen);
        return FIRC_TUN_GATE_WAIT;
    }
    if (!firc_tundev_is_opkg(device) || !o->named) {
        return FIRC_TUN_GATE_GO;
    }
    o->w.recheck = ++o->recheck;
    hold(o, id, gen);
    send_wants(t);
    return FIRC_TUN_GATE_WAIT;
}

static void open_waiter(firc_tunrun_t *t, orec_t *o)
{
    if (o->waiting && !o->cut) {
        o->waiting = false;
        firc_tunsup_gate_open(t->sup, o->wait_id, o->wait_gen);
    }
}

static void restart_bad(firc_tunrun_t *t, int n)
{
    firc_tun_state_t st[FIRC_TUN_MAX * 2];
    size_t k = firc_tunsup_states(t->sup, st, sizeof st / sizeof st[0]);
    for (size_t i = 0; i < k && i < sizeof st / sizeof st[0]; i++) {
        if (st[i].status == FIRC_TUN_ST_BAD_CONFIG && firc_tundev_name_index(st[i].device) == n) {
            (void)firc_tunsup_restart(t->sup, st[i].id);
        }
    }
}

static void removed(firc_tunrun_t *t, int n)
{
    orec_t *o = &t->orec[n];
    o->removing = false;
    o->gone = true;
    o->have_seen = false;
    t->router.pass(t->router.ud);
    open_waiter(t, o);
    restart_bad(t, n);
}

static int record_index(const char *name)
{
    static const char stem[] = "OpkgTun";
    if (strncmp(name, stem, sizeof stem - 1) != 0) {
        return -1;
    }
    char dev[16];
    snprintf(dev, sizeof dev, "opkgtun%s", name + sizeof stem - 1);
    return opkg_n(dev);
}

static bool route_wanted(const firc_tunrun_t *t, const orec_t *o, int64_t now)
{
    const firc_tunnel_t *tun = firc_tunnels_by_device(&t->cur, o->w.device);
    bool may = tun != NULL && tun->enable && o->w.policies && !o->latched && !o->tripped;
    bool settled = o->live && now - o->live_since >= GUARD_CLEAN_MS;
    bool unchecked = o->have_seen && o->seen.route && o->seen.route_adds != o->checked;
    bool kept = o->route_ok || (o->have_seen && o->seen.route);
    return may && (settled || (kept && !unchecked));
}

static void on_router(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)fd;
    (void)events;
    firc_tunrun_t *t = ud;
    if (!t->has_router || t->stopped) {
        return;
    }
    firc_opkgtun_seen_t v[FIRC_OPKGTUN_MAX_WANTS];
    uint32_t swept = 0;
    size_t m = t->router.seen(v, FIRC_OPKGTUN_MAX_WANTS, &swept, t->router.ud);
    bool resend = false;
    for (size_t i = 0; i < m && i < FIRC_OPKGTUN_MAX_WANTS; i++) {
        int n = record_index(v[i].name);
        if (n < 0) {
            continue;
        }
        orec_t *o = &t->orec[n];
        bool was_ready = o->have_seen && o->seen.state == FIRC_OPKG_READY && o->seen.removing == v[i].removing &&
                         o->seen.rechecked == v[i].rechecked;
        bool first_read = !(o->have_seen && o->seen.route_reads > 0) && v[i].route_reads > 0;
        o->seen = v[i];
        o->have_seen = true;
        if (first_read && o->named && o->w.policies && !v[i].removing) {
            o->route_ok = route_wanted(t, o, t->now());
            resend = true;
        }
        if (v[i].removing) {
            if (o->removing && v[i].state == FIRC_OPKG_READY && !firc_tunsup_holds_device(t->sup, o->w.device)) {
                removed(t, n);
                resend = true;
            }
            continue;
        }
        if (!o->named || v[i].state != FIRC_OPKG_READY) {
            continue;
        }
        if (!was_ready) {
            t->router.pass(t->router.ud);
        }
        if (v[i].rechecked == o->recheck) {
            open_waiter(t, o);
        }
        if (o->checked > v[i].route_adds) {
            o->checked = v[i].route_adds;
        }
    }
    for (int n = 0; n < OPKG_N; n++) {
        if ((swept & (1u << n)) != 0 && !t->orec[n].named) {
            removed(t, n);
        }
    }
    if (resend) {
        send_wants(t);
    }
}

void firc_tunrun_refresh_inbounds(firc_tunrun_t *t)
{
    if (t == NULL || t->stopped) {
        return;
    }
    for (size_t i = 0; i < t->cur.n; i++) {
        refresh_inbounds(t, i);
    }
}

static bool guard_armed(const orec_t *o)
{
    return o->named && (o->route_ok || o->tripped || o->cut || (o->have_seen && o->seen.route));
}

static void note_flap(orec_t *o, int64_t now)
{
    size_t k = 0;
    for (size_t i = 0; i < o->n_flaps; i++) {
        if (now - o->flaps[i] < FLAP_WINDOW_MS) {
            o->flaps[k++] = o->flaps[i];
        }
    }
    if (k == FLAP_MAX) {
        memmove(o->flaps, o->flaps + 1, (FLAP_MAX - 1) * sizeof o->flaps[0]);
        k--;
    }
    o->flaps[k++] = now;
    o->n_flaps = k;
}

static void trip(firc_tunrun_t *t, orec_t *o, int64_t now)
{
    o->tripped = true;
    o->trip_at = now;
    o->trip_reads = o->have_seen ? o->seen.route_reads : 0;
    o->route_ok = false;
    o->other = false;
    o->clean = false;
    note_flap(o, now);
    if (o->n_flaps >= FLAP_MAX && !o->latched) {
        o->latched = true;
        firc_log_from(FIRC_LOG_ERROR, o->w.device, LATCH_TEXT, o->w.name);
    } else if (!o->latched) {
        firc_log_from(FIRC_LOG_ERROR, o->w.device, TRIP_TEXT, o->w.name);
    }
    send_wants(t);
    if (t->router.trip != NULL) {
        t->router.trip(o->w.name, t->router.ud);
    }
}

static bool guard_one(firc_tunrun_t *t, orec_t *o, const int *oifs, size_t n_oifs, bool read_ok, int64_t now)
{
    const firc_tunnel_t *tun = firc_tunnels_by_device(&t->cur, o->w.device);
    firc_link_info_t li;
    bool found = false;
    bool carrier = firc_rtnl_link_by_name(t->r, o->w.device, &li, &found) == FIRC_OK && found && li.carrier;
    bool live = tun != NULL && tun->enable && carrier && firc_tunsup_ready(t->sup, tun->id);
    if (live && !o->live) {
        o->live_since = now;
    }
    o->live = live;
    bool via = false;
    bool other = false;
    for (size_t i = 0; read_ok && i < n_oifs; i++) {
        if (found && oifs[i] == li.ifindex) {
            via = true;
        } else {
            other = true;
        }
    }
    bool sent = false;
    if (guard_armed(o) && via && !o->tripped) {
        trip(t, o, now);
        sent = true;
    }
    bool gone = o->have_seen && o->seen.route_reads != o->trip_reads && !o->seen.route;
    if (o->tripped && !gone && !o->cut && tun != NULL && now - o->trip_at >= GUARD_BOUND_MS) {
        o->cut = true;
        firc_log_from(FIRC_LOG_ERROR, o->w.device, "%s", BOUND_TEXT);
        (void)firc_tunsup_restart(t->sup, tun->id);
    } else if (gone && o->cut) {
        o->cut = false;
        if (o->have_seen && o->seen.state == FIRC_OPKG_READY && o->seen.rechecked == o->recheck) {
            open_waiter(t, o);
        }
    }
    if (o->tripped) {
        if (!o->latched && !via && other && live) {
            if (!o->other) {
                o->other = true;
                o->other_since = now;
            }
            if (now - o->other_since >= GUARD_OTHER_MS) {
                o->tripped = false;
                o->other = false;
            }
        } else {
            o->other = false;
        }
    }
    bool changed = false;
    if (o->have_seen && o->seen.route && o->seen.route_adds != o->checked && !o->tripped) {
        if (live && read_ok && !via) {
            if (!o->clean) {
                o->clean = true;
                o->clean_since = now;
            }
            if (now - o->clean_since >= GUARD_CLEAN_MS) {
                o->checked = o->seen.route_adds;
                o->clean = false;
                changed = true;
            }
        } else {
            o->clean = false;
        }
    } else {
        o->clean = false;
    }
    bool want = route_wanted(t, o, now);
    if (want != o->route_ok) {
        o->route_ok = want;
        changed = true;
    }
    return changed && !sent;
}

static void guard(firc_tunrun_t *t)
{
    if (t->stopped || !t->has_router) {
        return;
    }
    int64_t now = t->now();
    bool any = false;
    for (int n = 0; n < OPKG_N; n++) {
        any = any || guard_armed(&t->orec[n]) || (t->orec[n].named && t->orec[n].w.policies);
    }
    if (!any) {
        return;
    }
    int oifs[GUARD_OIFS];
    size_t n_oifs = 0;
    bool read_ok = false;
    for (int n = 0; n < OPKG_N; n++) {
        if (guard_armed(&t->orec[n])) {
            read_ok = firc_rtnl_main_defaults(t->r, oifs, GUARD_OIFS, &n_oifs) == FIRC_OK;
            if (n_oifs > GUARD_OIFS) {
                n_oifs = GUARD_OIFS;
            }
            break;
        }
    }
    bool changed = false;
    for (int n = 0; n < OPKG_N; n++) {
        if (t->orec[n].named) {
            changed = guard_one(t, &t->orec[n], oifs, n_oifs, read_ok, now) || changed;
        }
    }
    if (changed) {
        send_wants(t);
    }
}

static void on_guard(firc_loop_t *loop, void *ud)
{
    (void)loop;
    guard(ud);
}

void firc_tunrun_guard_for_test(firc_tunrun_t *t)
{
    guard(t);
}

static bool copy_lan(firc_tunrun_t *t, const firc_tunrun_lan_t *lan)
{
    if (lan == NULL) {
        return true;
    }
    t->link = calloc(lan->n_link + 1, sizeof *t->link);
    t->wan = calloc(lan->n_link + 1, sizeof *t->wan);
    if (t->link == NULL || t->wan == NULL) {
        return false;
    }
    for (size_t i = 0; i < lan->n_link; i++) {
        t->link[i] = strdup(lan->link[i]);
        if (t->link[i] == NULL) {
            return false;
        }
        t->n_link++;
    }
    t->wan_of = lan->wan_of;
    t->wan_ud = lan->wan_ud;
    return true;
}

static void free_lan(firc_tunrun_t *t)
{
    for (size_t i = 0; i < t->n_link; i++) {
        free(t->link[i]);
    }
    free(t->link);
    free(t->wan);
}

firc_tunrun_t *firc_tunrun_new(firc_loop_t *loop, firc_rtnl_t *r, uint32_t start_table, const char *binary,
                               const firc_tunrun_lan_t *lan)
{
    firc_tunrun_t *t = calloc(1, sizeof *t);
    if (t == NULL) {
        return NULL;
    }
    if (!copy_lan(t, lan)) {
        free_lan(t);
        free(t);
        return NULL;
    }
    t->loop = loop;
    t->r = r;
    t->list_max = VLESS_CTL_NODES_MAX;
    t->start = start_table;
    t->now = mono_ms;
    t->sup = firc_tunsup_new(loop, binary, on_event, t);
    if (t->sup == NULL) {
        free_lan(t);
        free(t);
        return NULL;
    }
    firc_tunsup_set_on_reap(t->sup, on_reap, t);
    firc_tunsup_set_gate(t->sup, gate, t);
    if (firc_loop_add_timer(loop, WATCH_MS, WATCH_MS, on_watch, t, &t->watch_timer) != FIRC_OK) {
        t->watch_timer = 0;
        FIRC_WARN("tunnels: a tunnel with no working node will not be reported");
    }
    if (firc_loop_add_timer(loop, GUARD_MS, GUARD_MS, on_guard, t, &t->guard_timer) != FIRC_OK) {
        t->guard_timer = 0;
        FIRC_ERROR("tunnels: the main-default guard cannot run; firc adds no default route for router policies");
    }
    return t;
}

typedef struct {
    char id[FIRC_MARK_MAX_GROUPS][16];
    size_t n;
} tun_owners_t;

static void collect_tun_owners(void *ud, const firc_rtnl_field_t *v, size_t n)
{
    tun_owners_t *o = ud;
    size_t pl = sizeof(FIRC_MARK_TUNNEL_OWNER) - 1;
    for (size_t i = 0; i < n && o->n < FIRC_MARK_MAX_GROUPS; i++) {
        if (strncmp(v[i].owner, FIRC_MARK_TUNNEL_OWNER, pl) == 0 && strlen(v[i].owner + pl) < sizeof o->id[0]) {
            snprintf(o->id[o->n++], sizeof o->id[0], "%s", v[i].owner + pl);
        }
    }
}

static bool holds_field(const firc_tunrun_t *t, const char *id)
{
    for (size_t i = 0; i < t->cur.n; i++) {
        if (t->up[i].mark != 0 && strcmp(t->cur.t[i].id, id) == 0) {
            return true;
        }
    }
    return find_drain(t, id) >= 0;
}

static void free_unheld_fields(firc_tunrun_t *t)
{
    tun_owners_t *o = calloc(1, sizeof *o);
    if (o == NULL) {
        return;
    }
    firc_rtnl_fields_now(t->r, collect_tun_owners, o);
    for (size_t i = 0; i < o->n; i++) {
        if (!holds_field(t, o->id[i])) {
            forget(t, o->id[i]);
        }
    }
    free(o);
}

static ssize_t find(const firc_tunnels_t *all, const char *id)
{
    for (size_t i = 0; i < all->n; i++) {
        if (strcmp(all->t[i].id, id) == 0) {
            return (ssize_t)i;
        }
    }
    return -1;
}

static void keep(firc_tunrun_t *t, const firc_tunnel_t *nt, const firc_tunnels_t *want, firc_tunnet_uplink_t *u)
{
    firc_err_t err = firc_tunnet_uplink_refresh(t->r, nt, want, u);
    if (err != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, nt->device, "uplink route not rewritten: %s", firc_err_str(err));
    }
}

static void free_sendable(firc_tun_src_t *v, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        free(v[i].link);
    }
    free(v);
}

static void free_one_sent(sent_t *x)
{
    free_sendable(x->src, x->n);
    free(x->row_of);
    firc_tun_nodes_free(&x->rows);
    memset(x, 0, sizeof *x);
}

static size_t fit_stdin(const firc_tunnel_t *tun, firc_tun_src_t *v, size_t k, firc_tun_nodes_t *nodes,
                        const size_t *row_of, size_t max)
{
    size_t total = 1;
    size_t keep = 0;
    while (keep < k && total + strlen(v[keep].link) + 1 <= max) {
        total += strlen(v[keep].link) + 1;
        keep++;
    }
    if (keep == k) {
        return k;
    }
    for (size_t p = keep; p < k; p++) {
        free(v[p].link);
        v[p].link = NULL;
        snprintf(nodes->v[row_of[p]].skip_reason, sizeof nodes->v[row_of[p]].skip_reason, "%s",
                 "over tunvless's 4 MiB node list");
    }
    firc_log_from(FIRC_LOG_WARN, tun->device, "node list over %zu bytes, %zu nodes kept, %zu left out", max, keep, k - keep);
    return keep;
}

static firc_err_t sendable(const firc_tunnel_t *tun, uint32_t mark, const firc_tun_body_t *bodies, size_t n_bodies,
                           size_t max, sent_t *out)
{
    firc_tun_nodes_t nodes;
    firc_err_t err = firc_tun_nodes_build(tun, mark, bodies, n_bodies, &nodes);
    if (err != FIRC_OK) {
        return err;
    }
    firc_tun_src_t *v = calloc(nodes.n + 1, sizeof *v);
    size_t *row_of = calloc(nodes.n + 1, sizeof *row_of);
    if (v == NULL || row_of == NULL) {
        free(v);
        free(row_of);
        firc_tun_nodes_free(&nodes);
        return FIRC_ERR_NOMEM;
    }
    size_t k = 0;
    for (size_t i = 0; i < nodes.n; i++) {
        if (nodes.v[i].link != NULL) {
            v[k].kind = FIRC_TUN_SRC_LINK;
            v[k].link = nodes.v[i].link;
            nodes.v[i].link = NULL;
            row_of[k] = i;
            k++;
        }
    }
    k = fit_stdin(tun, v, k, &nodes, row_of, max);
    out->src = v;
    out->row_of = row_of;
    out->n = k;
    out->have = true;
    out->rows = nodes;
    return FIRC_OK;
}

static void want_subs(firc_tunrun_t *t)
{
    size_t n = t->cur.n;
    firc_tunnel_t *view = calloc(n + 1, sizeof *view);
    uint32_t *marks = calloc(n + 1, sizeof *marks);
    if (view == NULL || marks == NULL) {
        free(view);
        free(marks);
        FIRC_WARN("tunnels: out of memory, the subscriptions to fetch are not updated");
        return;
    }
    for (size_t i = 0; i < n; i++) {
        view[i] = t->cur.t[i];
        view[i].enable = view[i].enable && t->ok[i];
        marks[i] = t->ok[i] ? t->up[i].mark : 0;
    }
    firc_tunnels_t sel = {.t = view, .n = n};
    firc_tunsubs_want(t->subs, &sel, marks);
    free(view);
    free(marks);
}

static void mark_syn(firc_tunrun_t *t)
{
    for (size_t i = 0; i < t->cur.n; i++) {
        const firc_tunnel_t *tun = &t->cur.t[i];
        firc_tun_status_t cls = !tun->enable ? FIRC_TUN_ST_OFF
                                : !t->ok[i]  ? FIRC_TUN_ST_UPLINK_DOWN
                                : t->waiting[i] ? FIRC_TUN_ST_WAITING
                                                : FIRC_TUN_ST_STARTING;
        if (cls != t->ext[i].syn || t->ext[i].syn_since == 0) {
            t->ext[i].syn = cls;
            t->ext[i].syn_since = mono_ms();
        }
    }
}

static firc_err_t push(firc_tunrun_t *t, bool want)
{
    size_t n = t->cur.n;
    firc_tunnel_t *run = calloc(n + 1, sizeof *run);
    uint32_t *marks = calloc(n + 1, sizeof *marks);
    if (run == NULL || marks == NULL) {
        free(run);
        free(marks);
        return FIRC_ERR_NOMEM;
    }
    if (want && t->subs != NULL) {
        want_subs(t);
    }
    size_t n_bodies = 0;
    const firc_tun_body_t *bodies = firc_tunsubs_bodies(t->subs, &n_bodies);
    size_t k = 0;
    for (size_t i = 0; i < n; i++) {
        if (!t->ok[i]) {
            continue;
        }
        const firc_tunnel_t *tun = &t->cur.t[i];
        uint32_t mark = t->up[i].mark;
        sent_t fresh = {0};
        firc_err_t err = sendable(tun, mark, bodies, n_bodies, t->list_max, &fresh);
        if (err != FIRC_OK) {
            firc_log_from(FIRC_LOG_ERROR, tun->device, "node list not built: %s", firc_err_str(err));
        } else {
            free_one_sent(&t->sent[i]);
            t->sent[i] = fresh;
            prune(&t->ext[i].downs, &t->sent[i]);
            prune(&t->ext[i].acts, &t->sent[i]);
            prune(&t->ext[i].v6s, &t->sent[i]);
        }
        if (tun->enable && t->sent[i].n == 0) {
            if (!t->waiting[i]) {
                firc_log_from(FIRC_LOG_INFO, tun->device, "no nodes to use yet");
                t->waiting[i] = true;
            }
            continue;
        }
        t->waiting[i] = false;
        run[k] = *tun;
        run[k].src = t->sent[i].src;
        run[k].n_src = t->sent[i].n;
        marks[k] = mark;
        k++;
    }
    firc_tunnels_t sel = {.t = run, .n = k};
    t->pushing = true;
    firc_err_t rc = firc_tunsup_apply(t->sup, &sel, marks);
    t->pushing = false;
    if (rc != FIRC_OK) {
        FIRC_ERROR("tunnels: not every tunnel could be started: %s", firc_err_str(rc));
    }
    free(run);
    free(marks);
    mark_syn(t);
    for (size_t i = 0; i < n; i++) {
        refresh_inbounds(t, i);
    }
    return rc;
}

static void free_sent(sent_t *v, size_t n)
{
    for (size_t i = 0; v != NULL && i < n; i++) {
        free_one_sent(&v[i]);
    }
    free(v);
}

static void free_ext(ext_t *v, size_t n)
{
    for (size_t i = 0; v != NULL && i < n; i++) {
        free(v[i].downs.v);
        free(v[i].acts.v);
        free(v[i].v6s.v);
        free(v[i].sent_in);
    }
    free(v);
}

static void sync_tunnels(firc_tunrun_t *t)
{
    char names[FIRC_KTCP_DEVICES][FIRC_KTCP_NAME_CAP];
    firc_tunnels_names(&t->cur, names);
    bool changed = memcmp(names, t->names, sizeof names) != 0;
    memcpy(t->names, names, sizeof names);
    for (int dev = 0; dev < FIRC_KTCP_DEVICES; dev++) {
        bool kept = false;
        for (size_t i = 0; i < t->cur.n && t->ports[dev] != 0 && !kept; i++) {
            kept = firc_tunnel_tcp_port(&t->cur.t[i]) == t->ports[dev];
        }
        if (t->ports[dev] != 0 && !kept) {
            t->ports[dev] = 0;
            t->drifts[dev] = 0;
            changed = true;
        }
    }
    if (changed) {
        publish(t);
    }
}

static void remap_ib(ext_t *x, const firc_tunnel_t *was, const firc_tunnel_t *now)
{
    ib_t old[FIRC_TUN_MAX_INBOUNDS];
    memcpy(old, x->ib, sizeof old);
    memset(x->ib, 0, sizeof x->ib);
    for (size_t k = 0; k < now->n_inb; k++) {
        for (size_t j = 0; j < was->n_inb; j++) {
            const firc_tun_inbound_t *a = &was->inb[j];
            const firc_tun_inbound_t *b = &now->inb[k];
            if (a->port == b->port && strcmp(a->listen, b->listen) == 0) {
                x->ib[k] = old[j];
                if (strcmp(a->node, b->node) != 0) {
                    x->ib[k].verdict = IB_OK;
                }
                break;
            }
        }
    }
}

static bool plan_wants(const firc_tunnels_t *want, firc_opkgtun_want_t w[OPKG_N], bool named[OPKG_N])
{
    memset(w, 0, OPKG_N * sizeof *w);
    memset(named, 0, OPKG_N * sizeof *named);
    for (size_t i = 0; i < want->n; i++) {
        const firc_tunnel_t *tun = &want->t[i];
        int n = opkg_n(tun->device);
        if (n < 0) {
            continue;
        }
        char *desc = firc_opkgtun_description(tun->id, tun->description);
        if (desc == NULL) {
            firc_log_from(FIRC_LOG_ERROR, tun->device,
                          "tunnel %s: its id and description do not fit the router's record; nothing applied",
                          tun->id);
            return false;
        }
        firc_opkgtun_want_t *x = &w[n];
        memset(x, 0, sizeof *x);
        snprintf(x->name, sizeof x->name, "OpkgTun%u", (unsigned)n % OPKG_N);
        snprintf(x->device, sizeof x->device, "%s", tun->device);
        x->n = n;
        snprintf(x->mark, sizeof x->mark, "firc:%s", tun->id);
        snprintf(x->desc, sizeof x->desc, "%s", desc);
        x->present = true;
        x->policies = tun->router_policies;
        free(desc);
        named[n] = true;
    }
    return true;
}

static void record_wants(firc_tunrun_t *t, const firc_opkgtun_want_t w[OPKG_N], const bool named[OPKG_N],
                         const bool edited[OPKG_N])
{
    bool used[OPKG_N] = {false};
    for (size_t i = 0; i < t->cur.n; i++) {
        int n = firc_tunnel_index(&t->cur.t[i]);
        if (n >= 0 && n < OPKG_N) {
            used[n] = true;
        }
    }
    for (int n = 0; n < OPKG_N; n++) {
        orec_t *o = &t->orec[n];
        if (!used[n]) {
            o->waiting = false;
        }
        if (named[n]) {
            const firc_tunnel_t *tun = firc_tunnels_by_device(&t->cur, w[n].device);
            o->w = w[n];
            o->w.recheck = o->recheck;
            o->named = true;
            o->removing = false;
            o->route_ok = o->route_ok && w[n].policies && tun != NULL && tun->enable;
            if (edited[n]) {
                o->latched = false;
                o->n_flaps = 0;
            }
            if (!w[n].policies) {
                o->live = false;
                o->tripped = false;
                o->other = false;
            }
        } else {
            if (o->named && o->w.policies) {
                FIRC_WARN("tunnels: OpkgTun%d was removed; router policies that used it no longer have it", n);
            }
            o->removing = o->removing || o->named;
            o->named = false;
            o->route_ok = false;
            o->checked = 0;
            o->live = false;
            o->tripped = false;
            o->cut = false;
        }
    }
    send_wants(t);
}

firc_err_t firc_tunrun_apply(firc_tunrun_t *t, firc_tunnels_t *want)
{
    if (t->stopped) {
        return FIRC_ERR_STATE;
    }
    firc_opkgtun_want_t planned[OPKG_N];
    bool named[OPKG_N];
    if (!plan_wants(want, planned, named)) {
        return FIRC_ERR_INVAL;
    }
    size_t n = want->n;
    firc_tunnet_uplink_t *up = calloc(n + 1, sizeof *up);
    bool *ok = calloc(n + 1, sizeof *ok);
    bool *kept = calloc(t->cur.n + 1, sizeof *kept);
    sent_t *sent = calloc(n + 1, sizeof *sent);
    bool *waiting = calloc(n + 1, sizeof *waiting);
    ext_t *ext = calloc(n + 1, sizeof *ext);
    if (up == NULL || ok == NULL || kept == NULL || sent == NULL || waiting == NULL || ext == NULL) {
        free(up);
        free(ok);
        free(kept);
        free(sent);
        free(waiting);
        free(ext);
        return FIRC_ERR_NOMEM;
    }
    bool edited[OPKG_N];
    for (int k = 0; k < OPKG_N; k++) {
        edited[k] = true;
    }
    for (size_t i = 0; i < n; i++) {
        int k = opkg_n(want->t[i].device);
        const firc_tunnel_t *was = k >= 0 ? firc_tunnels_by_device(&t->cur, want->t[i].device) : NULL;
        if (k >= 0 && was != NULL && strcmp(was->id, want->t[i].id) == 0 && firc_tunnel_same_argv(was, &want->t[i]) &&
            strcmp(was->description, want->t[i].description) == 0 &&
            was->router_policies == want->t[i].router_policies) {
            edited[k] = false;
        }
    }
    for (size_t i = 0; i < n; i++) {
        const firc_tunnel_t *nt = &want->t[i];
        ssize_t j = find(&t->cur, nt->id);
        bool need = needs_uplink(nt);
        if (j >= 0) {
            kept[j] = true;
            sent[i] = t->sent[j];
            memset(&t->sent[j], 0, sizeof t->sent[j]);
            waiting[i] = t->waiting[j];
            ext[i] = t->ext[j];
            memset(&t->ext[j], 0, sizeof t->ext[j]);
            remap_ib(&ext[i], &t->cur.t[j], nt);
            if (t->up[j].mark != 0) {
                if (need) {
                    up[i] = t->up[j];
                    memset(&t->up[j], 0, sizeof t->up[j]);
                    keep(t, nt, want, &up[i]);
                } else {
                    drain(t, nt, &t->up[j]);
                }
                ok[i] = true;
                continue;
            }
        }
        if (!need) {
            ok[i] = true;
            continue;
        }
        ssize_t d = find_drain(t, nt->id);
        if (d >= 0) {
            up[i] = t->drains[d].u;
            drop_drain(t, (size_t)d);
            keep(t, nt, want, &up[i]);
            ok[i] = true;
            continue;
        }
        firc_err_t err = firc_tunnet_uplink_apply(t->r, t->start, nt, want, &up[i]);
        if (err != FIRC_OK) {
            if (up[i].mark != 0) {
                remove_uplink(t, nt->device, &up[i]);
            }
            memset(&up[i], 0, sizeof up[i]);
            forget(t, nt->id);
            firc_log_from(FIRC_LOG_ERROR, nt->device, "not started, its uplink could not be set up (%s)",
                          firc_err_str(err));
            continue;
        }
        ok[i] = true;
    }
    for (size_t j = 0; j < t->cur.n; j++) {
        if (!kept[j] && t->up[j].mark != 0) {
            drain(t, &t->cur.t[j], &t->up[j]);
        } else if (!kept[j] && find_drain(t, t->cur.t[j].id) < 0) {
            forget(t, t->cur.t[j].id);
        }
    }
    free_sent(t->sent, t->cur.n);
    free_ext(t->ext, t->cur.n);
    firc_tunnels_free(&t->cur);
    free(t->up);
    free(t->ok);
    free(t->waiting);
    t->cur = *want;
    t->up = up;
    t->ok = ok;
    t->sent = sent;
    t->waiting = waiting;
    t->ext = ext;
    want->t = NULL;
    want->n = 0;
    sync_tunnels(t);
    free_unheld_fields(t);
    record_wants(t, planned, named, edited);
    firc_err_t rc = push(t, true);
    firc_tunsubs_sweep(t->subs, &t->cur);
    for (size_t i = 0; i < t->cur.n; i++) {
        show_uplink(t, i);
    }
    free(kept);
    return rc;
}

static void detach_router(firc_tunrun_t *t)
{
    if (t->has_router && t->router.fd >= 0) {
        (void)firc_loop_del_fd(t->loop, t->router.fd);
    }
    t->has_router = false;
}

void firc_tunrun_set_router(firc_tunrun_t *t, const firc_tunrun_router_t *r)
{
    detach_router(t);
    if (r == NULL || t->stopped) {
        return;
    }
    t->router = *r;
    t->has_router = true;
    if (r->fd >= 0 && firc_loop_add_fd(t->loop, r->fd, EPOLLIN, on_router, t) != FIRC_OK) {
        FIRC_ERROR("tunnels: the router's records cannot be watched; opkgtun tunnels will not start");
        t->router.fd = -1;
    }
    send_wants(t);
}

firc_err_t firc_tunrun_start(firc_tunrun_t *t, firc_tunnels_t *want)
{
    if (want == NULL) {
        return FIRC_ERR_NOENT;
    }
    firc_err_t err = firc_tunrun_apply(t, want);
    if (err == FIRC_OK && t->has_router) {
        t->router.sweep(t->router.ud);
    }
    return err;
}

void firc_tunrun_link_down(firc_tunrun_t *t, const char *device)
{
    int n = device != NULL ? opkg_n(device) : -1;
    if (t == NULL || t->stopped || !t->has_router || n < 0) {
        return;
    }
    orec_t *o = &t->orec[n];
    if (!o->named || !o->have_seen || o->seen.removing || o->seen.state != FIRC_OPKG_READY) {
        return;
    }
    o->w.recheck = ++o->recheck;
    send_wants(t);
}

static bool router_pending(const orec_t *o)
{
    return !o->have_seen || o->seen.removing || o->seen.rechecked != o->recheck ||
           o->seen.state == FIRC_OPKG_PENDING;
}

static void router_status(const firc_tunrun_t *t, const firc_tunnel_t *tun, firc_tun_state_t *st)
{
    int n = firc_tunnel_index(tun);
    if (!t->has_router || n < 0 || n >= OPKG_N || st->status != FIRC_TUN_ST_STARTING || st->why[0] == 0) {
        return;
    }
    const orec_t *o = &t->orec[n];
    if (o->cut && firc_tundev_is_opkg(tun->device)) {
        st->status = FIRC_TUN_ST_ROUTER;
        snprintf(st->why, sizeof st->why, "%s", BOUND_TEXT);
        return;
    }
    if (o->removing) {
        if (o->have_seen && o->seen.removing && o->seen.state == FIRC_OPKG_FAILED && o->seen.error[0] != 0) {
            snprintf(st->why, sizeof st->why, "waiting for OpkgTun%d to be removed: %.100s", n, o->seen.error);
        } else {
            snprintf(st->why, sizeof st->why, "waiting for OpkgTun%d to be removed", n);
        }
        return;
    }
    if (!firc_tundev_is_opkg(tun->device)) {
        return;
    }
    st->status = FIRC_TUN_ST_ROUTER;
    if (!router_pending(o) && o->seen.error[0] != 0) {
        snprintf(st->why, sizeof st->why, "%s", o->seen.error);
    }
}

bool firc_tunrun_router_of(const firc_tunrun_t *t, const char *id, firc_opkgtun_seen_t *out)
{
    ssize_t i = id != NULL ? find(&t->cur, id) : -1;
    int n = i >= 0 ? opkg_n(t->cur.t[i].device) : -1;
    if (n < 0) {
        return false;
    }
    const orec_t *o = &t->orec[n];
    memset(out, 0, sizeof *out);
    if (o->have_seen && !o->seen.removing) {
        *out = o->seen;
    } else {
        out->state = FIRC_OPKG_PENDING;
    }
    if (o->latched || o->tripped) {
        out->policies = FIRC_OPKG_POL_FAILED;
        if (o->latched) {
            snprintf(out->pol_error, sizeof out->pol_error, LATCH_TEXT, o->w.name);
        } else if (o->cut) {
            snprintf(out->pol_error, sizeof out->pol_error, "%s", BOUND_TEXT);
        } else {
            snprintf(out->pol_error, sizeof out->pol_error, TRIP_TEXT, o->w.name);
        }
    }
    snprintf(out->name, sizeof out->name, "OpkgTun%u", (unsigned)n % OPKG_N);
    return true;
}

void firc_tunrun_set_subs(firc_tunrun_t *t, firc_tunsubs_t *subs)
{
    t->subs = subs;
}

void firc_tunrun_set_path(firc_tunrun_t *t, const char *path)
{
    t->path = path;
}

static void prune_keys(firc_tunrun_t *t)
{
    size_t n_bodies = 0;
    const firc_tun_body_t *bodies = firc_tunsubs_bodies(t->subs, &n_bodies);
    bool any = false;
    for (size_t i = 0; i < t->cur.n; i++) {
        firc_tunnel_t *tun = &t->cur.t[i];
        size_t dropped = 0;
        if (tun->enable && t->ok[i] &&
            firc_tun_nodes_prune(tun, t->up[i].mark, bodies, n_bodies, &dropped) == FIRC_OK && dropped > 0) {
            FIRC_INFO("tunnel %s: dropped %zu keys no source gives any more", tun->id, dropped);
            any = true;
        }
    }
    if (any && t->path != NULL && !firc_tunnels_file_unchanged(&t->cur, t->path)) {
        FIRC_WARN("tunnels.yaml changed on disk since it was loaded; stale node keys not removed, reload to apply");
    } else if (any && t->path != NULL) {
        firc_err_t err = firc_tunnels_save_file(&t->cur, t->path);
        if (err != FIRC_OK) {
            FIRC_ERROR("tunnels: %s not written: %s", t->path, firc_err_str(err));
        }
    }
}

void firc_tunrun_bodies_changed(void *ud)
{
    firc_tunrun_t *t = ud;
    if (t == NULL || t->stopped) {
        return;
    }
    prune_keys(t);
    (void)push(t, false);
}

size_t firc_tunrun_states(const firc_tunrun_t *t, firc_tun_state_t *out, size_t cap)
{
    size_t run_cap = (size_t)FIRC_TUN_MAX * 2;
    firc_tun_state_t *run = calloc(run_cap, sizeof(*run));
    if (run == NULL) {
        return 0;
    }
    size_t n_run = firc_tunsup_states(t->sup, run, run_cap);
    if (n_run > run_cap) {
        n_run = run_cap;
    }
    for (size_t i = 0; i < t->cur.n && i < cap; i++) {
        const firc_tunnel_t *tun = &t->cur.t[i];
        firc_tun_state_t *st = &out[i];
        memset(st, 0, sizeof *st);
        snprintf(st->id, sizeof st->id, "%s", tun->id);
        snprintf(st->device, sizeof st->device, "%s", tun->device);
        st->status = t->ext[i].syn;
        st->since_ms = t->ext[i].syn_since;
        st->uplink_ok = !needs_uplink(tun) || (t->ok[i] && t->up[i].routed);
        int dev = ktcp_dev(tun);
        uint16_t port = dev < 0 || t->ktcp_unusable ? 0 : t->ports[dev];
        st->tcp_port = port;
        if (t->ext[i].syn != FIRC_TUN_ST_UPLINK_DOWN && t->ext[i].syn != FIRC_TUN_ST_WAITING) {
            st->status = FIRC_TUN_ST_OFF;
            for (size_t k = 0; k < n_run; k++) {
                if (strcmp(run[k].id, tun->id) == 0) {
                    *st = run[k];
                    st->tcp_port = port;
                    router_status(t, tun, st);
                    break;
                }
            }
        }
        st->n_ib = tun->n_inb;
        for (size_t k = 0; k < tun->n_inb; k++) {
            const ib_t *s = &t->ext[i].ib[k];
            st->ib[k].port = tun->inb[k].port;
            memcpy(st->ib[k].state, s->state, sizeof st->ib[k].state);
            memcpy(st->ib[k].why, s->why, sizeof st->ib[k].why);
            st->ib[k].rx = s->rx;
            st->ib[k].tx = s->tx;
        }
    }
    free(run);
    return t->cur.n;
}

bool firc_tunrun_device_down(const firc_tunrun_t *t, const char *device)
{
    if (device == NULL) {
        return false;
    }
    firc_tun_state_t *st = calloc(FIRC_TUN_MAX, sizeof(*st));
    if (st == NULL) {
        return false;
    }
    size_t n = firc_tunrun_states(t, st, FIRC_TUN_MAX);
    bool down = false;
    for (size_t i = 0; i < n && i < FIRC_TUN_MAX; i++) {
        if (strcmp(st[i].device, device) == 0) {
            down = st[i].status != FIRC_TUN_ST_UP;
            break;
        }
    }
    free(st);
    return down;
}

bool firc_tunrun_device_starting(const firc_tunrun_t *t, const char *device, int64_t *since_ms)
{
    if (device == NULL) {
        return false;
    }
    firc_tun_state_t *st = calloc(FIRC_TUN_MAX, sizeof(*st));
    if (st == NULL) {
        return false;
    }
    size_t n = firc_tunrun_states(t, st, FIRC_TUN_MAX);
    bool starting = false;
    for (size_t i = 0; i < n && i < FIRC_TUN_MAX; i++) {
        if (strcmp(st[i].device, device) != 0) {
            continue;
        }
        int k = opkg_n(device);
        bool pending = st[i].status == FIRC_TUN_ST_ROUTER && k >= 0 && router_pending(&t->orec[k]);
        if (st[i].status == FIRC_TUN_ST_STARTING || st[i].status == FIRC_TUN_ST_WAITING || pending) {
            *since_ms = st[i].since_ms;
            starting = true;
        }
        break;
    }
    free(st);
    return starting;
}

const firc_tunnels_t *firc_tunrun_config(const firc_tunrun_t *t)
{
    return &t->cur;
}

firc_err_t firc_tunrun_restart(firc_tunrun_t *t, const char *id)
{
    if (t->stopped || find(&t->cur, id) < 0) {
        return FIRC_ERR_NOENT;
    }
    firc_err_t err = firc_tunsup_restart(t->sup, id);
    return err == FIRC_ERR_NOENT ? FIRC_ERR_STATE : err;
}

uint64_t firc_tunrun_start_seq(const firc_tunrun_t *t)
{
    return firc_tunsup_start_seq(t->sup);
}

uint64_t firc_tunrun_started(const firc_tunrun_t *t, const char *id)
{
    return firc_tunsup_started(t->sup, id);
}

uint64_t firc_tunrun_updated(const firc_tunrun_t *t, const char *id)
{
    return firc_tunsup_updated(t->sup, id);
}

const firc_tun_nodes_t *firc_tunrun_nodes(const firc_tunrun_t *t, const char *id)
{
    ssize_t i = find(&t->cur, id);
    if (i < 0 || !t->sent[i].have) {
        return NULL;
    }
    return &t->sent[i].rows;
}

bool firc_tunrun_node_down(const firc_tunrun_t *t, const char *id, const char *key, int64_t *since_ms)
{
    ssize_t i = find(&t->cur, id);
    const down_t *d = i < 0 ? NULL : key_find(&t->ext[i].downs, key);
    if (d == NULL) {
        return false;
    }
    *since_ms = d->since;
    return true;
}

void firc_tunrun_node_answered(firc_tunrun_t *t, const char *id, const char *key)
{
    ssize_t i = find(&t->cur, id);
    if (i >= 0) {
        key_del(&t->ext[i].downs, key);
    }
}

void firc_tunrun_wake(firc_tunrun_t *t, const char *id, const char *const *links, size_t n)
{
    if (n == 0 || !firc_tunsup_ready(t->sup, id)) {
        return;
    }
    size_t len = 0;
    for (size_t i = 0; i < n; i++) {
        len += strlen(links[i]) + 1;
    }
    char *text = malloc(len + 1);
    if (text == NULL) {
        FIRC_WARN("tunnels: out of memory, %s not woken", id);
        return;
    }
    size_t at = 0;
    for (size_t i = 0; i < n; i++) {
        size_t l = strlen(links[i]);
        memcpy(text + at, links[i], l);
        text[at + l] = '\n';
        at += l + 1;
    }
    firc_err_t err = firc_tunsup_command(t->sup, id, FIRC_TUN_CMD_WAKE, text, len, NULL, 0);
    free(text);
    if (err != FIRC_OK) {
        FIRC_WARN("tunnels: wake of %s not sent: %s", id, firc_err_str(err));
    }
}

bool firc_tunrun_node_active(const firc_tunrun_t *t, const char *id, const char *key)
{
    ssize_t i = find(&t->cur, id);
    return i >= 0 && key_find(&t->ext[i].acts, key) != NULL;
}

bool firc_tunrun_node_v6(const firc_tunrun_t *t, const char *id, const char *key)
{
    ssize_t i = find(&t->cur, id);
    return i >= 0 && key_find(&t->ext[i].v6s, key) != NULL;
}

bool firc_tunrun_mark_of(const firc_tunrun_t *t, const char *id, uint32_t *mark, bool *held)
{
    *mark = 0;
    *held = false;
    ssize_t i = find(&t->cur, id);
    if (i < 0) {
        return false;
    }
    *mark = t->up[i].mark;
    *held = *mark != 0 && firc_tunsup_holds(t->sup, id, *mark);
    return true;
}

void firc_tunrun_refresh_uplinks(firc_tunrun_t *t, const char *ifname)
{
    if (t == NULL || t->stopped) {
        return;
    }
    for (size_t i = 0; i < t->cur.n; i++) {
        const char *dev = firc_tunnet_uplink_device(&t->cur.t[i], &t->cur);
        if (t->up[i].mark != 0 && dev != NULL && (ifname == NULL || strcmp(dev, ifname) == 0)) {
            refresh(t, i);
        }
    }
}

static bool own_device(const firc_tunrun_t *t, const char *iface)
{
    if (iface == NULL) {
        return false;
    }
    if (firc_tunnels_by_device(&t->cur, iface) != NULL) {
        return true;
    }
    int n = opkg_n(iface);
    return n >= 0 && (t->orec[n].removing || t->orec[n].gone);
}

void firc_tunrun_net_event(firc_tunrun_t *t, const char *iface)
{
    if (t == NULL || t->stopped || own_device(t, iface)) {
        return;
    }
    firc_tunsup_net_changed(t->sup);
}

void firc_tunrun_stop(firc_tunrun_t *t, int grace_ms)
{
    if (t == NULL || t->stopped) {
        return;
    }
    t->stopping = true;
    detach_router(t);
    if (t->watch_timer != 0) {
        (void)firc_loop_del_timer(t->loop, t->watch_timer);
        t->watch_timer = 0;
    }
    if (t->guard_timer != 0) {
        (void)firc_loop_del_timer(t->loop, t->guard_timer);
        t->guard_timer = 0;
    }
    firc_tunsup_stop(t->sup, grace_ms);
    while (t->n_drains > 0) {
        release(t, t->n_drains - 1);
    }
    for (size_t i = 0; i < t->cur.n; i++) {
        remove_uplink(t, t->cur.t[i].device, &t->up[i]);
    }
    t->stopped = true;
}

void firc_tunrun_free(firc_tunrun_t *t)
{
    if (t == NULL) {
        return;
    }
    firc_tunrun_stop(t, 0);
    firc_tunsup_free(t->sup);
    free_sent(t->sent, t->cur.n);
    free_ext(t->ext, t->cur.n);
    firc_tunnels_free(&t->cur);
    free(t->up);
    free(t->ok);
    free(t->waiting);
    free(t->drains);
    free_lan(t);
    free(t);
}

void firc_tunrun_set_list_max_for_test(firc_tunrun_t *t, size_t bytes)
{
    t->list_max = bytes;
}

void firc_tunrun_set_ktcp(firc_tunrun_t *t, const firc_tunrun_ktcp_t *k)
{
    t->has_k = k != NULL;
    if (k != NULL) {
        t->k = *k;
        publish(t);
    }
}

typedef struct {
    firc_ktcp_cmds_t per[FIRC_KTCP_DEVICES];
    const firc_tunnel_t *to[FIRC_KTCP_DEVICES];
    bool failed[FIRC_KTCP_DEVICES];
} push_t;

static void delta_line(uint8_t dev, const uint8_t fake[4], const firc_ip_t *real, void *ud)
{
    push_t *p = ud;
    if (dev < FIRC_KTCP_DEVICES && p->to[dev] != NULL && !p->failed[dev] &&
        firc_ktcp_line(&p->per[dev], fake, real) != FIRC_OK) {
        p->failed[dev] = true;
    }
}

static const firc_tunnel_t *live_with_port(const firc_tunrun_t *t, int dev)
{
    for (size_t i = 0; i < t->cur.n && t->ports[dev] != 0; i++) {
        if (ktcp_dev(&t->cur.t[i]) == dev && firc_tunsup_ready(t->sup, t->cur.t[i].id)) {
            return &t->cur.t[i];
        }
    }
    return NULL;
}

void firc_tunrun_map_pushed(firc_tunrun_t *t, const firc_ktcp_image_t *prev, const firc_ktcp_image_t *next)
{
    if (t->stopped) {
        return;
    }
    push_t *p = calloc(1, sizeof *p);
    if (p == NULL) {
        FIRC_WARN("tunnels: out of memory, address map changes not sent");
        return;
    }
    bool any = false;
    for (int dev = 0; dev < FIRC_KTCP_DEVICES; dev++) {
        p->to[dev] = live_with_port(t, dev);
        any = any || p->to[dev] != NULL;
    }
    if (any) {
        firc_ktcp_image_diff(prev, next, delta_line, p);
    }
    for (int dev = 0; dev < FIRC_KTCP_DEVICES; dev++) {
        if (p->failed[dev]) {
            send_mapall(t, p->to[dev], dev, next);
        } else if (p->to[dev] != NULL) {
            send_cmds(t, p->to[dev], FIRC_TUN_CMD_MAP, &p->per[dev]);
        }
        firc_ktcp_cmds_free(&p->per[dev]);
    }
    free(p);
}

void firc_tunrun_set_ktcp_usable(firc_tunrun_t *t, bool usable)
{
    t->ktcp_unusable = !usable;
}

void firc_tunrun_set_tcp_pool(firc_tunrun_t *t, const uint8_t base[4], uint8_t len)
{
    firc_tunsup_set_tcp_pool(t->sup, base, len);
}

int firc_tunrun_close(firc_tunrun_t *t, uint8_t dev, const firc_nf_prefix_t *p, size_t n)
{
    const firc_tunnel_t *tun = t->stopped || dev >= FIRC_KTCP_DEVICES ? NULL : live_with_port(t, dev);
    if (tun == NULL) {
        return 0;
    }
    firc_ktcp_cmds_t c = {0};
    int sent = 0;
    if (firc_ktcp_close_cmd(p, n, &c) == FIRC_OK) {
        sent = send_cmds(t, tun, FIRC_TUN_CMD_CLOSE, &c);
    } else {
        firc_log_from(FIRC_LOG_WARN, tun->device, "out of memory, close not sent");
    }
    firc_ktcp_cmds_free(&c);
    return sent;
}

int firc_tunrun_set_watch_for_test(firc_tunrun_t *t, int64_t (*now_ms)(void), int period_ms)
{
    t->now = now_ms != NULL ? now_ms : mono_ms;
    if (t->watch_timer != 0) {
        (void)firc_loop_del_timer(t->loop, t->watch_timer);
    }
    if (firc_loop_add_timer(t->loop, (uint64_t)period_ms, (uint64_t)period_ms, on_watch, t, &t->watch_timer) != FIRC_OK) {
        t->watch_timer = 0;
    }
    return t->watch_timer;
}

bool firc_tunrun_ktcp_bytes(const firc_tunrun_t *t, const char *device, uint64_t *rx, uint64_t *tx)
{
    *rx = 0;
    *tx = 0;
    int n = t != NULL ? configured_index(t, device) : -1;
    if (n < 0 || n >= FIRC_KTCP_DEVICES) {
        return false;
    }
    *rx = t->kb_rx[n];
    *tx = t->kb_tx[n];
    return true;
}

void firc_tunrun_ktcp_bytes_add_for_test(firc_tunrun_t *t, const char *device, uint32_t rx, uint32_t tx)
{
    add_bytes(t, configured_index(t, device), rx, tx);
}
