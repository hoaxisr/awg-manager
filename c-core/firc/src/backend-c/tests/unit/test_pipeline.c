#include "greatest.h"

#include <unistd.h>

#include "firc/log.h"
#include "firc/logrep.h"

#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/dnspipeline.h"
#include "firc/events.h"
#include "firc/fakeip.h"
#include "firc/models.h"
#include "firc/recall.h"
#include "firc/rulesnap.h"
#include "firc/tap.h"

static firc_config_t make_cfg(void) {
    firc_config_t c;
    firc_config_init_defaults(&c);
    return c;
}

static void add_group(firc_config_t *cfg, uint8_t id, const char *name, const char *type,
                      const char *rule) {
    firc_group_t *g = firc_group_new();
    g->id = (firc_id_t){{id, 0, 0, 0}};
    firc_strset(&g->name, name);
    g->enable = true;
    firc_rule_t *r = firc_rule_new();
    r->id = (firc_id_t){{id, 1, 0, 0}};
    firc_strset(&r->type, type);
    firc_strset(&r->rule, rule);
    r->enable = true;
    firc_group_add_rule(g, r);
    firc_config_add_group(cfg, g);
}

static size_t put_name(uint8_t *buf, size_t p, const char *const *labels, size_t n) {
    for (size_t i = 0; i < n; i++) {
        size_t l = strlen(labels[i]);
        buf[p++] = (uint8_t)l;
        memcpy(buf + p, labels[i], l);
        p += l;
    }
    buf[p++] = 0;
    return p;
}

static size_t build_response(uint8_t *buf, const char *const *q, size_t nq,
                             const char *const *target, size_t ntarget) {
    size_t p = 0;
    buf[p++] = 0x12; buf[p++] = 0x34;
    buf[p++] = 0x81; buf[p++] = 0x80;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = (uint8_t)(ntarget ? 2 : 1);
    buf[p++] = 0x00; buf[p++] = 0x00;
    buf[p++] = 0x00; buf[p++] = 0x00;
    p = put_name(buf, p, q, nq);
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_A;
    buf[p++] = 0x00; buf[p++] = 0x01;

    if (ntarget) {
        p = put_name(buf, p, q, nq);
        buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_CNAME;
        buf[p++] = 0x00; buf[p++] = 0x01;
        buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 60;
        size_t at = p;
        buf[p++] = 0; buf[p++] = 0;
        size_t start = p;
        p = put_name(buf, p, target, ntarget);
        buf[at] = (uint8_t)((p - start) >> 8);
        buf[at + 1] = (uint8_t)(p - start);
    }

    const char *const *owner = ntarget ? target : q;
    size_t nowner = ntarget ? ntarget : nq;
    p = put_name(buf, p, owner, nowner);
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_A;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0; buf[p++] = 0; buf[p++] = 0; buf[p++] = 60;
    buf[p++] = 0; buf[p++] = 4;
    buf[p++] = 93; buf[p++] = 184; buf[p++] = 216; buf[p++] = 34;
    return p;
}

#define MAX_SEEN 16
typedef struct {
    char group[MAX_SEEN][64];
    char name[MAX_SEEN][128];
    char real[MAX_SEEN][48];
    bool has_real[MAX_SEEN];
    unsigned family[MAX_SEEN];
    size_t n;
} seen_t;

static void record(void *ud, const char *qname, const char *group_id, const firc_ip_t *fake,
                   const firc_ip_t *real) {
    seen_t *s = ud;
    (void)fake;
    if (s->n >= MAX_SEEN) { return; }
    snprintf(s->group[s->n], sizeof(s->group[0]), "%s", group_id);
    snprintf(s->name[s->n], sizeof(s->name[0]), "%s", qname);
    s->family[s->n] = real != NULL && real->len == 16 ? 6 : 4;
    s->has_real[s->n] = real != NULL;
    if (real != NULL && real->len == 4) {
        snprintf(s->real[s->n], sizeof(s->real[0]), "%u.%u.%u.%u", real->b[0], real->b[1], real->b[2], real->b[3]);
    } else {
        s->real[s->n][0] = '\0';
    }
    s->n++;
}

static void seen_from(firc_fakeip_t *pool, seen_t *s) {
    memset(s, 0, sizeof(*s));
    firc_fakeip_walk(pool, record, s);
}

