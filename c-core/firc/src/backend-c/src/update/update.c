#include "firc/update.h"

#include <dirent.h>
#include <errno.h>
#include <fcntl.h>
#include <limits.h>
#include <pthread.h>
#include <stdatomic.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <strings.h>
#include <sys/stat.h>
#include <sys/statvfs.h>
#include <time.h>
#include <unistd.h>

#include "firc/cancel.h"
#include "firc/log.h"

#define CHECK_TIMEOUT_S 15
#define DAILY_MS ((int64_t)(23 * 3600 + 1800) * 1000)
#define DOWNLOAD_TIMEOUT_S 300
#define DOWNLOAD_MAX_REDIRECTS 4
#define LOW_SPEED_BYTES 1024
#define LOW_SPEED_S 30
#define GRACE_MS 10000
#define SPACE_SLACK ((uint64_t)8 * 1024 * 1024)
#define INTERRUPTED                                                                                                    \
    "the last install did not finish: its installer is gone and left no result; see the system log and opkg status firc"

typedef enum { JOB_CHECK, JOB_INSTALL } job_kind_t;

typedef struct step step_t;

typedef struct result {
    firc_update_t *u;
    step_t *last;
    job_kind_t kind;
    firc_update_outcome_t o;
    bool have_release;
    firc_release_t release;
    char tag[64];
    char name[160];
    char url[512];
    uint64_t size;
    uint8_t sha256[FIRC_SHA256_DIGEST_LEN];
} result_t;

struct step {
    firc_update_t *u;
    firc_update_state_t state;
    int64_t epoch;
    char tag[64];
    char name[160];
    char error[CURL_ERROR_SIZE + 64];
};

struct firc_update {
    firc_loop_t *loop;
    char *dir, *api_url, *download_prefix, *version, *target, *sh, *opkg;
    firc_sub_fetch_url_fn url_ok;
    void (*checked)(void *ud, const firc_update_outcome_t *o);
    void *ud;
    uint64_t (*free_bytes)(const char *dir);
    void (*verified)(void *ud);
    int64_t grace_ms;
    const char *cannot_install;
    int64_t checked_at;
    int64_t attempt_mono;
    int64_t success_mono;
    uint32_t revision;
    firc_cancel_t *cancel;
    result_t *job;
    pthread_t thread;
    size_t posted;
    pthread_mutex_t mu;
    pthread_cond_t cv;
    firc_release_t latest;
    firc_update_state_t state;
    firc_version_t installed;
    bool installed_known;
    bool checking;
    bool attempted;
    bool have_success;
    bool have_latest;
    bool newer;
    bool failed;
    bool stop;
    bool started;
    bool dead;
    bool install_job;
    int64_t handoff_mono;
    int64_t handoff_epoch;
    char handoff_tag[64];
    uint64_t total;
    _Atomic uint64_t bytes;
    char error[CURL_ERROR_SIZE + 64];
    char check_error[CURL_ERROR_SIZE + 32];
};

const char *firc_update_state_name(firc_update_state_t s) {
    switch (s) {
    case FIRC_UPDATE_IDLE: return "idle";
    case FIRC_UPDATE_CHECKING: return "checking";
    case FIRC_UPDATE_DOWNLOADING: return "downloading";
    case FIRC_UPDATE_VERIFYING: return "verifying";
    case FIRC_UPDATE_INSTALLING: return "installing";
    case FIRC_UPDATE_FAILED: return "failed";
    }
    return "idle";
}

