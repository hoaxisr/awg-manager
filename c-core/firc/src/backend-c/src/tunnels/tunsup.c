#define _GNU_SOURCE /* NOLINT(bugprone-reserved-identifier) */

#include "firc/tunsup.h"

#include <arpa/inet.h>
#include <assert.h>
#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <signal.h>
#include <spawn.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/epoll.h>
#include <sys/types.h>
#include <sys/wait.h>
#include <time.h>
#include <unistd.h>

#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/tunaddr.h"
#include "firc/tunvless_name.h"

extern char **environ;

#define BACKOFF_BASE_S 3
#define BACKOFF_CAP_S 60
#define STABLE_S 60
#define REAP_RETRY_MS 100
#define STOP_POLL_MS 50
#define TERM_GRACE_S 5
#define ACK_S 10
#define INDEX_MAX 4096
#define RECOVERY_FLOOR_S 600
#define NUDGE_FLOOR_S 30
#define NUDGE_FLOOR_MAX_S 300
#define NUDGE_SHORT_S 5
#define NUDGE_SETTLE_S 2

static const char k_inb_verb[] = "inbounds\n";
static const char k_wake_verb[] = "wake\n";

typedef struct slot {
    firc_tunsup_t *s;
    firc_tunnel_t cfg;
    uint32_t mark;
    firc_tun_state_t st;
    pid_t pid;
    int in_fd;
    int out_fd;
    int err_fd;
    char *in_buf;
    size_t in_len;
    int inb_sent;
    bool inb_warned;
    size_t in_off;
    firc_tun_lines_t out_lines;
    firc_tun_lines_t err_lines;
    int reap_timer;
    int restart_timer;
    int kill_timer;
    int64_t started_ms;
    firc_logrep_t exits;
    int next_backoff_s;
    bool terminating;
    bool gone;
    bool warned_bad;
    bool warned_holder;
    bool warned_unread;
    bool warned_drop_out;
    bool warned_drop_err;
    bool deferred;
    pid_t stop_pid;
    uint32_t run_mark;
    char run_dev[16];
    uint64_t start_seq;
    uint64_t update_seq;
    uint64_t *ix;
    size_t n_ix;
    int acks;
    int ack_timer;
    bool ready;
    bool drift;
    size_t *want;
    size_t n_want;
    size_t *map_want;
    size_t n_map_want;
    bool gated;
    uint32_t gate_gen;
    bool starved;
    int64_t recovered_ms;
    int64_t nudged_ms;
    int nudge_s;
    int64_t pause_end_ms;
    int64_t held_ms;
    bool nudging;
    bool demand;
    bool owed;
} slot_t;

struct firc_tunsup {
    firc_loop_t *loop;
    char *binary;
    firc_tun_event_fn on_event;
    void *ud;
    firc_tun_reap_fn on_reap;
    void *reap_ud;
    firc_tun_gate_fn gate;
    void *gate_ud;
    pthread_t loop_thread;
    slot_t **v;
    size_t n;
    int second_ms;
    bool stopped;
    uint64_t seq;
    uint32_t gate_seq;
    uint8_t tcp_pool[4];
    uint8_t tcp_pool_len;
    int settle_timer;
    int owed_timer;
};

static void spawn(slot_t *sl);
static bool device_busy(const slot_t *sl);

static void ungate(slot_t *sl)
{
    sl->gate_gen = ++sl->s->gate_seq;
    sl->gated = false;
    sl->st.why[0] = 0;
}

static int64_t now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (int64_t)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static void free_src(firc_tun_src_t *v, size_t n)
{
    for (size_t i = 0; i < n; i++) {
        free(v[i].link);
        free(v[i].sub.url);
    }
    free(v);
}

static char *dup_or_null(const char *s, bool *ok)
{
    if (s == NULL) {
        return NULL;
    }
    char *d = strdup(s);
    if (d == NULL) {
        *ok = false;
    }
    return d;
}

static firc_err_t copy_cfg(firc_tunnel_t *dst, const firc_tunnel_t *src)
{
    firc_tun_src_t *v = NULL;
    if (src->n_src > 0) {
        v = calloc(src->n_src, sizeof *v);
        if (v == NULL) {
            return FIRC_ERR_NOMEM;
        }
        bool ok = true;
        for (size_t i = 0; i < src->n_src; i++) {
            v[i] = src->src[i];
            v[i].link = dup_or_null(src->src[i].link, &ok);
            v[i].sub.url = dup_or_null(src->src[i].sub.url, &ok);
        }
        if (!ok) {
            free_src(v, src->n_src);
            return FIRC_ERR_NOMEM;
        }
    }
    free_src(dst->src, dst->n_src);
    *dst = *src;
    dst->src = v;
    dst->filter = NULL;
    dst->filter_exclude = NULL;
    dst->order = NULL;
    dst->n_order = 0;
    dst->exclude = NULL;
    dst->n_exclude = 0;
    return FIRC_OK;
}

static void notify(slot_t *sl, const firc_tev_t *ev)
{
    if (!sl->gone && sl->s->on_event != NULL) {
        sl->s->on_event(&sl->st, ev, sl->s->ud);
    }
}

static void put_status(slot_t *sl, firc_tun_status_t status)
{
    if (sl->st.status != status) {
        sl->st.status = status;
        sl->st.since_ms = now_ms();
    }
    if (status == FIRC_TUN_ST_UP) { (void)firc_logrep_ok(&sl->exits, sl->cfg.device, "tunvless"); }
}

static void set_status(slot_t *sl, firc_tun_status_t status)
{
    put_status(sl, status);
    notify(sl, NULL);
}

static void drop_timer(firc_tunsup_t *s, int *id)
{
    if (*id != 0) {
        firc_loop_del_timer(s->loop, *id);
        *id = 0;
    }
}

static void drop_fd(firc_tunsup_t *s, int *fd)
{
    if (*fd >= 0) {
        firc_loop_del_fd(s->loop, *fd);
        close(*fd);
        *fd = -1;
    }
}

static void close_in(slot_t *sl)
{
    drop_fd(sl->s, &sl->in_fd);
    free(sl->in_buf);
    sl->in_buf = NULL;
    sl->in_len = 0;
    sl->in_off = 0;
}

static void release_io(slot_t *sl)
{
    close_in(sl);
    drop_fd(sl->s, &sl->out_fd);
    drop_fd(sl->s, &sl->err_fd);
    drop_timer(sl->s, &sl->reap_timer);
    drop_timer(sl->s, &sl->kill_timer);
    drop_timer(sl->s, &sl->ack_timer);
    sl->acks = 0;
    sl->n_want = 0;
    sl->n_map_want = 0;
    sl->inb_sent = -1;
    sl->inb_warned = false;
    sl->ready = false;
    memset(&sl->out_lines, 0, sizeof sl->out_lines);
    memset(&sl->err_lines, 0, sizeof sl->err_lines);
}

static void slot_free(slot_t *sl)
{
    release_io(sl);
    drop_timer(sl->s, &sl->restart_timer);
    free_src(sl->cfg.src, sl->cfg.n_src);
    free(sl->ix);
    free(sl->want);
    free(sl->map_want);
    free(sl);
}

static void slot_remove(firc_tunsup_t *s, slot_t *sl)
{
    for (size_t i = 0; i < s->n; i++) {
        if (s->v[i] == sl) {
            memmove(&s->v[i], &s->v[i + 1], (s->n - i - 1) * sizeof *s->v);
            s->n--;
            break;
        }
    }
    slot_free(sl);
}

