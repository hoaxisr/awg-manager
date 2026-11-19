#include "firc/tunsubs.h"

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#include "firc/atomic_write.h"
#include "firc/hash.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/sub_fetch.h"
#include "firc/subhdr.h"
#include "firc/subinfo.h"

#define RETRY_MS 60000
#define IDENT_MAX 32

typedef struct {
    char id[16];
    uint32_t mark;
    uint64_t hsig;
} user_t;

typedef struct {
    char *url;
    char key[17];
    char **lines;
    firc_subhdr_pair_t *own;
    size_t n_own;
    int interval_s;
    user_t users[FIRC_TUN_MAX];
    size_t n_users;
    char *body;
    size_t len;
    uint8_t sha[FIRC_SHA256_DIGEST_LEN];
    bool have;
    uint8_t cached_sha[FIRC_SHA256_DIGEST_LEN];
    bool cached;
    int64_t last_ok_ms, last_try_ms;
    int64_t done_mono;
    char error[160];
    firc_logrep_t failing;
    bool fetching;
    size_t nodes;
    firc_subinfo_t info;
    bool kept, was_wanted;
} entry_t;

typedef struct result {
    firc_tunsubs_t *s;
    char *url;
    char key[17];
    firc_err_t err;
    bool no_nodes;
    bool placeholder;
    char said[FIRC_TUN_SAID_MAX + 1];
    int status;
    char *body;
    size_t len;
    size_t nodes;
    firc_subinfo_t info;
    uint8_t sha[FIRC_SHA256_DIGEST_LEN];
    bool wrote;
    firc_err_t write_err;
    int64_t at_ms;
} result_t;

typedef struct job {
    struct job *next;
    uint32_t mark;
    char **lines;
    char path[512];
    bool cached;
    uint8_t cached_sha[FIRC_SHA256_DIGEST_LEN];
    result_t *r;
} job_t;

struct firc_tunsubs {
    firc_loop_t *loop;
    char dir[448];
    void (*changed)(void *ud);
    void *ud;
    firc_subhdr_set_t defaults;
    entry_t **v;
    size_t n;
    firc_tun_body_t *bodies;
    size_t n_bodies;
    int64_t offset_ms;
    pthread_mutex_t mu;
    pthread_cond_t cv;
    job_t *head, *tail;
    bool stop;
    atomic_bool abort;
    pthread_t thread;
    size_t posted;
    bool dead;
};

static int64_t wall_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_REALTIME, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static int64_t mono_ms(const firc_tunsubs_t *s)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000 + s->offset_ms;
}

static void ident_of(const firc_tunnel_t *t, char out[IDENT_MAX])
{
    switch (t->uplink) {
    case FIRC_UPLINK_IFACE:
        snprintf(out, IDENT_MAX, "iface:%s", t->uplink_ref);
        break;
    case FIRC_UPLINK_TUNNEL:
        snprintf(out, IDENT_MAX, "tunnel:%s", t->uplink_ref);
        break;
    case FIRC_UPLINK_AUTO:
    default:
        snprintf(out, IDENT_MAX, "auto");
        break;
    }
}

static void key_of(const char *ident, const firc_tun_sub_t *sub, char key[17])
{
    firc_sha256_ctx_t c;
    firc_sha256_init(&c);
    firc_sha256_update(&c, (const uint8_t *)sub->url, strlen(sub->url));
    firc_sha256_update(&c, (const uint8_t *)"\n", 1);
    firc_sha256_update(&c, (const uint8_t *)ident, strlen(ident));
    for (size_t i = 0; i < sub->n_headers; i++) {
        firc_sha256_update(&c, (const uint8_t *)"\n", 1);
        firc_sha256_update(&c, (const uint8_t *)sub->headers[i].name, strlen(sub->headers[i].name));
        firc_sha256_update(&c, (const uint8_t *)": ", 2);
        firc_sha256_update(&c, (const uint8_t *)sub->headers[i].value, strlen(sub->headers[i].value));
    }
    uint8_t d[FIRC_SHA256_DIGEST_LEN];
    firc_sha256_final(&c, d);
    static const char hex[] = "0123456789abcdef";
    for (size_t i = 0; i < 8; i++) {
        key[2 * i] = hex[d[i] >> 4];
        key[2 * i + 1] = hex[d[i] & 15];
    }
    key[16] = 0;
}

