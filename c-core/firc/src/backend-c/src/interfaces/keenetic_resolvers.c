#include "firc/keenetic_resolvers.h"

#include <cjson/cJSON.h>
#include <pthread.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/log.h"
#include "firc/logrep.h"

static bool addr_equal(const firc_resolver_addr_t *a, const firc_resolver_addr_t *b)
{
    return a->port == b->port && a->ip.len == b->ip.len && memcmp(a->ip.b, b->ip.b, a->ip.len) == 0;
}

static int cmp_iface(const void *a, const void *b)
{
    const firc_kn_iface_resolvers_t *x = a, *y = b;
    return strcmp(x->iface, y->iface);
}

static firc_kn_iface_resolvers_t *slot_for(firc_kn_resolver_map_t *m, size_t *cap, const char *iface)
{
    for (size_t i = 0; i < m->n; i++) {
        if (strcmp(m->items[i].iface, iface) == 0) { return &m->items[i]; }
    }
    if (m->n == *cap) {
        size_t nc = *cap ? *cap * 2 : 4;
        firc_kn_iface_resolvers_t *ni = realloc(m->items, nc * sizeof(*ni));
        if (ni == NULL) { return NULL; }
        m->items = ni;
        *cap = nc;
    }
    firc_kn_iface_resolvers_t *s = &m->items[m->n++];
    memset(s, 0, sizeof(*s));
    snprintf(s->iface, sizeof(s->iface), "%s", iface);
    return s;
}

/* *skip is set for split DNS and the global list, which are not errors. */
static bool entry_parse(const cJSON *e, const firc_kn_id_map_t *ids, const char **iface,
                        firc_resolver_addr_t *a, bool *skip, bool *unknown)
{
    *skip = false;
    *unknown = false;
    if (!cJSON_IsObject(e)) { return false; }
    const cJSON *dom = cJSON_GetObjectItemCaseSensitive(e, "domain");
    if (cJSON_IsString(dom) && dom->valuestring[0] != '\0') {
        *skip = true;
        return false;
    }
    const cJSON *ifc = cJSON_GetObjectItemCaseSensitive(e, "interface");
    if (ifc == NULL || (cJSON_IsString(ifc) && ifc->valuestring[0] == '\0')) {
        *skip = true;
        return false;
    }
    if (!cJSON_IsString(ifc)) { return false; }
    const cJSON *addr = cJSON_GetObjectItemCaseSensitive(e, "address");
    if (!cJSON_IsString(addr) || strchr(addr->valuestring, '[') != NULL ||
        strchr(addr->valuestring, ']') != NULL) {
        return false;
    }
    /* bare address only; a colon in v4 text is refused */
    if (firc_resolver_addr_parse(addr->valuestring, a) != FIRC_RESOLVER_ADDR_OK || a->port != 53 ||
        (a->ip.len == 4 && strchr(addr->valuestring, ':') != NULL)) {
        return false;
    }
    const cJSON *port = cJSON_GetObjectItemCaseSensitive(e, "port");
    if (port != NULL) {
        if (!cJSON_IsNumber(port) || port->valuedouble < 1 || port->valuedouble > 65535 ||
            port->valuedouble != (double)(int)port->valuedouble) {
            return false;
        }
        a->port = (uint16_t)port->valueint;
    }
    *iface = firc_kn_id_map_lookup(ids, ifc->valuestring);
    *unknown = *iface == NULL;
    return *iface != NULL;
}

firc_err_t firc_kn_resolver_map_parse(const char *json, const firc_kn_id_map_t *ids,
                                      firc_kn_resolver_map_t *out)
{
    memset(out, 0, sizeof(*out));
    cJSON *root = json != NULL ? cJSON_Parse(json) : NULL;
    if (!cJSON_IsArray(root)) {
        cJSON_Delete(root);
        return FIRC_ERR_PROTO;
    }
    size_t cap = 0;
    const cJSON *e = NULL;
    cJSON_ArrayForEach(e, root) {
        const char *iface = NULL;
        firc_resolver_addr_t a;
        bool skip = false, unknown = false;
        if (!entry_parse(e, ids, &iface, &a, &skip, &unknown)) {
            if (!skip) { out->dropped++; }
            if (unknown) { out->unknown_ids++; }
            continue;
        }
        firc_kn_iface_resolvers_t *s = slot_for(out, &cap, iface);
        if (s == NULL) {
            cJSON_Delete(root);
            firc_kn_resolver_map_free(out);
            return FIRC_ERR_NOMEM;
        }
        bool dup = false;
        for (size_t i = 0; i < s->n && !dup; i++) { dup = addr_equal(&s->servers[i], &a); }
        if (dup) { continue; }
        if (s->n == FIRC_RESOLVE_MAX_SERVERS) {
            out->dropped++;
            continue;
        }
        s->servers[s->n++] = a;
    }
    cJSON_Delete(root);
    /* Sorted so map_equal ignores the firmware's listing order. */
    if (out->n > 1) { qsort(out->items, out->n, sizeof(*out->items), cmp_iface); }
    return FIRC_OK;
}

