#include "firc/opkgtun.h"

#include <errno.h>
#include <fcntl.h>
#include <pthread.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include <cjson/cJSON.h>

#include "firc/atomic_write.h"
#include "firc/json.h"
#include "firc/log.h"
#include "firc/tunaddr.h"
#include "firc/tunnels.h"

#define T_WRITE 10L
#define T_SAVE 30L
#define DEV_POLLS 5
#define DEV_GAP_MS 500
#define DEBOUNCE_MS 1000
#define DELETE_GAP_MS 5000
#define DIRTY_WAIT_MS 10000
#define BACKOFF_MIN_MS 1000
#define BACKOFF_MAX_MS 60000
#define NAP_MAX_MS 1000
#define N_MAX 17
#define INTENT_MAX 4096
#define MSG_MAX 160
#define HOST32 "255.255.255.255"
#define NO_HOST "this build cannot reach the router"

static atomic_bool g_reach;

typedef struct {
    bool dirty;
    bool creating[N_MAX];
} intent_t;

typedef struct {
    const firc_opkgtun_io_t *io;
    intent_t in;
    bool pending;
    bool any_a3;
    int64_t last_a3;
} ctx_t;

typedef struct {
    firc_opkgtun_want_t w;
    firc_opkgtun_seen_t s;
    bool due;
    int64_t next_ms;
    unsigned attempts;
    bool own_create;
    bool unsaved;
    bool hold;
    bool adopt;
    bool pol_retry;
    unsigned pol_attempts;
    char kept[FIRC_OPKGTUN_DESC_MAX + 1];
} rec_t;

struct firc_opkgtun {
    pthread_t th;
    size_t n_wants;
    uint64_t gen;
    size_t n_seen;
    size_t n_recs;
    uint64_t gen_seen;
    int64_t sweep_next;
    int64_t gate_ms;
    int64_t settled_at;
    int64_t save_next;
    pthread_mutex_t mu;
    pthread_cond_t cv;
    ctx_t c;
    firc_opkgtun_io_t io;
    rec_t recs[FIRC_OPKGTUN_MAX_WANTS];
    rec_t scratch[FIRC_OPKGTUN_MAX_WANTS];
    int rfd;
    int wfd;
    unsigned sweep_attempts;
    unsigned save_attempts;
    firc_opkgtun_want_t wants[FIRC_OPKGTUN_MAX_WANTS];
    firc_opkgtun_seen_t seen[FIRC_OPKGTUN_MAX_WANTS];
    bool started;
    bool host;
    bool sweep_req;
    bool idle;
    atomic_bool own_stop;
    bool sweep_due;
    bool sweep_warned;
    bool saving;
    uint32_t swept;
    uint32_t trips;
};

typedef enum { L_OK, L_UNKNOWN, L_STOP } lread_t;
typedef enum { P_ANSWERED, P_LOST, P_STOP } post_t;
typedef enum { S_DONE, S_STOP } seq_t;
typedef enum { RM_DONE, RM_RETRY, RM_UNKNOWN, RM_STOP } rm_t;
typedef enum { H_FREE, H_HELD, H_STOP } hold_t;
typedef enum { RT_NONE, RT_HAS, RT_UNKNOWN, RT_STOP } route_t;

void firc_opkgtun_reach_for_test(bool on)
{
    atomic_store(&g_reach, on);
}

static void say(char *buf, size_t cap, const char *fmt, ...) __attribute__((format(printf, 3, 4)));
static void say(char *buf, size_t cap, const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    (void)vsnprintf(buf, cap, fmt, ap);
    va_end(ap);
}

static int name_index(const char *name)
{
    static const char stem[] = "OpkgTun";
    if (name == NULL || strncmp(name, stem, sizeof stem - 1) != 0) { return -1; }
    const char *d = name + sizeof stem - 1;
    if (d[0] < '0' || d[0] > '9' || (d[0] == '0' && d[1] != '\0')) { return -1; }
    int n = 0;
    for (; *d != '\0'; d++) {
        if (*d < '0' || *d > '9' || n > N_MAX) { return -1; }
        n = n * 10 + (*d - '0');
    }
    return n < N_MAX ? n : -1;
}

static bool halted(const ctx_t *c)
{
    return c->io->stop != NULL && atomic_load(c->io->stop);
}

static int64_t now_of(const ctx_t *c)
{
    return c->io->now_ms(c->io->ud);
}

bool firc_opkgtun_ours(const char *description)
{
    static const char mark[] = "firc:";
    if (description == NULL || strncmp(description, mark, sizeof mark - 1) != 0) { return false; }
    const char *p = description + sizeof mark - 1;
    size_t n = 0;
    while ((p[n] >= 'A' && p[n] <= 'Z') || (p[n] >= 'a' && p[n] <= 'z') || (p[n] >= '0' && p[n] <= '9') ||
           p[n] == '_' || p[n] == '-') {
        n++;
    }
    return n > 0 && (p[n] == '\0' || p[n] == ' ');
}

static void copy_str(char *dst, size_t cap, const char *src)
{
    size_t n = src != NULL ? strlen(src) : 0;
    if (n >= cap) { n = cap - 1; }
    if (n > 0 && src != NULL) { memcpy(dst, src, n); }
    dst[n] = '\0';
}

static const char *str_at(const cJSON *obj, const char *key)
{
    const cJSON *v = cJSON_GetObjectItemCaseSensitive(obj, key);
    return cJSON_IsString(v) ? v->valuestring : NULL;
}

static bool good_list(const cJSON *root)
{
    if (root == NULL || !cJSON_IsObject(root) || root->child == NULL ||
        cJSON_GetObjectItemCaseSensitive(root, "status") != NULL) {
        return false;
    }
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, root) {
        if (!cJSON_IsObject(e)) { return false; }
    }
    return true;
}

static void classify_in(const cJSON *root, const char *name, firc_opkgtun_rc_t *out)
{
    memset(out, 0, sizeof *out);
    out->kind = FIRC_OPKG_RC_ABSENT;
    const cJSON *item = cJSON_GetObjectItemCaseSensitive(root, name);
    if (!cJSON_IsObject(item)) { return; }
    const char *desc = str_at(item, "description");
    copy_str(out->description, sizeof out->description, desc);
    const cJSON *level = cJSON_GetObjectItemCaseSensitive(item, "security-level");
    out->public_level = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(level, "public"));
    const cJSON *ip = cJSON_GetObjectItemCaseSensitive(item, "ip");
    const cJSON *addr = cJSON_GetObjectItemCaseSensitive(ip, "address");
    copy_str(out->address, sizeof out->address, str_at(addr, "address"));
    copy_str(out->mask, sizeof out->mask, str_at(addr, "mask"));
    out->up = cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(item, "up"));
    const cJSON *global = cJSON_GetObjectItemCaseSensitive(ip, "global");
    out->global = cJSON_IsObject(global);
    bool unmarked = desc == NULL || desc[0] == '\0';
    if (name_index(name) >= 0 && unmarked) {
        out->kind = FIRC_OPKG_RC_UNMARKED;
    } else if (name_index(name) >= 0 && firc_opkgtun_ours(desc)) {
        out->kind = FIRC_OPKG_RC_OURS;
        copy_str(out->mark, sizeof out->mark, desc);
        char *sp = strchr(out->mark, ' ');
        if (sp != NULL) { *sp = '\0'; }
    } else {
        out->kind = FIRC_OPKG_RC_FOREIGN;
    }
}