static void pair_key(const firc_tunnel_t *uplink_of, const firc_tun_sub_t *sub, char key[17])
{
    char ident[IDENT_MAX];
    ident_of(uplink_of, ident);
    key_of(ident, sub, key);
}

static void mkdirs(const char *path)
{
    char p[448];
    snprintf(p, sizeof p, "%s", path);
    for (char *q = p + 1; *q != 0; q++) {
        if (*q == '/') {
            *q = 0;
            (void)mkdir(p, 0700);
            *q = '/';
        }
    }
    if (mkdir(p, 0700) == 0) {
        return;
    }
    struct stat st;
    if (errno != EEXIST || stat(p, &st) != 0 || !S_ISDIR(st.st_mode)) {
        FIRC_WARN("tunnel subscriptions: cache directory %s not created: %s", p, strerror(errno));
        return;
    }
    if ((st.st_mode & 077) != 0 && chmod(p, 0700) != 0) {
        FIRC_WARN("tunnel subscriptions: cache directory %s not made private: %s", p, strerror(errno));
    }
}

static size_t count_nodes(const char *url, const char *body, size_t len, bool *placeholder, char *said, size_t cap)
{
    firc_tun_src_t src;
    memset(&src, 0, sizeof src);
    src.kind = FIRC_TUN_SRC_SUB;
    snprintf(src.id, sizeof src.id, "00000000");
    src.sub.url = (char *)url;
    firc_tunnel_t t;
    memset(&t, 0, sizeof t);
    t.src = &src;
    t.n_src = 1;
    t.insecure = true;
    firc_tun_body_t b = {url, 0, body, len, 0};
    firc_tun_nodes_t nodes;
    size_t n = 0;
    if (firc_tun_nodes_build(&t, 0, &b, 1, &nodes) == FIRC_OK) {
        n = nodes.total;
        if (placeholder != NULL) {
            *placeholder = nodes.seen > 0 && nodes.sinks == nodes.seen;
            snprintf(said, cap, "%s", nodes.sink_name);
        }
        firc_tun_nodes_free(&nodes);
    }
    return n;
}

static void load_info(const firc_tunsubs_t *s, entry_t *e)
{
    char path[512];
    snprintf(path, sizeof path, "%s/%s.info", s->dir, e->key);
    int fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) {
        return;
    }
    char line[FIRC_SUBINFO_LINE_MAX + 1];
    ssize_t n = read(fd, line, sizeof line - 1);
    close(fd);
    if (n <= 0) {
        return;
    }
    line[n] = 0;
    line[strcspn(line, "\r\n")] = 0;
    (void)firc_subinfo_parse(line, &e->info);
}

static void save_info(const char *body_path, const firc_subinfo_t *info)
{
    char path[520];
    snprintf(path, sizeof path, "%s.info", body_path);
    if (!info->have) {
        (void)unlink(path);
        return;
    }
    char line[FIRC_SUBINFO_LINE_MAX + 2];
    firc_subinfo_format(info, line, sizeof line - 1);
    strcat(line, "\n");
    if (firc_atomic_write(path, line, strlen(line)) != FIRC_OK) {
        FIRC_WARN("tunnel subscriptions: traffic and expiry not kept in %s", path);
    }
}