static int exit_code(int status)
{
    if (WIFEXITED(status)) {
        return WEXITSTATUS(status);
    }
    if (WIFSIGNALED(status)) {
        return 128 + WTERMSIG(status);
    }
    return -1;
}

static void restart_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    slot_t *sl = ud;
    sl->restart_timer = 0;
    spawn(sl);
}

static void schedule_backoff(slot_t *sl)
{
    firc_tunsup_t *s = sl->s;
    if (sl->started_ms != 0 && now_ms() - sl->started_ms >= (int64_t)STABLE_S * s->second_ms) {
        sl->next_backoff_s = BACKOFF_BASE_S;
    }
    sl->st.backoff_s = sl->next_backoff_s;
    sl->next_backoff_s = sl->next_backoff_s * 2 > BACKOFF_CAP_S ? BACKOFF_CAP_S : sl->next_backoff_s * 2;
    drop_timer(s, &sl->restart_timer);
    if (firc_loop_add_timer(s->loop, (uint64_t)sl->st.backoff_s * (uint64_t)s->second_ms, 0, restart_cb, sl,
                            &sl->restart_timer) != FIRC_OK) {
        sl->restart_timer = 0;
        firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "cannot schedule a restart");
    }
    firc_logrep_fail(&sl->exits, FIRC_LOG_WARN, sl->cfg.device, "exited (%d), restart in %d s", sl->st.last_exit,
                     sl->st.backoff_s);
    set_status(sl, FIRC_TUN_ST_BACKOFF);
}

static void reaped(slot_t *sl, const char *dev, pid_t pid)
{
    if (sl->s->on_reap != NULL) {
        sl->s->on_reap(sl->cfg.id, dev, pid, sl->s->reap_ud);
    }
}

static void let_go(slot_t *sl)
{
    if (sl->held_ms != 0 && now_ms() - sl->held_ms >= (int64_t)NUDGE_FLOOR_S * sl->s->second_ms) {
        sl->nudge_s = 0;
    }
    sl->held_ms = 0;
}

static void exit_one(slot_t *sl, int code)
{
    firc_tunsup_t *s = sl->s;
    pid_t pid = sl->pid;
    char dev[sizeof sl->run_dev];
    memcpy(dev, sl->run_dev, sizeof dev);
    let_go(sl);
    sl->owed = false;
    release_io(sl);
    sl->pid = 0;
    sl->run_dev[0] = 0;
    sl->st.last_exit = code;
    sl->st.n_active = 0;
    reaped(sl, dev, pid);
    if (sl->gone) {
        slot_remove(s, sl);
        return;
    }
    if (sl->terminating) {
        sl->terminating = false;
        if (sl->cfg.enable) {
            spawn(sl);
        } else {
            set_status(sl, FIRC_TUN_ST_OFF);
        }
        return;
    }
    if (code == 2) {
        firc_log_from(FIRC_LOG_ERROR, sl->cfg.device, "configuration refused, not restarted");
        set_status(sl, FIRC_TUN_ST_BAD_CONFIG);
        return;
    }
    schedule_backoff(sl);
}

static void handle_exit(slot_t *sl, int code)
{
    firc_tunsup_t *s = sl->s;
    exit_one(sl, code);
    for (size_t i = 0; i < s->n; i++) {
        slot_t *o = s->v[i];
        if (o->deferred && o->pid <= 0 && !o->gone && o->cfg.enable && !device_busy(o)) {
            spawn(o);
        }
    }
}

static void check_reap(slot_t *sl);

static void reap_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    slot_t *sl = ud;
    sl->reap_timer = 0;
    check_reap(sl);
}

static void check_reap(slot_t *sl)
{
    if (sl->out_fd >= 0 || sl->err_fd >= 0 || sl->pid <= 0 || sl->reap_timer != 0) {
        return;
    }
    int status = 0;
    pid_t r;
    do {
        r = waitpid(sl->pid, &status, WNOHANG);
    } while (r < 0 && errno == EINTR);
    if (r == sl->pid) {
        handle_exit(sl, exit_code(status));
    } else if (r < 0) {
        handle_exit(sl, -1);
    } else if (firc_loop_add_timer(sl->s->loop, REAP_RETRY_MS, 0, reap_cb, sl, &sl->reap_timer) != FIRC_OK) {
        sl->reap_timer = 0;
    }
}

static void warn_bad_once(slot_t *sl)
{
    if (!sl->warned_bad) {
        sl->warned_bad = true;
        firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "unreadable event from tunvless ignored");
    }
}

static uint64_t link_hash_n(const char *link, size_t n)
{
    uint64_t h = 1469598103934665603ULL;
    for (size_t i = 0; i < n; i++) {
        h ^= (unsigned char)link[i];
        h *= 1099511628211ULL;
    }
    return h;
}

static uint64_t link_hash(const char *link)
{
    return link_hash_n(link, strlen(link));
}

static bool ix_add(slot_t *sl, uint64_t h)
{
    for (size_t k = 0; k < sl->n_ix; k++) {
        if (sl->ix[k] == h) {
            return true;
        }
    }
    if (sl->n_ix >= INDEX_MAX) {
        return false;
    }
    uint64_t *grown = realloc(sl->ix, (sl->n_ix + 1) * sizeof *grown);
    if (grown == NULL) {
        return false;
    }
    sl->ix = grown;
    sl->ix[sl->n_ix++] = h;
    return true;
}

static bool index_add(slot_t *sl)
{
    for (size_t i = 0; i < sl->cfg.n_src; i++) {
        if (sl->cfg.src[i].kind == FIRC_TUN_SRC_LINK && !ix_add(sl, link_hash(sl->cfg.src[i].link))) {
            return false;
        }
    }
    return true;
}

static void index_inbound_links(slot_t *sl, const char *text, size_t len)
{
    for (size_t at = 0; at < len;) {
        const char *end = memchr(text + at, '\n', len - at);
        size_t stop = end != NULL ? (size_t)(end - text) : len;
        size_t from = at;
        for (int f = 0; f < 3 && from < stop; f++) {
            const char *sp = memchr(text + from, ' ', stop - from);
            from = sp != NULL ? (size_t)(sp - text) + 1 : stop;
        }
        bool dash = stop - from == 1 && text[from] == '-';
        if (from < stop && !dash && !ix_add(sl, link_hash_n(text + from, stop - from)) && !sl->drift) {
            sl->drift = true;
            firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "node index full, node states go by name until it restarts");
        }
        at = stop + 1;
    }
}

static size_t distinct_links(const slot_t *sl)
{
    size_t n = 0;
    for (size_t i = 0; i < sl->cfg.n_src; i++) {
        if (sl->cfg.src[i].kind != FIRC_TUN_SRC_LINK) {
            continue;
        }
        bool seen = false;
        for (size_t k = 0; k < i && !seen; k++) {
            seen = sl->cfg.src[k].kind == FIRC_TUN_SRC_LINK && strcmp(sl->cfg.src[k].link, sl->cfg.src[i].link) == 0;
        }
        n += seen ? 0 : 1;
    }
    return n;
}

static void took(slot_t *sl, int count)
{
    if (sl->n_want == 0) {
        return;
    }
    size_t want = sl->want[0];
    memmove(&sl->want[0], &sl->want[1], (sl->n_want - 1) * sizeof *sl->want);
    sl->n_want--;
    if ((size_t)count != want && !sl->drift) {
        sl->drift = true;
        firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "tunvless took %d of %zu nodes, node states go by name until it restarts",
                  count, want);
    }
}