static firc_fakeip_cfg_t pool_cfg(size_t max_names) {
    firc_fakeip_cfg_t c = {0};
    c.v4.base.len = 4;
    c.v4.base.b[0] = 198;
    c.v4.base.b[1] = 18;
    c.v4.pool_cidr = 15;
    c.v4.chunk_cidr = 24;
    c.v6.base.len = 16;
    c.v6.base.b[0] = 0xfd;
    c.v6.base.b[1] = 0x37;
    c.v6.base.b[2] = 0x9a;
    c.v6.pool_cidr = 48;
    c.v6.chunk_cidr = 64;
    c.max_names = max_names;
    c.idle_secs = 86400;
    c.clamp_secs = 300;
    return c;
}

typedef struct {
    firc_config_t cfg;
    firc_fakeip_t *pool;
    firc_dns_pipeline_t *p;
    seen_t seen;
} rw_fixture_t;

/* The pipeline over `cfg` (taken over), with a pool of `max_names` */
static void rw_up_cfg(rw_fixture_t *f, firc_config_t cfg, size_t max_names) {
    f->cfg = cfg;
    firc_fakeip_cfg_t c = pool_cfg(max_names);
    if (firc_fakeip_new(&c, &f->pool) != FIRC_OK) { abort(); }
    memset(&f->seen, 0, sizeof(f->seen));
    f->p = firc_dns_pipeline_create();
    firc_dns_pipeline_set_snapshot(f->p, firc_ruleset_snapshot_build(&f->cfg));
    firc_dns_pipeline_set_pool(f->p, f->pool, 300);
}

static void rw_down(rw_fixture_t *f) {
    firc_dns_pipeline_destroy(f->p);
    firc_fakeip_free(f->pool);
    firc_config_clear(&f->cfg);
}

/* shop.example.com CNAME cdn.example.net, cdn.example.net A 93.184.216.34 */
static firc_dns_msg_t *chain_answer(void) {
    uint8_t buf[512];
    static const char *q[] = {"shop", "example", "com"};
    static const char *t[] = {"cdn", "example", "net"};
    size_t len = build_response(buf, q, 3, t, 3);
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(buf, len, &msg) != FIRC_OK) { abort(); }
    return msg;
}

typedef struct {
    int calls;
    char name[128];
    firc_ip_t client, real;
    size_t n;
} told_t;

typedef struct {
    bool seen4;
    uint8_t real4[4];
} recorded_t;

static uint64_t g_rep_ms = 1000;
static uint64_t rep_clock(void) { return g_rep_ms; }

static size_t answers_from(rw_fixture_t *f, uint8_t rcode, size_t n, firc_dns_resolver_t resolver, char *out,
                           size_t cap) {
    static const char *q[] = {"www", "example", "com"};
    int fds[2];
    if (pipe(fds) != 0) { return (size_t)-1; }
    firc_log_set_output_floor(FIRC_LOG_INFO);
    firc_log_set_fd(fds[1]);
    for (size_t i = 0; i < n; i++) {
        uint8_t buf[512];
        size_t len = build_response(buf, q, 3, NULL, 0);
        buf[3] = (uint8_t)(0x80u | rcode);
        firc_dns_msg_t *msg = NULL;
        if (firc_dns_msg_parse(buf, len, &msg) != FIRC_OK) { continue; }
        (void)firc_dns_pipeline_handle_message_from(f->p, msg, 1000, NULL, resolver, NULL);
        firc_dns_msg_free(msg);
    }
    firc_log_set_fd(STDOUT_FILENO);
    close(fds[1]);
    ssize_t got = read(fds[0], out, cap - 1);
    close(fds[0]);
    out[got > 0 ? got : 0] = '\0';
    return (size_t)(got > 0 ? got : 0);
}

static size_t journal_lines(firc_log_level_t level, const char *needle) {
    static firc_event_t ev[1024];
    uint64_t next = 0, dropped = 0;
    size_t n = firc_event_read(0, ev, 1024, &next, &dropped), c = 0;
    for (size_t i = 0; i < n; i++) {
        if (ev[i].kind == FIRC_EVENT_LOG && ev[i].u.log.level == level && strstr(ev[i].u.log.text, needle) != NULL) {
            c++;
        }
    }
    return c;
}

