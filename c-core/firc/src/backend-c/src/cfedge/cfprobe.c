#include "firc/cfedge.h"

#include <arpa/inet.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/id.h"
#include "firc/sub_fetch.h"

#define STACK_BYTES (256 * 1024)
#define POST_RETRY_NS 50000000L

static bool discard(void *ud, const uint8_t *data, size_t len) {
    (void)ud;
    (void)data;
    (void)len;
    return false;
}

static bool keep_going(void *ud, size_t bytes, size_t total) {
    (void)bytes;
    (void)total;
    _Atomic bool *stop = ud;
    return stop == NULL || !atomic_load(stop);
}

bool firc_cfprobe_tls(void *ud, const char *host, const firc_ip_t *addr, uint32_t mark, double *secs) {
    if (!keep_going(ud, 0, 0) || addr == NULL || (addr->len != 4 && addr->len != 16)) { return false; }
    char a[INET6_ADDRSTRLEN];
    if (inet_ntop(addr->len == 4 ? AF_INET : AF_INET6, addr->b, a, sizeof(a)) == NULL) { return false; }
    char url[300], pin[320];
    snprintf(url, sizeof(url), "https://%s/", host);
    snprintf(pin, sizeof(pin), addr->len == 4 ? "%s:443:%s" : "%s:443:[%s]", host, a);
    firc_sub_fetch_info_t info;
    memset(&info, 0, sizeof(info));
    firc_sub_fetch_opts_t o = {.mark = mark,
                               .timeout_s = FIRC_CFPROBE_TIMEOUT_S,
                               .no_redirects = true,
                               .resolve = pin,
                               .progress = keep_going,
                               .progress_ud = ud,
                               .info = &info};
    long status = 0;
    (void)firc_sub_fetch_to(url, &o, discard, NULL, &status);
    if (!info.tls_done) { return false; }
    *secs = info.appconnect_s;
    return true;
}

typedef struct {
    firc_cfprobe_fn probe;
    void *ud;
    const firc_fakeip_cf_cand_t *c;
    uint32_t mark;
    firc_cfprobe_t *out;
} one_t;

static double median(double *v, size_t n) {
    for (size_t i = 1; i < n; i++) {
        for (size_t k = i; k > 0 && v[k - 1] > v[k]; k--) {
            double t = v[k];
            v[k] = v[k - 1];
            v[k - 1] = t;
        }
    }
    if (n == 0) { return 0; }
    return n % 2 ? v[n / 2] : (v[n / 2 - 1] + v[n / 2]) / 2;
}

static void *run_one(void *p) {
    one_t *o = p;
    double t[FIRC_CFPROBE_TRIES];
    size_t ok = 0;
    for (int i = 0; i < FIRC_CFPROBE_TRIES; i++) {
        double s = 0;
        if (o->probe(o->ud, o->c->name, &o->c->addr, o->mark, &s)) { t[ok++] = s; }
    }
    o->out->addr = o->c->addr;
    memcpy(o->out->name, o->c->name, sizeof(o->out->name));
    o->out->ok = ok >= 2;
    o->out->median_s = median(t, ok);
    return NULL;
}

void firc_cfprobe_run(firc_cfprobe_fn probe, void *ud, const firc_fakeip_cf_cand_t *c, size_t n, uint32_t mark,
                      firc_cfprobe_t *out) {
    one_t *one = calloc(n, sizeof(*one));
    pthread_t *th = calloc(n, sizeof(*th));
    bool *started = calloc(n, sizeof(*started));
    pthread_attr_t attr;
    bool attr_ok = pthread_attr_init(&attr) == 0;
    if (attr_ok) { (void)pthread_attr_setstacksize(&attr, STACK_BYTES); }
    for (size_t i = 0; i < n; i++) {
        one_t local = {probe, ud, &c[i], mark, &out[i]};
        if (one == NULL || th == NULL || started == NULL) {
            run_one(&local);
            continue;
        }
        one[i] = local;
        started[i] = pthread_create(&th[i], attr_ok ? &attr : NULL, run_one, &one[i]) == 0;
        if (!started[i]) { run_one(&one[i]); }
    }
    for (size_t i = 0; started != NULL && th != NULL && i < n; i++) {
        if (started[i]) { pthread_join(th[i], NULL); }
    }
    if (attr_ok) { pthread_attr_destroy(&attr); }
    free(one);
    free(th);
    free(started);
}

typedef struct job {
    struct job *next;
    struct firc_cfjobs *owner;
    char gid[FIRC_ID_STR_LEN];
    unsigned fam;
    uint64_t tag;
    uint32_t mark;
    size_t n;
    firc_fakeip_cf_cand_t c[FIRC_CFPROBE_MAX];
    firc_cfprobe_t r[FIRC_CFPROBE_MAX];
    pthread_t th;
    bool joined;
    bool posted;
} job_t;

