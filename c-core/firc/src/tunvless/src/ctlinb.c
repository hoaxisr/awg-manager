#define _GNU_SOURCE
#include "ctlinb.h"
#include "control.h"
#include "ctlnodes.h"
#include "nodes.h"
#include "vless.h"
#include "vlink.h"
#include <arpa/inet.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <unistd.h>

#define LOG_W "tunvless[warn]: "
#define IB_LINES 4096

static struct {
    pthread_mutex_t mu;
    struct ib_cfg set[IB_MAX];
    size_t n;
    unsigned gen;
    int wake;
} g = { .mu = PTHREAD_MUTEX_INITIALIZER, .wake = -1 };

int ctlinb_init(void) {
    pthread_mutex_lock(&g.mu);
    if (g.wake < 0) g.wake = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    int fd = g.wake;
    pthread_mutex_unlock(&g.mu);
    return fd < 0 ? -1 : 0;
}

int ctlinb_wake_fd(void) {
    pthread_mutex_lock(&g.mu);
    int fd = g.wake;
    pthread_mutex_unlock(&g.mu);
    return fd;
}

size_t ctlinb_take(struct ib_cfg *out, size_t cap, unsigned *gen) {
    pthread_mutex_lock(&g.mu);
    size_t n = g.n < cap ? g.n : cap;
    memcpy(out, g.set, n * sizeof(*out));
    if (gen) *gen = g.gen;
    pthread_mutex_unlock(&g.mu);
    return n;
}

static int hexv(int c) {
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static int decode(const char *s, size_t n, char *out) {
    size_t o = 0;
    for (size_t i = 0; i < n; i++) {
        int c = (unsigned char)s[i];
        if (c == '%') {
            int hi = i + 1 < n ? hexv(s[i + 1]) : -1, lo = i + 2 < n ? hexv(s[i + 2]) : -1;
            if (hi < 0 || lo < 0) return -1;
            c = hi * 16 + lo;
            i += 2;
        }
        if (c == 0 || o + 1 >= 256) return -1;
        out[o++] = (char)c;
    }
    out[o] = 0;
    return 0;
}

static int field(const char *s, size_t n, char *out) {
    if (n == 1 && s[0] == '-') {
        out[0] = 0;
        return 0;
    }
    return decode(s, n, out);
}

static int endpoint(const char *s, size_t n, struct ib_cfg *c) {
    const char *colon = memchr(s, ':', n);
    char a[16];
    struct in_addr x;
    if (!colon || (size_t)(colon - s) >= sizeof a || colon - s == 0) return -1;
    memcpy(a, s, (size_t)(colon - s));
    a[colon - s] = 0;
    if (inet_pton(AF_INET, a, &x) != 1 || !x.s_addr) return -1;
    const char *p = colon + 1;
    size_t k = n - (size_t)(p - s);
    if (k < 1 || k > 5 || strspn(p, "0123456789") < k) return -1;
    long port = strtol(p, NULL, 10);
    if (port < 1 || port > 65535) return -1;
    c->addr = x.s_addr;
    c->port = (uint16_t)port;
    return 0;
}

static int parse_line(char *l, struct ib_cfg *c, char **host, char **dl_host) {
    char *f[4];
    size_t len[4];
    char *p = l;
    for (int i = 0; i < 3; i++) {
        char *sp = strchr(p, ' ');
        if (!sp) return -1;
        f[i] = p;
        len[i] = (size_t)(sp - p);
        p = sp + 1;
    }
    f[3] = p;
    len[3] = strlen(p);
    memset(c, 0, sizeof(*c));
    if (endpoint(f[0], len[0], c) != 0) return -1;
    if (field(f[1], len[1], c->user) != 0 || field(f[2], len[2], c->pass) != 0) return -1;
    *host = NULL;
    *dl_host = NULL;
    if (len[3] == 1 && f[3][0] == '-') {
        c->node = -1;
        return 0;
    }
    if (len[3] == 1 && f[3][0] == '*') {
        c->node = IB_NODE_POOL;
        return 0;
    }
    struct vless_node nd;
    memset(&nd, 0, sizeof(nd));
    if (vless_parse_url(f[3], &nd) != 0) return -2;
    vless_name_cut(nd.name);
    int i = nodes_add(&nd, f[3]);
    if (i < 0) return -3;
    c->node = i;
    *host = strdup(nd.host);
    if (vless_dl(&nd)) *dl_host = strdup(vless_dl(&nd)->host);
    return 0;
}

int ctlinb_read_cmd(FILE *in, char **line, size_t *cap) {
    struct ib_cfg buf[IB_MAX];
    char *hosts[2 * IB_MAX];
    size_t n = 0, nh = 0, lineno = 0;
    int ended = 0;
    ssize_t len;
    while ((len = getline(line, cap, in)) > 0) {
        while (len > 0 && ((*line)[len - 1] == '\n' || (*line)[len - 1] == '\r')) (*line)[--len] = 0;
        if (len == 0) { ended = 1; break; }
        if (++lineno > IB_LINES) continue;
        if (n == IB_MAX) {
            fprintf(stderr, LOG_W "inbounds line %zu: more than %d inbounds — left out\n", lineno, IB_MAX);
            continue;
        }
        char *got[2];
        int r = parse_line(*line, &buf[n], &got[0], &got[1]);
        if (r == -2) fprintf(stderr, LOG_W "inbounds line %zu: not a usable node link\n", lineno);
        else if (r == -3) fprintf(stderr, LOG_W "inbounds line %zu: no room for another node\n", lineno);
        else if (r != 0) fprintf(stderr, LOG_W "inbounds line %zu: malformed line\n", lineno);
        if (r != 0) continue;
        n++;
        for (int h = 0; h < 2; h++) {
            char *host = got[h];
            if (!host) continue;
            int dup = 0;
            for (size_t k = 0; k < nh && !dup; k++) dup = !strcmp(hosts[k], host);
            if (dup) free(host);
            else hosts[nh++] = host;
        }
    }
    if (!ended) {
        fprintf(stderr, LOG_W "stdin closed in the middle of an inbounds command — it is dropped\n");
        for (size_t k = 0; k < nh; k++) free(hosts[k]);
        return -1;
    }
    if (lineno > IB_LINES)
        fprintf(stderr, LOG_W "an inbounds command over %d lines — the lines after that are left out\n",
                IB_LINES);
    pthread_mutex_lock(&g.mu);
    memcpy(g.set, buf, n * sizeof(buf[0]));
    g.n = n;
    g.gen++;
    if (g.wake >= 0) {
        uint64_t one = 1;
        ssize_t w = write(g.wake, &one, sizeof one);
        (void)w;
    }
    pthread_mutex_unlock(&g.mu);
    if (nh) ctln_pin_extra(hosts, nh);
    ctl_emit_inbounds((long)n);
    return (int)n;
}