static void load_cache(firc_tunsubs_t *s, entry_t *e)
{
    load_info(s, e);
    char path[512];
    snprintf(path, sizeof path, "%s/%s", s->dir, e->key);
    int fd = open(path, O_RDONLY | O_CLOEXEC | O_NOFOLLOW);
    if (fd < 0) {
        if (errno != ENOENT) {
            FIRC_WARN("tunnel subscriptions: cache file %s not opened: %s", e->key, strerror(errno));
        }
        return;
    }
    struct stat st;
    if (fstat(fd, &st) != 0 || !S_ISREG(st.st_mode) || (uint64_t)st.st_size > FIRC_SUB_FETCH_MAX_BODY_BYTES) {
        FIRC_WARN("tunnel subscriptions: cache file %s refused (not a file, or over %zu bytes)", e->key,
                  FIRC_SUB_FETCH_MAX_BODY_BYTES);
        close(fd);
        return;
    }
    size_t len = (size_t)st.st_size;
    char *body = malloc(len + 1);
    size_t got = 0;
    while (body != NULL && got < len) {
        ssize_t r = read(fd, body + got, len - got);
        if (r < 0 && errno == EINTR) {
            continue;
        }
        if (r <= 0) {
            break;
        }
        got += (size_t)r;
    }
    close(fd);
    if (body == NULL || got != len || memchr(body, 0, len) != NULL) {
        FIRC_WARN("tunnel subscriptions: cache file %s not read", e->key);
        free(body);
        return;
    }
    body[len] = 0;
    e->body = body;
    e->len = len;
    e->have = true;
    firc_sha256((const uint8_t *)body, len, e->sha);
    memcpy(e->cached_sha, e->sha, sizeof e->sha);
    e->cached = true;
    e->nodes = count_nodes(e->url, body, len, NULL, NULL, 0);
}

static void post_result(firc_tunsubs_t *s, result_t *r);

static bool keep_going(void *ud, size_t bytes, size_t total)
{
    (void)bytes;
    (void)total;
    return !atomic_load(&((firc_tunsubs_t *)ud)->abort);
}

static void on_header(void *ud, const char *name, const char *value)
{
    firc_subinfo_t info;
    if (strcasecmp(name, FIRC_SUBINFO_HEADER) == 0 && firc_subinfo_parse(value, &info)) {
        ((result_t *)ud)->info = info;
    }
}

static void run_job(firc_tunsubs_t *s, job_t *j)
{
    result_t *r = j->r;
    j->r = NULL;
    r->err = firc_sub_fetch_list_mark_hdr(r->url, j->mark, (const char *const *)j->lines, &r->body, &r->len,
                                          &r->status, keep_going, s, on_header, r);
    r->at_ms = wall_ms();
    if (r->err == FIRC_OK && !r->info.have) {
        (void)firc_subinfo_from_body(r->body, r->len, &r->info);
    }
    if (r->err == FIRC_OK) {
        r->nodes = count_nodes(r->url, r->body, r->len, &r->placeholder, r->said, sizeof r->said);
        if (r->placeholder) {
            r->err = FIRC_ERR_PROTO;
        } else if (r->nodes == 0) {
            r->err = FIRC_ERR_PROTO;
            r->no_nodes = true;
        }
    }
    if (r->err == FIRC_OK) {
        firc_sha256((const uint8_t *)r->body, r->len, r->sha);
        if (!j->cached || memcmp(j->cached_sha, r->sha, sizeof r->sha) != 0) {
            r->write_err = firc_atomic_write(j->path, r->body, r->len);
            r->wrote = true;
        }
    }
    if (r->err == FIRC_OK || r->info.have) {
        save_info(j->path, &r->info);
    }
    post_result(s, r);
}

static void free_result(result_t *r)
{
    if (r == NULL) {
        return;
    }
    free(r->url);
    free(r->body);
    free(r);
}

static void free_job(job_t *j)
{
    free_result(j->r);
    firc_subhdr_lines_free(j->lines);
    free(j);
}

static void *worker(void *ud)
{
    firc_tunsubs_t *s = ud;
    for (;;) {
        pthread_mutex_lock(&s->mu);
        while (!s->stop && s->head == NULL) {
            pthread_cond_wait(&s->cv, &s->mu);
        }
        if (s->stop) {
            pthread_mutex_unlock(&s->mu);
            return NULL;
        }
        job_t *j = s->head;
        s->head = j->next;
        if (s->head == NULL) {
            s->tail = NULL;
        }
        pthread_mutex_unlock(&s->mu);
        run_job(s, j);
        free_job(j);
    }
}