firc_err_t firc_opkgtun_classify(const char *list_json, const char *name, firc_opkgtun_rc_t *out)
{
    memset(out, 0, sizeof *out);
    cJSON *root = list_json != NULL ? cJSON_Parse(list_json) : NULL;
    if (!good_list(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    classify_in(root, name, out);
    cJSON_Delete(root);
    return FIRC_OK;
}

static void parse_intent(const char *text, intent_t *in)
{
    memset(in, 0, sizeof *in);
    const char *p = text;
    while (*p != '\0') {
        size_t len = strcspn(p, "\n");
        char line[64];
        if (len >= sizeof line) {
            in->dirty = true;
        } else {
            memcpy(line, p, len);
            line[len] = '\0';
            int k = strncmp(line, "creating ", 9) == 0 ? name_index(line + 9) : -1;
            if (k >= 0) {
                in->creating[k] = true;
            } else if (line[0] != '\0') {
                in->dirty = true;
            }
        }
        p += len;
        if (*p == '\n') { p++; }
    }
}

static void read_intent(const char *path, intent_t *in)
{
    memset(in, 0, sizeof *in);
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        in->dirty = errno != ENOENT;
        return;
    }
    char *buf = malloc(INTENT_MAX + 1);
    ssize_t n = buf != NULL ? read(fd, buf, INTENT_MAX + 1) : -1;
    close(fd);
    if (n < 0 || n > INTENT_MAX) {
        free(buf);
        in->dirty = true;
        return;
    }
    buf[n] = '\0';
    parse_intent(buf, in);
    free(buf);
}

static int cmp_line(const void *a, const void *b)
{
    return strcmp(a, b);
}

static size_t intent_text(const intent_t *in, char *buf, size_t cap)
{
    char lines[N_MAX][24];
    size_t k = 0;
    for (int n = 0; n < N_MAX; n++) {
        if (in->creating[n]) { say(lines[k++], sizeof lines[0], "creating OpkgTun%d", n); }
    }
    qsort(lines, k, sizeof lines[0], cmp_line);
    size_t len = 0;
    buf[0] = '\0';
    for (size_t i = 0; i < k; i++) {
        say(buf + len, cap - len, "%s\n", lines[i]);
        len += strlen(buf + len);
    }
    if (in->dirty) {
        say(buf + len, cap - len, "dirty\n");
        len += strlen(buf + len);
    }
    return len;
}

static bool put_intent(ctx_t *c, const intent_t *next)
{
    if (memcmp(next, &c->in, sizeof *next) == 0) { return true; }
    char buf[N_MAX * 26 + 8];
    size_t len = intent_text(next, buf, sizeof buf);
    if (firc_atomic_write(c->io->intent_path, buf, len) != FIRC_OK) { return false; }
    c->in = *next;
    return true;
}

static bool mark_dirty(ctx_t *c)
{
    intent_t next = c->in;
    next.dirty = true;
    return put_intent(c, &next);
}

static void collect(const cJSON *node, int *msgs, int *errs, char *error, size_t error_cap)
{
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, node) {
        if (cJSON_IsArray(e) && e->string != NULL && strcmp(e->string, "status") == 0) {
            const cJSON *s = NULL;
            cJSON_ArrayForEach(s, e) {
                const char *kind = str_at(s, "status");
                if (kind != NULL && strcmp(kind, "error") == 0) {
                    if (error != NULL && error[0] == '\0') {
                        copy_str(error, error_cap, str_at(s, "message"));
                    }
                    (*errs)++;
                } else if (cJSON_IsObject(s)) {
                    (*msgs)++;
                }
            }
        } else if (cJSON_IsObject(e) || cJSON_IsArray(e)) {
            collect(e, msgs, errs, error, error_cap);
        }
    }
}

int firc_opkgtun_items(const char *answer, firc_opkg_item_t *out, size_t cap, char *error, size_t error_cap)
{
    if (error != NULL && error_cap > 0) { error[0] = '\0'; }
    cJSON *root = answer != NULL ? cJSON_Parse(answer) : NULL;
    if (!cJSON_IsArray(root)) {
        cJSON_Delete(root);
        return -1;
    }
    int n = 0;
    const cJSON *item = NULL;
    cJSON_ArrayForEach(item, root) {
        int msgs = 0;
        int errs = 0;
        collect(item, &msgs, &errs, error, error_cap);
        firc_opkg_item_t o = errs == 0 ? FIRC_OPKG_ITEM_APPLIED
                             : msgs == 0 ? FIRC_OPKG_ITEM_REFUSED
                                         : FIRC_OPKG_ITEM_PARTIAL;
        if ((size_t)n < cap) { out[n] = o; }
        n++;
    }
    cJSON_Delete(root);
    return n;
}

#define ID_MAX (sizeof ((firc_tunnel_t *)0)->id - 1)
#define LONGEST (sizeof "firc:" - 1 + ID_MAX + 1 + FIRC_TUN_DESCRIPTION_MAX)

_Static_assert(LONGEST + 1 == sizeof ((firc_opkgtun_want_t *)0)->desc, "a want holds the longest description");
_Static_assert(LONGEST <= FIRC_OPKGTUN_DESC_MAX, "the longest description fits the router's limit");

char *firc_opkgtun_description(const char *id, const char *desc)
{
    if (strlen(id) > ID_MAX || (desc != NULL && strlen(desc) > FIRC_TUN_DESCRIPTION_MAX)) { return NULL; }
    char *out = malloc(LONGEST + 1);
    if (out == NULL) { return NULL; }
    if (desc != NULL && desc[0] != '\0') {
        say(out, LONGEST + 1, "firc:%s %s", id, desc);
    } else {
        say(out, LONGEST + 1, "firc:%s", id);
    }
    return out;
}

static cJSON *iface_item(cJSON *arr, const char *name)
{
    cJSON *item = cJSON_CreateObject();
    cJSON *iface = cJSON_AddObjectToObject(item, "interface");
    if (iface == NULL || cJSON_AddStringToObject(iface, "name", name) == NULL ||
        !cJSON_AddItemToArray(arr, item)) {
        cJSON_Delete(item);
        return NULL;
    }
    return iface;
}

static char *dump(cJSON *arr, bool ok)
{
    char *s = ok ? firc_json_dump(arr) : NULL;
    cJSON_Delete(arr);
    return s;
}

static char *create_body(const char *name, const char *mark)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *iface = arr != NULL ? iface_item(arr, name) : NULL;
    return dump(arr, iface != NULL && cJSON_AddStringToObject(iface, "description", mark) != NULL);
}

static char *delete_body(const char *name)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *iface = arr != NULL ? iface_item(arr, name) : NULL;
    return dump(arr, iface != NULL && cJSON_AddTrueToObject(iface, "no") != NULL);
}

static char *global_body(const char *name, bool on)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *iface = arr != NULL ? iface_item(arr, name) : NULL;
    cJSON *ip = iface != NULL ? cJSON_AddObjectToObject(iface, "ip") : NULL;
    cJSON *g = ip != NULL ? cJSON_AddObjectToObject(ip, "global") : NULL;
    bool ok = g != NULL && (on ? cJSON_AddTrueToObject(g, "auto") != NULL
                               : cJSON_AddTrueToObject(g, "no") != NULL);
    return dump(arr, ok);
}

static char *route_body(const char *name, bool on)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *item = cJSON_CreateObject();
    cJSON *ip = cJSON_AddObjectToObject(item, "ip");
    cJSON *rt = cJSON_AddObjectToObject(ip, "route");
    bool ok = arr != NULL && cJSON_AddTrueToObject(rt, "default") != NULL &&
              cJSON_AddStringToObject(rt, "interface", name) != NULL &&
              (on || cJSON_AddTrueToObject(rt, "no") != NULL) && cJSON_AddItemToArray(arr, item);
    if (!ok) { cJSON_Delete(item); }
    return dump(arr, ok);
}

static char *save_body(void)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *item = cJSON_CreateObject();
    cJSON *sys = cJSON_AddObjectToObject(item, "system");
    cJSON *conf = cJSON_AddObjectToObject(sys, "configuration");
    bool ok = arr != NULL && cJSON_AddObjectToObject(conf, "save") != NULL && cJSON_AddItemToArray(arr, item);
    if (!ok) { cJSON_Delete(item); }
    return dump(arr, ok);
}

static char *ns_body(const char *addr, const char *domain, const char *name)
{
    cJSON *arr = cJSON_CreateArray();
    cJSON *item = cJSON_CreateObject();
    cJSON *ip = cJSON_AddObjectToObject(item, "ip");
    cJSON *ns = cJSON_AddObjectToObject(ip, "name-server");
    bool ok = arr != NULL && cJSON_AddTrueToObject(ns, "no") != NULL &&
              cJSON_AddStringToObject(ns, "address", addr) != NULL &&
              cJSON_AddStringToObject(ns, "domain", domain) != NULL &&
              cJSON_AddStringToObject(ns, "interface", name) != NULL && cJSON_AddItemToArray(arr, item);
    if (!ok) { cJSON_Delete(item); }
    return dump(arr, ok);
}