static void took_map(slot_t *sl, firc_tev_t *ev)
{
    if (sl->n_map_want == 0) {
        return;
    }
    size_t want = sl->map_want[0];
    memmove(&sl->map_want[0], &sl->map_want[1], (sl->n_map_want - 1) * sizeof *sl->map_want);
    sl->n_map_want--;
    ev->expect = (int)want;
    ev->drift = ev->count < 0 || (size_t)ev->count != want;
}

static bool known_index(const slot_t *sl, int index)
{
    return !sl->drift && index >= 0 && (size_t)index < sl->n_ix;
}

static int pos_of(const slot_t *sl, int index)
{
    if (!known_index(sl, index)) {
        return -1;
    }
    for (size_t i = 0; i < sl->cfg.n_src; i++) {
        if (sl->cfg.src[i].kind == FIRC_TUN_SRC_LINK && link_hash(sl->cfg.src[i].link) == sl->ix[index]) {
            return (int)i;
        }
    }
    return -1;
}

static void restart_child(slot_t *sl);

static void active_positions(const slot_t *sl, firc_tev_t *ev)
{
    size_t n = ev->n_active < 8 ? ev->n_active : 8;
    size_t left = 0;
    for (size_t j = 0; j < n; j++) {
        ev->active_pos[j] = -1;
        left += known_index(sl, ev->active_index[j]) ? 1 : 0;
    }
    for (size_t i = 0; i < sl->cfg.n_src && left > 0; i++) {
        if (sl->cfg.src[i].kind != FIRC_TUN_SRC_LINK) {
            continue;
        }
        uint64_t h = link_hash(sl->cfg.src[i].link);
        for (size_t j = 0; j < n; j++) {
            if (ev->active_pos[j] < 0 && known_index(sl, ev->active_index[j]) && sl->ix[ev->active_index[j]] == h) {
                ev->active_pos[j] = (int)i;
                left--;
            }
        }
    }
}

static bool complete_head(const slot_t *sl, const firc_tev_t *ev, bool part)
{
    size_t want = (size_t)sl->cfg.active < 8 ? (size_t)sl->cfg.active : 8;
    size_t head[8];
    uint64_t hash[8];
    size_t n = 0;
    for (size_t i = 0; i < sl->cfg.n_src && n < want; i++) {
        if (sl->cfg.src[i].kind != FIRC_TUN_SRC_LINK) {
            continue;
        }
        const char *link = sl->cfg.src[i].link;
        uint64_t h = link_hash(link);
        bool seen = false;
        for (size_t k = 0; k < n && !seen; k++) {
            seen = hash[k] == h && strcmp(sl->cfg.src[head[k]].link, link) == 0;
        }
        if (!seen) {
            head[n] = i;
            hash[n++] = h;
        }
    }
    if (ev->n_active > n || (!part && ev->n_active < n)) {
        return false;
    }
    for (size_t j = 0; j < ev->n_active && j < 8; j++) {
        bool in = false;
        for (size_t k = 0; k < n && !in; k++) {
            in = ev->active_pos[j] == (int)head[k];
        }
        if (!in) {
            return false;
        }
    }
    return true;
}

static void ack_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    slot_t *sl = ud;
    sl->ack_timer = 0;
    firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "tunvless did not take the %s in %d s, restarting",
                  sl->acks > 0 ? "node list" : "address map", ACK_S);
    restart_child(sl);
}

static void arm_ack(slot_t *sl)
{
    if (sl->ack_timer != 0 || (sl->acks == 0 && sl->n_map_want == 0) || !sl->ready || sl->terminating) {
        return;
    }
    if (firc_loop_add_timer(sl->s->loop, (uint64_t)ACK_S * (uint64_t)sl->s->second_ms, 0, ack_cb, sl,
                            &sl->ack_timer) != FIRC_OK) {
        sl->ack_timer = 0;
    }
}

static void on_out_line(const char *line, size_t len, void *ud)
{
    slot_t *sl = ud;
    firc_tev_t ev;
    firc_tun_event_parse(line, len, &ev);
    if (sl->terminating && (ev.kind == FIRC_TEV_READY || ev.kind == FIRC_TEV_ACTIVE || ev.kind == FIRC_TEV_NO_NODE ||
                            ev.kind == FIRC_TEV_NODE_UP || ev.kind == FIRC_TEV_NODE_DOWN)) {
        return;
    }
    switch (ev.kind) {
    case FIRC_TEV_READY:
        if (ev.dev[0] == 0) {
            warn_bad_once(sl);
            return;
        }
        sl->ready = true;
        arm_ack(sl);
        break;
    case FIRC_TEV_NODES:
        if (sl->acks > 0) {
            sl->acks--;
        }
        took(sl, ev.count);
        drop_timer(sl->s, &sl->ack_timer);
        arm_ack(sl);
        break;
    case FIRC_TEV_MAP:
        took_map(sl, &ev);
        drop_timer(sl->s, &sl->ack_timer);
        arm_ack(sl);
        break;
    case FIRC_TEV_CLOSE:
    case FIRC_TEV_MISS:
    case FIRC_TEV_BYTES:
    case FIRC_TEV_INBOUND:
    case FIRC_TEV_IBYTES:
        break;
    case FIRC_TEV_INBOUNDS:
        if (sl->inb_sent >= 0 && ev.count != sl->inb_sent) {
            sl->drift = true;
            if (!sl->inb_warned) {
                sl->inb_warned = true;
                firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "tunvless took %d inbound lines of %d sent", ev.count,
                              sl->inb_sent);
            }
        }
        break;
    case FIRC_TEV_PINS_FULL:
        if (!sl->terminating) {
            firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "pin table full, restarting");
            restart_child(sl);
        }
        return;
    case FIRC_TEV_ACTIVE:
        active_positions(sl, &ev);
        if (ev.n_active > 0 && sl->starved) {
            int64_t now = now_ms();
            if (!complete_head(sl, &ev, sl->demand) &&
                (sl->recovered_ms == 0 || now - sl->recovered_ms >= (int64_t)RECOVERY_FLOOR_S * sl->s->second_ms)) {
                firc_log_from(FIRC_LOG_INFO, sl->cfg.device, "nodes answer again, starting over from the first");
                sl->recovered_ms = now;
                restart_child(sl);
                return;
            }
            sl->starved = false;
        }
        if (ev.n_active > 0) {
            sl->owed = false;
        }
        if (ev.n_active == 0) {
            let_go(sl);
        } else if (sl->held_ms == 0) {
            sl->held_ms = now_ms();
        }
        memcpy(sl->st.active, ev.active, sizeof sl->st.active);
        sl->st.n_active = ev.n_active;
        put_status(sl, ev.n_active > 0 ? FIRC_TUN_ST_UP : FIRC_TUN_ST_NO_NODE);
        break;
    case FIRC_TEV_NO_NODE:
        let_go(sl);
        sl->starved = true;
        sl->demand = false;
        sl->pause_end_ms = now_ms() + (int64_t)ev.retry_s * sl->s->second_ms;
        sl->st.n_active = 0;
        put_status(sl, FIRC_TUN_ST_NO_NODE);
        break;
    case FIRC_TEV_NODE_DOWN:
    case FIRC_TEV_NODE_UP:
        if (ev.node[0] == 0) {
            warn_bad_once(sl);
            return;
        }
        ev.pos = pos_of(sl, ev.index);
        break;
    case FIRC_TEV_BAD:
    default:
        warn_bad_once(sl);
        return;
    }
    notify(sl, &ev);
}

