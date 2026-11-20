#define _GNU_SOURCE
#include "ctlmap.h"
#include <arpa/inet.h>
#include <pthread.h>
#include <stdlib.h>
#include <string.h>
#include <sys/eventfd.h>
#include <sys/types.h>
#include <unistd.h>

#define LOG_W "tunvless[warn]: "
#define SLOTS (1u << 17)
#define MAP_MAX 65536
#define ASK_MAX 256
#define CMD_LINES 4096
#define CLOSE_MAX 16384
#define CM_V6_CAP 4096

struct slot { uint32_t fake, real; };
struct ask { uint32_t fake; struct cm_real real; int state; };
struct pfx { uint32_t base; uint8_t len, none; uint32_t ends; };
struct six { uint32_t fake; uint8_t real[16]; };
struct pend { uint32_t a, b; uint8_t six[16]; uint8_t v6; };

static struct {
    pthread_mutex_t mu;
    uint32_t pool, mask;
    int on, wake, told_full;
    struct slot *t;
    size_t n, asks, closes, n6;
    struct ask ask[ASK_MAX];
    struct six six[CM_V6_CAP];
    struct pfx close[CLOSE_MAX];
} g = { .mu = PTHREAD_MUTEX_INITIALIZER, .wake = -1 };

static struct pend g_buf[CMD_LINES];

static uint32_t mask_of(int len) { return len ? htonl(~0u << (32 - len)) : 0; }

int ctlmap_init(uint32_t pool_be, int pool_len) {
    if (pool_len < 0 || pool_len > 32) return -1;
    pthread_mutex_lock(&g.mu);
    g.mask = mask_of(pool_len);
    g.pool = pool_be & g.mask;
    g.on = pool_len > 0;
    if (g.wake < 0) g.wake = eventfd(0, EFD_NONBLOCK | EFD_CLOEXEC);
    pthread_mutex_unlock(&g.mu);
    return 0;
}

int ctlmap_wake_fd(void) {
    pthread_mutex_lock(&g.mu);
    int fd = g.wake;
    pthread_mutex_unlock(&g.mu);
    return fd;
}

static void bump(void) {
    uint64_t one = 1;
    if (g.wake >= 0) {
        ssize_t r = write(g.wake, &one, sizeof one);
        (void)r;
    }
}

int ctlmap_in_pool(uint32_t a_be) {
    pthread_mutex_lock(&g.mu);
    int in = g.on && (a_be & g.mask) == g.pool;
    pthread_mutex_unlock(&g.mu);
    return in;
}

static size_t home(uint32_t fake) { return (ntohl(fake) * 2654435761u) >> 15; }

static size_t find(uint32_t fake, int *found) {
    size_t i = home(fake);
    while (g.t[i].fake && g.t[i].fake != fake) i = (i + 1) & (SLOTS - 1);
    *found = g.t[i].fake != 0;
    return i;
}

static void drop(size_t i) {
    for (size_t j = i;;) {
        j = (j + 1) & (SLOTS - 1);
        if (!g.t[j].fake) break;
        size_t h = home(g.t[j].fake);
        if (((j - h) & (SLOTS - 1)) >= ((j - i) & (SLOTS - 1))) {
            g.t[i] = g.t[j];
            i = j;
        }
    }
    g.t[i].fake = 0;
    g.n--;
}

static struct cm_real v4_real(uint32_t a) {
    struct cm_real r = { { 0 }, 0 };
    memcpy(r.b, &a, 4);
    return r;
}

static struct cm_real v6_real(const uint8_t *b) {
    struct cm_real r = { { 0 }, 1 };
    memcpy(r.b, b, 16);
    return r;
}

static size_t six_find(uint32_t fake) {
    size_t i = 0;
    while (i < g.n6 && g.six[i].fake != fake) i++;
    return i;
}

static void six_drop(size_t i) { g.six[i] = g.six[--g.n6]; }

static int known(uint32_t fake, struct cm_real *real) {
    int found = 0;
    if (!fake) return 0;
    if (g.t) {
        size_t i = find(fake, &found);
        if (found) *real = v4_real(g.t[i].real);
    }
    if (!found) {
        size_t j = six_find(fake);
        if (j < g.n6) {
            *real = v6_real(g.six[j].real);
            found = 1;
        }
    }
    return found;
}

