#define _GNU_SOURCE
#include <pthread.h>
#include <stdint.h>
#include <stdlib.h>
#include <string.h>
#include <sys/types.h>

#include "ctlinb.h"
#include "ctlmap.h"
#include "ctlnodes.h"
#include "nodes.h"
#include "transport.h"
#include "vless.h"
#include "vlink.h"

#define LOG_W "tunvless[warn]: "

static struct {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    struct ctln_hooks h;
    unsigned posted, taken;
    int *idx;
    size_t n;
    int told_full;
    char **xh;
    size_t xn;
} g_cn = { .mu = PTHREAD_MUTEX_INITIALIZER, .cv = PTHREAD_COND_INITIALIZER };

struct ctln_job { unsigned seq; const int *idx; size_t n; char *ok; int published; };

static const struct vless_node *vnode(int i) { return node_at(i); }

static void publish(struct ctln_job *j) {
    int *sel = malloc((j->n ? j->n : 1) * sizeof(*sel));
    if (!sel) return;
    size_t m = 0;
    for (size_t k = 0; k < j->n; k++)
        if (j->ok[k]) sel[m++] = j->idx[k];
    pthread_mutex_lock(&g_cn.mu);
    if (g_cn.posted == j->seq) {
        g_cn.h.select(sel, m);
        j->published = 1;
    }
    pthread_mutex_unlock(&g_cn.mu);
    free(sel);
}

static int superseded(void *ud) {
    const struct ctln_job *j = ud;
    pthread_mutex_lock(&g_cn.mu);
    int r = g_cn.posted != j->seq;
    pthread_mutex_unlock(&g_cn.mu);
    return r;
}

static int node_pinned(const struct vless_node *n) {
    const struct vless_node *d = vless_dl(n);
    return transport_pin_count(n->host) > 0 && (!d || transport_pin_count(d->host) > 0);
}

static void pinned(void *ud, const char *host, int ok) {
    struct ctln_job *j = ud;
    int more = 0, full = ok == TR_EPINFULL;
    for (size_t k = 0; k < j->n; k++) {
        const struct vless_node *n = vnode(j->idx[k]), *d = vless_dl(n);
        if (strcmp(n->host, host) && (!d || strcmp(d->host, host))) continue;
        if (ok > 0) {
            if (node_pinned(n)) { j->ok[k] = 1; more = 1; }
        } else if (full) fprintf(stderr, LOG_W "node %s: the pin table is full — left out\n", n->name);
        else fprintf(stderr, LOG_W "node %s: %s does not resolve — left out of the candidates\n",
                     n->name, host);
    }
    if (full) {
        pthread_mutex_lock(&g_cn.mu);
        int first = !g_cn.told_full;
        g_cn.told_full = 1;
        pthread_mutex_unlock(&g_cn.mu);
        if (first) {
            fprintf(stderr, LOG_W "pin table full: nodes with new names are left out until a restart\n");
            if (g_cn.h.pins_full) g_cn.h.pins_full();
        }
    }
    if (more) publish(j);
}

static void want_pin(const char **hosts, size_t *nh, const char *h) {
    if (transport_pin_count(h) > 0) return;
    for (size_t i = 0; i < *nh; i++)
        if (!strcmp(hosts[i], h)) return;
    hosts[(*nh)++] = h;
}

static void apply(struct ctln_job *j) {
    const char **hosts = calloc(j->n ? 2 * j->n : 1, sizeof(*hosts));
    if (!hosts) return;
    size_t nh = 0;
    for (size_t k = 0; k < j->n; k++) {
        const struct vless_node *n = vnode(j->idx[k]), *d = vless_dl(n);
        if (node_pinned(n)) { j->ok[k] = 1; continue; }
        want_pin(hosts, &nh, n->host);
        if (d) want_pin(hosts, &nh, d->host);
    }
    int any = 0;
    for (size_t k = 0; k < j->n; k++) any |= j->ok[k];
    if (any || !nh) publish(j);
    if (nh) transport_pin_hosts(hosts, nh, g_cn.h.resolve_ms, pinned, superseded, j);
    if (!j->published) publish(j);
    free(hosts);
}

