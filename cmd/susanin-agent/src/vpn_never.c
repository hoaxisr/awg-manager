#define _GNU_SOURCE
#include "vpn_never.h"
#include "vpn_always.h"
#include "backend.h"
#include "log.h"

#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#define VN_MAXDOM 256
#define VN_MAXIP  32
#define VN_TRACK  2048
#define VN_BUDGET_MS 4000
#define VN_QUERY_MAX_MS 2000
#define VN_BACKOFF_S 60
#define VN_FAIL_BACKOFF_S 3600  /* после 3 неудач подряд — проверять раз в час */
#define VN_RECENT 512           /* кэш адресов, недавно снятых из susanin_never */
#define VN_RECENT_TTL_S 3600
#define VN_SET "susanin_never"

typedef struct {
    char name[256];
    char ips[VN_MAXIP][16];
    int nips;
    int is_net;
    int wild;               /* *.domain: домен и все поддомены */
    int fails;              /* подряд неудачных резолвов (backoff) */
    time_t next_try;
} vn_dom;

/* Адреса, недавно снятые из susanin_never: их не учим в VPN какое-то время,
 * чтобы адрес не «прыгал» direct <-> VPN (план п.8.3). */
struct vn_recent { char ip[64]; time_t until; };
static struct vn_recent vn_recent[VN_RECENT];
static int vn_recent_n;

static void recent_note(const char *ip, time_t now)
{
    int i, oldest = 0;
    if (!ip || !ip[0] || strchr(ip, '/'))
        return;
    for (i = 0; i < vn_recent_n; i++) {
        if (strcmp(vn_recent[i].ip, ip) == 0) {
            vn_recent[i].until = now + VN_RECENT_TTL_S;
            return;
        }
        if (vn_recent[i].until < vn_recent[oldest].until)
            oldest = i;
    }
    if (vn_recent_n < VN_RECENT)
        i = vn_recent_n++;
    else {
        i = oldest;
        if (vn_recent[i].until > now)
            return;             /* все свежие — не вытесняем */
    }
    snprintf(vn_recent[i].ip, sizeof(vn_recent[i].ip), "%s", ip);
    vn_recent[i].until = now + VN_RECENT_TTL_S;
}

int vn_is_recently_never(const char *ip, time_t now)
{
    int i;
    for (i = 0; i < vn_recent_n; i++)
        if (strcmp(vn_recent[i].ip, ip) == 0)
            return now < vn_recent[i].until;
    return 0;
}

struct vpn_never {
    vn_dom dom[VN_MAXDOM];
    int nd;
    char track[VN_TRACK][64];
    unsigned char tmiss[VN_TRACK];
    int ntrack;
    long long seen_mtime;
    long long seen_size;
    int seen_exists;
    int warned;
    int dirty;
    long long epoch;        /* номер интервала (now/interval) для hysteresis */
};

vpn_never *vn_new(void)
{
    return calloc(1, sizeof(vpn_never));
}

void vn_free(vpn_never *v)
{
    free(v);
}

void vn_mark_dirty(vpn_never *v)
{
    if (v)
        v->dirty = 1;
}

static void trim_line(char *s)
{
    char *p = s + strlen(s);
    while (p > s && (p[-1] == ' ' || p[-1] == '\t' || p[-1] == '\r' ||
                     p[-1] == '\n'))
        *--p = '\0';
}

static void lower_str(char *s)
{
    for (; *s; s++)
        *s = (char)tolower((unsigned char)*s);
}

static int valid_cidr(const char *s, char *out, size_t n)
{
    char buf[48], *slash, *e;
    struct in_addr a;
    long pre;
    if (strlen(s) >= sizeof(buf))
        return 0;
    snprintf(buf, sizeof(buf), "%s", s);
    slash = strchr(buf, '/');
    if (!slash)
        return 0;
    *slash = '\0';
    if (inet_pton(AF_INET, buf, &a) != 1)
        return 0;
    errno = 0;
    pre = strtol(slash + 1, &e, 10);
    if (e == slash + 1 || *e != '\0' || errno != 0 || pre < 0 || pre > 32)
        return 0;
    snprintf(out, n, "%s", s);
    return 1;
}

static int valid_name(const char *s, char *out, size_t n)
{
    struct in_addr a;
    const char *p;
    size_t len = strlen(s);
    if (len == 0 || len >= n)
        return 0;
    if (inet_pton(AF_INET, s, &a) == 1) {
        snprintf(out, n, "%s", s);
        return 1;
    }
    if (strchr(s, '/'))
        return valid_cidr(s, out, n);
    if (len > 253)
        return 0;
    for (p = s; *p; p++) {
        char c = (char)tolower((unsigned char)*p);
        if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
              c == '.' || c == '-'))
            return 0;
    }
    snprintf(out, n, "%s", s);
    return 1;
}