int ctlmap_lookup(uint32_t fake_be, struct cm_real *real) {
    pthread_mutex_lock(&g.mu);
    int found = known(fake_be, real);
    pthread_mutex_unlock(&g.mu);
    return found ? CM_REAL : CM_NONE;
}

static struct ask *asked(uint32_t fake) {
    for (size_t i = 0; i < g.asks; i++)
        if (g.ask[i].fake == fake) return &g.ask[i];
    return NULL;
}

int ctlmap_ask(uint32_t fake_be) {
    int r = 1;
    struct cm_real real;
    pthread_mutex_lock(&g.mu);
    if (asked(fake_be)) r = 0;
    else if (g.asks == ASK_MAX) r = -1;
    else if (known(fake_be, &real)) {
        g.ask[g.asks++] = (struct ask){ fake_be, real, CM_REAL };
        r = 0;
    } else g.ask[g.asks++] = (struct ask){ fake_be, { { 0 }, 0 }, CM_PENDING };
    pthread_mutex_unlock(&g.mu);
    return r;
}

int ctlmap_answer(uint32_t fake_be, struct cm_real *real) {
    pthread_mutex_lock(&g.mu);
    struct ask *a = asked(fake_be);
    int st = a ? a->state : CM_NONE;
    if (st == CM_REAL) *real = a->real;
    pthread_mutex_unlock(&g.mu);
    return st;
}

void ctlmap_unask(uint32_t fake_be) {
    pthread_mutex_lock(&g.mu);
    struct ask *a = asked(fake_be);
    if (a) *a = g.ask[--g.asks];
    pthread_mutex_unlock(&g.mu);
}

static int addr6(const char *s, uint8_t *a) {
    char b[INET6_ADDRSTRLEN];
    size_t n = strlen(s);
    if (n >= sizeof b) return -1;
    memcpy(b, s, n + 1);
    return inet_pton(AF_INET6, b, a) == 1 ? 0 : -1;
}

static int addr(const char *s, size_t n, uint32_t *a) {
    char b[16];
    struct in_addr x;
    if (n == 0 || n >= sizeof b) return -1;
    memcpy(b, s, n);
    b[n] = 0;
    if (inet_pton(AF_INET, b, &x) != 1) return -1;
    *a = x.s_addr;
    return 0;
}

static int parse_map(const char *l, struct pend *p) {
    const char *sp = strchr(l, ' ');
    if (!sp || addr(l, (size_t)(sp - l), &p->a) != 0 || !p->a || !ctlmap_in_pool(p->a)) return -1;
    p->b = 0;
    p->v6 = strchr(sp + 1, ':') != NULL;
    if (p->v6) return addr6(sp + 1, p->six);
    if (!strcmp(sp + 1, "-")) return 0;
    return addr(sp + 1, strlen(sp + 1), &p->b) != 0 ? -1 : 0;
}

static int parse_close(const char *l, struct pend *p) {
    const char *sl = strchr(l, '/');
    if (!sl || addr(l, (size_t)(sl - l), &p->a) != 0) return -1;
    const char *d = sl + 1;
    size_t k = strlen(d);
    if (k < 1 || k > 2 || strspn(d, "0123456789") != k || atoi(d) > 32) return -1;
    p->b = (uint32_t)atoi(d);
    p->a &= mask_of((int)p->b);
    return 0;
}

static int gather(FILE *in, char **line, size_t *cap, const char *what,
                  int (*parse)(const char *, struct pend *), size_t *bad) {
    size_t n = 0, lineno = 0;
    *bad = 0;
    int ended = 0;
    ssize_t len;
    while ((len = getline(line, cap, in)) > 0) {
        while (len > 0 && ((*line)[len - 1] == '\n' || (*line)[len - 1] == '\r')) (*line)[--len] = 0;
        if (len == 0) { ended = 1; break; }
        if (++lineno > CMD_LINES) continue;
        if (parse(*line, &g_buf[n]) != 0) (*bad)++;
        else n++;
    }
    if (!ended) {
        fprintf(stderr, LOG_W "stdin closed in the middle of a %s command — it is dropped\n", what);
        return -1;
    }
    if (lineno > CMD_LINES)
        fprintf(stderr, LOG_W "a %s command over %d lines — the lines after that are left out\n",
                what, CMD_LINES);
    return (int)n;
}