static void pinned_extra(void *ud, const char *host, int ok) {
    (void)ud;
    if (ok <= 0) fprintf(stderr, LOG_W "inbound node host %s: not pinned\n", host);
}

static void pin_extra(char **hosts, size_t n) {
    transport_pin_hosts((const char *const *)hosts, n, g_cn.h.resolve_ms, pinned_extra, NULL, NULL);
    for (size_t i = 0; i < n; i++) free(hosts[i]);
    free(hosts);
}

void ctln_pin_extra(char **hosts, size_t n) {
    char **mine = malloc(n * sizeof(*mine));
    if (!mine) {
        for (size_t i = 0; i < n; i++) free(hosts[i]);
        return;
    }
    memcpy(mine, hosts, n * sizeof(*mine));
    pthread_mutex_lock(&g_cn.mu);
    char **all = realloc(g_cn.xh, (g_cn.xn + n) * sizeof(*all));
    if (all) {
        memcpy(all + g_cn.xn, mine, n * sizeof(*all));
        g_cn.xh = all;
        g_cn.xn += n;
        pthread_cond_signal(&g_cn.cv);
    } else {
        for (size_t i = 0; i < n; i++) free(mine[i]);
    }
    pthread_mutex_unlock(&g_cn.mu);
    free(mine);
}

static void *resolver(void *arg) {
    (void)arg;
    for (;;) {
        pthread_mutex_lock(&g_cn.mu);
        while (g_cn.taken == g_cn.posted && !g_cn.xh) pthread_cond_wait(&g_cn.cv, &g_cn.mu);
        if (g_cn.xh) {
            char **xh = g_cn.xh;
            size_t xn = g_cn.xn;
            g_cn.xh = NULL;
            g_cn.xn = 0;
            pthread_mutex_unlock(&g_cn.mu);
            pin_extra(xh, xn);
            continue;
        }
        struct ctln_job j = { g_cn.posted, g_cn.idx, g_cn.n, NULL, 0 };
        g_cn.taken = g_cn.posted;
        g_cn.idx = NULL;
        g_cn.n = 0;
        pthread_mutex_unlock(&g_cn.mu);
        j.ok = calloc(j.n ? j.n : 1, 1);
        if (j.ok) apply(&j);
        free(j.ok);
        free((void *)j.idx);
    }
    return NULL;
}

int ctln_start(const struct ctln_hooks *h) {
    g_cn.h = *h;
    pthread_attr_t a;
    pthread_attr_init(&a);
    pthread_attr_setdetachstate(&a, PTHREAD_CREATE_DETACHED);
    pthread_t t;
    int err = pthread_create(&t, &a, resolver, NULL);
    pthread_attr_destroy(&a);
    return err;
}

static void post(int *idx, size_t n) {
    pthread_mutex_lock(&g_cn.mu);
    free(g_cn.idx);
    g_cn.idx = idx;
    g_cn.n = n;
    g_cn.posted++;
    pthread_cond_signal(&g_cn.cv);
    pthread_mutex_unlock(&g_cn.mu);
}