static char *config_body(const char *name, const char *desc, bool level, const char *addr, bool up, int *count)
{
    cJSON *arr = cJSON_CreateArray();
    bool ok = arr != NULL;
    *count = 0;
    if (ok && desc != NULL) {
        cJSON *i = iface_item(arr, name);
        ok = i != NULL && cJSON_AddStringToObject(i, "description", desc) != NULL;
        (*count)++;
    }
    if (ok && level) {
        cJSON *i = iface_item(arr, name);
        cJSON *l = i != NULL ? cJSON_AddObjectToObject(i, "security-level") : NULL;
        ok = l != NULL && cJSON_AddTrueToObject(l, "public") != NULL;
        (*count)++;
    }
    if (ok && addr != NULL) {
        cJSON *i = iface_item(arr, name);
        cJSON *ip = i != NULL ? cJSON_AddObjectToObject(i, "ip") : NULL;
        cJSON *a = ip != NULL ? cJSON_AddObjectToObject(ip, "address") : NULL;
        ok = a != NULL && cJSON_AddStringToObject(a, "address", addr) != NULL &&
             cJSON_AddStringToObject(a, "mask", HOST32) != NULL;
        (*count)++;
    }
    if (ok && up) {
        cJSON *i = iface_item(arr, name);
        ok = i != NULL && cJSON_AddTrueToObject(i, "up") != NULL;
        (*count)++;
    }
    return dump(arr, ok);
}

static post_t post_items(ctx_t *c, char *body, long timeout_s, firc_opkg_item_t *out, int count, char *err,
                         size_t err_cap)
{
    err[0] = '\0';
    if (body == NULL) {
        say(err, err_cap, "out of memory");
        return P_LOST;
    }
    char *ans = NULL;
    firc_err_t e = c->io->post(c->io->ud, body, timeout_s, &ans);
    free(body);
    if (halted(c)) {
        free(ans);
        return P_STOP;
    }
    if (e != FIRC_OK) {
        free(ans);
        say(err, err_cap, "%s", firc_err_str(e));
        return P_LOST;
    }
    int n = firc_opkgtun_items(ans, out, (size_t)count, err, err_cap);
    free(ans);
    if (n != count) {
        say(err, err_cap, "the router's answer cannot be read");
        return P_LOST;
    }
    return P_ANSWERED;
}

static void tidy_lines(ctx_t *c, const cJSON *root)
{
    intent_t next = c->in;
    for (int k = 0; k < N_MAX; k++) {
        char name[16];
        say(name, sizeof name, "OpkgTun%d", k);
        firc_opkgtun_rc_t rc;
        classify_in(root, name, &rc);
        if (next.creating[k] && rc.kind != FIRC_OPKG_RC_UNMARKED) {
            next.creating[k] = false;
        }
    }
    if (!put_intent(c, &next)) { FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path); }
}

static cJSON *read_list(ctx_t *c, lread_t *lr, char *err, size_t err_cap)
{
    char *body = NULL;
    firc_err_t e = c->io->get(c->io->ud, FIRC_OPKGTUN_LIST_PATH, &body);
    if (halted(c)) {
        free(body);
        *lr = L_STOP;
        return NULL;
    }
    cJSON *root = e == FIRC_OK && body != NULL ? cJSON_Parse(body) : NULL;
    free(body);
    if (!good_list(root)) {
        cJSON_Delete(root);
        say(err, err_cap, "the router's interface list cannot be read (%s)",
            e != FIRC_OK ? firc_err_str(e) : "not a list");
        *lr = L_UNKNOWN;
        return NULL;
    }
    tidy_lines(c, root);
    *lr = L_OK;
    return root;
}

static lread_t list_class(ctx_t *c, const char *name, firc_opkgtun_rc_t *rc, char *err, size_t err_cap)
{
    memset(rc, 0, sizeof *rc);
    rc->kind = FIRC_OPKG_RC_ABSENT;
    lread_t lr;
    cJSON *root = read_list(c, &lr, err, err_cap);
    if (root != NULL) {
        classify_in(root, name, rc);
        cJSON_Delete(root);
    }
    return lr;
}

static unsigned backoff(unsigned *attempts)
{
    unsigned shift = *attempts < 6 ? *attempts : 6;
    unsigned d = (unsigned)BACKOFF_MIN_MS << shift;
    if (*attempts < 16) { (*attempts)++; }
    return d < BACKOFF_MAX_MS ? d : BACKOFF_MAX_MS;
}

static void finish(ctx_t *c, rec_t *r, firc_opkg_state_t st, bool retry, const char *fmt, ...)
    __attribute__((format(printf, 5, 6)));
static void finish(ctx_t *c, rec_t *r, firc_opkg_state_t st, bool retry, const char *fmt, ...)
{
    char msg[MSG_MAX];
    va_list ap;
    va_start(ap, fmt);
    (void)vsnprintf(msg, sizeof msg, fmt, ap);
    va_end(ap);
    bool changed = r->s.state != st || strcmp(r->s.error, msg) != 0;
    r->s.state = st;
    copy_str(r->s.error, sizeof r->s.error, msg);
    if (retry) {
        r->due = true;
        r->next_ms = now_of(c) + backoff(&r->attempts);
    } else {
        r->due = false;
        r->attempts = 0;
    }
    if (!changed) { return; }
    if (st == FIRC_OPKG_FAILED || st == FIRC_OPKG_FOREIGN) {
        FIRC_WARN("opkgtun: %s: %s", r->w.name, msg);
    } else if (st == FIRC_OPKG_READY) {
        FIRC_INFO("opkgtun: %s %s", r->w.name, r->w.present ? "is ready" : "is removed");
    }
}

static void want_addr(int n, char out[20])
{
    uint8_t v6[16];
    firc_tun_addr(n, out, v6);
    char *slash = strchr(out, '/');
    if (slash != NULL) { *slash = '\0'; }
}

static hold_t hold(ctx_t *c, const firc_opkgtun_want_t *w, char holder[16])
{
    holder[0] = '\0';
    bool held = c->io->addr_holder(c->io->ud, w->n, w->device, holder);
    if (halted(c)) { return H_STOP; }
    char prev[16];
    say(prev, sizeof prev, "tunvless%d", w->n);
    return held && strcmp(holder, prev) != 0 ? H_HELD : H_FREE;
}

static int dev_wait(ctx_t *c, const char *dev, bool present)
{
    for (int i = 0; i < DEV_POLLS; i++) {
        bool there = c->io->dev_exists(c->io->ud, dev);
        if (halted(c)) { return -1; }
        if (there == present) { return 1; }
        if (i + 1 < DEV_POLLS && (!c->io->sleep_ms(c->io->ud, DEV_GAP_MS) || halted(c))) { return -1; }
    }
    return 0;
}

static bool read_back(const firc_opkgtun_rc_t *rc, const firc_opkgtun_want_t *w, const char *addr, char *msg,
                      size_t cap)
{
    if (rc->kind != FIRC_OPKG_RC_OURS || strcmp(rc->mark, w->mark) != 0) {
        if (rc->kind == FIRC_OPKG_RC_ABSENT) {
            say(msg, cap, "the router does not list %s", w->name);
        } else {
            say(msg, cap, "the router kept description \"%.80s\"", rc->description);
        }
        return false;
    }
    if (!rc->public_level) {
        say(msg, cap, "the router did not keep security level public");
        return false;
    }
    if (strcmp(rc->address, addr) != 0) {
        say(msg, cap, "the router kept address %s", rc->address[0] != '\0' ? rc->address : "none");
        return false;
    }
    if (strcmp(rc->mask, HOST32) != 0) {
        say(msg, cap, "the router kept mask %s", rc->mask[0] != '\0' ? rc->mask : "none");
        return false;
    }
    if (!rc->up) {
        say(msg, cap, "the router kept %s down", w->name);
        return false;
    }
    return true;
}

static seq_t device_step(ctx_t *c, rec_t *r)
{
    int d = dev_wait(c, r->w.device, true);
    if (d < 0) { return S_STOP; }
    if (d == 0) {
        finish(c, r, FIRC_OPKG_FAILED, true, "no device yet");
        return S_DONE;
    }
    r->own_create = false;
    finish(c, r, FIRC_OPKG_READY, false, "%s", "");
    if (r->pol_retry) {
        r->due = true;
        r->next_ms = now_of(c) + backoff(&r->pol_attempts);
    }
    return S_DONE;
}

static void pol_set(rec_t *r, firc_opkg_pol_t st, const char *fmt, ...) __attribute__((format(printf, 3, 4)));
static void pol_set(rec_t *r, firc_opkg_pol_t st, const char *fmt, ...)
{
    char msg[MSG_MAX];
    va_list ap;
    va_start(ap, fmt);
    (void)vsnprintf(msg, sizeof msg, fmt, ap);
    va_end(ap);
    bool changed = r->s.policies != st || strcmp(r->s.pol_error, msg) != 0;
    r->s.policies = st;
    copy_str(r->s.pol_error, sizeof r->s.pol_error, msg);
    r->pol_retry = st == FIRC_OPKG_POL_FAILED;
    if (!r->pol_retry) { r->pol_attempts = 0; }
    if (changed && st == FIRC_OPKG_POL_FAILED) {
        FIRC_WARN("opkgtun: %s: router policies: %s", r->w.name, msg);
    } else if (changed) {
        FIRC_INFO("opkgtun: %s: router policies are %s", r->w.name, st == FIRC_OPKG_POL_ON ? "on" : "off");
    }
}

