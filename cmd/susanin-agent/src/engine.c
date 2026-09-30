#define _GNU_SOURCE
#include "engine.h"
#include "backend.h"
#include "classifier.h"
#include "conntrack.h"
#include "health.h"
#include "log.h"
#include "state.h"
#include "vpn_always.h"
#include "vpn_never.h"

#include <arpa/inet.h>
#include <fcntl.h>
#include <signal.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>
#include <unistd.h>

static volatile sig_atomic_t g_stop = 0;
static volatile sig_atomic_t g_reload = 0;
static void on_sig(int s) { (void)s; g_stop = 1; }
static void on_hup(int s) { (void)s; g_reload = 1; }

typedef struct { ct_flow *v; int n; int cap; } flowlist;

static int collect(void *ud, const ct_flow *f)
{
    flowlist *L = ud;
    if (L->n >= L->cap) {
        int ncap = L->cap ? L->cap * 2 : 1024;
        ct_flow *nv = realloc(L->v, (size_t)ncap * sizeof(ct_flow));
        if (!nv)
            return 1;
        L->v = nv;
        L->cap = ncap;
    }
    L->v[L->n++] = *f;
    return 0;
}

static int ip_in_cidr(const char *ip, const char *cidr)
{
    char c[64];
    char *slash;
    struct in_addr a, n;
    uint32_t mask, ah, nh;
    int pre;
    snprintf(c, sizeof(c), "%s", cidr);
    slash = strchr(c, '/');
    if (!slash)
        return 0;
    *slash = '\0';
    pre = atoi(slash + 1);
    if (inet_pton(AF_INET, ip, &a) != 1 || inet_pton(AF_INET, c, &n) != 1)
        return 0;
    mask = pre == 0 ? 0 : (0xffffffffu << (32 - pre));
    ah = ntohl(a.s_addr);
    nh = ntohl(n.s_addr);
    return (ah & mask) == (nh & mask);
}

static int from_lan(const susanin_config *cfg, const char *src)
{
    char buf[512], *save = NULL, *tok;
    snprintf(buf, sizeof(buf), "%s", cfg->lan_subnets);
    for (tok = strtok_r(buf, ",", &save); tok; tok = strtok_r(NULL, ",", &save)) {
        while (*tok == ' ') tok++;
        if (ip_in_cidr(src, tok))
            return 1;
    }
    return 0;
}

static void resync_sets(const susanin_config *cfg, susanin_state *st)
{
    int udp, phase;
    for (udp = 0; udp < 2; udp++) {
        for (phase = 0; phase < 2; phase++) {
            const state_set *set = phase ? st_ok(st, udp) : st_test(st, udp);
            int i;
            for (i = 0; i < set->n; i++) {
                int ttl = phase ? cfg->ok_ttl : cfg->test_ttl;
                backend_ipset_add(cfg, udp, phase, set->v[i].addr, ttl);
            }
        }
    }
}

static void sweep_direct(const susanin_config *cfg, susanin_state *st,
                         const ct_flow *flows, int n)
{
    int i;
    time_t now = time(NULL);
    for (i = 0; i < n; i++) {
        const ct_flow *f = &flows[i];
        int udp;
        if ((f->ctmark & cfg->mark_mask) != 0) continue;
        if (cfg->policy_mark != 0 && (f->ctmark & 0x0fffffff) != cfg->policy_mark) continue;
        if (!from_lan(cfg, f->src)) continue;
        if (f->l4proto != 6 && f->l4proto != 17) continue;
        udp = (f->l4proto == 17);
        if (state_has(st_ok(st, udp), f->dst, now))
            backend_ct_delete(f);
    }
}

/* Bounded GC (upstream v0.12 idea): keep per-proto ok-cache within a limit by
 * evicting the oldest entries. Runs periodically; 0 in config disables. */
static void trim_ok(const susanin_config *cfg, susanin_state *st)
{
    int udp;
    if (cfg->ok_max_entries <= 0)
        return;
    for (udp = 0; udp < 2; udp++) {
        state_set *set = st_ok(st, udp);
        while (set->n > cfg->ok_max_entries && set->n > 0) {
            char ip[64];
            snprintf(ip, sizeof(ip), "%s", set->v[0].addr);
            backend_ipset_del(cfg, udp, 1, ip);
            state_remove(set, ip);
            slogf(SL_INFO, "GC: evict ok %s (%s), limit %d", ip,
                  udp ? "udp" : "tcp", cfg->ok_max_entries);
        }
    }
}