static void said_bad(const char *what, size_t bad) {
    if (bad) fprintf(stderr, LOG_W "%s: %zu malformed lines left out\n", what, bad);
}

static int apply_line(const struct pend *p, int *full, int *nomem) {
    struct ask *a = asked(p->a);
    if (a) {
        a->state = p->v6 || p->b ? CM_REAL : CM_DENIED;
        a->real = p->v6 ? v6_real(p->six) : v4_real(p->b);
    }
    int found;
    size_t i;
    if (g.t && !p->b) {
        i = find(p->a, &found);
        if (found) drop(i);
    }
    size_t j = six_find(p->a);
    if (p->v6) {
        if (j == g.n6) {
            if (g.n6 >= CM_V6_CAP) return a ? 1 : -1;
            g.six[g.n6++].fake = p->a;
        }
        memcpy(g.six[j].real, p->six, 16);
        return 1;
    }
    if (j < g.n6) six_drop(j);
    if (!p->b) return 1;
    if (!g.t && !(g.t = calloc(SLOTS, sizeof(*g.t)))) {
        *nomem = 1;
        return 0;
    }
    i = find(p->a, &found);
    if (!found && g.n >= MAP_MAX) {
        if (a) return 1;
        *full = 1;
        return 0;
    }
    if (!found) g.n++;
    g.t[i] = (struct slot){ p->a, p->b };
    return 1;
}

int ctlmap_read_cmd(FILE *in, char **line, size_t *cap, int all) {
    const char *what = all ? "mapall" : "map";
    size_t bad;
    int n = gather(in, line, cap, what, parse_map, &bad);
    if (n < 0) return -1;
    int took = 0, full = 0, warn = 0, nomem = 0;
    pthread_mutex_lock(&g.mu);
    if (all && g.t) {
        memset(g.t, 0, SLOTS * sizeof(*g.t));
        g.n = 0;
    }
    if (all) g.n6 = 0;
    for (int i = 0; i < n; i++) {
        int r = apply_line(&g_buf[i], &full, &nomem);
        if (r > 0) took++;
        else if (r < 0) bad++;
    }
    if (full && !g.told_full) g.told_full = warn = 1;
    bump();
    pthread_mutex_unlock(&g.mu);
    said_bad(what, bad);
    if (warn) fprintf(stderr, LOG_W "the address map is full (%d), later lines are dropped\n", MAP_MAX);
    if (nomem) fprintf(stderr, LOG_W "no memory for the address map — %s lines left out\n", what);
    return took;
}

int ctlmap_read_close(FILE *in, char **line, size_t *cap) {
    size_t bad;
    int n = gather(in, line, cap, "close", parse_close, &bad);
    if (n < 0) return -1;
    said_bad("close", bad);
    int over = 0;
    pthread_mutex_lock(&g.mu);
    for (int i = 0; i < n; i++) {
        if (g.closes == CLOSE_MAX) {
            uint32_t ends = 0;
            for (size_t j = 0; j < g.closes; j++) ends += g.close[j].ends;
            g.close[0] = (struct pfx){ 0, 0, 0, ends };
            g.closes = 1;
            over = 1;
        }
        g.close[g.closes++] = (struct pfx){ g_buf[i].a, (uint8_t)g_buf[i].b, 0, 0 };
    }
    if (g.closes > 0) g.close[g.closes - 1].ends++;
    else g.close[g.closes++] = (struct pfx){ 0, 0, 1, 1 };
    bump();
    pthread_mutex_unlock(&g.mu);
    if (over) fprintf(stderr, LOG_W "%d prefixes wait to be closed — every connection is closed\n", CLOSE_MAX);
    return n;
}

size_t ctlmap_take_closes(uint32_t *base_be, uint8_t *len, size_t cap, int *ends) {
    pthread_mutex_lock(&g.mu);
    size_t k = 0, i = 0;
    uint32_t e = 0;
    while (i < g.closes) {
        const struct pfx *p = &g.close[i];
        if (!p->none) {
            if (k == cap) break;
            base_be[k] = p->base;
            len[k] = p->len;
            k++;
        }
        i++;
        if (p->ends) {
            e = p->ends;
            break;
        }
    }
    memmove(g.close, g.close + i, (g.closes - i) * sizeof(g.close[0]));
    g.closes -= i;
    pthread_mutex_unlock(&g.mu);
    if (ends) *ends = (int)e;
    return k;
}