static void destroy(firc_tunsubs_t *s)
{
    pthread_cond_destroy(&s->cv);
    pthread_mutex_destroy(&s->mu);
    free(s);
}

static bool settle(firc_tunsubs_t *s, bool *last)
{
    pthread_mutex_lock(&s->mu);
    s->posted--;
    bool dead = s->dead;
    *last = dead && s->posted == 0;
    pthread_mutex_unlock(&s->mu);
    return dead;
}

static void drop_result(void *ud)
{
    result_t *r = ud;
    firc_tunsubs_t *s = r->s;
    free_result(r);
    bool last = false;
    if (settle(s, &last) && last) {
        destroy(s);
    }
}

static entry_t *find(const firc_tunsubs_t *s, const char *url, const char *key)
{
    for (size_t i = 0; i < s->n; i++) {
        if (strcmp(s->v[i]->key, key) == 0 && strcmp(s->v[i]->url, url) == 0) {
            return s->v[i];
        }
    }
    return NULL;
}

static bool user_seen(const entry_t *e, size_t upto, uint32_t mark, uint64_t hsig)
{
    for (size_t u = 0; u < upto; u++) {
        if (e->users[u].mark == mark && e->users[u].hsig == hsig) {
            return true;
        }
    }
    return false;
}

static void rebuild_bodies(firc_tunsubs_t *s)
{
    size_t k = 0;
    for (size_t i = 0; i < s->n; i++) {
        entry_t *e = s->v[i];
        for (size_t u = 0; e->have && u < e->n_users; u++) {
            if (user_seen(e, u, e->users[u].mark, e->users[u].hsig)) {
                continue;
            }
            s->bodies[k] = (firc_tun_body_t){
                .url = e->url, .mark = e->users[u].mark, .body = e->body, .len = e->len, .hsig = e->users[u].hsig};
            k++;
        }
    }
    s->n_bodies = k;
}

static void describe(const result_t *r, char *buf, size_t cap)
{
    if (r->placeholder) {
        snprintf(buf, cap, "the server answered: %.120s", r->said);
    } else if (r->no_nodes) {
        snprintf(buf, cap, "no nodes in the answer");
    } else if (r->err == FIRC_ERR_PROTO && r->status != 0 && (r->status < 200 || r->status >= 300)) {
        snprintf(buf, cap, "HTTP %d", r->status);
    } else if (r->err == FIRC_ERR_SYS) {
        snprintf(buf, cap, "the uplink mark could not be set");
    } else if (r->err == FIRC_ERR_LIMIT) {
        snprintf(buf, cap, "over %zu bytes or too many redirects", FIRC_SUB_FETCH_MAX_BODY_BYTES);
    } else if (r->err == FIRC_ERR_IO) {
        snprintf(buf, cap, "no answer");
    } else {
        snprintf(buf, cap, "%s", firc_err_str(r->err));
    }
}