static void on_err_line(const char *line, size_t len, void *ud)
{
    slot_t *sl = ud;
    size_t skip;
    firc_log_level_t level = firc_tun_err_level(line, len, &skip);
    firc_log_from(level, sl->cfg.device, "%.*s", (int)(len - skip), line + skip);
}

static void drain(slot_t *sl, int *fd, firc_tun_lines_t *lines, firc_tun_line_fn fn, bool *warned,
                  const char *what)
{
    char buf[4096];
    for (;;) {
        ssize_t n = read(*fd, buf, sizeof buf);
        if (n > 0) {
            if (firc_tun_lines_feed(lines, buf, (size_t)n, fn, sl) > 0 && !*warned) {
                *warned = true;
                firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "oversize %s line from tunvless dropped", what);
            }
            if (*fd < 0) {
                return;
            }
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && errno == EAGAIN) {
            return;
        }
        drop_fd(sl->s, fd);
        check_reap(sl);
        return;
    }
}

static void out_cb(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)fd;
    (void)events;
    slot_t *sl = ud;
    drain(sl, &sl->out_fd, &sl->out_lines, on_out_line, &sl->warned_drop_out, "stdout");
}

static void err_cb(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    (void)loop;
    (void)fd;
    (void)events;
    slot_t *sl = ud;
    drain(sl, &sl->err_fd, &sl->err_lines, on_err_line, &sl->warned_drop_err, "stderr");
}

static void in_cb(firc_loop_t *loop, int fd, uint32_t events, void *ud)
{
    slot_t *sl = ud;
    if (sl->in_off >= sl->in_len && (events & (EPOLLERR | EPOLLHUP)) != 0) {
        close_in(sl);
        return;
    }
    while (sl->in_off < sl->in_len) {
        ssize_t n = write(fd, sl->in_buf + sl->in_off, sl->in_len - sl->in_off);
        if (n > 0) {
            sl->in_off += (size_t)n;
            continue;
        }
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && errno == EAGAIN) {
            return;
        }
        firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "node list not delivered: %s", strerror(errno));
        close_in(sl);
        if (sl->pid > 0) {
            kill(sl->pid, SIGTERM);
        }
        return;
    }
    free(sl->in_buf);
    sl->in_buf = NULL;
    sl->in_len = 0;
    sl->in_off = 0;
    if (firc_loop_mod_fd(loop, fd, 0) != FIRC_OK) {
        close_in(sl);
    }
}

static char *node_list(const firc_tunnel_t *t, size_t *len);

static bool queue_nodes(slot_t *sl)
{
    if (sl->in_fd < 0 || !index_add(sl)) {
        return false;
    }
    static const char cmd[] = "nodes\n";
    size_t ln = 0;
    char *list = node_list(&sl->cfg, &ln);
    size_t pend = sl->in_len - sl->in_off;
    char *buf = list == NULL ? NULL : malloc(pend + sizeof cmd - 1 + ln);
    if (buf == NULL) {
        free(list);
        return false;
    }
    if (pend > 0) {
        memcpy(buf, sl->in_buf + sl->in_off, pend);
    }
    memcpy(buf + pend, cmd, sizeof cmd - 1);
    memcpy(buf + pend + sizeof cmd - 1, list, ln);
    free(list);
    free(sl->in_buf);
    sl->in_buf = buf;
    sl->in_len = pend + sizeof cmd - 1 + ln;
    sl->in_off = 0;
    size_t *grown = realloc(sl->want, (sl->n_want + 1) * sizeof *grown);
    if (grown == NULL || firc_loop_mod_fd(sl->s->loop, sl->in_fd, EPOLLOUT) != FIRC_OK) {
        if (grown != NULL) {
            sl->want = grown;
        }
        return false;
    }
    sl->want = grown;
    sl->want[sl->n_want++] = distinct_links(sl);
    sl->acks++;
    arm_ack(sl);
    return true;
}

static char *node_list(const firc_tunnel_t *t, size_t *len)
{
    size_t n = 1;
    for (size_t i = 0; i < t->n_src; i++) {
        if (t->src[i].kind == FIRC_TUN_SRC_LINK) {
            n += strlen(t->src[i].link) + 1;
        }
    }
    char *buf = malloc(n);
    if (buf == NULL) {
        return NULL;
    }
    size_t off = 0;
    for (size_t i = 0; i < t->n_src; i++) {
        if (t->src[i].kind != FIRC_TUN_SRC_LINK) {
            continue;
        }
        size_t l = strlen(t->src[i].link);
        memcpy(buf + off, t->src[i].link, l);
        off += l;
        buf[off++] = '\n';
    }
    buf[off++] = '\n';
    *len = off;
    return buf;
}

static int set_nonblock(int fd)
{
    int fl = fcntl(fd, F_GETFL, 0);
    return fl < 0 ? -1 : fcntl(fd, F_SETFL, fl | O_NONBLOCK);
}

static void close_pair(int p[2])
{
    if (p[0] >= 0) {
        close(p[0]);
    }
    if (p[1] >= 0) {
        close(p[1]);
    }
}

static int start_child(firc_tunsup_t *s, const char *const argv[], pid_t *pid, int *in_fd, int *out_fd,
                       int *err_fd)
{
    int in[2] = {-1, -1};
    int out[2] = {-1, -1};
    int err[2] = {-1, -1};
    if (pipe2(in, O_CLOEXEC) != 0 || pipe2(out, O_CLOEXEC) != 0 || pipe2(err, O_CLOEXEC) != 0) {
        int e = errno;
        close_pair(in);
        close_pair(out);
        close_pair(err);
        return e;
    }
    int rc = 0;
    if (set_nonblock(in[1]) != 0 || set_nonblock(out[0]) != 0 || set_nonblock(err[0]) != 0) {
        rc = errno;
    }
    posix_spawn_file_actions_t fa;
    posix_spawnattr_t attr;
    bool fa_ok = false;
    bool attr_ok = false;
    if (rc == 0) {
        rc = posix_spawn_file_actions_init(&fa);
        fa_ok = rc == 0;
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, in[0], STDIN_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, out[1], STDOUT_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawn_file_actions_adddup2(&fa, err[1], STDERR_FILENO);
    }
    if (rc == 0) {
        rc = posix_spawnattr_init(&attr);
        attr_ok = rc == 0;
    }
    sigset_t all;
    sigset_t none;
    sigfillset(&all);
    sigemptyset(&none);
    if (rc == 0) {
        rc = posix_spawnattr_setsigdefault(&attr, &all);
    }
    if (rc == 0) {
        rc = posix_spawnattr_setsigmask(&attr, &none);
    }
    if (rc == 0) {
        rc = posix_spawnattr_setflags(&attr, POSIX_SPAWN_SETSIGDEF | POSIX_SPAWN_SETSIGMASK);
    }
    if (rc == 0) {
        rc = posix_spawnp(pid, s->binary, &fa, &attr, (char *const *)argv, environ);
    }
    if (attr_ok) {
        posix_spawnattr_destroy(&attr);
    }
    if (fa_ok) {
        posix_spawn_file_actions_destroy(&fa);
    }
    close(in[0]);
    close(out[1]);
    close(err[1]);
    if (rc != 0) {
        close(in[1]);
        close(out[0]);
        close(err[0]);
        return rc;
    }
    *in_fd = in[1];
    *out_fd = out[0];
    *err_fd = err[0];
    return 0;
}