static void clear_on_refusal(ctx_t *c)
{
    if (c->pending) { return; }
    intent_t next = c->in;
    next.dirty = false;
    if (!put_intent(c, &next)) { FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path); }
}

static route_t read_route(ctx_t *c, rec_t *r, const char *name, char *err, size_t err_cap)
{
    char *body = NULL;
    firc_err_t e = c->io->get(c->io->ud, FIRC_OPKGTUN_ROUTE_PATH, &body);
    if (halted(c)) {
        free(body);
        return RT_STOP;
    }
    cJSON *root = e == FIRC_OK && body != NULL ? cJSON_Parse(body) : NULL;
    free(body);
    if (!cJSON_IsArray(root)) {
        cJSON_Delete(root);
        say(err, err_cap, "the router's routes cannot be read (%s)", e != FIRC_OK ? firc_err_str(e) : "not a list");
        return RT_UNKNOWN;
    }
    bool has = false;
    const cJSON *it = NULL;
    cJSON_ArrayForEach(it, root) {
        const char *iface = str_at(it, "interface");
        if (cJSON_IsTrue(cJSON_GetObjectItemCaseSensitive(it, "default")) && iface != NULL && strcmp(iface, name) == 0) {
            has = true;
        }
    }
    cJSON_Delete(root);
    if (r != NULL) {
        if (r->adopt && has) {
            r->s.route_adds++;
            r->hold = true;
        }
        r->adopt = false;
        r->s.route = has;
        r->s.route_reads++;
        if (!has) { r->hold = false; }
    }
    return has ? RT_HAS : RT_NONE;
}

static rm_t route_clear(ctx_t *c, rec_t *r, const char *name, char *err, size_t err_cap)
{
    route_t rt = read_route(c, r, name, err, err_cap);
    if (rt == RT_STOP) { return RM_STOP; }
    if (rt == RT_UNKNOWN) { return RM_RETRY; }
    if (rt == RT_NONE) { return RM_DONE; }
    if (!mark_dirty(c)) {
        say(err, err_cap, "cannot write %s", c->io->intent_path);
        return RM_RETRY;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, route_body(name, false), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) { return RM_STOP; }
    c->pending = true;
    if (r != NULL) { r->unsaved = true; }
    rt = read_route(c, r, name, err, err_cap);
    if (rt == RT_STOP) { return RM_STOP; }
    if (rt == RT_UNKNOWN) { return RM_RETRY; }
    if (rt == RT_HAS) {
        say(err, err_cap, "the router kept the default route via %s (%.100s)", name, perr[0] != '\0' ? perr : "no answer");
        return RM_RETRY;
    }
    return RM_DONE;
}

static seq_t global_off(ctx_t *c, rec_t *r)
{
    if (!mark_dirty(c)) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "cannot write %s", c->io->intent_path);
        return S_DONE;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, global_body(r->w.name, false), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) { return S_STOP; }
    if (p == P_ANSWERED && out[0] == FIRC_OPKG_ITEM_REFUSED) {
        clear_on_refusal(c);
        pol_set(r, FIRC_OPKG_POL_FAILED,
                "%s is still in a router policy; remove it there to finish turning router policies off (%.60s)",
                r->w.name, perr);
        return S_DONE;
    }
    c->pending = true;
    r->unsaved = true;
    if (p == P_LOST) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "ip global was not cleared on %s (%s)", r->w.name, perr);
        return S_DONE;
    }
    pol_set(r, FIRC_OPKG_POL_OFF, "%s", "");
    return S_DONE;
}

static bool global_on(ctx_t *c, rec_t *r, seq_t *seq)
{
    *seq = S_DONE;
    const char *name = r->w.name;
    if (!mark_dirty(c)) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "cannot write %s", c->io->intent_path);
        return false;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, global_body(name, true), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) {
        *seq = S_STOP;
        return false;
    }
    if (p == P_ANSWERED && out[0] == FIRC_OPKG_ITEM_REFUSED) {
        clear_on_refusal(c);
        pol_set(r, FIRC_OPKG_POL_FAILED, "%s", perr);
        return false;
    }
    c->pending = true;
    r->unsaved = true;
    firc_opkgtun_rc_t back;
    char lerr[MSG_MAX];
    lread_t lr = list_class(c, name, &back, lerr, sizeof lerr);
    if (lr == L_STOP) {
        *seq = S_STOP;
        return false;
    }
    if (lr == L_UNKNOWN) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "%s", lerr);
        return false;
    }
    if (!back.global) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "the router did not keep global on %s", name);
        return false;
    }
    return true;
}

static seq_t route_on(ctx_t *c, rec_t *r)
{
    const char *name = r->w.name;
    char err[MSG_MAX];
    route_t rt = read_route(c, r, name, err, sizeof err);
    if (rt == RT_STOP) { return S_STOP; }
    if (rt == RT_UNKNOWN) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err);
        return S_DONE;
    }
    if (rt == RT_HAS) {
        pol_set(r, FIRC_OPKG_POL_ON, "%s", "");
        return S_DONE;
    }
    if (!mark_dirty(c)) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "cannot write %s", c->io->intent_path);
        return S_DONE;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, route_body(name, true), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) { return S_STOP; }
    r->s.route_adds++;
    r->hold = true;
    rt = read_route(c, r, name, err, sizeof err);
    if (rt == RT_STOP) { return S_STOP; }
    if (rt == RT_HAS) {
        c->pending = true;
        r->unsaved = true;
    } else if (rt == RT_NONE && p == P_ANSWERED && out[0] == FIRC_OPKG_ITEM_REFUSED) {
        clear_on_refusal(c);
    }
    if (rt == RT_UNKNOWN) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err);
        return S_DONE;
    }
    if (rt == RT_NONE) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "the router did not add the default route via %s (%.100s)", name,
                perr[0] != '\0' ? perr : "no answer");
        return S_DONE;
    }
    pol_set(r, FIRC_OPKG_POL_ON, "%s", "");
    return S_DONE;
}

static seq_t policies_step(ctx_t *c, rec_t *r, const firc_opkgtun_rc_t *rc)
{
    const char *name = r->w.name;
    char err[MSG_MAX];
    if (!r->w.policies && !rc->global && !r->s.route && r->s.route_reads > 0 && r->s.policies == FIRC_OPKG_POL_OFF) {
        pol_set(r, FIRC_OPKG_POL_OFF, "%s", "");
        return S_DONE;
    }
    if (!r->w.policies || !rc->global) {
        rm_t g = route_clear(c, r, name, err, sizeof err);
        if (g == RM_STOP) { return S_STOP; }
        if (g != RM_DONE) {
            pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err);
            return S_DONE;
        }
    }
    if (!r->w.policies && !rc->global) {
        pol_set(r, FIRC_OPKG_POL_OFF, "%s", "");
        return S_DONE;
    }
    if (!r->w.policies) { return global_off(c, r); }
    if (!rc->global) {
        seq_t seq;
        if (!global_on(c, r, &seq)) { return seq; }
    }
    if (r->w.route) { return route_on(c, r); }
    if (r->w.keep) {
        route_t rt = read_route(c, r, name, err, sizeof err);
        if (rt == RT_STOP) { return S_STOP; }
        if (rt == RT_UNKNOWN) {
            pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err);
            return S_DONE;
        }
        pol_set(r, FIRC_OPKG_POL_ON, "%s", "");
        return S_DONE;
    }
    rm_t g = route_clear(c, r, name, err, sizeof err);
    if (g == RM_STOP) { return S_STOP; }
    if (g != RM_DONE) {
        pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err);
        return S_DONE;
    }
    pol_set(r, FIRC_OPKG_POL_ON, "%s", "");
    return S_DONE;
}

static seq_t settle(ctx_t *c, rec_t *r, const firc_opkgtun_rc_t *rc)
{
    seq_t p = policies_step(c, r, rc);
    return p == S_STOP ? S_STOP : device_step(c, r);
}