static void on_result(firc_loop_t *loop, void *ud)
{
    (void)loop;
    result_t *r = ud;
    firc_tunsubs_t *s = r->s;
    bool last = false;
    if (settle(s, &last)) {
        free_result(r);
        if (last) {
            destroy(s);
        }
        return;
    }
    entry_t *e = find(s, r->url, r->key);
    if (e == NULL) {
        free_result(r);
        return;
    }
    char shown[160];
    firc_sub_url_redact(e->url, shown, sizeof shown);
    e->fetching = false;
    e->last_try_ms = r->at_ms;
    e->done_mono = mono_ms(s);
    if (r->err != FIRC_OK) {
        if (r->info.have) {
            e->info = r->info;
        }
        describe(r, e->error, sizeof e->error);
        firc_logrep_fail(&e->failing, FIRC_LOG_WARN, NULL, "tunnel subscription %s: fetch failed: %s", shown, e->error);
        free_result(r);
        return;
    }
    e->error[0] = 0;
    (void)firc_logrep_ok(&e->failing, NULL, "tunnel subscription %s", shown);
    e->last_ok_ms = r->at_ms;
    e->info = r->info;
    if (r->wrote && r->write_err != FIRC_OK) {
        FIRC_WARN("tunnel subscription %s: cache not written: %s", shown, firc_err_str(r->write_err));
    } else if (r->wrote) {
        memcpy(e->cached_sha, r->sha, sizeof r->sha);
        e->cached = true;
    }
    bool changed = !e->have || memcmp(e->sha, r->sha, sizeof e->sha) != 0;
    if (!changed) {
        free_result(r);
        return;
    }
    free(e->body);
    e->body = r->body;
    e->len = r->len;
    r->body = NULL;
    memcpy(e->sha, r->sha, sizeof e->sha);
    e->have = true;
    e->nodes = r->nodes;
    free_result(r);
    rebuild_bodies(s);
    FIRC_INFO("tunnel subscription %s: updated, %zu nodes", shown, e->nodes);
    if (s->changed != NULL) {
        s->changed(s->ud);
    }
}

static void post_result(firc_tunsubs_t *s, result_t *r)
{
    pthread_mutex_lock(&s->mu);
    s->posted++;
    pthread_mutex_unlock(&s->mu);
    while (firc_loop_post_with_drop(s->loop, on_result, r, drop_result) != FIRC_OK) {
        if (atomic_load(&s->abort)) {
            pthread_mutex_lock(&s->mu);
            s->posted--;
            pthread_mutex_unlock(&s->mu);
            free_result(r);
            return;
        }
        struct timespec ts = {0, 50 * 1000000L};
        nanosleep(&ts, NULL);
    }
}

firc_tunsubs_t *firc_tunsubs_new(firc_loop_t *loop, const char *cache_dir, void (*changed)(void *ud), void *ud)
{
    firc_tunsubs_t *s = calloc(1, sizeof *s);
    if (s == NULL) {
        return NULL;
    }
    s->loop = loop;
    snprintf(s->dir, sizeof s->dir, "%s", cache_dir);
    s->changed = changed;
    s->ud = ud;
    firc_subhdr_defaults(NULL, NULL, NULL, &s->defaults);
    atomic_init(&s->abort, false);
    if (pthread_mutex_init(&s->mu, NULL) != 0) {
        free(s);
        return NULL;
    }
    if (pthread_cond_init(&s->cv, NULL) != 0) {
        pthread_mutex_destroy(&s->mu);
        free(s);
        return NULL;
    }
    if (pthread_create(&s->thread, NULL, worker, s) != 0) {
        destroy(s);
        return NULL;
    }
    mkdirs(s->dir);
    return s;
}

static void enqueue(firc_tunsubs_t *s, entry_t *e)
{
    if (e->fetching || e->n_users == 0) {
        return;
    }
    job_t *j = calloc(1, sizeof *j);
    result_t *r = calloc(1, sizeof *r);
    char *url = strdup(e->url);
    char **lines = firc_subhdr_lines_copy(e->lines);
    if (j == NULL || r == NULL || url == NULL || lines == NULL) {
        free(j);
        free(r);
        free(url);
        firc_subhdr_lines_free(lines);
        FIRC_WARN("tunnel subscriptions: out of memory, a fetch is left for the next tick");
        return;
    }
    r->s = s;
    r->url = url;
    memcpy(r->key, e->key, sizeof r->key);
    j->r = r;
    j->mark = e->users[0].mark;
    j->lines = lines;
    snprintf(j->path, sizeof j->path, "%s/%s", s->dir, e->key);
    j->cached = e->cached;
    memcpy(j->cached_sha, e->cached_sha, sizeof j->cached_sha);
    e->fetching = true;
    pthread_mutex_lock(&s->mu);
    if (s->tail != NULL) {
        s->tail->next = j;
    } else {
        s->head = j;
    }
    s->tail = j;
    pthread_cond_signal(&s->cv);
    pthread_mutex_unlock(&s->mu);
}

