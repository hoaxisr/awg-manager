#define _GNU_SOURCE
#include "vpn_always.h"
#include "backend.h"
#include "log.h"

#include <arpa/inet.h>
#include <ctype.h>
#include <errno.h>
#include <net/if.h>
#include <netinet/in.h>
#include <poll.h>
#include <sys/ioctl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <time.h>
#include <unistd.h>

#define VA_MAXDOM 256
#define VA_MAXIP  32
#define VA_TRACK  4096
#define VA_LINE   320
#define VA_BUDGET_MS 4000
#define VA_QUERY_MAX_MS 2000
#define VA_BACKOFF_S 60

typedef struct {
    char name[256];
    char ips[VA_MAXIP][16]; /* последние успешно отресолвленные A-записи */
    int nips;
    int fail_logged;        /* INFO об ошибке уже печатали (не спамим) */
    int fails;              /* подряд неудачных резолвов (backoff) */
    int is_net;             /* строка файла — CIDR (a.b.c.d/n), не домен */
    int wild;               /* *.domain: домен и все поддомены */
    time_t next_try;        /* когда перепроверять домен */
} va_dom;

struct vpn_always {
    va_dom dom[VA_MAXDOM];
    int nd;
    char track[VA_TRACK][16]; /* IP, добавленные нами в ok-наборы */
    unsigned char tmiss[VA_TRACK]; /* сколько refresh'ей IP не в списке */
    int ntrack;
    char tracknet[VA_TRACK][40]; /* CIDR, добавленные в susanin_ok_net */
    unsigned char tmissnet[VA_TRACK];
    int ntracknet;
    long long seen_mtime;
    long long seen_size;
    int seen_exists;
    int warned;
    int dirty;              /* ipset мог быть очищен — передобавить пины */
    int populated;          /* первый проход уже был (не рвать conntrack на старте) */
    long long epoch;        /* номер интервала (now/interval) для hysteresis */
};

vpn_always *va_new(void)
{
    return calloc(1, sizeof(vpn_always));
}

void va_free(vpn_always *v)
{
    free(v);
}

void va_mark_dirty(vpn_always *v)
{
    if (v)
        v->dirty = 1;
}

/* ---------------- файл списка ---------------- */

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

/* Валидно: IPv4 (пинится как есть), CIDR a.b.c.d/n (в susanin_ok_net) или
 * DNS-имя из [a-z0-9.-]. */
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
        char c = *p;
        if (!((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
              c == '.' || c == '-'))
            return 0;
    }
    snprintf(out, n, "%s", s);
    return 1;
}