static size_t journal(firc_event_t *out, size_t cap) {
    uint64_t next = 0, dropped = 0;
    return firc_event_read(0, out, cap, &next, &dropped);
}

static size_t dns_events(firc_event_t *all, size_t n, firc_event_t *out) {
    size_t k = 0;
    for (size_t i = 0; i < n; i++) {
        if (all[i].kind == FIRC_EVENT_DNS) { out[k++] = all[i]; }
    }
    return k;
}

/* One question, one A record, no chain: the name answered with 93.184.216.34. */
static firc_dns_msg_t *plain_answer(const char *const *labels, size_t n, uint8_t rcode) {
    uint8_t buf[1024];
    size_t len = build_response(buf, labels, n, NULL, 0);
    buf[3] = (uint8_t)(0x80 | rcode);
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(buf, len, &msg) != FIRC_OK) { abort(); }
    return msg;
}

static const char *const k_ntc[] = {"ntc", "example", "com"};


static const uint8_t k_ntc_v6[16] = {0x2a, 0x02, 0x0e, 0x00, 0xff, 0xec, 0x04, 0xb8, 0, 0, 0, 0, 0, 0, 0, 1};

static firc_dns_msg_t *empty_a(void) {
    firc_dns_msg_t *msg = plain_answer(k_ntc, 3, 0);
    free(msg->answers[0].rdata);
    msg->n_answers = 0;
    return msg;
}

static firc_dns_msg_t *follow_up(const uint8_t *addr, uint32_t ttl) {
    uint8_t buf[512];
    size_t p = 0;
    buf[p++] = 0x12; buf[p++] = 0x34;
    buf[p++] = 0x81; buf[p++] = 0x80;
    buf[p++] = 0x00; buf[p++] = 0x01;
    buf[p++] = 0x00; buf[p++] = (uint8_t)(addr != NULL ? 1 : 0);
    buf[p++] = 0x00; buf[p++] = 0x00;
    buf[p++] = 0x00; buf[p++] = 0x00;
    p = put_name(buf, p, k_ntc, 3);
    buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_AAAA;
    buf[p++] = 0x00; buf[p++] = 0x01;
    if (addr != NULL) {
        p = put_name(buf, p, k_ntc, 3);
        buf[p++] = 0x00; buf[p++] = FIRC_DNS_TYPE_AAAA;
        buf[p++] = 0x00; buf[p++] = 0x01;
        buf[p++] = (uint8_t)(ttl >> 24); buf[p++] = (uint8_t)(ttl >> 16);
        buf[p++] = (uint8_t)(ttl >> 8); buf[p++] = (uint8_t)ttl;
        buf[p++] = 0; buf[p++] = 16;
        memcpy(buf + p, addr, 16);
        p += 16;
    }
    firc_dns_msg_t *msg = NULL;
    if (firc_dns_msg_parse(buf, p, &msg) != FIRC_OK) { abort(); }
    return msg;
}

static bool cross_for(void *ud, firc_id_t group_id) {
    return firc_id_equal(*(const firc_id_t *)ud, group_id);
}

typedef struct {
    firc_ip_t fake;
    int kind;
} kind_probe_t;

GREATEST_MAIN_DEFS();

static void dns_group(firc_config_t *cfg, uint8_t id, const char *name) {
    add_group(cfg, id, name, FIRC_RULE_NAMESPACE, "example.com");
    firc_group_t *g = cfg->groups[cfg->n_groups - 1];
    firc_strset(&g->iface, FIRC_IFACE_DNS);
    firc_strset(&g->resolve.server, "1.1.1.1");
}

static firc_dns_msg_t *plain_answer_ttl(const char *const *labels, size_t n, uint32_t ttl) {
    firc_dns_msg_t *msg = plain_answer(labels, n, 0);
    msg->answers[0].ttl = ttl;
    return msg;
}