int engine_run(susanin_config *cfg, const char *conf_path)
{
    susanin_state st;
    classifier_ctx ctx;
    flowlist L;
    vpn_always *va = NULL;
    vpn_never *nv = NULL;
    int tunnel_up = 1, miss = 0, dp_ok = 0, ei = 0, efails = 0;
    time_t last[4] = { 0, 0, 0, 0 };
    time_t last_save = 0;
    time_t last_recon = 0;
    time_t next_dp_try = 0;
    time_t last_force = 0;
    time_t last_never = 0;
    int never_pending = 0;
    time_t last_trim = 0;
    int force_pending = 0;
    const char *state_path = "/opt/susanin/var/susanin.state";

    signal(SIGINT, on_sig);
    signal(SIGTERM, on_sig);
    signal(SIGHUP, on_hup);     /* reload конфига */
    {
        const char *lf = getenv("SUSANIN_LOG");
        if (lf && *lf) {
            int fd = open(lf, O_WRONLY | O_CREAT | O_APPEND, 0644);
            if (fd >= 0) {
                dup2(fd, 1);
                dup2(fd, 2);
                if (fd > 2)
                    close(fd);
            }
        }
    }
    slog_init(cfg->log_level);
    if (strcmp(cfg->disk_mode, "soft") == 0)
        slogf(SL_INFO, "disk_mode=soft: log file off, state not saved, no backups");

    state_init(&st);
    ctx.cfg = cfg;
    ctx.st = &st;
    memset(&L, 0, sizeof(L));

    {
        char perr[256];
        if (backend_preflight(cfg, perr, sizeof(perr)) != 0)
            slogf(SL_ERROR, "preflight: %s", perr);
    }
    /* Стартуем с первого egress из списка (фейловер переключит при падении). */
    if (cfg->n_egress > 0)
        backend_set_egress(cfg, cfg->egress_list[0]);
    if (backend_provision(cfg) == 0) {
        dp_ok = 1;
    } else {
        slogf(SL_ERROR, "data plane is NOT active; traffic stays DIRECT until set-up succeeds");
        next_dp_try = time(NULL) + 60;
    }
    if (state_load(state_path, &st) == 0)
        slogf(SL_INFO, "restored cache from %s", state_path);
    if (tunnel_up)
        resync_sets(cfg, &st);
    if (cfg->vpn_always_file[0])
        va = va_new();
    if (cfg->vpn_never_file[0])
        nv = vn_new();
    slogf(SL_INFO, "engine started (egress=%s table=%d)", cfg->egress_interface, cfg->routing_table);
    if (va && tunnel_up) {
        last_force = time(NULL);
        force_pending = va_refresh(va, cfg);
    }
    if (nv) {
        last_never = time(NULL);
        never_pending = vn_refresh(nv, cfg);
    }

    while (!g_stop) {
        time_t now = time(NULL);

        if (g_reload) {
            susanin_config nc;
            g_reload = 0;
            if (conf_path && config_load(conf_path, &nc) == 0) {
                *cfg = nc;
                slogf(SL_INFO, "config reloaded: %s", conf_path);
                {
                    char perr[256];
                    if (backend_preflight(cfg, perr, sizeof(perr)) != 0)
                        slogf(SL_ERROR, "preflight: %s", perr);
                }
                ei = 0;
                efails = 0;
                miss = 0;
                if (cfg->n_egress > 0)
                    backend_set_egress(cfg, cfg->egress_list[0]);
                if (backend_provision(cfg) == 0)
                    dp_ok = 1;
                else
                    dp_ok = 0;
                last_force = 0;
                last_never = 0;
            } else {
                slogf(SL_ERROR, "config reload failed: %s",
                      conf_path ? conf_path : "?");
            }
        }

        L.n = 0;
        if (conntrack_scan("/proc/net/nf_conntrack", collect, &L) < 0)
            slogf(SL_DEBUG, "conntrack scan failed");

        if (tunnel_up) {
            if (now - last[0] >= cfg->fast_interval) { last[0] = now; clr_fast(&ctx, L.v, L.n, now); }
            if (now - last[1] >= cfg->soft_interval) { last[1] = now; clr_soft(&ctx, L.v, L.n, now); }
            if (now - last[2] >= cfg->judge_interval) { last[2] = now; clr_judge(&ctx, L.v, L.n, now); }
        }

        if (now - last[3] >= cfg->health_interval) {
            int ok = 0, total = 0;
            const char *psrc = cfg->egress_addr[ei][0] ? cfg->egress_addr[ei]
                                                       : cfg->egress_address;
            last[3] = now;
            /* Команда re-scan egress: интерфейс мог исчезнуть (VPN удалили). */
            if (cfg->n_egress > 0) {
                char np[256];
                snprintf(np, sizeof(np), "/sys/class/net/%s",
                         cfg->egress_list[ei]);
                if (access(np, F_OK) != 0) {
                    int k, ni = -1;
                    for (k = 1; k <= cfg->n_egress; k++) {
                        int idx = (ei + k) % cfg->n_egress;
                        snprintf(np, sizeof(np), "/sys/class/net/%s",
                                 cfg->egress_list[idx]);
                        if (access(np, F_OK) == 0) {
                            ni = idx;
                            break;
                        }
                    }
                    if (ni >= 0) {
                        slogf(SL_WARN, "egress %s отсутствует — переключаюсь на %s",
                              cfg->egress_list[ei], cfg->egress_list[ni]);
                        ei = ni;
                        backend_set_egress(cfg, cfg->egress_list[ei]);
                        backend_ct_flush_vpn(cfg);
                    } else if (tunnel_up) {
                        tunnel_up = 0;
                        efails = 0;
                        slogf(SL_ERROR, "нет живых egress — fail-open DIRECT");
                        backend_ipset_flush(cfg);
                        va_mark_dirty(va);
                        vn_mark_dirty(nv);
                    }
                    continue;     /* в этом тике пробу не делаем */
                }
            }
            health_probe(cfg, psrc, &ok, &total);
            if (ok > 0) {
                miss = 0;
                efails = 0;
                if (!tunnel_up) {
                    tunnel_up = 1;
                    slogf(SL_INFO, "tunnel UP via %s, recovery", cfg->egress_list[ei]);
                    resync_sets(cfg, &st);
                    sweep_direct(cfg, &st, L.v, L.n);
                    last_force = 0;
                }
            } else {
                miss++;
                if (miss >= cfg->health_miss_debounce) {
                    miss = 0;
                    if (cfg->n_egress > 1 && efails + 1 < cfg->n_egress) {
                        /* Фейловер: переключаем default в таблице на следующий egress. */
                        efails++;
                        ei = (ei + 1) % cfg->n_egress;
                        slogf(SL_WARN, "egress %s DOWN, failover -> %s",
                              cfg->egress_list[(ei + cfg->n_egress - 1) % cfg->n_egress],
                              cfg->egress_list[ei]);
                        backend_set_egress(cfg, cfg->egress_list[ei]);
                        backend_ct_flush_vpn(cfg);
                    } else if (tunnel_up) {
                        tunnel_up = 0;
                        efails = 0;
                        slogf(SL_ERROR, "all egress DOWN, fail-open DIRECT");
                        backend_ipset_flush(cfg);
                        va_mark_dirty(va);
                        vn_mark_dirty(nv);
                    } else if (cfg->n_egress > 1) {
                        /* Уже fail-open: по кругу пробуем следующий кандидат. */
                        ei = (ei + 1) % cfg->n_egress;
                        backend_set_egress(cfg, cfg->egress_list[ei]);
                        backend_ct_flush_vpn(cfg);
                    }
                }
            }
        }

        {
            /* Обычный режим — раз в 5 минут. Soft — редко (по умолчанию раз в
             * 12 часов), чтобы почти не писать на носитель. 0 = не сохранять. */
            int every = (strcmp(cfg->disk_mode, "soft") == 0)
                            ? cfg->soft_state_interval
                            : 300;
            if (every > 0 && now - last_save >= every) {
                last_save = now;
                state_save(state_path, &st);
            }
        }

        if (now - last_trim >= 30) {
            last_trim = now;
            trim_ok(cfg, &st);
        }

        if (now - last_recon >= 15) {
            last_recon = now;
            if (dp_ok && !backend_ready(cfg)) {
                /* Was active and disappeared (e.g. NDM/firewall rebuild). */
                slogf(SL_WARN, "data plane missing (NDM rebuild?), re-provisioning");
                if (backend_provision(cfg) == 0) {
                    if (tunnel_up) {
                        resync_sets(cfg, &st);
                        last_force = 0;
                        va_mark_dirty(va);
                        vn_mark_dirty(nv);
                    }
                } else {
                    dp_ok = 0;
                    next_dp_try = now + 60;
                }
            } else if (!dp_ok && now >= next_dp_try) {
                /* Never provisioned (or lost earlier): retry slowly. The exact
                 * reason is logged by backend_provision() itself. */
                next_dp_try = now + 60;
                if (backend_provision(cfg) == 0) {
                    dp_ok = 1;
                    slogf(SL_INFO, "data plane provisioned");
                    if (tunnel_up) {
                        resync_sets(cfg, &st);
                        last_force = 0;
                        va_mark_dirty(va);
                        vn_mark_dirty(nv);
                    }
                }
            }
        }

        if (tunnel_up && va &&
            (force_pending ||
             now - last_force >= (time_t)cfg->vpn_always_interval ||
             va_changed(va, cfg))) {
            time_t prev = last_force;
            last_force = now;
            force_pending = va_refresh(va, cfg);
            if (force_pending)
                last_force = prev;  /* догоняем оставшиеся домены вскоре */
        }

        if (nv &&
            (never_pending ||
             now - last_never >= (time_t)cfg->vpn_never_interval ||
             vn_changed(nv, cfg))) {
            time_t prev = last_never;
            last_never = now;
            never_pending = vn_refresh(nv, cfg);
            if (never_pending)
                last_never = prev;
        }

        usleep(200000);
    }

    state_save(state_path, &st);
    slogf(SL_INFO, "engine stopped");
    va_free(va);
    vn_free(nv);
    state_free(&st);
    free(L.v);
    return 0;
}