static int file_load(const char *path, char names[][256], int wilds[],
                     int maxnames)
{
    FILE *fp = fopen(path, "r");
    char line[320];
    int n = 0;
    if (!fp)
        return -1;
    while (fgets(line, sizeof(line), fp)) {
        char buf[256];
        const char *p = line;
        int i, wild = 0;
        size_t l;
        trim_line(line);
        if (line[0] == '\0' || line[0] == '#')
            continue;
        if (line[0] == '*' && line[1] == '.') {
            wild = 1;
            p = line + 2;
        }
        lower_str((char *)p);
        if (!valid_name(p, buf, sizeof(buf)))
            continue;
        if (n >= maxnames)
            break;
        for (i = 0; i < n; i++)
            if (strcmp(names[i], buf) == 0 && wilds[i] == wild)
                break;
        if (i == n) {
            l = strlen(buf) + 1;
            if (l > sizeof(names[n]))
                l = sizeof(names[n]);
            memcpy(names[n], buf, l);
            names[n][sizeof(names[n]) - 1] = '\0';
            wilds[n] = wild;
            n++;
        }
    }
    fclose(fp);
    return n;
}

static vn_dom *dom_find(vpn_never *v, const char *name)
{
    int i;
    for (i = 0; i < v->nd; i++)
        if (strcmp(v->dom[i].name, name) == 0)
            return &v->dom[i];
    return NULL;
}

static void doms_reconcile(vpn_never *v, char names[][256], int wilds[], int n)
{
    vn_dom keep[VN_MAXDOM];
    int nk = 0, i;
    for (i = 0; i < n && i < VN_MAXDOM; i++) {
        vn_dom *old = dom_find(v, names[i]);
        vn_dom *d = &keep[nk++];
        if (old && old->wild == wilds[i])
            *d = *old;
        else {
            size_t l;
            memset(d, 0, sizeof(*d));
            l = strlen(names[i]) + 1;
            if (l > sizeof(d->name))
                l = sizeof(d->name);
            memcpy(d->name, names[i], l);
            d->name[sizeof(d->name) - 1] = '\0';
            d->is_net = strchr(d->name, '/') ? 1 : 0;
            d->wild = wilds[i];
            d->next_try = 0;
        }
    }
    memcpy(v->dom, keep, (size_t)nk * sizeof(vn_dom));
    v->nd = nk;
}

static int tracked_has(const vpn_never *v, const char *val)
{
    int i;
    for (i = 0; i < v->ntrack; i++)
        if (strcmp(v->track[i], val) == 0)
            return 1;
    return 0;
}

static void tracked_add(vpn_never *v, const char *val)
{
    size_t l;
    if (v->ntrack >= VN_TRACK)
        return;
    l = strlen(val) + 1;
    if (l > sizeof(v->track[0]))
        l = sizeof(v->track[0]);
    memcpy(v->track[v->ntrack], val, l);
    v->track[v->ntrack][sizeof(v->track[0]) - 1] = '\0';
    v->tmiss[v->ntrack] = 0;
    v->ntrack++;
}

static void tracked_remove(vpn_never *v, const char *val)
{
    int i, w = 0;
    for (i = 0; i < v->ntrack; i++) {
        if (strcmp(v->track[i], val) == 0)
            continue;
        if (w != i) {
            memcpy(v->track[w], v->track[i], 64);
            v->tmiss[w] = v->tmiss[i];
        }
        w++;
    }
    v->ntrack = w;
}

static long long now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

int vn_changed(vpn_never *v, const susanin_config *cfg)
{
    struct stat st;
    int had;
    if (!v || !cfg->vpn_never_file[0])
        return 0;
    had = v->seen_exists;
    if (stat(cfg->vpn_never_file, &st) != 0)
        return had ? 1 : 0;
    if (!had)
        return 1;
    return st.st_mtime != v->seen_mtime || st.st_size != v->seen_size;
}

static int resolve_dom(const char *server, vn_dom *d, int timeout)
{
    int n, total = 0, k;
    char tmp[VN_MAXIP][16];
    char probe[300];
    if (!d->wild)
        return va_dns_query(server, d->name, d->ips, VN_MAXIP, timeout);
    /* *.domain: apex + a couple of random subdomain probes (wildcard DNS) */
    n = va_dns_query(server, d->name, d->ips, VN_MAXIP, timeout);
    if (n > 0)
        total = n;
    for (k = 0; k < 2 && total < VN_MAXIP; k++) {
        int m, j, x, dup;
        snprintf(probe, sizeof(probe), "susanin-%08lx%d.%s",
                 (unsigned long)(time(NULL) ^ (getpid() << 8)), k, d->name);
        m = va_dns_query(server, probe, tmp, VN_MAXIP, timeout);
        for (j = 0; j < m && total < VN_MAXIP; j++) {
            dup = 0;
            for (x = 0; x < total; x++)
                if (strcmp(d->ips[x], tmp[j]) == 0) {
                    dup = 1;
                    break;
                }
            if (!dup) {
                size_t l = strlen(tmp[j]);
                if (l > 15)
                    l = 15;
                memcpy(d->ips[total], tmp[j], l);
                d->ips[total][l] = '\0';
                total++;
            }
        }
    }
    return total;
}