static bool device_busy(const slot_t *sl)
{
    const firc_tunsup_t *s = sl->s;
    for (size_t i = 0; i < s->n; i++) {
        const slot_t *o = s->v[i];
        if (o != sl && o->pid > 0 && firc_tundev_name_index(o->run_dev) == firc_tunnel_index(&sl->cfg)) {
            return true;
        }
    }
    return false;
}

static void spawn_past(slot_t *sl, bool open)
{
    firc_tunsup_t *s = sl->s;
    const firc_tunnel_t *t = &sl->cfg;
    sl->deferred = device_busy(sl);
    if (sl->deferred) {
        firc_log_from(FIRC_LOG_INFO, t->device, "waiting for the previous holder of the device to exit");
        sl->st.why[0] = 0;
        sl->st.n_active = 0;
        set_status(sl, FIRC_TUN_ST_STARTING);
        return;
    }
    if (!open && s->gate != NULL) {
        uint32_t gen = ++s->gate_seq;
        sl->gate_gen = gen;
        if (s->gate(t->id, t->device, firc_tunnel_index(t), gen, s->gate_ud) == FIRC_TUN_GATE_WAIT) {
            sl->gated = true;
            snprintf(sl->st.why, sizeof sl->st.why, "waiting for the router");
            sl->st.n_active = 0;
            set_status(sl, FIRC_TUN_ST_STARTING);
            return;
        }
    }
    ungate(sl);
    char addr[20];
    uint8_t addr6[16];
    firc_tun_addr(firc_tunnel_index(t), addr, addr6);
    char holder[16];
    int held = firc_tun_addr_holder(firc_tunnel_index(t), t->device, holder);
    char shown[INET6_ADDRSTRLEN];
    if (held == AF_INET6) {
        inet_ntop(AF_INET6, addr6, shown, sizeof shown);
    } else {
        snprintf(shown, sizeof shown, "%.*s", (int)strcspn(addr, "/"), addr);
    }
    if (held > 0) {
        if (!sl->warned_holder) {
            sl->warned_holder = true;
            firc_log_from(FIRC_LOG_ERROR, t->device, "%s is already on %s", shown, holder);
        }
        sl->st.n_active = 0;
        set_status(sl, FIRC_TUN_ST_BAD_CONFIG);
        return;
    }
    sl->warned_holder = false;
    if (held < 0 && !sl->warned_unread) {
        sl->warned_unread = true;
        firc_log_from(FIRC_LOG_WARN, t->device, "cannot read the interface addresses, %s not checked", shown);
    } else if (held == 0) {
        sl->warned_unread = false;
    }
    char active[16];
    char interval[16];
    char silence[16];
    char timeout[16];
    char mark[16];
    char tcp_port[8];
    char tcp_pool[24];
    snprintf(tcp_port, sizeof tcp_port, "%u", (unsigned)firc_tunnel_tcp_port(t));
    snprintf(tcp_pool, sizeof tcp_pool, "%u.%u.%u.%u/%u", s->tcp_pool[0], s->tcp_pool[1], s->tcp_pool[2],
             s->tcp_pool[3], s->tcp_pool_len);
    snprintf(active, sizeof active, "%d", t->active);
    snprintf(interval, sizeof interval, "%d", t->interval_s);
    snprintf(silence, sizeof silence, "%d", t->silence_s);
    snprintf(timeout, sizeof timeout, "%d", t->timeout_s);
    snprintf(mark, sizeof mark, "0x%x", (unsigned)sl->mark);
    const char *argv[32];
    size_t a = 0;
    argv[a++] = s->binary;
    argv[a++] = "--control";
    argv[a++] = "-d";
    argv[a++] = t->device;
    argv[a++] = "-a";
    argv[a++] = addr;
    argv[a++] = "-A";
    argv[a++] = active;
    argv[a++] = "--by";
    argv[a++] = t->by;
    argv[a++] = "--interval";
    argv[a++] = interval;
    argv[a++] = "--silence";
    argv[a++] = silence;
    argv[a++] = "-t";
    argv[a++] = timeout;
    if (sl->mark != 0) {
        argv[a++] = "-m";
        argv[a++] = mark;
    }
    if (t->insecure) {
        argv[a++] = "--insecure";
    }
    if (t->ca[0] != 0) {
        argv[a++] = "--ca";
        argv[a++] = t->ca;
    }
    if (t->fingerprint[0] != 0) {
        argv[a++] = "--fp";
        argv[a++] = t->fingerprint;
    }
    if (firc_tunnel_tcp_port(t) != 0) {
        argv[a++] = "--tcp-port";
        argv[a++] = tcp_port;
        if (s->tcp_pool_len != 0) {
            argv[a++] = "--tcp-pool";
            argv[a++] = tcp_pool;
        }
    }
    argv[a] = NULL;

    sl->st.n_active = 0;
    sl->warned_bad = false;
    sl->warned_drop_out = false;
    sl->warned_drop_err = false;
    sl->started_ms = 0;
    sl->n_ix = 0;
    sl->drift = false;
    sl->starved = sl->nudging;
    sl->demand = sl->nudging;
    sl->nudging = false;
    sl->owed = false;
    sl->held_ms = 0;
    sl->pause_end_ms = 0;
    (void)index_add(sl);
    sl->in_buf = node_list(t, &sl->in_len);
    sl->in_off = 0;
    int rc = sl->in_buf == NULL ? ENOMEM : start_child(s, argv, &sl->pid, &sl->in_fd, &sl->out_fd, &sl->err_fd);
    if (rc != 0) {
        free(sl->in_buf);
        sl->in_buf = NULL;
        sl->pid = 0;
        firc_log_from(FIRC_LOG_WARN, t->device, "cannot start %s: %s", s->binary, strerror(rc));
        sl->st.last_exit = -1;
        schedule_backoff(sl);
        return;
    }
    sl->started_ms = now_ms();
    snprintf(sl->run_dev, sizeof sl->run_dev, "%s", t->device);
    sl->run_mark = sl->mark;
    if (firc_loop_add_fd(s->loop, sl->out_fd, EPOLLIN, out_cb, sl) != FIRC_OK ||
        firc_loop_add_fd(s->loop, sl->err_fd, EPOLLIN, err_cb, sl) != FIRC_OK ||
        firc_loop_add_fd(s->loop, sl->in_fd, EPOLLOUT, in_cb, sl) != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, t->device, "cannot watch the child");
        kill(sl->pid, SIGKILL);
        int status = 0;
        while (waitpid(sl->pid, &status, 0) < 0 && errno == EINTR) {
        }
        release_io(sl);
        sl->pid = 0;
        sl->st.last_exit = -1;
        schedule_backoff(sl);
        return;
    }
    set_status(sl, FIRC_TUN_ST_STARTING);
}

static void spawn(slot_t *sl)
{
    spawn_past(sl, false);
}

static void terminate(slot_t *sl);

static void kill_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    slot_t *sl = ud;
    sl->kill_timer = 0;
    if (sl->pid > 0) {
        kill(sl->pid, SIGKILL);
    }
}