static int command(FILE *in, char **line, size_t *cap) {
    int *idx = NULL;
    size_t n = 0, icap = 0, total = 0, lineno = 0;
    int over = 0, ended = 0;
    ssize_t len;
    while ((len = getline(line, cap, in)) > 0) {
        total += (size_t)len;
        while (len > 0 && ((*line)[len - 1] == '\n' || (*line)[len - 1] == '\r')) (*line)[--len] = 0;
        if (len == 0) { ended = 1; break; }
        lineno++;
        if (over || total > VLESS_CTL_NODES_MAX) { over = 1; continue; }
        struct vless_node nd;
        memset(&nd, 0, sizeof(nd));
        int r = vless_parse_url(*line, &nd);
        if (r < 0) {
            fprintf(stderr, LOG_W "nodes line %zu: not a vless:// or hysteria2:// link\n", lineno);
            continue;
        }
        if (r > 0) {
            fprintf(stderr, LOG_W "nodes line %zu: link cannot be used: %s\n", lineno, nd.skip_reason);
            continue;
        }
        vless_name_cut(nd.name);
        int i = nodes_add(&nd, *line);
        if (i < 0) {
            fprintf(stderr, LOG_W "nodes line %zu: no room for another node\n", lineno);
            continue;
        }
        int dup = 0;
        for (size_t k = 0; k < n && !dup; k++) dup = idx[k] == i;
        if (dup) continue;
        if (n == icap) {
            size_t nc = icap ? icap * 2 : 16;
            int *p = realloc(idx, nc * sizeof(*p));
            if (!p) {
                fprintf(stderr, LOG_W "nodes line %zu: no memory — node %s left out\n", lineno, nd.name);
                continue;
            }
            idx = p;
            icap = nc;
        }
        idx[n++] = i;
    }
    if (!ended) {
        fprintf(stderr, LOG_W "stdin closed in the middle of a nodes command — it is dropped\n");
        free(idx);
        return -1;
    }
    if (over)
        fprintf(stderr, LOG_W "nodes over %zu bytes — the lines after that are left out\n", VLESS_CTL_NODES_MAX);
    g_cn.h.ack((int)n);
    post(idx, n);
    return 0;
}

static int wake(FILE *in, char **line, size_t *cap) {
    int *idx = NULL;
    size_t n = 0, icap = 0, total = 0, lineno = 0;
    int over = 0;
    ssize_t len;
    while ((len = getline(line, cap, in)) > 0) {
        total += (size_t)len;
        while (len > 0 && ((*line)[len - 1] == '\n' || (*line)[len - 1] == '\r')) (*line)[--len] = 0;
        if (len == 0) {
            if (over)
                fprintf(stderr, LOG_W "wake over %zu bytes — the lines after that are left out\n",
                        VLESS_CTL_NODES_MAX);
            if (g_cn.h.wake) g_cn.h.wake(idx, n);
            free(idx);
            return 0;
        }
        lineno++;
        if (over || total > VLESS_CTL_NODES_MAX) { over = 1; continue; }
        int i = nodes_find(*line);
        if (i < 0) continue;
        if (n == icap) {
            size_t nc = icap ? icap * 2 : 16;
            int *p = realloc(idx, nc * sizeof(*p));
            if (!p) {
                fprintf(stderr, LOG_W "wake line %zu: no memory — left out\n", lineno);
                continue;
            }
            idx = p;
            icap = nc;
        }
        idx[n++] = i;
    }
    fprintf(stderr, LOG_W "stdin closed in the middle of a wake command — it is dropped\n");
    free(idx);
    return -1;
}

void ctln_read(FILE *in) {
    sl_set_interner(nodes_intern);
    char *line = NULL;
    size_t cap = 0;
    ssize_t len;
    while ((len = getline(&line, &cap, in)) >= 0) {
        while (len > 0 && (line[len - 1] == '\n' || line[len - 1] == '\r')) line[--len] = 0;
        if (len == 0) continue;
        int r;
        if (!strcmp(line, "nodes")) {
            r = command(in, &line, &cap);
        } else if (!strcmp(line, "map") || !strcmp(line, "mapall")) {
            r = ctlmap_read_cmd(in, &line, &cap, line[3] == 'a');
            if (r >= 0 && g_cn.h.map_ack) g_cn.h.map_ack(r);
        } else if (!strcmp(line, "inbounds")) {
            r = ctlinb_read_cmd(in, &line, &cap);
        } else if (!strcmp(line, "wake")) {
            r = wake(in, &line, &cap);
        } else if (!strcmp(line, "close")) {
            r = ctlmap_read_close(in, &line, &cap);
        } else {
            fprintf(stderr, LOG_W "stdin: unknown command — ignored\n");
            continue;
        }
        if (r < 0) break;
    }
    sl_set_interner(NULL);
    free(line);
}