bool firc_kn_resolver_map_equal(const firc_kn_resolver_map_t *a, const firc_kn_resolver_map_t *b)
{
    if (a->n != b->n) { return false; }
    for (size_t i = 0; i < a->n; i++) {
        const firc_kn_iface_resolvers_t *x = &a->items[i], *y = &b->items[i];
        if (strcmp(x->iface, y->iface) != 0 || x->n != y->n) { return false; }
        for (size_t j = 0; j < x->n; j++) {
            if (!addr_equal(&x->servers[j], &y->servers[j])) { return false; }
        }
    }
    return true;
}

void firc_kn_resolver_map_free(firc_kn_resolver_map_t *m)
{
    if (m == NULL) { return; }
    free(m->items);
    memset(m, 0, sizeof(*m));
}

struct firc_kn_resolvers {
    pthread_mutex_t mu;
    pthread_cond_t cv;
    firc_kn_resolver_map_t map;
    char *base_url;
    unsigned refresh_secs;
    bool running, stop;
    pthread_t th;
    firc_kn_resolvers_changed_fn on_change;
    void *ud;
    size_t last_dropped;
    /* Cached id map; refresher thread only. */
    firc_kn_id_map_t ids;
    bool have_ids;
};

firc_err_t firc_kn_resolvers_refresh_from(firc_kn_resolvers_t *r, const char *body,
                                          firc_kn_id_map_fetch_fn fetch_ids, void *ud)
{
    firc_kn_resolver_map_t m = {0};
    /* Validate the body's shape before any id-map fetch. */
    firc_err_t err = firc_kn_resolver_map_parse(body, &r->ids, &m);
    if (err != FIRC_OK) { return err; } /* not a list: an id map wouldn't help */
    if (!r->have_ids || m.unknown_ids > 0) {
        firc_kn_id_map_t fresh = {0};
        err = fetch_ids(ud, &fresh);
        if (err != FIRC_OK) {
            firc_kn_resolver_map_free(&m);
            return err;
        }
        firc_kn_id_map_free(&r->ids);
        r->ids = fresh;
        r->have_ids = true;
        firc_kn_resolver_map_free(&m);
        err = firc_kn_resolver_map_parse(body, &r->ids, &m);
        if (err != FIRC_OK) { return err; }
    }
    if (m.dropped != 0 && m.dropped != r->last_dropped) {
        FIRC_WARN("keenetic resolvers: %zu name-server entr%s could not be used (an interface "
                  "id with no kernel name, a malformed address or port, or more than %d on one "
                  "interface)",
                  m.dropped, m.dropped == 1 ? "y" : "ies", FIRC_RESOLVE_MAX_SERVERS);
    }
    r->last_dropped = m.dropped;
    firc_kn_resolvers_swap(r, &m);
    return FIRC_OK;
}

static firc_err_t fetch_ids_rci(void *ud, firc_kn_id_map_t *out)
{
    return firc_kn_get_id_map_from((const char *)ud, out);
}

static firc_err_t fetch(firc_kn_resolvers_t *r)
{
    char *body = NULL;
    firc_err_t err = firc_kn_rci_get(r->base_url, "/rci/show/rc/ip/name-server", &body);
    if (err != FIRC_OK) { return err; }
    err = firc_kn_resolvers_refresh_from(r, body, fetch_ids_rci, r->base_url);
    free(body);
    return err;
}