static seq_t configure(ctx_t *c, rec_t *r, const firc_opkgtun_rc_t *rc, bool held_ok)
{
    const firc_opkgtun_want_t *w = &r->w;
    char addr[20];
    want_addr(w->n, addr);
    bool d_desc =
        strcmp(rc->description, w->desc) != 0 && (r->kept[0] == '\0' || strcmp(rc->description, r->kept) != 0);
    bool d_level = !rc->public_level;
    bool d_addr = strcmp(rc->address, addr) != 0 || strcmp(rc->mask, HOST32) != 0;
    bool d_up = !rc->up;
    if (!d_desc && !d_level && !d_addr && !d_up) { return settle(c, r, rc); }
    if (d_addr && !held_ok) {
        char holder[16];
        hold_t h = hold(c, w, holder);
        if (h == H_STOP) { return S_STOP; }
        if (h == H_HELD) {
            finish(c, r, FIRC_OPKG_FAILED, true, "%s is already on %s", addr, holder);
            return S_DONE;
        }
    }
    bool only_up = !d_desc && !d_level && !d_addr;
    if (!only_up && !mark_dirty(c)) {
        finish(c, r, FIRC_OPKG_FAILED, true, "cannot write %s", c->io->intent_path);
        return S_DONE;
    }
    int count = 0;
    char *body = config_body(w->name, d_desc ? w->desc : NULL, d_level, d_addr ? addr : NULL, d_up, &count);
    firc_opkg_item_t out[4] = {FIRC_OPKG_ITEM_REFUSED};
    char err[MSG_MAX];
    post_t p = post_items(c, body, T_WRITE, out, count, err, sizeof err);
    if (p == P_STOP) { return S_STOP; }
    if (p == P_ANSWERED) {
        bool applied = false;
        bool refused = false;
        for (int i = 0; i < count; i++) {
            applied = applied || out[i] != FIRC_OPKG_ITEM_REFUSED;
            refused = refused || out[i] != FIRC_OPKG_ITEM_APPLIED;
        }
        if (applied && !only_up) {
            c->pending = true;
            r->unsaved = true;
        }
        if (refused) {
            if (!applied && !c->pending) {
                intent_t next = c->in;
                next.dirty = false;
                if (!put_intent(c, &next)) { FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path); }
            }
            finish(c, r, FIRC_OPKG_FAILED, false, "%s", err);
            return S_DONE;
        }
    } else if (!only_up) {
        c->pending = true;
        r->unsaved = true;
    }
    firc_opkgtun_rc_t back;
    char lerr[MSG_MAX];
    lread_t lr = list_class(c, w->name, &back, lerr, sizeof lerr);
    if (lr == L_STOP) { return S_STOP; }
    if (lr == L_UNKNOWN) {
        finish(c, r, FIRC_OPKG_FAILED, true, "%s", lerr);
        return S_DONE;
    }
    char msg[MSG_MAX];
    if (!read_back(&back, w, addr, msg, sizeof msg)) {
        finish(c, r, FIRC_OPKG_FAILED, true, "%s", msg);
        return S_DONE;
    }
    if (d_desc) { copy_str(r->kept, sizeof r->kept, back.description); }
    return settle(c, r, &back);
}

static seq_t create(ctx_t *c, rec_t *r, int k)
{
    const firc_opkgtun_want_t *w = &r->w;
    intent_t next = c->in;
    next.creating[k] = true;
    next.dirty = true;
    if (!put_intent(c, &next)) {
        finish(c, r, FIRC_OPKG_FAILED, true, "cannot write %s", c->io->intent_path);
        return S_DONE;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char err[MSG_MAX];
    post_t p = post_items(c, create_body(w->name, w->mark), T_WRITE, out, 1, err, sizeof err);
    if (p == P_STOP) { return S_STOP; }
    if (p == P_ANSWERED && out[0] == FIRC_OPKG_ITEM_REFUSED) {
        next = c->in;
        next.creating[k] = false;
        if (!c->pending) { next.dirty = false; }
        if (!put_intent(c, &next)) { FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path); }
        finish(c, r, FIRC_OPKG_FAILED, false, "%s", err);
        return S_DONE;
    }
    bool lost = p == P_LOST;
    bool applied = !lost && out[0] == FIRC_OPKG_ITEM_APPLIED;
    c->pending = true;
    r->unsaved = true;
    firc_opkgtun_rc_t rc;
    char lerr[MSG_MAX];
    lread_t lr = list_class(c, w->name, &rc, lerr, sizeof lerr);
    if (lr == L_STOP) { return S_STOP; }
    if (lr == L_UNKNOWN) {
        finish(c, r, FIRC_OPKG_PENDING, true, "%s", lerr);
        return S_DONE;
    }
    switch (rc.kind) {
    case FIRC_OPKG_RC_ABSENT:
        if (applied) {
            r->own_create = true;
            finish(c, r, FIRC_OPKG_PENDING, true, "waiting for the router to list %s", w->name);
        } else if (lost) {
            finish(c, r, FIRC_OPKG_FAILED, true, "the create of %s got no answer (%s) and the router does not list it",
                   w->name, err);
        } else {
            finish(c, r, FIRC_OPKG_FAILED, true, "the router answered the create of %s but does not list it",
                   w->name);
        }
        return S_DONE;
    case FIRC_OPKG_RC_FOREIGN:
        finish(c, r, FIRC_OPKG_FOREIGN, false, "%s is not firc's: its description is \"%.80s\"", w->name,
               rc.description);
        return S_DONE;
    case FIRC_OPKG_RC_UNMARKED:
    case FIRC_OPKG_RC_OURS:
        break;
    }
    return configure(c, r, &rc, true);
}

static seq_t present(ctx_t *c, rec_t *r)
{
    const firc_opkgtun_want_t *w = &r->w;
    int k = name_index(w->name);
    if (k < 0) {
        finish(c, r, FIRC_OPKG_FAILED, false, "%s is not an OpkgTun name", w->name);
        return S_DONE;
    }
    firc_opkgtun_rc_t rc;
    char err[MSG_MAX];
    lread_t lr = list_class(c, w->name, &rc, err, sizeof err);
    if (lr == L_STOP) { return S_STOP; }
    if (lr == L_UNKNOWN) {
        finish(c, r, FIRC_OPKG_PENDING, true, "%s", err);
        return S_DONE;
    }
    switch (rc.kind) {
    case FIRC_OPKG_RC_FOREIGN:
        finish(c, r, FIRC_OPKG_FOREIGN, false, "%s is not firc's: its description is \"%.80s\"", w->name,
               rc.description);
        return S_DONE;
    case FIRC_OPKG_RC_UNMARKED:
        if (!c->in.creating[k]) {
            finish(c, r, FIRC_OPKG_FOREIGN, false, "%s is not firc's: it has no description", w->name);
            return S_DONE;
        }
        return configure(c, r, &rc, false);
    case FIRC_OPKG_RC_OURS:
        return configure(c, r, &rc, false);
    case FIRC_OPKG_RC_ABSENT:
        break;
    }
    bool dev = c->io->dev_exists(c->io->ud, w->device);
    if (halted(c)) { return S_STOP; }
    if (dev) {
        if (r->own_create) {
            finish(c, r, FIRC_OPKG_PENDING, true, "waiting for the router to list %s", w->name);
        } else {
            finish(c, r, FIRC_OPKG_FOREIGN, false, "%s exists and is not a router interface", w->device);
        }
        return S_DONE;
    }
    char addr[20];
    want_addr(w->n, addr);
    char holder[16];
    hold_t h = hold(c, w, holder);
    if (h == H_STOP) { return S_STOP; }
    if (h == H_HELD) {
        finish(c, r, FIRC_OPKG_FAILED, true, "%s is already on %s", addr, holder);
        return S_DONE;
    }
    return create(c, r, k);
}