static void terminate(slot_t *sl)
{
    kill(sl->pid, SIGTERM);
    drop_timer(sl->s, &sl->ack_timer);
    sl->owed = false;
    if (sl->terminating && sl->kill_timer != 0) {
        return;
    }
    sl->terminating = true;
    drop_timer(sl->s, &sl->kill_timer);
    if (firc_loop_add_timer(sl->s->loop, (uint64_t)TERM_GRACE_S * (uint64_t)sl->s->second_ms, 0, kill_cb, sl, &sl->kill_timer) != FIRC_OK) {
        sl->kill_timer = 0;
    }
}

static slot_t *find_slot(const firc_tunsup_t *s, const char *id)
{
    for (size_t i = 0; i < s->n; i++) {
        if (!s->v[i]->gone && strcmp(s->v[i]->cfg.id, id) == 0) {
            return s->v[i];
        }
    }
    return NULL;
}

static slot_t *slot_new(firc_tunsup_t *s, const firc_tunnel_t *t, uint32_t mark)
{
    slot_t *sl = calloc(1, sizeof *sl);
    if (sl == NULL) {
        return NULL;
    }
    if (copy_cfg(&sl->cfg, t) != FIRC_OK) {
        free(sl);
        return NULL;
    }
    sl->s = s;
    sl->mark = mark;
    sl->in_fd = -1;
    sl->out_fd = -1;
    sl->err_fd = -1;
    sl->inb_sent = -1;
    sl->next_backoff_s = BACKOFF_BASE_S;
    snprintf(sl->st.id, sizeof sl->st.id, "%s", t->id);
    snprintf(sl->st.device, sizeof sl->st.device, "%s", t->device);
    sl->st.status = FIRC_TUN_ST_OFF;
    sl->st.since_ms = now_ms();
    sl->st.uplink_ok = true;
    return sl;
}

static size_t *start_order(const firc_tunnels_t *want)
{
    size_t n = want->n;
    size_t *order = calloc(n + 1, sizeof *order);
    bool *placed = calloc(n + 1, sizeof *placed);
    if (order == NULL || placed == NULL) {
        free(order);
        free(placed);
        return NULL;
    }
    size_t k = 0;
    while (k < n) {
        bool progress = false;
        for (size_t i = 0; i < n; i++) {
            if (placed[i]) {
                continue;
            }
            const firc_tunnel_t *t = &want->t[i];
            bool ready = true;
            if (t->uplink == FIRC_UPLINK_TUNNEL) {
                for (size_t j = 0; j < n; j++) {
                    if (j != i && !placed[j] && strcmp(want->t[j].id, t->uplink_ref) == 0) {
                        ready = false;
                    }
                }
            }
            if (ready) {
                placed[i] = true;
                order[k++] = i;
                progress = true;
            }
        }
        if (!progress) {
            for (size_t i = 0; i < n; i++) {
                if (!placed[i]) {
                    placed[i] = true;
                    order[k++] = i;
                }
            }
        }
    }
    free(placed);
    return order;
}

static void retire(firc_tunsup_t *s, slot_t *sl)
{
    if (sl->pid > 0) {
        sl->gone = true;
        drop_timer(s, &sl->restart_timer);
        terminate(sl);
        return;
    }
    slot_remove(s, sl);
}

static void reconfigure(slot_t *sl, const firc_tunnel_t *t, uint32_t mark)
{
    if (copy_cfg(&sl->cfg, t) != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, t->device, "out of memory, left as it was");
        return;
    }
    sl->mark = mark;
    snprintf(sl->st.device, sizeof sl->st.device, "%s", t->device);
    sl->next_backoff_s = BACKOFF_BASE_S;
    sl->deferred = false;
    ungate(sl);
    sl->nudging = false;
    drop_timer(sl->s, &sl->restart_timer);
    if (sl->pid > 0) {
        terminate(sl);
    } else if (t->enable) {
        spawn(sl);
    } else {
        set_status(sl, FIRC_TUN_ST_OFF);
    }
}

static void restart_child(slot_t *sl)
{
    firc_tunsup_t *s = sl->s;
    sl->nudging = false;
    sl->start_seq = ++s->seq;
    sl->next_backoff_s = BACKOFF_BASE_S;
    drop_timer(s, &sl->restart_timer);
    if (sl->pid > 0) {
        terminate(sl);
    } else if (!sl->deferred) {
        spawn(sl);
    }
}

static void update_nodes(slot_t *sl, const firc_tunnel_t *t, uint32_t mark)
{
    if (sl->pid <= 0 || sl->terminating) {
        if (sl->pid <= 0 && t->enable) {
            sl->start_seq = ++sl->s->seq;
        }
        reconfigure(sl, t, mark);
        return;
    }
    if (copy_cfg(&sl->cfg, t) != FIRC_OK) {
        firc_log_from(FIRC_LOG_WARN, t->device, "out of memory, left as it was");
        return;
    }
    if (queue_nodes(sl)) {
        sl->update_seq = ++sl->s->seq;
        return;
    }
    firc_log_from(FIRC_LOG_WARN, t->device, "the new node list cannot be sent, restarting");
    restart_child(sl);
}

static bool listed(slot_t *const *v, size_t n, const slot_t *sl)
{
    for (size_t i = 0; i < n; i++) {
        if (v[i] == sl) {
            return true;
        }
    }
    return false;
}

static void sort_like(firc_tunsup_t *s, const firc_tunnels_t *want)
{
    slot_t **v = calloc(s->n + 1, sizeof *v);
    if (v == NULL) {
        return;
    }
    size_t n = 0;
    for (size_t i = 0; i < want->n && n < s->n; i++) {
        slot_t *sl = find_slot(s, want->t[i].id);
        if (sl != NULL && !listed(v, n, sl)) {
            v[n++] = sl;
        }
    }
    for (size_t i = 0; i < s->n && n < s->n; i++) {
        if (!listed(v, n, s->v[i])) {
            v[n++] = s->v[i];
        }
    }
    memcpy(s->v, v, n * sizeof *v);
    free(v);
}

firc_err_t firc_tunsup_apply(firc_tunsup_t *s, const firc_tunnels_t *want, const uint32_t *marks)
{
    assert(pthread_equal(s->loop_thread, pthread_self()));
    if (!pthread_equal(s->loop_thread, pthread_self()) || s->stopped) {
        return FIRC_ERR_STATE;
    }
    for (size_t i = 0; i < want->n; i++) {
        if (firc_tunnel_index(&want->t[i]) < 0) {
            return FIRC_ERR_INVAL;
        }
    }
    size_t *order = start_order(want);
    if (order == NULL) {
        return FIRC_ERR_NOMEM;
    }
    for (size_t i = 0; i < s->n;) {
        slot_t *sl = s->v[i];
        bool wanted = false;
        for (size_t j = 0; j < want->n && !sl->gone; j++) {
            wanted = wanted || strcmp(want->t[j].id, sl->cfg.id) == 0;
        }
        if (sl->gone || wanted) {
            i++;
            continue;
        }
        size_t before = s->n;
        retire(s, sl);
        if (s->n == before) {
            i++;
        }
    }
    slot_t **grown = realloc(s->v, (s->n + want->n + 1) * sizeof *grown);
    if (grown == NULL) {
        free(order);
        return FIRC_ERR_NOMEM;
    }
    s->v = grown;
    firc_err_t rc = FIRC_OK;
    for (size_t k = 0; k < want->n; k++) {
        size_t i = order[k];
        const firc_tunnel_t *t = &want->t[i];
        uint32_t mark = marks != NULL ? marks[i] : 0;
        slot_t *sl = find_slot(s, t->id);
        if (sl == NULL) {
            sl = slot_new(s, t, mark);
            if (sl == NULL) {
                rc = FIRC_ERR_NOMEM;
                continue;
            }
            s->v[s->n++] = sl;
            if (t->enable) {
                sl->start_seq = ++s->seq;
                spawn(sl);
            } else {
                notify(sl, NULL);
            }
        } else if (!firc_tunnel_same_argv(&sl->cfg, t) || sl->mark != mark) {
            if (t->enable) {
                sl->start_seq = ++s->seq;
            }
            reconfigure(sl, t, mark);
        } else if (!firc_tunnel_same_nodes(&sl->cfg, t)) {
            update_nodes(sl, t, mark);
        }
    }
    free(order);
    sort_like(s, want);
    return rc;
}