static void lower_copy(char *d, const char *s, size_t n)
{
    size_t i = 0;
    for (; s[i] && i + 1 < n; i++)
        d[i] = (char)tolower((unsigned char)s[i]);
    d[i] = '\0';
}

static int is_sub_of(const char *sub, const char *zone)
{
    size_t ls = strlen(sub), lz = strlen(zone);
    if (ls <= lz || sub[ls - lz - 1] != '.')
        return 0;
    return strcmp(sub + (ls - lz), zone) == 0;
}

/* Returns 1 if the same/overlapping zone is listed in vpn_always. */
static int zone_in_always(const susanin_config *cfg, const char *name, int wild)
{
    FILE *fp;
    char line[320];
    int found = 0;
    if (!cfg->vpn_always_file[0])
        return 0;
    fp = fopen(cfg->vpn_always_file, "r");
    if (!fp)
        return 0;
    while (fgets(line, sizeof(line), fp)) {
        char buf[256], low[256];
        const char *p = line;
        int w = 0;
        trim_line(line);
        if (line[0] == '\0' || line[0] == '#')
            continue;
        if (line[0] == '*' && line[1] == '.') {
            w = 1;
            p = line + 2;
        }
        lower_copy(low, p, sizeof(low));
        if (!valid_name(low, buf, sizeof(buf)))
            continue;
        if (strcmp(buf, name) == 0 ||
            (w == 1 && is_sub_of(name, buf)) ||
            (wild == 1 && is_sub_of(buf, name))) {
            found = 1;
            break;
        }
    }
    fclose(fp);
    return found;
}