static rm_t clear_name_servers(ctx_t *c, const char *name, char *err, size_t err_cap)
{
    char *body = NULL;
    firc_err_t e = c->io->get(c->io->ud, FIRC_OPKGTUN_NS_PATH, &body);
    if (halted(c)) {
        free(body);
        return RM_STOP;
    }
    cJSON *root = e == FIRC_OK && body != NULL ? cJSON_Parse(body) : NULL;
    free(body);
    if (!cJSON_IsArray(root)) {
        cJSON_Delete(root);
        say(err, err_cap, "the router's name servers cannot be read (%s)",
            e != FIRC_OK ? firc_err_str(e) : "not a list");
        return RM_RETRY;
    }
    rm_t res = RM_DONE;
    const cJSON *ns = NULL;
    cJSON_ArrayForEach(ns, root) {
        const char *iface = str_at(ns, "interface");
        const char *addr = str_at(ns, "address");
        const char *domain = str_at(ns, "domain");
        if (iface == NULL || addr == NULL || strcmp(iface, name) != 0) { continue; }
        if (!mark_dirty(c)) {
            say(err, err_cap, "cannot write %s", c->io->intent_path);
            res = RM_RETRY;
            break;
        }
        firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
        char perr[MSG_MAX];
        post_t p = post_items(c, ns_body(addr, domain != NULL ? domain : "", name), T_WRITE, out, 1, perr,
                              sizeof perr);
        if (p == P_STOP) {
            res = RM_STOP;
            break;
        }
        if (p == P_LOST || out[0] != FIRC_OPKG_ITEM_REFUSED) { c->pending = true; }
        if (p == P_ANSWERED && out[0] != FIRC_OPKG_ITEM_APPLIED) {
            FIRC_WARN("opkgtun: name server %s on %s was not removed (%s); it goes with the interface", addr, name,
                      perr);
        }
        if (p == P_LOST) {
            say(err, err_cap, "name server %.40s on %s was not removed (%s)", addr, name, perr);
            res = RM_RETRY;
            break;
        }
    }
    cJSON_Delete(root);
    return res;
}

static rm_t clear_global(ctx_t *c, const char *name)
{
    if (!mark_dirty(c)) {
        FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path);
        return RM_RETRY;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, global_body(name, false), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) { return RM_STOP; }
    if (p == P_LOST || out[0] != FIRC_OPKG_ITEM_REFUSED) { c->pending = true; }
    if (p != P_ANSWERED || out[0] != FIRC_OPKG_ITEM_APPLIED) {
        FIRC_WARN("opkgtun: ip global on %s was not cleared (%s); it goes with the interface", name, perr);
    }
    return RM_DONE;
}

static rm_t remove_record(ctx_t *c, rec_t *r, const char *name, const char *dev, bool global, char *err,
                          size_t err_cap)
{
    rm_t res = route_clear(c, r, name, err, err_cap);
    if (res != RM_DONE) { return res; }
    if (global) {
        res = clear_global(c, name);
        if (res != RM_DONE) {
            say(err, err_cap, "cannot write %s", c->io->intent_path);
            return res;
        }
    }
    res = clear_name_servers(c, name, err, err_cap);
    if (res != RM_DONE) { return res; }
    if (!mark_dirty(c)) {
        say(err, err_cap, "cannot write %s", c->io->intent_path);
        return RM_RETRY;
    }
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char perr[MSG_MAX];
    post_t p = post_items(c, delete_body(name), T_WRITE, out, 1, perr, sizeof perr);
    if (p == P_STOP) { return RM_STOP; }
    c->last_a3 = now_of(c);
    c->any_a3 = true;
    if (p == P_LOST || out[0] != FIRC_OPKG_ITEM_REFUSED) { c->pending = true; }
    if (!c->pending) {
        intent_t next = c->in;
        next.dirty = false;
        if (!put_intent(c, &next)) { FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path); }
    }
    if (p == P_LOST || out[0] != FIRC_OPKG_ITEM_APPLIED) {
        firc_opkgtun_rc_t rc;
        char lerr[MSG_MAX];
        lread_t lr = list_class(c, name, &rc, lerr, sizeof lerr);
        if (lr == L_STOP) { return RM_STOP; }
        if (lr == L_UNKNOWN) {
            say(err, err_cap, "%s", lerr);
            return RM_RETRY;
        }
        if (rc.kind != FIRC_OPKG_RC_ABSENT) {
            say(err, err_cap, "the router did not remove %s (%s)", name, perr);
            return RM_RETRY;
        }
    }
    int d = dev_wait(c, dev, false);
    if (d < 0) { return RM_STOP; }
    if (d == 0) {
        firc_err_t e = c->io->unpersist(c->io->ud, dev);
        if (e != FIRC_OK) { FIRC_WARN("opkgtun: %s stays until a reboot (%s)", dev, firc_err_str(e)); }
    }
    return RM_DONE;
}

static seq_t absent(ctx_t *c, rec_t *r)
{
    firc_opkgtun_rc_t rc;
    char err[MSG_MAX];
    lread_t lr = list_class(c, r->w.name, &rc, err, sizeof err);
    if (lr == L_STOP) { return S_STOP; }
    if (lr == L_UNKNOWN) {
        finish(c, r, FIRC_OPKG_PENDING, true, "%s", err);
        return S_DONE;
    }
    if (rc.kind != FIRC_OPKG_RC_OURS) {
        finish(c, r, FIRC_OPKG_READY, false, "%s", "");
        return S_DONE;
    }
    rm_t res = remove_record(c, r, r->w.name, r->w.device, rc.global, err, sizeof err);
    if (res == RM_STOP) { return S_STOP; }
    if (res != RM_DONE) {
        finish(c, r, FIRC_OPKG_FAILED, true, "%s", err);
        return S_DONE;
    }
    r->unsaved = true;
    finish(c, r, FIRC_OPKG_READY, false, "%s", "");
    return S_DONE;
}

static bool kept(const char (*keep)[16], size_t n_keep, const char *name)
{
    for (size_t i = 0; i < n_keep; i++) {
        if (strcmp(keep[i], name) == 0) { return true; }
    }
    return false;
}

static rm_t sweep_core(ctx_t *c, const char (*keep)[16], size_t n_keep, size_t *removed, uint32_t *gone, char *err,
                       size_t err_cap)
{
    lread_t lr;
    cJSON *root = read_list(c, &lr, err, err_cap);
    if (lr == L_STOP) { return RM_STOP; }
    if (lr == L_UNKNOWN) { return RM_UNKNOWN; }
    char doomed[N_MAX][16];
    bool global[N_MAX];
    size_t n = 0;
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, root) {
        int k = name_index(e->string);
        if (k < 0 || kept(keep, n_keep, e->string)) { continue; }
        firc_opkgtun_rc_t rc;
        classify_in(root, e->string, &rc);
        if (rc.kind == FIRC_OPKG_RC_OURS && n < N_MAX) {
            global[n] = rc.global;
            copy_str(doomed[n++], sizeof doomed[0], e->string);
        } else if (rc.kind == FIRC_OPKG_RC_UNMARKED && c->in.creating[k]) {
            FIRC_WARN("opkgtun: %s has no description and may be firc's; delete it in the router if it is not",
                      e->string);
        }
    }
    cJSON_Delete(root);
    rm_t res = RM_DONE;
    for (size_t i = 0; i < n; i++) {
        char dev[16];
        say(dev, sizeof dev, "opkgtun%d", name_index(doomed[i]));
        char rerr[MSG_MAX];
        rm_t one = remove_record(c, NULL, doomed[i], dev, global[i], rerr, sizeof rerr);
        if (one == RM_STOP) { return RM_STOP; }
        if (one == RM_DONE) {
            (*removed)++;
            *gone |= 1u << name_index(doomed[i]);
            FIRC_INFO("opkgtun: %s is removed", doomed[i]);
            if (global[i]) {
                FIRC_WARN("opkgtun: %s was removed; router policies that used it no longer have it", doomed[i]);
            }
        } else {
            FIRC_WARN("opkgtun: %s", rerr);
            if (res == RM_DONE) { say(err, err_cap, "%s", rerr); }
            res = RM_RETRY;
        }
    }
    return res;
}

typedef enum { SV_OK, SV_FAIL, SV_STOP, SV_CUT } save_t;

static save_t save_now(ctx_t *c)
{
    firc_opkg_item_t out[1] = {FIRC_OPKG_ITEM_REFUSED};
    char err[MSG_MAX];
    post_t p = post_items(c, save_body(), T_SAVE, out, 1, err, sizeof err);
    if (p == P_STOP) { return SV_STOP; }
    if (p != P_ANSWERED || out[0] != FIRC_OPKG_ITEM_APPLIED) {
        FIRC_WARN("opkgtun: the router's configuration was not saved (%s)", err);
        return SV_FAIL;
    }
    c->pending = false;
    intent_t next = c->in;
    next.dirty = false;
    if (!put_intent(c, &next)) {
        FIRC_WARN("opkgtun: cannot write %s", c->io->intent_path);
        c->in.dirty = false;
    }
    FIRC_INFO("opkgtun: the router's configuration is saved");
    return SV_OK;
}