TEST a_dns_owners_answer_passes_untouched(void) {
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    firc_ip_t client = {{192, 168, 1, 42}, 4};
    firc_dns_msg_t *msg = chain_answer();
    uint8_t *before = NULL, *after = NULL;
    size_t nb = 0, na = 0;
    ASSERT_EQ(FIRC_OK, firc_dns_msg_pack(msg, &before, &nb));
    bool changed = true;
    ASSERT_EQ((int)FIRC_DNS_PASS, (int)firc_dns_pipeline_handle_message_from(f.p, msg, 1000, &client,
                                                                              FIRC_DNS_RESOLVER_GROUP, &changed));
    ASSERT_FALSE(changed);
    ASSERT_EQ(FIRC_OK, firc_dns_msg_pack(msg, &after, &na));
    ASSERT_EQ_FMT(nb, na, "%zu");
    ASSERT_MEM_EQ(before, after, nb);
    seen_t seen;
    seen_from(f.pool, &seen);
    ASSERT_EQ_FMTm("no mapping exists", (size_t)0, seen.n, "%zu");
    free(before);
    free(after);
    firc_dns_msg_free(msg);
    rw_down(&f);
    PASS();
}

TEST a_dns_owners_answer_is_journalled_dns_only(void) {
    firc_event_reset_for_test();
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    firc_ip_t client = {{192, 168, 1, 42}, 4};
    firc_dns_msg_t *msg = plain_answer(k_ntc, 3, 0);
    (void)firc_dns_pipeline_handle_message_from(f.p, msg, (int64_t)time(NULL), &client,
                                                FIRC_DNS_RESOLVER_FALLBACK_TIMEOUT, NULL);
    firc_dns_msg_free(msg);
    firc_event_t all[8], ev[8];
    ASSERT_EQ_FMT((size_t)1, dns_events(all, journal(all, 8), ev), "%zu");
    const firc_event_dns_t *d = &ev[0].u.dns;
    ASSERT_EQ_FMT((unsigned)FIRC_DNS_DNS_ONLY, (unsigned)d->decision, "%u");
    ASSERT_EQ_FMT((unsigned)FIRC_DNS_RESOLVER_FALLBACK_TIMEOUT, (unsigned)d->resolver, "%u");
    ASSERT_STR_EQ("dns", d->group_name);
    ASSERT_EQ_FMT(0u, (unsigned)d->fake.len, "%u");
    firc_recall_answer_t last;
    ASSERT(firc_recall_last_answer(firc_dns_pipeline_recall(f.p), &client, "ntc.example.com", FIRC_RECALL_V4,
                                   (int64_t)time(NULL), &last));
    ASSERT_EQ_FMT((unsigned)FIRC_DNS_DNS_ONLY, (unsigned)last.decision, "%u");
    rw_down(&f);
    PASS();
}

TEST a_dns_owners_ttl_is_not_clamped(void) {
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    firc_ip_t client = {{192, 168, 1, 42}, 4};
    firc_dns_msg_t *msg = plain_answer_ttl(k_ntc, 3, 3000);
    ASSERT_EQ((int)FIRC_DNS_PASS, (int)firc_dns_pipeline_handle_message(f.p, msg, 1000, &client, NULL));
    ASSERT_EQ_FMT(3000u, msg->answers[0].ttl, "%u");
    firc_dns_msg_free(msg);
    rw_down(&f);
    PASS();
}

TEST a_device_outside_the_dns_groups_selector_gets_the_next_owner(void) {
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    firc_group_t *g = cfg.groups[0];
    g->devices.allow = calloc(1, sizeof(char *));
    g->devices.allow[0] = strdup("192.168.1.5");
    g->devices.n_allow = 1;
    add_group(&cfg, 6, "routed", FIRC_RULE_NAMESPACE, "example.com");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    const firc_ip_t member = {{192, 168, 1, 5}, 4}, other = {{192, 168, 1, 9}, 4};
    firc_dns_msg_t *msg = chain_answer();
    ASSERT_EQ((int)FIRC_DNS_PASS, (int)firc_dns_pipeline_handle_message(f.p, msg, 1000, &member, NULL));
    ASSERT_MEM_EQ("\x5d\xb8\xd8\x22", msg->answers[1].rdata, 4);
    firc_dns_msg_free(msg);
    msg = chain_answer();
    ASSERT_EQ((int)FIRC_DNS_HOLD, (int)firc_dns_pipeline_handle_message(f.p, msg, 1000, &other, NULL));
    ASSERT_EQ_FMT(4u, (unsigned)msg->answers[0].rdata_len, "%u");
    ASSERT_EQ_FMT(198u, (unsigned)msg->answers[0].rdata[0], "%u");
    firc_dns_msg_free(msg);
    rw_down(&f);
    PASS();
}