static void free_own(firc_subhdr_pair_t *v, size_t n)
{
    for (size_t i = 0; v != NULL && i < n; i++) {
        free(v[i].name);
        free(v[i].value);
    }
    free(v);
}

static firc_subhdr_pair_t *copy_own(const firc_tun_sub_t *sub)
{
    if (sub->n_headers == 0) {
        return NULL;
    }
    firc_subhdr_pair_t *v = calloc(sub->n_headers, sizeof *v);
    for (size_t i = 0; v != NULL && i < sub->n_headers; i++) {
        v[i].name = strdup(sub->headers[i].name);
        v[i].value = strdup(sub->headers[i].value);
        if (v[i].name == NULL || v[i].value == NULL) {
            free_own(v, i + 1);
            return NULL;
        }
    }
    return v;
}

static char **lines_of(const firc_tunsubs_t *s, const firc_subhdr_pair_t *own, size_t n_own)
{
    firc_subhdr_set_t eff;
    firc_subhdr_merge(&s->defaults, own, n_own, &eff);
    return firc_subhdr_lines(&eff);
}

static void free_entry(entry_t *e)
{
    free(e->url);
    free(e->body);
    firc_subhdr_lines_free(e->lines);
    free_own(e->own, e->n_own);
    free(e);
}

static entry_t *new_entry(firc_tunsubs_t *s, const char *url, const char *key, const firc_tun_sub_t *sub)
{
    entry_t **grown = realloc(s->v, (s->n + 1) * sizeof *grown);
    if (grown == NULL) {
        return NULL;
    }
    s->v = grown;
    entry_t *e = calloc(1, sizeof *e);
    if (e == NULL) {
        return NULL;
    }
    e->n_own = sub->n_headers;
    e->own = copy_own(sub);
    e->url = strdup(url);
    e->lines = lines_of(s, e->own, e->n_own);
    if (e->url == NULL || e->lines == NULL || (e->n_own > 0 && e->own == NULL)) {
        free_entry(e);
        return NULL;
    }
    memcpy(e->key, key, sizeof e->key);
    load_cache(s, e);
    s->v[s->n++] = e;
    return e;
}

static void add_user(entry_t *e, const char *id, uint32_t mark, uint64_t hsig, int interval_s)
{
    bool known = false;
    for (size_t i = 0; i < e->n_users; i++) {
        known = known || (strcmp(e->users[i].id, id) == 0 && e->users[i].hsig == hsig);
    }
    if (!known && e->n_users < FIRC_TUN_MAX) {
        snprintf(e->users[e->n_users].id, sizeof e->users[0].id, "%s", id);
        e->users[e->n_users].mark = mark;
        e->users[e->n_users].hsig = hsig;
        e->n_users++;
    }
    if (interval_s > 0 && (e->interval_s == 0 || interval_s < e->interval_s)) {
        e->interval_s = interval_s;
    }
}

void firc_tunsubs_want(firc_tunsubs_t *s, const firc_tunnels_t *t, const uint32_t *marks)
{
    size_t old = s->n;
    for (size_t i = 0; i < old; i++) {
        s->v[i]->was_wanted = s->v[i]->n_users > 0;
        s->v[i]->n_users = 0;
        s->v[i]->interval_s = 0;
        s->v[i]->kept = false;
    }
    for (size_t i = 0; i < t->n; i++) {
        const firc_tunnel_t *tun = &t->t[i];
        for (size_t k = 0; k < tun->n_src; k++) {
            const firc_tun_src_t *src = &tun->src[k];
            if (src->kind != FIRC_TUN_SRC_SUB || src->sub.url == NULL) {
                continue;
            }
            char key[17];
            pair_key(tun, &src->sub, key);
            entry_t *e = find(s, src->sub.url, key);
            if (e == NULL) {
                e = new_entry(s, src->sub.url, key, &src->sub);
            }
            if (e != NULL && tun->enable) {
                add_user(e, tun->id, marks[i], firc_tun_sub_headers_sig(&src->sub), src->sub.interval_s);
            } else if (e != NULL) {
                e->kept = true;
            }
        }
    }
    size_t k = 0;
    size_t users = 0;
    for (size_t i = 0; i < s->n; i++) {
        entry_t *e = s->v[i];
        if (e->n_users == 0 && !e->kept) {
            free_entry(e);
            continue;
        }
        s->v[k++] = e;
        users += e->n_users;
        if (!e->was_wanted && e->n_users > 0 && (!e->have || e->interval_s > 0)) {
            enqueue(s, e);
        }
    }
    s->n = k;
    firc_tun_body_t *b = realloc(s->bodies, (users + 1) * sizeof *b);
    if (b != NULL) {
        s->bodies = b;
        rebuild_bodies(s);
    } else {
        s->n_bodies = 0;
    }
}