firc_tunsup_t *firc_tunsup_new(firc_loop_t *loop, const char *binary, firc_tun_event_fn on_event, void *ud)
{
    firc_tunsup_t *s = calloc(1, sizeof *s);
    if (s == NULL) {
        return NULL;
    }
    s->binary = strdup(binary);
    if (s->binary == NULL) {
        free(s);
        return NULL;
    }
    s->loop = loop;
    s->on_event = on_event;
    s->ud = ud;
    s->loop_thread = pthread_self();
    s->second_ms = 1000;
    return s;
}

size_t firc_tunsup_states(const firc_tunsup_t *s, firc_tun_state_t *out, size_t cap)
{
    size_t n = 0;
    for (size_t i = 0; i < s->n; i++) {
        if (s->v[i]->gone) {
            continue;
        }
        if (n < cap) {
            out[n] = s->v[i]->st;
        }
        n++;
    }
    return n;
}

static bool reap_now(slot_t *sl)
{
    int status = 0;
    pid_t r;
    do {
        r = waitpid(sl->pid, &status, WNOHANG);
    } while (r < 0 && errno == EINTR);
    if (r == 0) {
        return false;
    }
    sl->st.last_exit = r == sl->pid ? exit_code(status) : -1;
    sl->pid = 0;
    return true;
}

void firc_tunsup_stop(firc_tunsup_t *s, int grace_ms)
{
    assert(pthread_equal(s->loop_thread, pthread_self()));
    drop_timer(s, &s->settle_timer);
    drop_timer(s, &s->owed_timer);
    for (size_t i = 0; i < s->n; i++) {
        slot_t *sl = s->v[i];
        release_io(sl);
        drop_timer(s, &sl->restart_timer);
        ungate(sl);
        sl->stop_pid = sl->pid;
        if (sl->pid > 0) {
            kill(sl->pid, SIGTERM);
        }
    }
    int64_t t0 = now_ms();
    for (;;) {
        size_t left = 0;
        for (size_t i = 0; i < s->n; i++) {
            if (s->v[i]->pid > 0 && !reap_now(s->v[i])) {
                left++;
            }
        }
        if (left == 0 || now_ms() - t0 >= grace_ms) {
            break;
        }
        struct timespec ts = {0, STOP_POLL_MS * 1000000L};
        nanosleep(&ts, NULL);
    }
    for (size_t i = 0; i < s->n; i++) {
        slot_t *sl = s->v[i];
        if (sl->pid > 0) {
            kill(sl->pid, SIGKILL);
            int status = 0;
            pid_t r;
            do {
                r = waitpid(sl->pid, &status, 0);
            } while (r < 0 && errno == EINTR);
            sl->st.last_exit = r == sl->pid ? exit_code(status) : -1;
            sl->pid = 0;
        }
        let_go(sl);
        sl->owed = false;
        sl->terminating = false;
        sl->deferred = false;
        char dev[sizeof sl->run_dev];
        memcpy(dev, sl->run_dev, sizeof dev);
        sl->run_dev[0] = 0;
        sl->st.n_active = 0;
        put_status(sl, FIRC_TUN_ST_OFF);
        notify(sl, NULL);
        if (sl->stop_pid > 0) {
            pid_t pid = sl->stop_pid;
            sl->stop_pid = 0;
            reaped(sl, dev, pid);
        }
    }
    for (size_t i = 0; i < s->n;) {
        if (s->v[i]->gone) {
            slot_remove(s, s->v[i]);
        } else {
            i++;
        }
    }
    s->stopped = true;
}

void firc_tunsup_free(firc_tunsup_t *s)
{
    if (s == NULL) {
        return;
    }
    firc_tunsup_stop(s, 0);
    for (size_t i = 0; i < s->n; i++) {
        slot_free(s->v[i]);
    }
    free(s->v);
    free(s->binary);
    free(s);
}

void firc_tunsup_set_on_reap(firc_tunsup_t *s, firc_tun_reap_fn fn, void *ud)
{
    s->on_reap = fn;
    s->reap_ud = ud;
}

void firc_tunsup_set_gate(firc_tunsup_t *s, firc_tun_gate_fn fn, void *ud)
{
    s->gate = fn;
    s->gate_ud = ud;
}

void firc_tunsup_gate_open(firc_tunsup_t *s, const char *id, uint32_t gen)
{
    slot_t *sl = find_slot(s, id);
    if (sl == NULL || !sl->gated || sl->gate_gen != gen) {
        return;
    }
    sl->gated = false;
    spawn_past(sl, true);
}

bool firc_tunsup_holds_device(const firc_tunsup_t *s, const char *device)
{
    for (size_t i = 0; i < s->n; i++) {
        if (s->v[i]->pid > 0 && strcmp(s->v[i]->run_dev, device) == 0) {
            return true;
        }
    }
    return false;
}

void firc_tunsup_set_uplink_ok(firc_tunsup_t *s, const char *id, bool ok)
{
    slot_t *sl = find_slot(s, id);
    if (sl != NULL) {
        sl->st.uplink_ok = ok;
    }
}

bool firc_tunsup_holds(const firc_tunsup_t *s, const char *id, uint32_t mark)
{
    for (size_t i = 0; i < s->n; i++) {
        const slot_t *sl = s->v[i];
        if (sl->pid > 0 && sl->run_mark == mark && strcmp(sl->cfg.id, id) == 0) {
            return true;
        }
    }
    return false;
}

void firc_tunsup_set_second_ms_for_test(firc_tunsup_t *s, int ms)
{
    s->second_ms = ms;
}

firc_err_t firc_tunsup_restart(firc_tunsup_t *s, const char *id)
{
    slot_t *sl = find_slot(s, id);
    if (sl == NULL) {
        return FIRC_ERR_NOENT;
    }
    if (!sl->cfg.enable || s->stopped) {
        return FIRC_ERR_STATE;
    }
    restart_child(sl);
    return FIRC_OK;
}

static void settle_cb(firc_loop_t *loop, void *ud);
static void owed_cb(firc_loop_t *loop, void *ud);

static void arm_at(firc_tunsup_t *s, int *timer, int64_t at, firc_timer_cb fn)
{
    drop_timer(s, timer);
    int64_t left = at - now_ms();
    if (firc_loop_add_timer(s->loop, left > 1 ? (uint64_t)left : 1, 0, fn, s, timer) != FIRC_OK) {
        *timer = 0;
    }
}