struct firc_cfjobs {
    firc_loop_t *loop;
    firc_cfprobe_fn probe;
    void *probe_ud;
    firc_cfjobs_done_fn done;
    void *ud;
    _Atomic bool stop;
    job_t *head;
    size_t queued;
    bool dead;
};

firc_cfjobs_t *firc_cfjobs_new(firc_loop_t *loop, firc_cfjobs_done_fn done, void *ud) {
    firc_cfjobs_t *j = calloc(1, sizeof(*j));
    if (j == NULL) { return NULL; }
    j->loop = loop;
    j->done = done;
    j->ud = ud;
    atomic_init(&j->stop, false);
    firc_cfjobs_set_probe(j, NULL, NULL);
    return j;
}

void firc_cfjobs_set_probe(firc_cfjobs_t *j, firc_cfprobe_fn probe, void *ud) {
    if (j == NULL) { return; }
    j->probe = probe != NULL ? probe : firc_cfprobe_tls;
    j->probe_ud = probe != NULL ? ud : (void *)&j->stop;
}

static bool same(const job_t *x, const char *gid, unsigned fam) {
    return x->fam == fam && strncmp(x->gid, gid, sizeof(x->gid)) == 0;
}

bool firc_cfjobs_busy(const firc_cfjobs_t *j, const char *gid, unsigned fam) {
    for (const job_t *x = j != NULL ? j->head : NULL; x != NULL; x = x->next) {
        if (same(x, gid, fam)) { return true; }
    }
    return false;
}

static void unlink_job(firc_cfjobs_t *j, job_t *x) {
    for (job_t **pp = &j->head; *pp != NULL; pp = &(*pp)->next) {
        if (*pp == x) {
            *pp = x->next;
            return;
        }
    }
}

static void settle(job_t *x, bool deliver) {
    firc_cfjobs_t *j = x->owner;
    if (!x->joined) {
        pthread_join(x->th, NULL);
        x->joined = true;
    }
    if (j->dead) {
        free(x);
        if (--j->queued == 0) { free(j); }
        return;
    }
    unlink_job(j, x);
    if (deliver) { j->done(j->ud, x->gid, x->fam, x->tag, x->r, x->n); }
    free(x);
}

static void job_posted(firc_loop_t *loop, void *ud) {
    (void)loop;
    settle(ud, true);
}

static void job_dropped(void *ud) {
    settle(ud, false);
}

static void *job_main(void *p) {
    job_t *x = p;
    firc_cfjobs_t *j = x->owner;
    firc_cfprobe_run(j->probe, j->probe_ud, x->c, x->n, x->mark, x->r);
    while (!atomic_load(&j->stop)) {
        x->posted = true;
        if (firc_loop_post_with_drop(j->loop, job_posted, x, job_dropped) == FIRC_OK) { return NULL; }
        x->posted = false;
        struct timespec nap = {0, POST_RETRY_NS};
        nanosleep(&nap, NULL);
    }
    return NULL;
}

firc_err_t firc_cfjobs_start(firc_cfjobs_t *j, const char *gid, unsigned fam, uint64_t tag, uint32_t mark,
                             const firc_fakeip_cf_cand_t *c, size_t n) {
    if (j == NULL || gid == NULL || n == 0) { return FIRC_ERR_INVAL; }
    if (j->loop == NULL) { return FIRC_ERR_STATE; }
    if (firc_cfjobs_busy(j, gid, fam)) { return FIRC_ERR_STATE; }
    if (n > FIRC_CFPROBE_MAX) { n = FIRC_CFPROBE_MAX; }
    job_t *x = calloc(1, sizeof(*x));
    if (x == NULL) { return FIRC_ERR_NOMEM; }
    x->owner = j;
    snprintf(x->gid, sizeof(x->gid), "%s", gid);
    x->fam = fam;
    x->tag = tag;
    x->mark = mark;
    x->n = n;
    memcpy(x->c, c, n * sizeof(*c));
    pthread_attr_t attr;
    bool attr_ok = pthread_attr_init(&attr) == 0;
    if (attr_ok) { (void)pthread_attr_setstacksize(&attr, STACK_BYTES); }
    x->next = j->head;
    j->head = x;
    bool ok = pthread_create(&x->th, attr_ok ? &attr : NULL, job_main, x) == 0;
    if (attr_ok) { pthread_attr_destroy(&attr); }
    if (!ok) {
        j->head = x->next;
        free(x);
        return FIRC_ERR_SYS;
    }
    return FIRC_OK;
}

void firc_cfjobs_free(firc_cfjobs_t *j) {
    if (j == NULL) { return; }
    atomic_store(&j->stop, true);
    size_t queued = 0;
    job_t *x = j->head;
    j->head = NULL;
    while (x != NULL) {
        job_t *next = x->next;
        if (!x->joined) {
            pthread_join(x->th, NULL);
            x->joined = true;
        }
        if (x->posted) {
            queued++;
        } else {
            free(x);
        }
        x = next;
    }
    j->dead = true;
    j->queued = queued;
    if (queued == 0) { free(j); }
}