static void *refresher(void *ud)
{
    firc_kn_resolvers_t *r = ud;
    unsigned retry = 1;
    firc_logrep_t failing = {0};
    pthread_mutex_lock(&r->mu);
    while (!r->stop) {
        pthread_mutex_unlock(&r->mu);
        firc_err_t err = fetch(r);
        unsigned wait;
        if (err == FIRC_OK) {
            (void)firc_logrep_ok(&failing, NULL, "keenetic resolvers: RCI");
            retry = 1;
            wait = r->refresh_secs;
        } else {
            /* the last good list stays */
            firc_logrep_fail(&failing, FIRC_LOG_WARN, NULL, "keenetic resolvers: refresh failed: %s", firc_err_str(err));
            wait = retry < r->refresh_secs ? retry : r->refresh_secs;
            if (retry < r->refresh_secs) { retry *= 2; }
        }
        pthread_mutex_lock(&r->mu);
        if (r->stop) { break; }
        struct timespec until;
        clock_gettime(CLOCK_MONOTONIC, &until);
        until.tv_sec += (time_t)wait;
        pthread_cond_timedwait(&r->cv, &r->mu, &until);
    }
    pthread_mutex_unlock(&r->mu);
    return NULL;
}

firc_kn_resolvers_t *firc_kn_resolvers_start(const char *base_url, unsigned refresh_secs,
                                             firc_kn_resolvers_changed_fn on_change, void *ud)
{
    firc_kn_resolvers_t *r = calloc(1, sizeof(*r));
    if (r == NULL) { return NULL; }
    pthread_mutex_init(&r->mu, NULL);
    pthread_condattr_t ca;
    pthread_condattr_init(&ca);
    pthread_condattr_setclock(&ca, CLOCK_MONOTONIC); /* routers have no RTC */
    pthread_cond_init(&r->cv, &ca);
    pthread_condattr_destroy(&ca);
    r->refresh_secs = refresh_secs ? refresh_secs : 30;
    r->on_change = on_change;
    r->ud = ud;
    if (base_url != NULL) {
        r->base_url = strdup(base_url);
        if (r->base_url == NULL || pthread_create(&r->th, NULL, refresher, r) != 0) {
            firc_kn_resolvers_stop(r);
            return NULL;
        }
        r->running = true;
    }
    return r;
}

void firc_kn_resolvers_stop(firc_kn_resolvers_t *r)
{
    if (r == NULL) { return; }
    pthread_mutex_lock(&r->mu);
    r->stop = true;
    pthread_cond_broadcast(&r->cv);
    bool joinable = r->running;
    pthread_mutex_unlock(&r->mu);
    if (joinable) { pthread_join(r->th, NULL); }
    firc_kn_resolver_map_free(&r->map);
    firc_kn_id_map_free(&r->ids);
    free(r->base_url);
    pthread_cond_destroy(&r->cv);
    pthread_mutex_destroy(&r->mu);
    free(r);
}

void firc_kn_resolvers_swap(firc_kn_resolvers_t *r, firc_kn_resolver_map_t *m)
{
    pthread_mutex_lock(&r->mu);
    firc_kn_resolver_map_t old = r->map;
    bool changed = !firc_kn_resolver_map_equal(&old, m);
    r->map = *m;
    pthread_mutex_unlock(&r->mu);
    memset(m, 0, sizeof(*m));
    firc_kn_resolver_map_free(&old);
    if (changed && r->on_change != NULL) { r->on_change(r->ud); }
}

size_t firc_kn_resolvers_for(firc_kn_resolvers_t *r, const char *kernel_iface, firc_resolver_addr_t *out,
                             size_t cap)
{
    if (r == NULL || kernel_iface == NULL) { return 0; }
    size_t n = 0;
    pthread_mutex_lock(&r->mu);
    for (size_t i = 0; i < r->map.n; i++) {
        if (strcmp(r->map.items[i].iface, kernel_iface) == 0) {
            n = r->map.items[i].n < cap ? r->map.items[i].n : cap;
            memcpy(out, r->map.items[i].servers, n * sizeof(*out));
            break;
        }
    }
    pthread_mutex_unlock(&r->mu);
    return n;
}

size_t firc_kn_resolvers_list(firc_kn_resolvers_t *r, firc_kn_iface_resolvers_t *out, size_t cap)
{
    if (r == NULL) { return 0; }
    pthread_mutex_lock(&r->mu);
    size_t n = r->map.n < cap ? r->map.n : cap;
    memcpy(out, r->map.items, n * sizeof(*out));
    pthread_mutex_unlock(&r->mu);
    return n;
}