static size_t pass(firc_tunsup_t *s, bool event)
{
    if (!pthread_equal(s->loop_thread, pthread_self()) || s->stopped) {
        return 0;
    }
    size_t n = 0;
    int64_t now = now_ms();
    int64_t defer = 0;
    for (size_t i = 0; i < s->n; i++) {
        slot_t *sl = s->v[i];
        if (sl->pid <= 0 || sl->terminating || !sl->starved ||
            sl->pause_end_ms - now <= (int64_t)NUDGE_SHORT_S * s->second_ms) {
            sl->owed = false;
            continue;
        }
        if (!event && !sl->owed) {
            continue;
        }
        int floor_s = sl->nudge_s > 0 ? sl->nudge_s : NUDGE_FLOOR_S;
        int64_t end = sl->nudged_ms + (int64_t)floor_s * s->second_ms;
        if (sl->nudged_ms != 0 && now < end) {
            sl->owed = true;
            defer = defer == 0 || end < defer ? end : defer;
            continue;
        }
        if (sl->nudge_s == 0) {
            sl->nudge_s = NUDGE_FLOOR_S;
        } else {
            sl->nudge_s = sl->nudge_s * 2 > NUDGE_FLOOR_MAX_S ? NUDGE_FLOOR_MAX_S : sl->nudge_s * 2;
        }
        firc_log_from(FIRC_LOG_INFO, sl->cfg.device, "the network changed, starting over");
        sl->nudged_ms = now;
        restart_child(sl);
        sl->nudging = true;
        n++;
    }
    if (defer == 0) {
        drop_timer(s, &s->owed_timer);
    } else {
        arm_at(s, &s->owed_timer, defer, owed_cb);
    }
    return n;
}

size_t firc_tunsup_restart_starved(firc_tunsup_t *s)
{
    assert(pthread_equal(s->loop_thread, pthread_self()));
    return pass(s, true);
}

static void settle_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    firc_tunsup_t *s = ud;
    s->settle_timer = 0;
    (void)pass(s, true);
}

static void owed_cb(firc_loop_t *loop, void *ud)
{
    (void)loop;
    firc_tunsup_t *s = ud;
    s->owed_timer = 0;
    (void)pass(s, false);
}

void firc_tunsup_net_changed(firc_tunsup_t *s)
{
    assert(pthread_equal(s->loop_thread, pthread_self()));
    if (!pthread_equal(s->loop_thread, pthread_self()) || s->stopped) {
        return;
    }
    arm_at(s, &s->settle_timer, now_ms() + (int64_t)NUDGE_SETTLE_S * s->second_ms, settle_cb);
}

uint64_t firc_tunsup_start_seq(const firc_tunsup_t *s)
{
    return s->seq;
}

uint64_t firc_tunsup_started(const firc_tunsup_t *s, const char *id)
{
    const slot_t *sl = find_slot(s, id);
    return sl != NULL ? sl->start_seq : 0;
}

uint64_t firc_tunsup_updated(const firc_tunsup_t *s, const char *id)
{
    const slot_t *sl = find_slot(s, id);
    return sl != NULL ? sl->update_seq : 0;
}

void firc_tunsup_set_tcp_pool(firc_tunsup_t *s, const uint8_t base[4], uint8_t len)
{
    memcpy(s->tcp_pool, base, sizeof s->tcp_pool);
    s->tcp_pool_len = len;
}

static bool live(const slot_t *sl)
{
    return sl != NULL && sl->pid > 0 && sl->ready && !sl->terminating && sl->in_fd >= 0;
}

static firc_err_t write_out(slot_t *sl, const char *text, size_t len, size_t *done)
{
    *done = 0;
    if (sl->in_off < sl->in_len) {
        return FIRC_OK;
    }
    while (*done < len) {
        ssize_t n = write(sl->in_fd, text + *done, len - *done);
        if (n > 0) {
            *done += (size_t)n;
        } else if (n < 0 && errno == EAGAIN) {
            return FIRC_OK;
        } else if (n == 0 || errno != EINTR) {
            firc_log_from(FIRC_LOG_WARN, sl->cfg.device, "command not delivered: %s", strerror(errno));
            close_in(sl);
            kill(sl->pid, SIGTERM);
            return FIRC_ERR_IO;
        }
    }
    return FIRC_OK;
}

static firc_err_t keep_tail(slot_t *sl, const char *tail, size_t len)
{
    size_t pend = sl->in_len - sl->in_off;
    char *buf = malloc(pend + len);
    if (buf == NULL) {
        return FIRC_ERR_NOMEM;
    }
    if (pend > 0) {
        memcpy(buf, sl->in_buf + sl->in_off, pend);
    }
    memcpy(buf + pend, tail, len);
    free(sl->in_buf);
    sl->in_buf = buf;
    sl->in_len = pend + len;
    sl->in_off = 0;
    if (firc_loop_mod_fd(sl->s->loop, sl->in_fd, EPOLLOUT) != FIRC_OK) {
        close_in(sl);
        kill(sl->pid, SIGTERM);
        return FIRC_ERR_IO;
    }
    return FIRC_OK;
}

firc_err_t firc_tunsup_command(firc_tunsup_t *s, const char *id, firc_tun_cmd_t kind, const char *text, size_t len,
                               const size_t *expect, size_t n_expect)
{
    assert(pthread_equal(s->loop_thread, pthread_self()));
    slot_t *sl = find_slot(s, id);
    if (!live(sl)) {
        return FIRC_ERR_STATE;
    }
    if (kind == FIRC_TUN_CMD_MAP && n_expect > 0) {
        size_t *grown = realloc(sl->map_want, (sl->n_map_want + n_expect) * sizeof *grown);
        if (grown == NULL) {
            return FIRC_ERR_NOMEM;
        }
        sl->map_want = grown;
    }
    char *framed = NULL;
    if (kind == FIRC_TUN_CMD_INBOUNDS || kind == FIRC_TUN_CMD_WAKE) {
        const char *verb = kind == FIRC_TUN_CMD_WAKE ? k_wake_verb : k_inb_verb;
        size_t vl = strlen(verb);
        framed = malloc(vl + len + 1);
        if (framed == NULL) {
            return FIRC_ERR_NOMEM;
        }
        memcpy(framed, verb, vl);
        if (len > 0) {
            memcpy(framed + vl, text, len);
        }
        framed[vl + len] = '\n';
        text = framed;
        len += vl + 1;
    }
    if (kind == FIRC_TUN_CMD_INBOUNDS) {
        sl->inb_sent = 0;
        for (size_t i = 0; i < len; i++) {
            sl->inb_sent += text[i] == '\n';
        }
        sl->inb_sent -= 2;
    }
    size_t done = 0;
    firc_err_t err = write_out(sl, text, len, &done);
    if (err == FIRC_OK && done < len) {
        err = keep_tail(sl, text + done, len - done);
    }
    if (err == FIRC_OK && kind == FIRC_TUN_CMD_INBOUNDS) {
        index_inbound_links(sl, framed + sizeof k_inb_verb - 1, len - sizeof k_inb_verb);
    }
    free(framed);
    if (err != FIRC_OK) {
        if (err != FIRC_ERR_IO && done > 0) {
            restart_child(sl);
        }
        return err;
    }
    if (kind == FIRC_TUN_CMD_MAP && n_expect > 0) {
        memcpy(&sl->map_want[sl->n_map_want], expect, n_expect * sizeof *expect);
        sl->n_map_want += n_expect;
        arm_ack(sl);
    }
    return FIRC_OK;
}

bool firc_tunsup_ready(const firc_tunsup_t *s, const char *id)
{
    return live(find_slot(s, id));
}
