#define _GNU_SOURCE
#include "classifier.h"
#include "backend.h"
#include "vpn_never.h"
#include "log.h"

#include <arpa/inet.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int is_udp(const ct_flow *f) { return f->l4proto == 17; }

/* Порты из learn_exclude_ports не участвуют в автообучении (типовой скан-шум
 * на 22/23/445/554 и т.п.): такие потоки пропускаем целиком. */
static int port_excluded(const susanin_config *cfg, unsigned dport)
{
    char buf[256], *save = NULL, *tok;
    if (!cfg->learn_exclude_ports[0])
        return 0;
    snprintf(buf, sizeof(buf), "%s", cfg->learn_exclude_ports);
    for (tok = strtok_r(buf, ",", &save); tok; tok = strtok_r(NULL, ",", &save)) {
        while (*tok == ' ' || *tok == '\t') tok++;
        if ((unsigned)strtoul(tok, NULL, 10) == dport)
            return 1;
    }
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

static int is_private_dst(const char *dst, int *is_private)
{
    static const char *priv[] = {
        "0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
        "169.254.0.0/16", "172.16.0.0/12", "192.168.0.0/16",
        "224.0.0.0/4", "240.0.0.0/4", NULL
    };
    int i;
    (void)is_private;
    for (i = 0; priv[i]; i++)
        if (ip_in_cidr(dst, priv[i]))
            return 1;
    return 0;
}

static int ours(const ct_flow *f, const susanin_config *cfg)
{
    return f->ctmark && ((f->ctmark & cfg->mark_mask) == cfg->mark_test ||
                         (f->ctmark & cfg->mark_mask) == cfg->mark_ok);
}

/* rate cache (two-sample deltas) */
#define RCAP 2048
typedef struct { char key[192]; unsigned long op, rp; time_t t; int seen; } rslot;
static rslot rc[RCAP];
static int rc_n = 0;

static void rc_begin(void)
{
    int i;
    for (i = 0; i < rc_n; i++) rc[i].seen = 0;
}

static void rc_end(void)
{
    int i, w = 0;
    for (i = 0; i < rc_n; i++)
        if (rc[i].seen) rc[w++] = rc[i];
    rc_n = w;
}

static rslot *rc_find(const char *key)
{
    int i;
    for (i = 0; i < rc_n; i++)
        if (strcmp(rc[i].key, key) == 0)
            return &rc[i];
    return NULL;
}

static rslot *rc_store(const char *key, unsigned long op, unsigned long rp, time_t now)
{
    rslot *s;
    if (rc_n >= RCAP) rc_n = 0;
    s = &rc[rc_n++];
    snprintf(s->key, sizeof(s->key), "%s", key);
    s->op = op; s->rp = rp; s->t = now; s->seen = 1;
    return s;
}

static void flow_key(const ct_flow *f, char *buf, size_t n)
{
    snprintf(buf, n, "%s|%s|%u|%s|%u", f->proto, f->src, f->sport, f->dst, f->dport);
}

/* returns 1 if caller should consume origActive/replSilent (has previous sample) */
static int rate_delta(const ct_flow *f, time_t now, int *orig_active, int *repl_silent)
{
    char key[192];
    rslot *s;
    flow_key(f, key, sizeof(key));
    s = rc_find(key);
    if (s) {
        *orig_active = (f->op > s->op);
        *repl_silent = (f->rp == s->rp);
        s->op = f->op; s->rp = f->rp; s->seen = 1; s->t = now;
        return 1;
    }
    rc_store(key, f->op, f->rp, now);
    return 0;
}

/* Гистерезис снятия из ok: считаем подряд идущие «сбои» по адресу, чтобы не
 * дёргать один и тот же адрес (OK-CHURN) из-за одного наблюдения. */
#define OFCAP 256
struct ofslot { char ip[64]; int udp; int miss; int seen; };
static struct ofslot of[OFCAP];
static int of_n;

/* Сводка OK-CHURN: пишем в лог не каждое событие, а раз в ~5 минут. */
static time_t oc_last = 0;
static int oc_cnt = 0;
static char oc_name[80];

static struct ofslot *of_find(const char *ip, int udp)
{
    int i;
    for (i = 0; i < of_n; i++)
        if (of[i].seen && of[i].udp == udp && strcmp(of[i].ip, ip) == 0)
            return &of[i];
    return NULL;
}

static struct ofslot *of_touch(const char *ip, int udp)
{
    struct ofslot *s = of_find(ip, udp);
    if (s) {
        s->miss++;
        return s;
    }
    if (of_n >= OFCAP)
        of_n = 0;
    s = &of[of_n++];
    snprintf(s->ip, sizeof(s->ip), "%s", ip);
    s->udp = udp;
    s->miss = 1;
    s->seen = 1;
    return s;
}

static void of_forget(const char *ip, int udp)
{
    struct ofslot *s = of_find(ip, udp);
    if (s)
        s->seen = 0;
}

/* Ограничитель «проб»: не более promo_per_min новых переводов в VPN в минуту
 * (0 = без лимита). Защита от лавины ложных заворотов при агрессивном детекте. */
static int promo_ok(const susanin_config *cfg, time_t now)
{
    static time_t win = 0;
    static int cnt = 0;
    if (cfg->promo_per_min <= 0)
        return 1;
    if (!win || now - win >= 60) {
        win = now;
        cnt = 0;
    }
    if (cnt >= cfg->promo_per_min)
        return 0;
    cnt++;
    return 1;
}

static void promote_test(classifier_ctx *ctx, const ct_flow *f, time_t now,
                         const char *stage, const char *reason)
{
    int udp = is_udp(f);
    const susanin_config *cfg = ctx->cfg;
    /* Не учим в VPN адрес, недавно бывший в vpn_never: он должен идти напрямую,
     * иначе возможен «прыжок» direct <-> VPN (план п.8.3). */
    if (vn_is_recently_never(f->dst, now)) {
        slogf(SL_DEBUG, "AUTO-SUSANIN: skip %s:%u (recently vpn_never)",
              f->dst, f->dport);
        return;
    }
    if (!promo_ok(cfg, now))
        return;
    state_add(st_test(ctx->st, udp), f->dst, now, cfg->test_ttl, 0);
    state_remove(st_watch(ctx->st, udp), f->dst);
    backend_ipset_add(cfg, udp, 0, f->dst, cfg->test_ttl);
    slogf(SL_INFO, "AUTO-SUSANIN: %s %s %s:%u", stage, reason, f->dst, f->dport);
    backend_ct_delete(f);
    backend_ct_flush_ip(f->dst);
}

static int candidate_ok(const classifier_ctx *ctx, const ct_flow *f, time_t now)
{
    int udp = is_udp(f);
    return !state_has(st_ok(ctx->st, udp), f->dst, now) &&
           !state_has(st_test(ctx->st, udp), f->dst, now) &&
           !state_has(st_cool(ctx->st, udp), f->dst, now);
}

void clr_fast(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now)
{
    const susanin_config *cfg = ctx->cfg;
    int i;
    for (i = 0; i < n; i++) {
        const ct_flow *f = &flows[i];
        if (f->l4proto != 6 && f->l4proto != 17) continue;
        if (ours(f, cfg) || (f->ctmark & cfg->mark_mask) != 0) continue;
        if (cfg->policy_mark != 0 && (f->ctmark & 0x0fffffff) != cfg->policy_mark) continue;
        if (!from_lan(cfg, f->src)) continue;
        if (is_private_dst(f->dst, NULL)) continue;
        if (port_excluded(cfg, f->dport)) continue;
        if (!candidate_ok(ctx, f, now)) continue;

        if (f->l4proto == 6) {
            if (strcmp(f->tcp_state, "SYN_SENT") == 0 && f->op >= (unsigned long)cfg->fast_syn_min_op && f->rp == 0)
                promote_test(ctx, f, now, "FAST", "TCP-SYN");
            else if (strcmp(f->tcp_state, "CLOSE") == 0 && f->op >= 1 &&
                     f->rp <= 2 && f->rb < 256)
                promote_test(ctx, f, now, "FAST", "TCP-CLOSE");
        } else if (f->l4proto == 17) {
            if (f->dport == 443 && f->op >= 2 && f->rp == 0)
                promote_test(ctx, f, now, "FAST", "QUIC");
        }
    }
}

void clr_soft(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now)
{
    const susanin_config *cfg = ctx->cfg;
    int i;
    unsigned long late_bytes = (cfg->late_stall_bytes > 0) ? (unsigned long)cfg->late_stall_bytes : 65536UL;
    rc_begin();
    for (i = 0; i < n; i++) {
        const ct_flow *f = &flows[i];
        if (f->l4proto != 6 && f->l4proto != 17) continue;
        if (ours(f, cfg) || (f->ctmark & cfg->mark_mask) != 0) continue;
        if (cfg->policy_mark != 0 && (f->ctmark & 0x0fffffff) != cfg->policy_mark) continue;
        if (!from_lan(cfg, f->src)) continue;
        if (is_private_dst(f->dst, NULL)) continue;

        if (f->l4proto == 6 && strcmp(f->tcp_state, "ESTABLISHED") == 0) {
            /* Early TLS ClientHello / HTTP stall: client has retransmitted (op>=4, ob>=400)
             * but server never returned data beyond initial ACK (rp<=2, rb<256).
             * Triggers in ~1s on 1st retransmission instead of waiting 10-15s. */
            if (f->op >= 4 && f->ob >= 400 && f->rp <= 2 && f->rb < 256) {
                if (candidate_ok(ctx, f, now))
                    promote_test(ctx, f, now, "SOFT", "TCP-STALL");
                continue;
            }
            /* HTTPS/HTTP stall where server replied with initial TLS packets but stalled before late_bytes */
            if ((f->dport == 443 || f->dport == 80) && f->op >= 5 && f->rp > 0 && f->rb < late_bytes) {
                if (candidate_ok(ctx, f, now))
                    promote_test(ctx, f, now, "SOFT", "TCP-STALL-443");
                continue;
            }
            if (f->op >= 5 && f->rp > 0) {
                int oa, rs;
                if (rate_delta(f, now, &oa, &rs) && oa && rs) {
                    /* suspicious: watch -> maybe late-stall */
                    if (!state_has(st_watch(ctx->st, 0), f->dst, now)) {
                        if (candidate_ok(ctx, f, now))
                            state_add(st_watch(ctx->st, 0), f->dst, now, cfg->watch_ttl, 0);
                    } else {
                        time_t at = state_at(st_watch(ctx->st, 0), f->dst, now);
                        if (at && (int)(at - now) <= cfg->watch_retry_below) {
                            if (candidate_ok(ctx, f, now)) {
                                state_remove(st_watch(ctx->st, 0), f->dst);
                                promote_test(ctx, f, now, "SOFT", "TCP-LATE-STALL");
                            }
                        }
                    }
                }
            }
        } else if (f->l4proto == 17) {
            if (!f->has_reply) {
                if (f->dport != 443 && f->dport != 53 && f->dport != 67 &&
                    f->dport != 68 && f->dport != 123 && f->op >= 5 && f->rp == 0) {
                    if (candidate_ok(ctx, f, now))
                        promote_test(ctx, f, now, "SOFT", "UDP");
                }
            } else if (f->dport == 443 && f->op >= 4) {
                int oa, rs;
                if (rate_delta(f, now, &oa, &rs) && oa && rs) {
                    if (!state_has(st_watch(ctx->st, 1), f->dst, now)) {
                        if (candidate_ok(ctx, f, now))
                            state_add(st_watch(ctx->st, 1), f->dst, now, cfg->watch_ttl, 0);
                    } else {
                        time_t at = state_at(st_watch(ctx->st, 1), f->dst, now);
                        if (at && (int)(at - now) <= cfg->watch_retry_below) {
                            if (candidate_ok(ctx, f, now)) {
                                state_remove(st_watch(ctx->st, 1), f->dst);
                                promote_test(ctx, f, now, "SOFT", "QUIC-LATE-STALL");
                            }
                        }
                    }
                }
            }
        }
    }
    rc_end();
}

void clr_judge(classifier_ctx *ctx, const ct_flow *flows, int n, time_t now)
{
    const susanin_config *cfg = ctx->cfg;
    int i;
    for (i = 0; i < n; i++) {
        const ct_flow *f = &flows[i];
        int udp;
        int good, failed, healthy = 0;
        if (f->l4proto != 6 && f->l4proto != 17) continue;
        if (!from_lan(cfg, f->src)) continue;
        udp = is_udp(f);

        if (!ours(f, cfg)) continue;

        if ((f->ctmark & cfg->mark_mask) == cfg->mark_test) {
            if (!state_has(st_test(ctx->st, udp), f->dst, now)) continue;
            good = failed = 0;
            if (f->l4proto == 6) {
                if (f->rp >= 2 || f->rb >= 128) good = 1;
                if ((strcmp(f->tcp_state, "SYN_SENT") == 0 && f->op >= 3 && f->rp == 0) ||
                    (strcmp(f->tcp_state, "ESTABLISHED") == 0 && f->op >= 10 &&
                     f->ob >= 3000 && f->rp <= 1 && f->rb < 128)) failed = 1;
            } else {
                if (f->rp >= 1) good = 1;
                if ((f->dport == 443 && f->op >= 10 && f->rp == 0) ||
                    (f->dport != 443 && f->op >= 20 && f->rp == 0)) failed = 1;
            }
            if (good) {
                state_add(st_ok(ctx->st, udp), f->dst, now, cfg->ok_ttl, 0);
                state_remove(st_test(ctx->st, udp), f->dst);
                state_remove(st_watch(ctx->st, udp), f->dst);
                state_remove(st_cool(ctx->st, udp), f->dst);
                backend_ipset_add(cfg, udp, 1, f->dst, cfg->ok_ttl);
                backend_ipset_del(cfg, udp, 0, f->dst);
                slogf(SL_INFO, "AUTO-SUSANIN: CONFIRMED %s:%u", f->dst, f->dport);
            } else if (failed) {
                state_remove(st_test(ctx->st, udp), f->dst);
                state_remove(st_watch(ctx->st, udp), f->dst);
                state_add(st_cool(ctx->st, udp), f->dst, now, cfg->cooldown_ttl, 0);
                backend_ipset_del(cfg, udp, 0, f->dst);
                slogf(SL_INFO, "AUTO-SUSANIN: COOLDOWN %s:%u", f->dst, f->dport);
                backend_ct_delete(f);
            }
        } else if ((f->ctmark & cfg->mark_mask) == cfg->mark_ok) {
            if (!state_has(st_ok(ctx->st, udp), f->dst, now)) continue;
            healthy = failed = 0;
            if (f->l4proto == 6) {
                if (f->rp >= 2 || f->rb >= 128) healthy = 1;
                /* rp==0 (а не rp<=1): любой ответ означает живой адрес — так
                 * долгоживущие соединения мессенджеров (Telegram/WhatsApp) не
                 * вылетают из ok из-за одного «тихого» среза. */
                if ((strcmp(f->tcp_state, "SYN_SENT") == 0 && f->op >= 4 && f->rp == 0) ||
                    (strcmp(f->tcp_state, "ESTABLISHED") == 0 && f->op >= 15 &&
                     f->ob >= 5000 && f->rp == 0 && f->rb < 128)) failed = 1;
            } else {
                if (f->rp >= 1) healthy = 1;
                if (f->dport == 443 && f->op >= 16 && f->rp == 0) failed = 1;
            }
            if (failed && !healthy) {
                if (cfg->ok_evict_misses > 1) {
                    struct ofslot *os = of_touch(f->dst, udp);
                    if (os->miss < cfg->ok_evict_misses)
                        continue;   /* ещё наблюдаем — из ok не снимаем */
                    os->seen = 0;
                }
                state_remove(st_ok(ctx->st, udp), f->dst);
                state_remove(st_test(ctx->st, udp), f->dst);
                state_remove(st_watch(ctx->st, udp), f->dst);
                state_add(st_cool(ctx->st, udp), f->dst, now, cfg->cooldown_ok_ttl, 0);
                backend_ipset_del(cfg, udp, 1, f->dst);
                slogf(SL_DEBUG, "AUTO-SUSANIN: OK-CHURN %s:%u", f->dst, f->dport);
                oc_cnt++;
                snprintf(oc_name, sizeof(oc_name), "%s:%u", f->dst, f->dport);
                if (!oc_last) {
                    oc_last = now;
                } else if (now - oc_last >= 300) {
                    slogf(SL_INFO, "AUTO-SUSANIN: OK-CHURN: %d за ~5 мин (последний %s)",
                          oc_cnt, oc_name);
                    oc_cnt = 0;
                    oc_last = now;
                }
                backend_ct_delete(f);
            } else if (healthy && !failed) {
                of_forget(f->dst, udp);
                time_t at = state_at(st_ok(ctx->st, udp), f->dst, now);
                if (at && (int)(at - now) <= cfg->ok_refresh_below) {
                    state_add(st_ok(ctx->st, udp), f->dst, now, cfg->ok_ttl, 1);
                    backend_ipset_add(cfg, udp, 1, f->dst, cfg->ok_ttl);
                }
            }
        }
    }
}