static bool is_key_name(const char *name)
{
    size_t n = 0;
    for (; name[n] != 0; n++) {
        if (!((name[n] >= '0' && name[n] <= '9') || (name[n] >= 'a' && name[n] <= 'f'))) {
            return false;
        }
    }
    return n == 16;
}

static bool key_kept(const firc_tunnels_t *all, const char *name)
{
    for (size_t i = 0; i < all->n; i++) {
        const firc_tunnel_t *tun = &all->t[i];
        for (size_t k = 0; k < tun->n_src; k++) {
            if (tun->src[k].kind != FIRC_TUN_SRC_SUB || tun->src[k].sub.url == NULL) {
                continue;
            }
            char key[17];
            pair_key(tun, &tun->src[k].sub, key);
            if (strcmp(key, name) == 0) {
                return true;
            }
        }
    }
    return false;
}

void firc_tunsubs_sweep(firc_tunsubs_t *s, const firc_tunnels_t *all)
{
    if (s == NULL) {
        return;
    }
    DIR *d = opendir(s->dir);
    if (d == NULL) {
        return;
    }
    struct dirent *de;
    while ((de = readdir(d)) != NULL) {
        char key[sizeof de->d_name];
        snprintf(key, sizeof key, "%s", de->d_name);
        char *dot = strchr(key, '.');
        if (dot != NULL && strcmp(dot, ".info") == 0) {
            *dot = 0;
        }
        if (!is_key_name(key) || key_kept(all, key)) {
            continue;
        }
        char path[sizeof s->dir + sizeof de->d_name + 1];
        snprintf(path, sizeof path, "%s/%s", s->dir, de->d_name);
        if (unlink(path) == 0) {
            FIRC_INFO("tunnel subscriptions: cache file %s no longer used, removed", de->d_name);
        }
    }
    closedir(d);
}

void firc_tunsubs_due(firc_tunsubs_t *s)
{
    if (s == NULL) {
        return;
    }
    int64_t now = mono_ms(s);
    for (size_t i = 0; i < s->n; i++) {
        entry_t *e = s->v[i];
        bool scheduled = e->interval_s > 0 && now >= e->done_mono + (int64_t)e->interval_s * 1000;
        bool retry = !e->have && e->done_mono != 0 && now >= e->done_mono + RETRY_MS;
        if (!e->fetching && (scheduled || retry)) {
            enqueue(s, e);
        }
    }
}

firc_err_t firc_tunsubs_refresh(firc_tunsubs_t *s, const char *tunnel_id)
{
    bool any = false;
    for (size_t i = 0; i < s->n; i++) {
        entry_t *e = s->v[i];
        for (size_t u = 0; u < e->n_users; u++) {
            if (strcmp(e->users[u].id, tunnel_id) == 0) {
                enqueue(s, e);
                any = true;
                break;
            }
        }
    }
    return any ? FIRC_OK : FIRC_ERR_NOENT;
}

const firc_tun_body_t *firc_tunsubs_bodies(const firc_tunsubs_t *s, size_t *n)
{
    *n = s != NULL ? s->n_bodies : 0;
    return s != NULL ? s->bodies : NULL;
}