static int64_t mono_ms(void) {
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

typedef struct mem {
    char *data;
    size_t len;
    bool over;
    bool nomem;
} mem_t;

static bool mem_write(void *ud, const uint8_t *data, size_t n) {
    mem_t *m = ud;
    if (n > FIRC_RELEASE_JSON_MAX - m->len) {
        m->over = true;
        return false;
    }
    char *grown = realloc(m->data, m->len + n + 1);
    if (grown == NULL) {
        m->nomem = true;
        return false;
    }
    m->data = grown;
    memcpy(m->data + m->len, data, n);
    m->len += n;
    m->data[m->len] = '\0';
    return true;
}

typedef struct seen {
    bool none_left;
    bool have_reset;
    int64_t reset;
    bool have_retry;
    int64_t retry;
} seen_t;

static bool number(const char *s, int64_t *out) {
    char *end = NULL;
    long long v = strtoll(s, &end, 10);
    if (end == s || *end != '\0' || v < 0) { return false; }
    *out = (int64_t)v;
    return true;
}

static void on_header(void *ud, const char *name, const char *value) {
    seen_t *s = ud;
    if (strcasecmp(name, "x-ratelimit-remaining") == 0) {
        s->none_left = strcmp(value, "0") == 0;
    } else if (strcasecmp(name, "x-ratelimit-reset") == 0) {
        s->have_reset = number(value, &s->reset);
    } else if (strcasecmp(name, "retry-after") == 0) {
        s->have_retry = number(value, &s->retry);
    }
}

static bool keep_going(void *ud, size_t bytes, size_t total) {
    (void)bytes;
    (void)total;
    return !firc_cancel_raised(((firc_update_t *)ud)->cancel);
}

static bool api_ok(void *ud, const char *url) {
    (void)ud;
    return firc_update_api_url_ok(url);
}

static bool download_ok(void *ud, const char *url) {
    (void)ud;
    return firc_update_download_url_ok(url);
}

void firc_update_limited_text(int64_t reset_at, char *buf, size_t cap) {
    struct tm tm;
    time_t t = (time_t)reset_at;
    char when[32];
    if (reset_at > 0 && gmtime_r(&t, &tm) != NULL && strftime(when, sizeof(when), "%Y-%m-%dT%H:%M:%SZ", &tm) > 0) {
        snprintf(buf, cap, "GitHub refuses more checks from this address until %s", when);
    } else {
        snprintf(buf, cap, "GitHub refuses more checks from this address for now");
    }
}

static void describe_limit(long status, char *buf, size_t cap) {
    if (firc_sub_fetch_is_redirect(status)) {
        snprintf(buf, cap, "too many redirects");
    } else {
        snprintf(buf, cap, "GitHub answered %ld", status);
    }
}

static void describe(firc_err_t err, long status, const mem_t *m, const char *errbuf, char *buf, size_t cap) {
    if (err == FIRC_ERR_IO) {
        snprintf(buf, cap, "%s", errbuf[0] != '\0' ? errbuf : "no answer from GitHub");
    } else if (m->over) {
        snprintf(buf, cap, "GitHub's answer is over %zu KiB", FIRC_RELEASE_JSON_MAX / 1024);
    } else if (err == FIRC_ERR_NOMEM || m->nomem) {
        snprintf(buf, cap, "out of memory");
    } else if (err == FIRC_ERR_INVAL) {
        snprintf(buf, cap, "GitHub sent the check to a refused address");
    } else if (err == FIRC_ERR_LIMIT) {
        describe_limit(status, buf, cap);
    } else {
        snprintf(buf, cap, "%s", firc_err_str(err));
    }
}

static void run_check(firc_update_t *u, result_t *r) {
    char agent[96];
    snprintf(agent, sizeof(agent), "User-Agent: firc/%s", u->version);
    const char *headers[] = {"Accept: application/vnd.github+json", agent, NULL};
    char errbuf[CURL_ERROR_SIZE] = "";
    seen_t seen = {0};
    firc_sub_fetch_opts_t o = {
        .headers = headers,
        .timeout_s = CHECK_TIMEOUT_S,
        .url_ok = u->url_ok != NULL ? u->url_ok : api_ok,
        .on_header = on_header,
        .header_ud = &seen,
        .progress = keep_going,
        .progress_ud = u,
        .errbuf = errbuf,
        .errbuf_cap = sizeof(errbuf),
    };
    mem_t m = {0};
    long status = 0;
    firc_err_t err = firc_sub_fetch_to(u->api_url, &o, mem_write, &m, &status);
    r->o.http_status = status;
    if (err == FIRC_OK) {
        firc_release_want_t want = {.target = u->target, .download_prefix = u->download_prefix};
        firc_err_t perr = firc_release_parse(m.data != NULL ? m.data : "", m.len, &want, &r->release);
        if (perr == FIRC_OK) {
            r->have_release = true;
            r->o.err = FIRC_OK;
        } else {
            firc_release_clear(&r->release);
            r->o.err = FIRC_ERR_UPSTREAM;
            snprintf(r->o.message, sizeof(r->o.message), "%s",
                     perr == FIRC_ERR_NOMEM ? "out of memory" : "GitHub's answer is not a release");
        }
    } else if (err == FIRC_ERR_PROTO && status == 404) {
        r->o.err = FIRC_OK;
    } else if (err == FIRC_ERR_PROTO && ((status == 403 && seen.none_left) || status == 429)) {
        r->o.err = FIRC_ERR_AGAIN;
        r->o.reset_at = seen.have_reset ? seen.reset : seen.have_retry ? (int64_t)time(NULL) + seen.retry : 0;
        firc_update_limited_text(r->o.reset_at, r->o.message, sizeof(r->o.message));
    } else if (err == FIRC_ERR_PROTO) {
        r->o.err = FIRC_ERR_UPSTREAM;
        snprintf(r->o.message, sizeof(r->o.message), "GitHub answered %ld", status);
    } else {
        r->o.err = FIRC_ERR_UPSTREAM;
        describe(err, status, &m, errbuf, r->o.message, sizeof(r->o.message));
    }
    free(m.data);
}

static void free_result(result_t *r) {
    if (r == NULL) { return; }
    firc_release_clear(&r->release);
    free(r->last);
    free(r);
}

static void destroy(firc_update_t *u) {
    pthread_cond_destroy(&u->cv);
    pthread_mutex_destroy(&u->mu);
    firc_cancel_free(u->cancel);
    free(u);
}

static bool unpost(firc_update_t *u, bool *last) {
    pthread_mutex_lock(&u->mu);
    u->posted--;
    bool dead = u->dead;
    *last = dead && u->posted == 0;
    pthread_mutex_unlock(&u->mu);
    return dead;
}

static void drop_result(void *ud) {
    result_t *r = ud;
    firc_update_t *u = r->u;
    free_result(r);
    bool last = false;
    if (unpost(u, &last) && last) { destroy(u); }
}

static void on_result(firc_loop_t *loop, void *ud) {
    (void)loop;
    result_t *r = ud;
    firc_update_t *u = r->u;
    bool last = false;
    if (unpost(u, &last)) {
        free_result(r);
        if (last) { destroy(u); }
        return;
    }
    u->checking = false;
    u->state = FIRC_UPDATE_IDLE;
    u->attempted = true;
    u->checked_at = (int64_t)time(NULL);
    u->attempt_mono = mono_ms();
    if (r->o.err == FIRC_OK) {
        u->failed = false;
        u->check_error[0] = '\0';
        u->have_success = true;
        u->success_mono = u->attempt_mono;
        firc_release_clear(&u->latest);
        u->have_latest = r->have_release;
        if (r->have_release) {
            u->latest = r->release;
            r->release.notes = NULL;
        }
        u->newer = u->have_latest && u->latest.known && u->installed_known &&
                   firc_version_cmp(&u->latest.version, &u->installed) > 0;
        if (u->newer) { FIRC_INFO("update: release %s is newer than %s", u->latest.version_text, u->version); }
    } else {
        u->failed = true;
        snprintf(u->check_error, sizeof(u->check_error), "%s", r->o.message);
        FIRC_WARN("update: check failed: %s", u->check_error);
    }
    if (u->checked != NULL) { u->checked(u->ud, &r->o); }
    free_result(r);
}

static void post(firc_update_t *u, firc_post_cb cb, firc_post_drop_cb drop, void *msg, void (*release)(void *)) {
    pthread_mutex_lock(&u->mu);
    u->posted++;
    pthread_mutex_unlock(&u->mu);
    while (firc_loop_post_with_drop(u->loop, cb, msg, drop) != FIRC_OK) {
        if (firc_cancel_raised(u->cancel)) {
            pthread_mutex_lock(&u->mu);
            u->posted--;
            pthread_mutex_unlock(&u->mu);
            release(msg);
            return;
        }
        struct timespec ts = {0, 50 * 1000000L};
        nanosleep(&ts, NULL);
    }
}

static void release_result(void *ud) { free_result(ud); }

static void drop_step(void *ud) {
    step_t *s = ud;
    firc_update_t *u = s->u;
    free(s);
    bool last = false;
    if (unpost(u, &last) && last) { destroy(u); }
}

static void on_step(firc_loop_t *loop, void *ud) {
    (void)loop;
    step_t *s = ud;
    firc_update_t *u = s->u;
    bool last = false;
    if (unpost(u, &last)) {
        free(s);
        if (last) { destroy(u); }
        return;
    }
    u->state = s->state;
    if (s->state == FIRC_UPDATE_INSTALLING) {
        u->install_job = false;
        u->handoff_mono = mono_ms();
        u->handoff_epoch = s->epoch;
        snprintf(u->handoff_tag, sizeof(u->handoff_tag), "%s", s->tag);
        FIRC_INFO("update: installing %s", s->name);
    } else if (s->state == FIRC_UPDATE_FAILED) {
        u->install_job = false;
        snprintf(u->error, sizeof(u->error), "%s", s->error);
        FIRC_WARN("update: %s failed: %s", s->name, u->error);
    } else {
        FIRC_INFO("update: %s %s", firc_update_state_name(s->state), s->name);
    }
    free(s);
}

static void post_step(firc_update_t *u, const result_t *j, step_t *s, firc_update_state_t state, int64_t epoch,
                      const char *error) {
    if (s == NULL) { s = calloc(1, sizeof(*s)); }
    if (s == NULL) { return; }
    s->u = u;
    s->state = state;
    s->epoch = epoch;
    snprintf(s->name, sizeof(s->name), "%s", j->name);
    snprintf(s->tag, sizeof(s->tag), "%s", j->tag);
    snprintf(s->error, sizeof(s->error), "%s", error != NULL ? error : "");
    post(u, on_step, drop_step, s, free);
}

uint64_t firc_update_space_need(uint64_t size) {
    if (size > (UINT64_MAX - SPACE_SLACK) / 3) { return UINT64_MAX; }
    return 3 * size + SPACE_SLACK;
}

bool firc_update_file_sink_write(void *ud, const uint8_t *p, size_t n) {
    firc_update_file_sink_t *s = ud;
    if (s->err != 0) { return false; }
    if (s->got > s->want || n > s->want - s->got) {
        s->got += n;
        return false;
    }
    firc_sha256_update(&s->sha, p, n);
    size_t done = 0;
    while (done < n) {
        ssize_t w = write(s->fd, p + done, n - done);
        if (w < 0 && errno == EINTR) { continue; }
        if (w <= 0) {
            s->err = w < 0 ? errno : ENOSPC;
            if (s->progress != NULL) { atomic_store(s->progress, s->got); }
            return false;
        }
        done += (size_t)w;
        s->got += (uint64_t)w;
    }
    if (s->progress != NULL) { atomic_store(s->progress, s->got); }
    return true;
}

static bool path_in(char *buf, size_t cap, const char *dir, const char *name, const char *suffix) {
    int n = snprintf(buf, cap, "%s/%s%s", dir, name, suffix);
    return n > 0 && (size_t)n < cap;
}

static bool ends_with(const char *s, const char *suffix) {
    size_t n = strlen(s), k = strlen(suffix);
    return n > k && strcmp(s + n - k, suffix) == 0;
}

static void clean_dir(const char *dir) {
    DIR *d = opendir(dir);
    if (d != NULL) {
        int fd = dirfd(d);
        struct dirent *de;
        while ((de = readdir(d)) != NULL) {
            if (ends_with(de->d_name, ".part") || ends_with(de->d_name, ".ipk")) { (void)unlinkat(fd, de->d_name, 0); }
        }
        closedir(d);
    }
    char path[PATH_MAX];
    if (path_in(path, sizeof(path), dir, "result", "")) { (void)unlink(path); }
}

static void describe_download(firc_err_t err, long status, const firc_update_file_sink_t *s, const char *errbuf,
                              bool canceled, char *buf, size_t cap) {
    if (s->err != 0) {
        snprintf(buf, cap, "no space left: %llu of %llu bytes written (%s)", (unsigned long long)s->got,
                 (unsigned long long)s->want, strerror(s->err));
    } else if (s->got > s->want) {
        snprintf(buf, cap, "the download is %llu bytes; the release says %llu", (unsigned long long)s->got,
                 (unsigned long long)s->want);
    } else if (canceled) {
        snprintf(buf, cap, "the download was stopped");
    } else if (err == FIRC_ERR_PROTO) {
        snprintf(buf, cap, "GitHub answered %ld", status);
    } else if (err == FIRC_ERR_IO) {
        snprintf(buf, cap, "%s", errbuf[0] != '\0' ? errbuf : "no answer from GitHub");
    } else if (err == FIRC_ERR_INVAL) {
        snprintf(buf, cap, "GitHub sent the download to a refused address");
    } else if (err == FIRC_ERR_LIMIT) {
        describe_limit(status, buf, cap);
    } else {
        snprintf(buf, cap, "%s", firc_err_str(err));
    }
}

static bool download(firc_update_t *u, const result_t *j, const char *part, char *msg, size_t cap) {
    int fd = open(part, O_WRONLY | O_CREAT | O_TRUNC | O_CLOEXEC | O_NOFOLLOW, 0600);
    if (fd < 0) {
        snprintf(msg, cap, "the download could not be opened: %s", strerror(errno));
        return false;
    }
    firc_update_file_sink_t s = {.fd = fd, .want = j->size, .progress = &u->bytes};
    firc_sha256_init(&s.sha);
    char agent[96];
    snprintf(agent, sizeof(agent), "User-Agent: firc/%s", u->version);
    const char *headers[] = {"Accept: application/octet-stream", agent, NULL};
    char errbuf[CURL_ERROR_SIZE] = "";
    firc_sub_fetch_opts_t o = {
        .headers = headers,
        .timeout_s = DOWNLOAD_TIMEOUT_S,
        .low_speed_bytes = LOW_SPEED_BYTES,
        .low_speed_s = LOW_SPEED_S,
        .max_redirects = DOWNLOAD_MAX_REDIRECTS,
        .url_ok = u->url_ok != NULL ? u->url_ok : download_ok,
        .progress = keep_going,
        .progress_ud = u,
        .errbuf = errbuf,
        .errbuf_cap = sizeof(errbuf),
    };
    long status = 0;
    firc_err_t err = firc_sub_fetch_to(j->url, &o, firc_update_file_sink_write, &s, &status);
    if (err != FIRC_OK) {
        close(fd);
        describe_download(err, status, &s, errbuf, firc_cancel_raised(u->cancel), msg, cap);
        return false;
    }
    post_step(u, j, NULL, FIRC_UPDATE_VERIFYING, 0, NULL);
    uint8_t digest[FIRC_SHA256_DIGEST_LEN];
    firc_sha256_final(&s.sha, digest);
    if (s.got != j->size) {
        close(fd);
        snprintf(msg, cap, "the download is %llu bytes; the release says %llu", (unsigned long long)s.got,
                 (unsigned long long)j->size);
        return false;
    }
    if (!firc_hash_equal(digest, j->sha256, sizeof(digest))) {
        close(fd);
        snprintf(msg, cap, "checksum: the download does not match the release's digest");
        return false;
    }
    if (fsync(fd) != 0) {
        snprintf(msg, cap, "the download could not be saved: %s", strerror(errno));
        close(fd);
        return false;
    }
    if (close(fd) != 0) {
        snprintf(msg, cap, "the download could not be saved: %s", strerror(errno));
        return false;
    }
    return true;
}

static step_t *last_step(result_t *j) {
    step_t *s = j->last;
    j->last = NULL;
    return s;
}

static void run_install(firc_update_t *u, result_t *j) {
    char part[PATH_MAX], ipk[PATH_MAX], msg[CURL_ERROR_SIZE + 64] = "";
    if (!path_in(part, sizeof(part), u->dir, j->name, ".part") || !path_in(ipk, sizeof(ipk), u->dir, j->name, "")) {
        post_step(u, j, last_step(j), FIRC_UPDATE_FAILED, 0, "the update directory's path is too long");
        return;
    }
    clean_dir(u->dir);
    bool ok = download(u, j, part, msg, sizeof(msg));
    if (ok && rename(part, ipk) != 0) {
        snprintf(msg, sizeof(msg), "the download could not be saved: %s", strerror(errno));
        ok = false;
    }
    if (ok && u->verified != NULL) { u->verified(u->ud); }
    if (ok && firc_cancel_raised(u->cancel)) {
        snprintf(msg, sizeof(msg), "the download was stopped");
        ok = false;
    }
    if (ok) {
        int64_t epoch = (int64_t)time(NULL);
        firc_err_t err = firc_update_handoff(u->dir, u->sh, u->opkg, ipk, j->tag, epoch);
        if (err == FIRC_OK) {
            post_step(u, j, last_step(j), FIRC_UPDATE_INSTALLING, epoch, NULL);
            return;
        }
        snprintf(msg, sizeof(msg), "the installer could not start: %s", firc_err_str(err));
    }
    (void)unlink(part);
    (void)unlink(ipk);
    post_step(u, j, last_step(j), FIRC_UPDATE_FAILED, 0, msg);
}

static void *worker(void *ud) {
    firc_update_t *u = ud;
    for (;;) {
        pthread_mutex_lock(&u->mu);
        while (!u->stop && u->job == NULL) { pthread_cond_wait(&u->cv, &u->mu); }
        if (u->stop) {
            pthread_mutex_unlock(&u->mu);
            return NULL;
        }
        result_t *r = u->job;
        u->job = NULL;
        pthread_mutex_unlock(&u->mu);
        if (r->kind == JOB_INSTALL) {
            run_install(u, r);
            free_result(r);
        } else {
            run_check(u, r);
            post(u, on_result, drop_result, r, release_result);
        }
    }
}

static char *dup_or_empty(const char *s, bool *ok) {
    char *d = strdup(s != NULL ? s : "");
    if (d == NULL) { *ok = false; }
    return d;
}

static void free_strings(firc_update_t *u) {
    free(u->dir);
    free(u->api_url);
    free(u->download_prefix);
    free(u->version);
    free(u->target);
    free(u->sh);
    free(u->opkg);
}

firc_update_t *firc_update_new(const firc_update_deps_t *d) {
    firc_update_t *u = calloc(1, sizeof(*u));
    if (u == NULL) { return NULL; }
    bool ok = true;
    u->dir = dup_or_empty(d->dir, &ok);
    u->api_url = dup_or_empty(d->api_url, &ok);
    u->download_prefix = dup_or_empty(d->download_prefix, &ok);
    u->version = dup_or_empty(d->version, &ok);
    u->target = dup_or_empty(d->target, &ok);
    u->sh = dup_or_empty(d->sh, &ok);
    u->opkg = dup_or_empty(d->opkg, &ok);
    u->cancel = firc_cancel_new();
    if (!ok || u->cancel == NULL) {
        free_strings(u);
        firc_cancel_free(u->cancel);
        free(u);
        return NULL;
    }
    if (pthread_mutex_init(&u->mu, NULL) != 0) {
        free_strings(u);
        firc_cancel_free(u->cancel);
        free(u);
        return NULL;
    }
    if (pthread_cond_init(&u->cv, NULL) != 0) {
        pthread_mutex_destroy(&u->mu);
        free_strings(u);
        firc_cancel_free(u->cancel);
        free(u);
        return NULL;
    }
    u->loop = d->loop;
    u->url_ok = d->url_ok;
    u->checked = d->checked;
    u->ud = d->ud;
    u->free_bytes = d->free_bytes;
    u->verified = d->verified;
    u->grace_ms = d->handoff_grace_ms > 0 ? d->handoff_grace_ms : GRACE_MS;
    u->revision = d->revision;
    u->installed_known = firc_version_parse_installed(u->version, d->revision, &u->installed);
    if (u->target[0] == '\0') {
        u->cannot_install = "no-target";
    } else if (!u->installed_known) {
        u->cannot_install = "unknown-version";
    } else if (u->installed.dev) {
        u->cannot_install = "dev-build";
    }
    u->state = FIRC_UPDATE_IDLE;
    return u;
}

firc_err_t firc_update_start(firc_update_t *u) {
    if (u->started) { return FIRC_ERR_STATE; }
    if (pthread_create(&u->thread, NULL, worker, u) != 0) { return FIRC_ERR_SYS; }
    u->started = true;
    return FIRC_OK;
}

void firc_update_free(firc_update_t *u) {
    if (u == NULL) { return; }
    firc_cancel_raise(u->cancel);
    pthread_mutex_lock(&u->mu);
    u->stop = true;
    pthread_cond_broadcast(&u->cv);
    pthread_mutex_unlock(&u->mu);
    if (u->started) { pthread_join(u->thread, NULL); }
    free_result(u->job);
    u->job = NULL;
    firc_release_clear(&u->latest);
    free_strings(u);
    pthread_mutex_lock(&u->mu);
    u->dead = true;
    bool last = u->posted == 0;
    pthread_mutex_unlock(&u->mu);
    if (last) { destroy(u); }
}

firc_update_settled_t firc_update_settle(bool mem_installing, int64_t since_handoff_ms, int64_t grace_ms,
                                         firc_update_marker_t marker, bool has_result, int64_t result_finished_at,
                                         int64_t epoch) {
    if (mem_installing && marker == FIRC_UPDATE_MARKER_NO_PID && since_handoff_ms < grace_ms) {
        return FIRC_UPDATE_SETTLED_INSTALLING;
    }
    if (marker == FIRC_UPDATE_MARKER_ALIVE) { return FIRC_UPDATE_SETTLED_INSTALLING; }
    if (marker == FIRC_UPDATE_MARKER_NONE && (epoch == 0 || (has_result && result_finished_at >= epoch))) {
        return FIRC_UPDATE_SETTLED_IDLE;
    }
    return FIRC_UPDATE_SETTLED_FAILED;
}

static firc_update_settled_t settled(firc_update_t *u, firc_update_result_t *res, bool *has_res) {
    firc_update_marker_t m = firc_update_marker_read(u->dir, NULL, 0, NULL);
    *has_res = firc_update_result_read(u->dir, res);
    bool mem = u->state == FIRC_UPDATE_INSTALLING;
    int64_t at = *has_res ? res->finished_at : 0;
    if (*has_res && u->handoff_epoch != 0 && strcmp(res->version, u->handoff_tag) == 0 && at < u->handoff_epoch) {
        at = u->handoff_epoch;
    }
    firc_update_settled_t s =
        firc_update_settle(mem, mono_ms() - u->handoff_mono, u->grace_ms, m, *has_res, at, u->handoff_epoch);
    if (mem && s == FIRC_UPDATE_SETTLED_IDLE) {
        u->state = FIRC_UPDATE_IDLE;
    } else if (mem && s == FIRC_UPDATE_SETTLED_FAILED) {
        u->state = FIRC_UPDATE_FAILED;
        snprintf(u->error, sizeof(u->error), "%s", INTERRUPTED);
    }
    return s;
}

bool firc_update_installing(firc_update_t *u) {
    firc_update_result_t res;
    bool has = false;
    return settled(u, &res, &has) == FIRC_UPDATE_SETTLED_INSTALLING;
}

static bool job_running(const firc_update_t *u) {
    return u->install_job || u->state == FIRC_UPDATE_DOWNLOADING || u->state == FIRC_UPDATE_VERIFYING;
}

bool firc_update_busy(firc_update_t *u) { return job_running(u) || firc_update_installing(u); }

void firc_update_cancel(firc_update_t *u) {
    if (u != NULL) { firc_cancel_raise(u->cancel); }
}

firc_err_t firc_update_check(firc_update_t *u) {
    if (firc_update_busy(u)) { return FIRC_ERR_STATE; }
    if (u->checking) { return FIRC_OK; }
    result_t *r = calloc(1, sizeof(*r));
    if (r == NULL) { return FIRC_ERR_NOMEM; }
    r->u = u;
    u->checking = true;
    u->state = FIRC_UPDATE_CHECKING;
    pthread_mutex_lock(&u->mu);
    u->job = r;
    pthread_cond_signal(&u->cv);
    pthread_mutex_unlock(&u->mu);
    return FIRC_OK;
}

void firc_update_auto_check(firc_update_t *u, bool enabled, int64_t now_mono_ms) {
    if (!enabled) { return; }
    if (u->have_success && now_mono_ms - u->success_mono < DAILY_MS) { return; }
    (void)firc_update_check(u);
}

static bool make_dir(const char *dir) {
    char p[PATH_MAX];
    if (snprintf(p, sizeof(p), "%s", dir) >= (int)sizeof(p) || p[0] == '\0') { return false; }
    for (char *q = p + 1; *q != '\0'; q++) {
        if (*q != '/') { continue; }
        *q = '\0';
        if (mkdir(p, 0700) != 0 && errno != EEXIST) { return false; }
        *q = '/';
    }
    if (mkdir(p, 0700) == 0) { return chmod(p, 0700) == 0; }
    struct stat st;
    return errno == EEXIST && stat(p, &st) == 0 && S_ISDIR(st.st_mode);
}

static bool free_space(const firc_update_t *u, uint64_t *out) {
    if (u->free_bytes != NULL) {
        *out = u->free_bytes(u->dir);
        return true;
    }
    struct statvfs sv;
    if (statvfs(u->dir, &sv) != 0) { return false; }
    uint64_t bs = (uint64_t)sv.f_frsize;
    uint64_t blocks = (uint64_t)sv.f_bavail;
    *out = bs != 0 && blocks > UINT64_MAX / bs ? UINT64_MAX : blocks * bs;
    return true;
}

firc_update_refusal_t firc_update_install(firc_update_t *u, const char *tag, firc_update_install_ans_t *ans) {
    memset(ans, 0, sizeof(*ans));
    if (u->cannot_install != NULL) {
        ans->reason = u->cannot_install;
        return FIRC_UPDATE_CANNOT;
    }
    if (firc_update_busy(u)) { return FIRC_UPDATE_BUSY; }
    if (u->checking) { return FIRC_UPDATE_CHECK_RUNNING; }
    if (!u->have_latest || tag == NULL || strcmp(u->latest.tag, tag) != 0) { return FIRC_UPDATE_STALE_CHECK; }
    if (!u->newer) { return FIRC_UPDATE_NOT_NEWER; }
    if (!u->latest.installable) {
        ans->reason = u->latest.reason;
        return FIRC_UPDATE_NOT_INSTALLABLE;
    }
    uint64_t free_bytes = 0;
    if (!make_dir(u->dir) || !free_space(u, &free_bytes)) { return FIRC_UPDATE_IO; }
    ans->free_bytes = free_bytes;
    ans->need_bytes = firc_update_space_need(u->latest.asset.size);
    if (free_bytes < ans->need_bytes) { return FIRC_UPDATE_NO_SPACE; }
    result_t *j = calloc(1, sizeof(*j));
    step_t *last = calloc(1, sizeof(*last));
    if (j == NULL || last == NULL) {
        free(j);
        free(last);
        return FIRC_UPDATE_IO;
    }
    j->last = last;
    j->u = u;
    j->kind = JOB_INSTALL;
    snprintf(j->tag, sizeof(j->tag), "%s", u->latest.tag);
    snprintf(j->name, sizeof(j->name), "%s", u->latest.asset.name);
    snprintf(j->url, sizeof(j->url), "%s", u->latest.asset.url);
    j->size = u->latest.asset.size;
    memcpy(j->sha256, u->latest.asset.sha256, sizeof(j->sha256));
    snprintf(ans->tag, sizeof(ans->tag), "%s", j->tag);
    snprintf(ans->asset, sizeof(ans->asset), "%s", j->name);
    ans->size = j->size;
    u->install_job = true;
    u->state = FIRC_UPDATE_DOWNLOADING;
    u->handoff_epoch = 0;
    u->handoff_tag[0] = '\0';
    u->handoff_mono = 0;
    u->error[0] = '\0';
    u->total = j->size;
    atomic_store(&u->bytes, 0);
    FIRC_INFO("update: downloading %s", j->name);
    pthread_mutex_lock(&u->mu);
    u->job = j;
    pthread_cond_signal(&u->cv);
    pthread_mutex_unlock(&u->mu);
    return FIRC_UPDATE_OK;
}

void firc_update_view(firc_update_t *u, firc_update_view_t *out) {
    memset(out, 0, sizeof(*out));
    out->version = u->version;
    out->target = u->target;
    out->revision = u->revision;
    out->state = u->state;
    if (!job_running(u)) {
        firc_update_settled_t s = settled(u, &out->result, &out->has_result);
        if (s == FIRC_UPDATE_SETTLED_INSTALLING) {
            out->state = FIRC_UPDATE_INSTALLING;
        } else if (s == FIRC_UPDATE_SETTLED_FAILED && (u->state == FIRC_UPDATE_IDLE || u->state == FIRC_UPDATE_FAILED)) {
            out->state = FIRC_UPDATE_FAILED;
        } else {
            out->state = u->state;
        }
        if (s == FIRC_UPDATE_SETTLED_FAILED && u->state == FIRC_UPDATE_IDLE) { out->error = INTERRUPTED; }
        if (out->has_result) { firc_update_log_read(u->dir, out->log, sizeof(out->log)); }
    }
    if (out->state == FIRC_UPDATE_FAILED && out->error == NULL) { out->error = u->error; }
    out->bytes = atomic_load(&u->bytes);
    out->total = u->total;
    out->cannot_install = u->cannot_install;
    out->attempted = u->attempted;
    out->checked_at = u->checked_at;
    out->age_s = u->attempted ? (mono_ms() - u->attempt_mono) / 1000 : 0;
    out->latest = u->have_latest ? &u->latest : NULL;
    out->newer = u->newer;
    out->check_error = u->failed ? u->check_error : NULL;
}