int vn_refresh(vpn_never *v, const susanin_config *cfg)
{
    char names[VN_MAXDOM][256];
    int wilds[VN_MAXDOM] = { 0 };
    char server[64];
    char desired[VN_TRACK][64];
    char (*des)[64] = desired;
    struct stat st;
    time_t now = time(NULL);
    int i, nd = 0, added = 0, removed = 0, pending = 0, new_epoch = 0;
    int nfail = 0, nex = 0;
    char exa[3][64] = { "", "", "" };
    long long t0 = now_ms();
    int interval = cfg->vpn_never_interval > 0 ? cfg->vpn_never_interval : 300;

    if (!v || !cfg->vpn_never_file[0])
        return 0;

    /* Hysteresis считает интервалы, а не вызовы (см. vpn_always.c). */
    {
        long long epoch = (long long)now / interval;
        new_epoch = (epoch != v->epoch);
        if (new_epoch)
            v->epoch = epoch;
    }

    if (stat(cfg->vpn_never_file, &st) != 0) {
        if (v->seen_exists) {
            v->seen_exists = 0;
            v->nd = 0;
            while (v->ntrack > 0) {
                backend_set_del(cfg, VN_SET, v->track[0]);
                slogf(SL_INFO, "vpn_never: unpin %s (file removed)",
                      v->track[0]);
                recent_note(v->track[0], time(NULL));
                tracked_remove(v, v->track[0]);
            }
        } else if (!v->warned) {
            v->warned = 1;
            slogf(SL_INFO, "vpn_never: %s absent (disabled)",
                  cfg->vpn_never_file);
        }
        return 0;
    }
    v->seen_exists = 1;
    v->warned = 0;
    if (!v->seen_mtime || v->seen_mtime != st.st_mtime ||
        v->seen_size != st.st_size) {
        int nf = file_load(cfg->vpn_never_file, names, wilds, VN_MAXDOM);
        if (nf >= 0) {
            doms_reconcile(v, names, wilds, nf);
            for (i = 0; i < v->nd; i++)
                if (zone_in_always(cfg, v->dom[i].name, v->dom[i].wild))
                    slogf(SL_WARN,
                          "vpn_never: %s%s conflicts with vpn_always; direct wins",
                          v->dom[i].wild ? "*." : "", v->dom[i].name);
        }
        v->seen_mtime = st.st_mtime;
        v->seen_size = st.st_size;
    }

    va_pick_resolver(cfg, server, sizeof(server));

    for (i = 0; i < v->nd; i++) {
        vn_dom *d = &v->dom[i];
        long long spent;
        int n, k;
        if (d->next_try > now)
            continue;
        if (d->is_net) {
            d->next_try = now + interval;
            continue;
        }
        {
            struct in_addr lit;
            if (inet_pton(AF_INET, d->name, &lit) == 1) {
                snprintf(d->ips[0], 16, "%.15s", d->name);
                d->nips = 1;
                d->next_try = now + interval;
                continue;
            }
        }
        spent = now_ms() - t0;
        if (spent >= VN_BUDGET_MS) {
            pending = 1;
            continue;
        }
        {
            long left = VN_BUDGET_MS - (long)spent;
            int to = VN_QUERY_MAX_MS;
            if (left < 300)
                left = 300;
            if (to > left)
                to = (int)left;
            n = resolve_dom(server, d, to);
        }
        if (n > 0) {
            for (k = 0; k < n; k++)
                slogf(SL_DEBUG, "vpn_never: %s -> %s", d->name, d->ips[k]);
            d->nips = n;
            d->fails = 0;
            d->next_try = now + interval;
        } else {
            if (d->nips == 0) {
                d->fails++;
                nfail++;
                if (nex < 3)
                    snprintf(exa[nex++], sizeof(exa[0]), "%s", d->name);
            } else {
                slogf(SL_DEBUG, "vpn_never: %s resolve failed (keep %d old ip)",
                      d->name, d->nips);
            }
            d->next_try = now + (d->fails >= 3 ? VN_FAIL_BACKOFF_S
                                               : VN_BACKOFF_S);
        }
    }

    if (nfail > 0) {
        /* Одна сводка вместо строки на каждый домен; не чаще раза в час,
         * если число не меняется (как в vpn_always). */
        static int last_nfail = -1;
        static time_t last_tsum = 0;
        if (nfail != last_nfail || now - last_tsum >= 3600) {
            char ex[200];
            int k;
            ex[0] = '\0';
            for (k = 0; k < nex; k++) {
                size_t l = strlen(ex);
                snprintf(ex + l, sizeof(ex) - l, "%s%s", l ? ", " : "", exa[k]);
            }
            slogf(SL_WARN, "vpn_never: %d домен(ов) без A-записей — НЕ защищены, "
                  "могут уйти в VPN (напр.: %s)", nfail, ex);
            last_nfail = nfail;
            last_tsum = now;
        }
    }

    for (i = 0; i < v->nd; i++) {
        int k;
        if (v->dom[i].is_net) {
            int j;
            for (j = 0; j < nd; j++)
                if (strcmp(des[j], v->dom[i].name) == 0)
                    break;
            if (j == nd && nd < VN_TRACK)
                snprintf(des[nd++], 64, "%s", v->dom[i].name);
            continue;
        }
        for (k = 0; k < v->dom[i].nips && nd < VN_TRACK; k++) {
            int j;
            for (j = 0; j < nd; j++)
                if (strcmp(des[j], v->dom[i].ips[k]) == 0)
                    break;
            if (j == nd)
                snprintf(des[nd++], 64, "%s", v->dom[i].ips[k]);
        }
    }

    for (i = 0; i < v->ntrack; ) {
        int j;
        for (j = 0; j < nd; j++)
            if (strcmp(v->track[i], des[j]) == 0)
                break;
        if (j == nd) {
            if (!v->dirty) {
                if (new_epoch)
                    v->tmiss[i]++;
                if (v->tmiss[i] < 2) {
                    i++;
                    continue;
                }
            }
            backend_set_del(cfg, VN_SET, v->track[i]);
            slogf(SL_DEBUG, "vpn_never: allow-direct off %s", v->track[i]);
            recent_note(v->track[i], now);
            removed++;
            tracked_remove(v, v->track[i]);
        } else {
            v->tmiss[i] = 0;
            i++;
        }
    }
    for (i = 0; i < nd; i++) {
        if (tracked_has(v, des[i]) && !v->dirty)
            continue;
        backend_set_add(cfg, VN_SET, des[i], 0);
        if (!strchr(des[i], '/')) {
            /* Сбросить старые VPN-потоки и убрать из ok/test-кэша: адрес должен
             * идти напрямую (важно при FASTNAT и при позднем добавлении домена). */
            backend_ct_flush_ip(des[i]);
            backend_ipset_del(cfg, 0, 1, des[i]);
            backend_ipset_del(cfg, 0, 0, des[i]);
            backend_ipset_del(cfg, 1, 1, des[i]);
            backend_ipset_del(cfg, 1, 0, des[i]);
        }
        if (!tracked_has(v, des[i]))
            tracked_add(v, des[i]);
        added++;
    }
    v->dirty = 0;

    if (added || removed)
        slogf(SL_INFO, "vpn_never: +%d/-%d entries, %d domain(s), now %d direct",
              added, removed, v->nd, v->ntrack);

    return pending;
}