firc_err_t firc_opkgtun_sweep(const firc_opkgtun_io_t *io, const firc_opkgtun_want_t *keep, size_t n_keep,
                              size_t *removed, bool *unsaved)
{
    *removed = 0;
    if (unsaved != NULL) { *unsaved = false; }
    ctx_t c = {.io = io};
    read_intent(io->intent_path, &c.in);
    c.pending = c.in.dirty;
    char (*names)[16] = calloc(n_keep > 0 ? n_keep : 1, sizeof *names);
    if (names == NULL) { return FIRC_ERR_NOMEM; }
    for (size_t i = 0; i < n_keep; i++) { copy_str(names[i], sizeof names[i], keep[i].name); }
    char err[MSG_MAX] = "";
    uint32_t gone = 0;
    rm_t res = sweep_core(&c, (const char (*)[16])names, n_keep, removed, &gone, err, sizeof err);
    free(names);
    if (res == RM_STOP) { return FIRC_ERR_CANCELED; }
    if (res == RM_UNKNOWN) {
        FIRC_WARN("opkgtun: %s", err);
        return FIRC_ERR_UPSTREAM;
    }
    if (c.in.dirty) {
        for (int64_t now = now_of(&c); c.any_a3 && now < c.last_a3 + DELETE_GAP_MS; now = now_of(&c)) {
            int64_t left = c.last_a3 + DELETE_GAP_MS - now;
            if (!io->sleep_ms(io->ud, (int)(left < NAP_MAX_MS ? left : NAP_MAX_MS)) || halted(&c)) {
                return FIRC_ERR_CANCELED;
            }
        }
        save_t s = save_now(&c);
        if (s == SV_STOP) { return FIRC_ERR_CANCELED; }
        if (s == SV_FAIL && unsaved != NULL) { *unsaved = true; }
        if (s == SV_FAIL && res == RM_DONE) { return FIRC_ERR_STATE; }
    }
    return res == RM_DONE ? FIRC_OK : FIRC_ERR_UPSTREAM;
}

static void signal_fd(firc_opkgtun_t *m)
{
    char b = 1;
    ssize_t n = write(m->wfd, &b, 1);
    (void)n;
}

static void publish(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    for (size_t i = 0; i < m->n_recs; i++) {
        m->recs[i].s.saved = !m->recs[i].unsaved;
        m->seen[i] = m->recs[i].s;
    }
    m->n_seen = m->n_recs;
    pthread_mutex_unlock(&m->mu);
    signal_fd(m);
}

static bool same_want(const firc_opkgtun_want_t *a, const firc_opkgtun_want_t *b)
{
    return strcmp(a->name, b->name) == 0 && strcmp(a->device, b->device) == 0 && a->n == b->n &&
           strcmp(a->mark, b->mark) == 0 && strcmp(a->desc, b->desc) == 0 && a->present == b->present;
}

static void sync_wants(firc_opkgtun_t *m)
{
    firc_opkgtun_want_t wants[FIRC_OPKGTUN_MAX_WANTS];
    size_t n = 0;
    bool fresh = false;
    pthread_mutex_lock(&m->mu);
    if (m->gen != m->gen_seen) {
        n = m->n_wants;
        memcpy(wants, m->wants, n * sizeof wants[0]);
        m->gen_seen = m->gen;
        fresh = true;
    }
    if (m->sweep_req) {
        m->sweep_req = false;
        m->sweep_due = true;
        m->sweep_next = 0;
        m->sweep_attempts = 0;
    }
    pthread_mutex_unlock(&m->mu);
    if (!fresh) { return; }
    rec_t *next = m->scratch;
    memset(next, 0, sizeof m->scratch);
    for (size_t i = 0; i < n; i++) {
        rec_t *r = &next[i];
        const rec_t *old = NULL;
        for (size_t j = 0; j < m->n_recs && old == NULL; j++) {
            if (strcmp(m->recs[j].w.name, wants[i].name) == 0) { old = &m->recs[j]; }
        }
        if (old != NULL && same_want(&old->w, &wants[i])) {
            *r = *old;
            if (old->w.policies != wants[i].policies || old->w.route != wants[i].route ||
                old->w.keep != wants[i].keep) {
                r->due = true;
                r->next_ms = 0;
                r->pol_attempts = 0;
            }
            if (old->w.recheck != wants[i].recheck) {
                r->due = true;
                r->next_ms = 0;
                r->attempts = 0;
                r->s.state = FIRC_OPKG_PENDING;
            }
        } else {
            r->unsaved = old != NULL ? old->unsaved : m->c.in.dirty;
            if (old != NULL) {
                r->hold = old->hold;
                r->adopt = old->adopt;
                r->s.route = old->s.route;
                r->s.route_reads = old->s.route_reads;
                r->s.route_adds = old->s.route_adds;
            } else {
                r->adopt = m->c.in.dirty;
            }
            r->due = true;
            copy_str(r->s.name, sizeof r->s.name, wants[i].name);
            r->s.state = FIRC_OPKG_PENDING;
            r->s.removing = !wants[i].present;
        }
        if (wants[i].checked == r->s.route_adds) { r->hold = false; }
        r->w = wants[i];
    }
    memcpy(m->recs, next, sizeof m->recs);
    m->n_recs = n;
    publish(m);
}

static bool seen_moved(const firc_opkgtun_seen_t *a, const firc_opkgtun_seen_t *b)
{
    return a->state != b->state || strcmp(a->error, b->error) != 0;
}

static bool nap(firc_opkgtun_t *m, int64_t ms)
{
    int d = (int)(ms < NAP_MAX_MS ? (ms > 0 ? ms : 1) : NAP_MAX_MS);
    return m->io.sleep_ms(m->io.ud, d) && !halted(&m->c);
}

static void wait_work(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    while (!halted(&m->c) && m->gen == m->gen_seen && !m->sweep_req && m->trips == 0) {
        m->idle = true;
        pthread_cond_wait(&m->cv, &m->mu);
    }
    m->idle = false;
    pthread_mutex_unlock(&m->mu);
}

static bool anything_of_ours(const intent_t *in)
{
    bool any = in->dirty;
    for (int k = 0; k < N_MAX; k++) { any = any || in->creating[k]; }
    return any;
}

static bool run_sweep(firc_opkgtun_t *m)
{
    char keep[FIRC_OPKGTUN_MAX_WANTS][16];
    size_t n = 0;
    pthread_mutex_lock(&m->mu);
    for (size_t i = 0; i < m->n_wants; i++) { copy_str(keep[n++], sizeof keep[0], m->wants[i].name); }
    pthread_mutex_unlock(&m->mu);
    size_t removed = 0;
    uint32_t gone = 0;
    char err[MSG_MAX] = "";
    rm_t res = sweep_core(&m->c, (const char (*)[16])keep, n, &removed, &gone, err, sizeof err);
    if (gone != 0) {
        pthread_mutex_lock(&m->mu);
        m->swept |= gone;
        pthread_mutex_unlock(&m->mu);
        signal_fd(m);
    }
    if (res == RM_STOP) { return false; }
    if (res == RM_DONE) {
        if (m->sweep_warned) { FIRC_INFO("opkgtun: the sweep ran"); }
        m->sweep_warned = false;
        m->sweep_due = false;
        m->sweep_attempts = 0;
    } else {
        if (res == RM_UNKNOWN && !m->sweep_warned && (n > 0 || anything_of_ours(&m->c.in))) {
            FIRC_WARN("opkgtun: the sweep waits: %s", err);
            m->sweep_warned = true;
        } else if (res == RM_UNKNOWN) {
            FIRC_DEBUG("opkgtun: the sweep waits: %s", err);
        }
        m->sweep_next = now_of(&m->c) + backoff(&m->sweep_attempts);
    }
    return true;
}

static bool run_trips(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    uint32_t bits = m->trips;
    m->trips = 0;
    pthread_mutex_unlock(&m->mu);
    for (size_t i = 0; bits != 0 && i < m->n_recs; i++) {
        rec_t *r = &m->recs[i];
        int k = name_index(r->w.name);
        if (k < 0 || (bits & (1u << k)) == 0 || !r->w.present) { continue; }
        char err[MSG_MAX];
        rm_t res = route_clear(&m->c, r, r->w.name, err, sizeof err);
        if (res == RM_STOP) { return false; }
        if (res != RM_DONE) { pol_set(r, FIRC_OPKG_POL_FAILED, "%s", err); }
        publish(m);
    }
    return true;
}

static bool held(const firc_opkgtun_t *m)
{
    for (size_t i = 0; i < m->n_recs; i++) {
        if (m->recs[i].hold) { return true; }
    }
    return false;
}