static const firc_tun_sub_t *saved_sub(const firc_tunnel_t *saved, const firc_tun_src_t *src)
{
    for (size_t i = 0; i < saved->n_src; i++) {
        const firc_tun_src_t *x = &saved->src[i];
        if (x->kind == FIRC_TUN_SRC_SUB && x->sub.url != NULL && strcmp(x->id, src->id) == 0 &&
            strcmp(x->sub.url, src->sub.url) == 0) {
            return &x->sub;
        }
    }
    return &src->sub;
}

size_t firc_tunsubs_held(const firc_tunsubs_t *s, const firc_tunnel_t *uplink_of, const firc_tunnel_t *t,
                         firc_tun_body_t *out, size_t cap)
{
    size_t k = 0;
    if (s == NULL) {
        return 0;
    }
    for (size_t i = 0; i < t->n_src && k < cap; i++) {
        const firc_tun_src_t *src = &t->src[i];
        if (src->kind != FIRC_TUN_SRC_SUB || src->sub.url == NULL) {
            continue;
        }
        char key[17];
        pair_key(uplink_of, saved_sub(uplink_of, src), key);
        const entry_t *e = find(s, src->sub.url, key);
        if (e != NULL && e->have) {
            out[k++] = (firc_tun_body_t){.url = e->url,
                                         .mark = 0,
                                         .body = e->body,
                                         .len = e->len,
                                         .hsig = firc_tun_sub_headers_sig(&src->sub)};
        }
    }
    return k;
}

size_t firc_tunsubs_state(const firc_tunsubs_t *s, const char *url, uint32_t mark, uint64_t hsig,
                          firc_tun_sub_state_t *out)
{
    const entry_t *e = NULL;
    for (size_t i = 0; i < s->n && e == NULL; i++) {
        if (strcmp(s->v[i]->url, url) == 0 && user_seen(s->v[i], s->v[i]->n_users, mark, hsig)) {
            e = s->v[i];
        }
    }
    if (e == NULL) {
        return 0;
    }
    memset(out, 0, sizeof *out);
    memcpy(out->url_hash, e->key, sizeof out->url_hash);
    out->last_ok_ms = e->last_ok_ms;
    out->last_try_ms = e->last_try_ms;
    snprintf(out->error, sizeof out->error, "%s", e->error);
    out->fetching = e->fetching;
    out->nodes = e->nodes;
    out->info = e->info;
    return 1;
}

void firc_tunsubs_set_defaults(firc_tunsubs_t *s, const firc_subhdr_set_t *defaults)
{
    if (s == NULL) {
        return;
    }
    s->defaults = *defaults;
    for (size_t i = 0; i < s->n; i++) {
        char **lines = lines_of(s, s->v[i]->own, s->v[i]->n_own);
        if (lines != NULL) {
            firc_subhdr_lines_free(s->v[i]->lines);
            s->v[i]->lines = lines;
        }
    }
}

const firc_subhdr_set_t *firc_tunsubs_defaults(const firc_tunsubs_t *s)
{
    return s != NULL ? &s->defaults : NULL;
}

void firc_tunsubs_advance_for_test(firc_tunsubs_t *s, int64_t ms)
{
    s->offset_ms += ms;
}

void firc_tunsubs_free(firc_tunsubs_t *s)
{
    if (s == NULL) {
        return;
    }
    atomic_store(&s->abort, true);
    pthread_mutex_lock(&s->mu);
    s->stop = true;
    pthread_cond_broadcast(&s->cv);
    pthread_mutex_unlock(&s->mu);
    pthread_join(s->thread, NULL);
    while (s->head != NULL) {
        job_t *j = s->head;
        s->head = j->next;
        free_job(j);
    }
    for (size_t i = 0; i < s->n; i++) {
        free_entry(s->v[i]);
    }
    free(s->v);
    free(s->bodies);
    pthread_mutex_lock(&s->mu);
    s->dead = true;
    bool last = s->posted == 0;
    pthread_mutex_unlock(&s->mu);
    if (last) {
        destroy(s);
    }
}