TEST a_dns_owners_nxdomain_is_journalled_dns_only(void) {
    firc_event_reset_for_test();
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    firc_ip_t client = {{192, 168, 1, 42}, 4};
    firc_dns_msg_t *msg = plain_answer(k_ntc, 3, 3);
    ASSERT_EQ((int)FIRC_DNS_PASS, (int)firc_dns_pipeline_handle_message_from(f.p, msg, 1000, &client,
                                                                              FIRC_DNS_RESOLVER_GROUP, NULL));
    ASSERT_EQ(3u, msg->flags & 0x000fu);
    firc_dns_msg_free(msg);
    firc_event_t all[8], ev[8];
    ASSERT_EQ_FMT((size_t)1, dns_events(all, journal(all, 8), ev), "%zu");
    ASSERT_EQ_FMT((unsigned)FIRC_DNS_DNS_ONLY, (unsigned)ev[0].u.dns.decision, "%u");
    ASSERT_EQ_FMT(3u, (unsigned)ev[0].u.dns.rcode, "%u");
    rw_down(&f);
    PASS();
}

TEST a_cross_answer_for_a_dns_owner_issues_no_fake(void) {
    firc_event_reset_for_test();
    firc_config_t cfg = make_cfg();
    dns_group(&cfg, 5, "dns");
    rw_fixture_t f;
    rw_up_cfg(&f, cfg, 64);
    firc_dns_pipeline_set_cross_fn(f.p, cross_for, &f.cfg.groups[0]->id);
    firc_ip_t client = {{192, 168, 1, 42}, 4};
    firc_dns_msg_t *msg = empty_a();
    firc_dns_msg_t *aaaa = follow_up(k_ntc_v6, 600);
    bool changed = true;
    ASSERT_EQ((int)FIRC_DNS_PASS, (int)firc_dns_pipeline_cross_answer(f.p, msg, aaaa, 1000, &client,
                                                                      FIRC_DNS_RESOLVER_GROUP, &changed));
    ASSERT_FALSE(changed);
    ASSERT_EQ_FMT((size_t)0, msg->n_answers, "%zu");
    seen_t seen;
    seen_from(f.pool, &seen);
    ASSERT_EQ_FMT((size_t)0, seen.n, "%zu");
    firc_dns_msg_free(msg);
    firc_dns_msg_free(aaaa);
    rw_down(&f);
    PASS();
}

TEST servfails_for_a_dns_owner_never_count_as_an_upstream_outage(void) {
    static char out[4096];
    for (int dns = 0; dns < 2; dns++) {
        firc_config_t cfg = make_cfg();
        if (dns) {
            dns_group(&cfg, 5, "dns");
        } else {
            add_group(&cfg, 5, "plain", FIRC_RULE_NAMESPACE, "example.com");
        }
        rw_fixture_t f;
        rw_up_cfg(&f, cfg, 64);
        g_rep_ms = 1000;
        firc_logrep_set_clock_for_test(rep_clock);
        firc_event_reset_for_test();
        (void)answers_from(&f, 2, 40, FIRC_DNS_RESOLVER_LOCAL, out, sizeof(out));
        firc_logrep_set_clock_for_test(NULL);
        size_t lines = journal_lines(FIRC_LOG_WARN, "upstream is answering rcode=");
        if (dns) {
            ASSERT_EQ_FMT((size_t)0, lines, "%zu");
        } else {
            ASSERTm("the detector still counts other owners", lines > 0);
        }
        rw_down(&f);
    }
    PASS();
}

int main(int argc, char **argv)
{
    GREATEST_MAIN_BEGIN();
    RUN_TEST(a_dns_owners_answer_passes_untouched);
    RUN_TEST(a_dns_owners_answer_is_journalled_dns_only);
    RUN_TEST(a_dns_owners_ttl_is_not_clamped);
    RUN_TEST(a_device_outside_the_dns_groups_selector_gets_the_next_owner);
    RUN_TEST(a_dns_owners_nxdomain_is_journalled_dns_only);
    RUN_TEST(a_cross_answer_for_a_dns_owner_issues_no_fake);
    RUN_TEST(servfails_for_a_dns_owner_never_count_as_an_upstream_outage);
    GREATEST_MAIN_END();
}