/* Читает файл, дедуплицирует имена. -1 если файл недоступен. */
static int file_load(const char *path, char names[][256], int wilds[],
                     int maxnames)
{
    FILE *fp = fopen(path, "r");
    char line[VA_LINE];
    int n = 0;
    if (!fp)
        return -1;
    while (fgets(line, sizeof(line), fp)) {
        char buf[256];
        const char *p = line;
        int i, wild = 0;
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
            size_t l = strlen(buf) + 1;
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

static va_dom *dom_find(vpn_always *v, const char *name)
{
    int i;
    for (i = 0; i < v->nd; i++)
        if (strcmp(v->dom[i].name, name) == 0)
            return &v->dom[i];
    return NULL;
}

/* Согласует список доменов с файлом: новые — next_try=0 (резолвить сразу),
 * удалённые выкидываются вместе с их IP. */
static void doms_reconcile(vpn_always *v, char names[][256], int wilds[], int n)
{
    va_dom keep[VA_MAXDOM];
    int nk = 0, i;
    for (i = 0; i < n && i < VA_MAXDOM; i++) {
        va_dom *old = dom_find(v, names[i]);
        va_dom *d = &keep[nk++];
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
    memcpy(v->dom, keep, (size_t)nk * sizeof(va_dom));
    v->nd = nk;
}

/* ---------------- DNS (A-запрос) ---------------- */

static unsigned short dns_id(void)
{
    static unsigned short c = 0;
    if (c == 0)
        c = (unsigned short)(time(NULL) ^ (getpid() << 8));
    c = (unsigned short)(c + 1);
    return c ? c : 1;
}

/* Кодирует имя в DNS-метки. Возвращает длину или -1. */
static int enc_name(unsigned char *dst, size_t cap, const char *name)
{
    size_t o = 0;
    const char *p = name;
    while (*p) {
        const char *dot = strchr(p, '.');
        size_t l = dot ? (size_t)(dot - p) : strlen(p);
        if (l == 0 || l > 63 || o + l + 2 > cap)
            return -1;
        dst[o++] = (unsigned char)l;
        memcpy(dst + o, p, l);
        o += l;
        if (!dot)
            break;
        p = dot + 1;
    }
    if (o + 1 > cap)
        return -1;
    dst[o++] = 0;
    return (int)o;
}

/* Пропускает (возможно, сжатое) DNS-имя. Возвращает позицию сразу ПОСЛЕ
 * представления имени в пакете: для указателя сжатия это +2 от его начала,
 * для обычных меток — после завершающего нуля. -1 при ошибке. */
static int skip_name(const unsigned char *b, size_t n, size_t off)
{
    while (off < n) {
        unsigned char c = b[off];
        if (c == 0)
            return (int)(off + 1);
        if ((c & 0xc0) == 0xc0) {
            if (off + 1 >= n)
                return -1;
            return (int)(off + 2);
        }
        if ((c & 0xc0) != 0 || off + 1u + c > n)
            return -1;
        off += 1u + c;
    }
    return -1;
}

/*
 * UDP A-запрос к server:53. Возвращает число A-записей (>=0) в ips или -1
 * при ошибке/таймауте/плохом ответе.
 */
static int dns_query_a(const char *server, const char *domain,
                       char ips[][16], int max, int timeout_ms)
{
    unsigned char q[512], r[2048];
    struct sockaddr_in sa;
    struct pollfd pfd;
    unsigned short id;
    int ql, fd = -1, i, ret = -1, rn = 0, matched = 0;
    size_t off;

    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons(53);
    if (inet_pton(AF_INET, server, &sa.sin_addr) != 1)
        return -1;

    id = dns_id();
    q[0] = (unsigned char)(id >> 8);
    q[1] = (unsigned char)(id & 0xff);
    q[2] = 0x01; q[3] = 0x00;          /* RD */
    q[4] = 0; q[5] = 1;                /* QDCOUNT=1 */
    q[6] = 0; q[7] = 0; q[8] = 0; q[9] = 0; q[10] = 0; q[11] = 0;
    ql = 12;
    {
        int l = enc_name(q + ql, sizeof(q) - (size_t)ql, domain);
        if (l < 0)
            return -1;
        ql += l;
    }
    if ((size_t)ql + 4 > sizeof(q))
        return -1;
    q[ql++] = 0; q[ql++] = 1;          /* qtype A */
    q[ql++] = 0; q[ql++] = 1;          /* qclass IN */

    fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (fd < 0)
        return -1;
    if (connect(fd, (struct sockaddr *)&sa, sizeof(sa)) != 0)
        goto out;
    if (send(fd, q, (size_t)ql, 0) != ql)
        goto out;

    pfd.fd = fd;
    pfd.events = POLLIN;
    for (i = 0; i < 2 && !matched; i++) {
        if (poll(&pfd, 1, timeout_ms) <= 0)
            break;
        rn = (int)recv(fd, r, sizeof(r), 0);
        if (rn < 12)
            continue;
        if (r[0] == q[0] && r[1] == q[1] && (r[2] & 0x80))
            matched = 1;
    }
    if (!matched || rn < 12)
        goto out;
    {
        unsigned short flags = (unsigned short)((r[2] << 8) | r[3]);
        unsigned short an = (unsigned short)((r[6] << 8) | r[7]);
        int nans = 0;
        int o;
        if ((flags & 0x000f) != 0 || (flags & 0x0200)) { /* rcode!=0 / TC */
            ret = -1;
            goto out;
        }
        o = skip_name(r, (size_t)rn, 12);
        if (o < 0 || (size_t)o + 4 > (size_t)rn) { ret = -1; goto out; }
        off = (size_t)o + 4;
        while (an-- > 0 && nans < max) {
            unsigned short type, rdlen;
            o = skip_name(r, (size_t)rn, off);
            if (o < 0 || (size_t)o + 10 > (size_t)rn) { ret = -1; goto out; }
            off = (size_t)o;
            type = (unsigned short)((r[off] << 8) | r[off + 1]);
            rdlen = (unsigned short)((r[off + 8] << 8) | r[off + 9]);
            off += 10;
            if (off + rdlen > (size_t)rn) { ret = -1; goto out; }
            if (type == 1 && rdlen == 4) {
                snprintf(ips[nans], 16, "%u.%u.%u.%u", r[off], r[off + 1],
                         r[off + 2], r[off + 3]);
                nans++;
            }
            off += rdlen;
        }
        ret = nans;
    }
out:
    if (fd >= 0)
        close(fd);
    return ret;
}

/* Первый nameserver из /etc/resolv.conf, либо пустая строка. */
static void resolv_server(char *out, size_t n)
{
    FILE *fp = fopen("/etc/resolv.conf", "r");
    char line[300];
    out[0] = '\0';
    if (!fp)
        return;
    while (fgets(line, sizeof(line), fp)) {
        char *p = line, *v;
        struct in_addr a;
        while (*p == ' ' || *p == '\t') p++;
        if (strncmp(p, "nameserver", 10) != 0)
            continue;
        p += 10;
        while (*p == ' ' || *p == '\t') p++;
        v = p;
        while (*v && *v != '\n' && *v != ' ' && *v != '\t' && *v != '#') v++;
        *v = '\0';
        if (inet_pton(AF_INET, p, &a) == 1) {
            snprintf(out, n, "%s", p);
            break;
        }
    }
    fclose(fp);
}

int va_dns_query(const char *server, const char *domain, char ips[][16],
                 int max, int timeout_ms)
{
    return dns_query_a(server, domain, ips, max, timeout_ms);
}

/* IPv4-адрес интерфейса (для резолвера-по-умолчанию). */
static int iface_addr(const char *name, char *out, size_t n)
{
    int fd = socket(AF_INET, SOCK_DGRAM, 0);
    struct ifreq ifr;
    struct sockaddr_in *sin;
    if (fd < 0)
        return -1;
    memset(&ifr, 0, sizeof(ifr));
    snprintf(ifr.ifr_name, sizeof(ifr.ifr_name), "%s", name);
    if (ioctl(fd, SIOCGIFADDR, &ifr) != 0) {
        close(fd);
        return -1;
    }
    sin = (struct sockaddr_in *)&ifr.ifr_addr;
    if (inet_ntop(AF_INET, &sin->sin_addr, out, n) == NULL) {
        close(fd);
        return -1;
    }
    close(fd);
    return 0;
}

void va_pick_resolver(const susanin_config *cfg, char *out, size_t n)
{
    struct in_addr a;
    if (cfg->vpn_always_dns[0] &&
        inet_pton(AF_INET, cfg->vpn_always_dns, &a) == 1) {
        snprintf(out, n, "%.63s", cfg->vpn_always_dns);
        return;
    }
    resolv_server(out, n);
    if (!out[0] || strcmp(out, "127.0.0.1") == 0) {
        /* Локальная петля (NDM) или пусто: пробуем адрес LAN-моста —
         * он отвечает надёжнее в ряде сборок. */
        char ifs[256], lan[64];
        char *save = NULL, *tok;
        snprintf(ifs, sizeof(ifs), "%s", cfg->lan_interfaces);
        for (tok = strtok_r(ifs, ",", &save); tok;
             tok = strtok_r(NULL, ",", &save)) {
            while (*tok == ' ' || *tok == '\t')
                tok++;
            if (*tok && iface_addr(tok, lan, sizeof(lan)) == 0) {
                snprintf(out, n, "%s", lan);
                return;
            }
        }
    }
    if (!out[0])
        snprintf(out, n, "%s", "8.8.8.8");
}

static long long now_ms(void)
{
    struct timespec ts;
    clock_gettime(CLOCK_MONOTONIC, &ts);
    return (long long)ts.tv_sec * 1000 + ts.tv_nsec / 1000000;
}

static int tracked_has(const vpn_always *v, const char *ip)
{
    int i;
    for (i = 0; i < v->ntrack; i++)
        if (strcmp(v->track[i], ip) == 0)
            return 1;
    return 0;
}

static int tracked_add(vpn_always *v, const char *ip)
{
    if (v->ntrack >= VA_TRACK)
        return -1;
    snprintf(v->track[v->ntrack], 16, "%s", ip);
    v->tmiss[v->ntrack] = 0;
    v->ntrack++;
    return 0;
}

static void tracked_remove(vpn_always *v, const char *ip)
{
    int i, w = 0;
    for (i = 0; i < v->ntrack; i++) {
        if (strcmp(v->track[i], ip) == 0)
            continue;
        if (w != i) {
            memcpy(v->track[w], v->track[i], 16);
            v->tmiss[w] = v->tmiss[i];
        }
        w++;
    }
    v->ntrack = w;
}

static int trackednet_has(const vpn_always *v, const char *cidr)
{
    int i;
    for (i = 0; i < v->ntracknet; i++)
        if (strcmp(v->tracknet[i], cidr) == 0)
            return 1;
    return 0;
}

static int trackednet_add(vpn_always *v, const char *cidr)
{
    size_t l;
    if (v->ntracknet >= VA_TRACK)
        return -1;
    l = strlen(cidr) + 1;
    if (l > sizeof(v->tracknet[0]))
        l = sizeof(v->tracknet[0]);
    memcpy(v->tracknet[v->ntracknet], cidr, l);
    v->tracknet[v->ntracknet][sizeof(v->tracknet[0]) - 1] = '\0';
    v->tmissnet[v->ntracknet] = 0;
    v->ntracknet++;
    return 0;
}

static void trackednet_remove(vpn_always *v, const char *cidr)
{
    int i, w = 0;
    for (i = 0; i < v->ntracknet; i++) {
        if (strcmp(v->tracknet[i], cidr) == 0)
            continue;
        if (w != i) {
            memcpy(v->tracknet[w], v->tracknet[i], 40);
            v->tmissnet[w] = v->tmissnet[i];
        }
        w++;
    }
    v->ntracknet = w;
}

int va_changed(vpn_always *v, const susanin_config *cfg)
{
    struct stat st;
    int had;
    if (!v || !cfg->vpn_always_file[0])
        return 0;
    had = v->seen_exists;
    if (stat(cfg->vpn_always_file, &st) != 0)
        return had ? 1 : 0;       /* файл исчез — нужно снять пины */
    if (!had)
        return 1;                 /* файл появился — включить */
    return st.st_mtime != v->seen_mtime || st.st_size != v->seen_size;
}

static int resolve_dom(const char *server, va_dom *d, int timeout)
{
    int n, total = 0, k;
    char tmp[VA_MAXIP][16];
    char probe[300];
    n = dns_query_a(server, d->name, d->ips, VA_MAXIP, timeout);
    if (n > 0)
        total = n;
    /* *.domain — всегда зона; обычный домен без A на apex — возможно, это
     * тоже зона/CDN, поэтому пробуем пару случайных поддоменов. */
    if (!d->wild && total > 0)
        return total;
    for (k = 0; k < 2 && total < VA_MAXIP; k++) {
        int m, j, x, dup;
        snprintf(probe, sizeof(probe), "susanin-%08lx%d.%s",
                 (unsigned long)(time(NULL) ^ (getpid() << 8)), k, d->name);
        m = dns_query_a(server, probe, tmp, VA_MAXIP, timeout);
        for (j = 0; j < m && total < VA_MAXIP; j++) {
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

int va_refresh(vpn_always *v, const susanin_config *cfg)
{
    char names[VA_MAXDOM][256];
    int wilds[VA_MAXDOM] = { 0 };
    char server[64];
    struct stat st;
    time_t now = time(NULL);
    int i, ndes = 0, ndesn = 0, added = 0, removed = 0, pending = 0;
    int addn = 0, remn = 0, new_epoch = 0;
    int nfail = 0, nex = 0;
    char exa[3][64] = { "", "", "" };
    long long t0 = now_ms();
    int interval = cfg->vpn_always_interval > 0 ? cfg->vpn_always_interval : 300;
    char (*desired)[16];
    char (*desired_net)[40];

    if (!v || !cfg->vpn_always_file[0])
        return 0;

    /* Hysteresis считает интервалы, а не вызовы: va_refresh зовётся многократно
     * за один интервал (добор доменов по бюджету времени). */
    {
        long long epoch = (long long)now / interval;
        new_epoch = (epoch != v->epoch);
        if (new_epoch)
            v->epoch = epoch;
    }

    if (stat(cfg->vpn_always_file, &st) != 0) {
        /* Файла нет — функция выключена. Если раньше был список — снимаем. */
        if (v->seen_exists) {
            v->seen_exists = 0;
            v->nd = 0;
            while (v->ntrack > 0) {
                backend_ipset_del(cfg, 0, 1, v->track[0]);
                backend_ipset_del(cfg, 1, 1, v->track[0]);
                slogf(SL_INFO, "vpn_always: unpin %s (file removed)",
                      v->track[0]);
                tracked_remove(v, v->track[0]);
            }
            while (v->ntracknet > 0) {
                backend_net_del(cfg, v->tracknet[0]);
                slogf(SL_INFO, "vpn_always: unpin %s (file removed)",
                      v->tracknet[0]);
                trackednet_remove(v, v->tracknet[0]);
            }
        } else if (!v->warned) {
            v->warned = 1;
            slogf(SL_INFO, "vpn_always: %s absent (disabled)",
                  cfg->vpn_always_file);
        }
        return 0;
    }
    v->seen_exists = 1;
    v->warned = 0;
    if (!v->seen_mtime || v->seen_mtime != st.st_mtime ||
        v->seen_size != st.st_size) {
        int nf = file_load(cfg->vpn_always_file, names, wilds, VA_MAXDOM);
        if (nf >= 0)
            doms_reconcile(v, names, wilds, nf);
        v->seen_mtime = st.st_mtime;
        v->seen_size = st.st_size;
    }

    va_pick_resolver(cfg, server, sizeof(server));

    desired = calloc((size_t)VA_TRACK, sizeof(*desired));
    desired_net = calloc((size_t)VA_TRACK, sizeof(*desired_net));
    if (!desired || !desired_net) {
        free(desired);
        free(desired_net);
        return 0;
    }

    /* 1) резолвим домены, чьё время перепроверки наступило (с бюджетом) */
    for (i = 0; i < v->nd; i++) {
        va_dom *d = &v->dom[i];
        int n, k;
        long long spent = now_ms() - t0;
        long left;
        int to;
        if (d->next_try > now)
            continue;
        if (d->is_net) {
            /* CIDR — пинится как есть, без DNS */
            d->next_try = now + interval;
            continue;
        }
        {
            struct in_addr lit;
            if (inet_pton(AF_INET, d->name, &lit) == 1) {
                /* строка файла — уже IP: пин без DNS-запроса */
                if (d->nips == 0)
                    slogf(SL_DEBUG, "vpn_always: literal %s", d->name);
                snprintf(d->ips[0], 16, "%.15s", d->name);
                d->nips = 1;
                d->next_try = now + interval;
                continue;
            }
        }
        if (spent >= VA_BUDGET_MS) {
            pending = 1;
            continue;
        }
        left = VA_BUDGET_MS - spent;
        if (left < 300)
            left = 300;
        to = VA_QUERY_MAX_MS < left ? VA_QUERY_MAX_MS : (int)left;
        n = resolve_dom(server, d, to);
        if (n > 0) {
            if (d->nips == 0) {
                for (k = 0; k < n; k++)
                    slogf(SL_DEBUG, "vpn_always: %s -> %s", d->name,
                          d->ips[k]);
                slogf(SL_DEBUG, "vpn_always: %s resolved to %d ip", d->name,
                      n);
            }
            d->nips = n;
            d->fail_logged = 0;
            d->fails = 0;
            d->next_try = now + interval;
        } else {
            if (d->nips > 0) {
                slogf(SL_DEBUG,
                      "vpn_always: %s resolve failed (keep %d old ip)",
                      d->name, d->nips);
            } else {
                /* нет A-записей: не спамим по каждому домену — одна сводка
                 * в конце прохода; повторяющиеся неудачи реже дёргаем. */
                d->fails++;
                nfail++;
                if (nex < 3)
                    snprintf(exa[nex++], sizeof(exa[0]), "%s", d->name);
            }
            d->next_try = now + (d->fails >= 3 ? 3600 : VA_BACKOFF_S);
        }
    }

    /* 2) желаемый набор = объединение IP доменов + CIDR-строк списка */
    for (i = 0; i < v->nd; i++) {
        int k;
        if (v->dom[i].is_net) {
            int j;
            for (j = 0; j < ndesn; j++)
                if (strcmp(desired_net[j], v->dom[i].name) == 0)
                    break;
            if (j == ndesn && ndesn < VA_TRACK)
                snprintf(desired_net[ndesn++], 40, "%s", v->dom[i].name);
            continue;
        }
        for (k = 0; k < v->dom[i].nips && ndes < VA_TRACK; k++) {
            int j;
            for (j = 0; j < ndes; j++)
                if (strcmp(desired[j], v->dom[i].ips[k]) == 0)
                    break;
            if (j == ndes)
                snprintf(desired[ndes++], 16, "%s", v->dom[i].ips[k]);
        }
    }

    /* 3) снять пины IP, которых больше нет в списке/в A-записях */
    for (i = 0; i < v->ntrack; ) {
        int j;
        for (j = 0; j < ndes; j++)
            if (strcmp(v->track[i], desired[j]) == 0)
                break;
        if (j == ndes) {
            /* hysteresis: unpin only after 2 consecutive интервала отсутствия
             * (new_epoch — первый вызов в новом интервале обновления списка) */
            if (!v->dirty) {
                if (new_epoch)
                    v->tmiss[i]++;
                if (v->tmiss[i] < 2) {
                    i++;
                    continue;
                }
            }
            backend_ipset_del(cfg, 0, 1, v->track[i]);
            backend_ipset_del(cfg, 1, 1, v->track[i]);
            slogf(SL_DEBUG, "vpn_always: unpin %s", v->track[i]);
            removed++;
            tracked_remove(v, v->track[i]);
        } else {
            v->tmiss[i] = 0;
            i++;
        }
    }

    /* 3b) снять CIDR, которых больше нет в списке */
    for (i = 0; i < v->ntracknet; ) {
        int j;
        for (j = 0; j < ndesn; j++)
            if (strcmp(v->tracknet[i], desired_net[j]) == 0)
                break;
        if (j == ndesn) {
            if (!v->dirty) {
                if (new_epoch)
                    v->tmissnet[i]++;
                if (v->tmissnet[i] < 2) {
                    i++;
                    continue;
                }
            }
            backend_net_del(cfg, v->tracknet[i]);
            slogf(SL_DEBUG, "vpn_always: unpin %s", v->tracknet[i]);
            remn++;
            trackednet_remove(v, v->tracknet[i]);
        } else {
            v->tmissnet[i] = 0;
            i++;
        }
    }

    /* 4) добавить недостающие пины (tcp+udp, без истечения); при dirty —
       передобавить все, т.к. наборы могли быть очищены fail-open'ом */
    for (i = 0; i < ndes; i++) {
        int known = tracked_has(v, desired[i]);
        if (known && !v->dirty)
            continue;
        backend_ipset_add(cfg, 0, 1, desired[i], 0);
        backend_ipset_add(cfg, 1, 1, desired[i], 0);
        if (!known)
            tracked_add(v, desired[i]);
        /* Адрес только что переведён в VPN: рвём уже открытые прямые потоки,
         * чтобы клиент переподключился через туннель (не на первом проходе,
         * чтобы не рвать активные соединения при старте демона). */
        if (v->populated)
            backend_ct_flush_ip(desired[i]);
        slogf(SL_DEBUG, "vpn_always: pin %s", desired[i]);
        added++;
    }
    for (i = 0; i < ndesn; i++) {
        int known = trackednet_has(v, desired_net[i]);
        if (known && !v->dirty)
            continue;
        backend_net_add(cfg, desired_net[i], 0);
        if (!known)
            trackednet_add(v, desired_net[i]);
        slogf(SL_DEBUG, "vpn_always: pin net %s", desired_net[i]);
        addn++;
    }
    v->dirty = 0;
    v->populated = 1;

    if (added || removed || addn || remn)
        slogf(SL_INFO, "vpn_always: +%d/-%d ip, +%d/-%d net, %d domain(s), "
              "now %d ip + %d net pinned",
              added, removed, addn, remn, v->nd, v->ntrack, v->ntracknet);
    else if (slog_enabled(SL_DEBUG))
        slogf(SL_DEBUG, "vpn_always: no changes (%d ip + %d net pinned)",
              v->ntrack, v->ntracknet);

    if (nfail > 0) {
        /* одна сводка вместо строки на каждый домен (и не чаще раза в час,
         * если число не меняется) */
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
            slogf(SL_INFO, "vpn_always: %d домен(ов) без A-записей (напр.: %s)",
                  nfail, ex);
            last_nfail = nfail;
            last_tsum = now;
        }
    }

    free(desired);
    free(desired_net);
    return pending;
}