static save_t save_cut(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    bool queued = m->trips != 0;
    m->saving = !queued;
    pthread_mutex_unlock(&m->mu);
    if (queued) { return SV_CUT; }
    save_t s = save_now(&m->c);
    pthread_mutex_lock(&m->mu);
    m->saving = false;
    if (m->io.cut != NULL && atomic_exchange(m->io.cut, false) && s == SV_FAIL) { s = SV_CUT; }
    pthread_mutex_unlock(&m->mu);
    return s;
}

static int64_t later(int64_t a, int64_t b)
{
    return a > b ? a : b;
}

static void *worker(void *arg)
{
    firc_opkgtun_t *m = arg;
    ctx_t *c = &m->c;
    int64_t t0 = now_of(c);
    m->gate_ms = c->in.dirty ? t0 + DIRTY_WAIT_MS : t0;
    m->settled_at = t0;
    while (!halted(c)) {
        sync_wants(m);
        if (!run_trips(m)) { break; }
        int64_t now = now_of(c);
        if (now < m->gate_ms) {
            if (!nap(m, m->gate_ms - now)) { break; }
            continue;
        }
        bool worked = false;
        bool moved = false;
        for (size_t i = 0; i < m->n_recs; i++) {
            rec_t *r = &m->recs[i];
            if (!r->due || r->next_ms > now) { continue; }
            firc_opkgtun_seen_t before = r->s;
            bool fresh = r->attempts == 0;
            seq_t s = r->w.present ? present(c, r) : absent(c, r);
            if (s == S_STOP) { return NULL; }
            r->s.rechecked = r->w.recheck;
            moved = moved || fresh || seen_moved(&before, &r->s);
            worked = true;
            publish(m);
        }
        if (m->sweep_due && m->sweep_next <= now) {
            if (!run_sweep(m)) { return NULL; }
            worked = true;
            moved = true;
        }
        if (moved) { m->settled_at = now_of(c); }
        if (worked) { continue; }
        int64_t deadline = INT64_MAX;
        if (c->in.dirty && !held(m)) {
            int64_t at = later(later(m->settled_at + DEBOUNCE_MS, m->gate_ms), m->save_next);
            if (c->any_a3) { at = later(at, c->last_a3 + DELETE_GAP_MS); }
            if (now >= at) {
                save_t s = save_cut(m);
                if (s == SV_STOP) { return NULL; }
                if (s == SV_OK) {
                    m->save_attempts = 0;
                    m->save_next = 0;
                    for (size_t i = 0; i < m->n_recs; i++) { m->recs[i].unsaved = false; }
                } else if (s == SV_FAIL) {
                    m->save_next = now_of(c) + backoff(&m->save_attempts);
                }
                publish(m);
                continue;
            }
            deadline = at;
        }
        for (size_t i = 0; i < m->n_recs; i++) {
            if (m->recs[i].due && m->recs[i].next_ms < deadline) { deadline = m->recs[i].next_ms; }
        }
        if (m->sweep_due && m->sweep_next < deadline) { deadline = m->sweep_next; }
        if (deadline == INT64_MAX) {
            wait_work(m);
        } else if (!nap(m, deadline - now)) {
            break;
        }
    }
    return NULL;
}

static bool make_pipe(firc_opkgtun_t *m)
{
    int p[2];
    if (pipe(p) != 0) { return false; }
    for (int i = 0; i < 2; i++) {
        int fl = fcntl(p[i], F_GETFL);
        if (fl < 0 || fcntl(p[i], F_SETFL, fl | O_NONBLOCK) != 0 || fcntl(p[i], F_SETFD, FD_CLOEXEC) != 0) {
            close(p[0]);
            close(p[1]);
            return false;
        }
    }
    m->rfd = p[0];
    m->wfd = p[1];
    return true;
}

firc_opkgtun_t *firc_opkgtun_start(const firc_opkgtun_io_t *io)
{
    firc_opkgtun_t *m = calloc(1, sizeof *m);
    if (m == NULL) { return NULL; }
    if (!make_pipe(m)) {
        free(m);
        return NULL;
    }
    m->io = *io;
    if (m->io.stop == NULL) { m->io.stop = &m->own_stop; }
    m->c.io = &m->io;
    pthread_mutex_init(&m->mu, NULL);
    pthread_cond_init(&m->cv, NULL);
#ifdef FIRC_ENTWARE_KN
    m->host = false;
#else
    m->host = !atomic_load(&g_reach);
#endif
    if (m->host) { return m; }
    read_intent(m->io.intent_path, &m->c.in);
    m->c.pending = m->c.in.dirty;
    if (pthread_create(&m->th, NULL, worker, m) != 0) {
        FIRC_ERROR("opkgtun: the worker thread cannot start");
        pthread_cond_destroy(&m->cv);
        pthread_mutex_destroy(&m->mu);
        close(m->rfd);
        close(m->wfd);
        free(m);
        return NULL;
    }
    m->started = true;
    return m;
}

static bool wants_ok(const firc_opkgtun_want_t *w, size_t n)
{
    if (n > FIRC_OPKGTUN_MAX_WANTS) {
        FIRC_WARN("opkgtun: %zu records wanted, at most %d; the last set stays", n, FIRC_OPKGTUN_MAX_WANTS);
        return false;
    }
    for (size_t i = 0; i < n; i++) {
        for (size_t j = i + 1; j < n; j++) {
            if (strcmp(w[i].name, w[j].name) == 0) {
                FIRC_WARN("opkgtun: %s is wanted twice; the last set stays", w[i].name);
                return false;
            }
        }
    }
    return true;
}

void firc_opkgtun_want(firc_opkgtun_t *m, const firc_opkgtun_want_t *w, size_t n)
{
    if (!wants_ok(w, n)) { return; }
    pthread_mutex_lock(&m->mu);
    if (n > 0) { memcpy(m->wants, w, n * sizeof *w); }
    m->n_wants = n;
    m->gen++;
    if (m->host) {
        for (size_t i = 0; i < n; i++) {
            firc_opkgtun_seen_t *s = &m->seen[i];
            memset(s, 0, sizeof *s);
            copy_str(s->name, sizeof s->name, w[i].name);
            s->state = w[i].present ? FIRC_OPKG_FAILED : FIRC_OPKG_READY;
            s->saved = true;
            s->removing = !w[i].present;
            s->rechecked = w[i].recheck;
            if (w[i].present) { copy_str(s->error, sizeof s->error, NO_HOST); }
        }
        m->n_seen = n;
    }
    pthread_cond_broadcast(&m->cv);
    pthread_mutex_unlock(&m->mu);
    if (m->host) { signal_fd(m); }
}

void firc_opkgtun_trip(firc_opkgtun_t *m, const char *name)
{
    int k = name_index(name);
    if (k < 0) { return; }
    pthread_mutex_lock(&m->mu);
    m->trips |= 1u << k;
    if (m->saving && m->io.cut != NULL) { atomic_store(m->io.cut, true); }
    pthread_cond_broadcast(&m->cv);
    pthread_mutex_unlock(&m->mu);
}

void firc_opkgtun_sweep_now(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    m->sweep_req = !m->host;
    pthread_cond_broadcast(&m->cv);
    pthread_mutex_unlock(&m->mu);
}

size_t firc_opkgtun_seen(firc_opkgtun_t *m, firc_opkgtun_seen_t *out, size_t cap)
{
    char buf[64];
    while (read(m->rfd, buf, sizeof buf) > 0) {}
    pthread_mutex_lock(&m->mu);
    size_t n = m->n_seen < cap ? m->n_seen : cap;
    memcpy(out, m->seen, n * sizeof *out);
    pthread_mutex_unlock(&m->mu);
    return n;
}

uint32_t firc_opkgtun_swept(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    uint32_t v = m->swept;
    m->swept = 0;
    pthread_mutex_unlock(&m->mu);
    return v;
}

int firc_opkgtun_fd(const firc_opkgtun_t *m)
{
    return m->rfd;
}

bool firc_opkgtun_idle_for_test(firc_opkgtun_t *m)
{
    pthread_mutex_lock(&m->mu);
    bool idle = m->idle;
    pthread_mutex_unlock(&m->mu);
    return idle;
}

void firc_opkgtun_stop(firc_opkgtun_t *m)
{
    if (m == NULL) { return; }
    atomic_store(m->io.stop, true);
    if (m->io.cut != NULL) { atomic_store(m->io.cut, true); }
    pthread_mutex_lock(&m->mu);
    pthread_cond_broadcast(&m->cv);
    pthread_mutex_unlock(&m->mu);
    if (m->started) { pthread_join(m->th, NULL); }
    pthread_cond_destroy(&m->cv);
    pthread_mutex_destroy(&m->mu);
    close(m->rfd);
    close(m->wfd);
    free(m);
}
