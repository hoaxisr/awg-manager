#include "firc/dnspipeline.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <time.h>

#include "firc/dnsrewrite.h"
#include "firc/events.h"
#include "firc/log.h"
#include "firc/logrep.h"
#include "firc/resolver_addr.h"
#include "firc/utf8.h"

#define FIRC_DNS_OUTAGE_RUN 8
#define FIRC_DNS_OUTAGE_ERROR_AFTER_MS 60000u

struct firc_dns_pipeline {
    firc_fakeip_t *pool; /* borrowed, nullable */
    uint32_t clamp_secs;
    uint32_t unmatched_ttl_secs;
    firc_dns_v6_fn v6_fn;
    void *v6_ud;
    firc_dns_cross_fn cross_fn;
    void *cross_ud;
    firc_devsel_policy_fn policy_fn;
    firc_devsel_device_fn device_fn;
    void *policy_ud;
    firc_ruleset_snapshot_t *snapshot; /* owned */
    uint32_t failing_rcode;
    uint64_t failing_count;
    uint64_t healthy_count;
    bool outage_said;
    uint64_t last_healthy_ms;
    firc_logrep_t outage;
    firc_lograte_t said_reals, said_collapse, said_refused;
    bool someone_covers_all;
    firc_recall_t *recall;
    firc_dns_unmatched_fn unmatched_fn;
    void *unmatched_ud;
};

void firc_dns_pipeline_set_unmatched_fn(firc_dns_pipeline_t *p, firc_dns_unmatched_fn fn, void *ud)
{
    p->unmatched_fn = fn;
    p->unmatched_ud = ud;
}

firc_dns_pipeline_t *firc_dns_pipeline_create(void)
{
    firc_dns_pipeline_t *p = calloc(1, sizeof(firc_dns_pipeline_t));
    if (p == NULL) { return NULL; }
    p->recall = firc_recall_new(FIRC_RECALL_ADDRS, FIRC_RECALL_PAIRS, FIRC_RECALL_HORIZON);
    return p;
}

void firc_dns_pipeline_destroy(firc_dns_pipeline_t *p)
{
    if (p == NULL) {
        return;
    }
    firc_ruleset_snapshot_free(p->snapshot);
    firc_recall_free(p->recall);
    free(p);
}

void firc_dns_pipeline_set_snapshot(firc_dns_pipeline_t *p,
                                  firc_ruleset_snapshot_t *snap)
{
    firc_ruleset_snapshot_t *old = p->snapshot;
    p->snapshot = snap;
    firc_ruleset_snapshot_free(old);
    p->someone_covers_all = false;
    for (size_t i = 0; snap != NULL && i < snap->n_groups; i++) {
        if (firc_devsel_is_empty(snap->groups[i]->devices)) { p->someone_covers_all = true; }
    }
}

firc_ruleset_snapshot_t *firc_dns_pipeline_snapshot(firc_dns_pipeline_t *p)
{
    return p != NULL ? p->snapshot : NULL;
}

void firc_dns_pipeline_set_pool(firc_dns_pipeline_t *p, firc_fakeip_t *pool, uint32_t clamp_secs)
{
    p->pool = pool;
    p->clamp_secs = clamp_secs;
}

void firc_dns_pipeline_set_v6_fn(firc_dns_pipeline_t *p, firc_dns_v6_fn fn, void *ud)
{
    p->v6_fn = fn;
    p->v6_ud = ud;
}

void firc_dns_pipeline_set_cross_fn(firc_dns_pipeline_t *p, firc_dns_cross_fn fn, void *ud)
{
    p->cross_fn = fn;
    p->cross_ud = ud;
}

void firc_dns_pipeline_set_unmatched_ttl(firc_dns_pipeline_t *p, uint32_t secs)
{
    p->unmatched_ttl_secs = secs;
}

void firc_dns_pipeline_set_policy_resolver(firc_dns_pipeline_t *p, firc_devsel_policy_fn fn,
                                           firc_devsel_device_fn device, void *ud)
{
    p->policy_fn = fn;
    p->device_fn = device;
    p->policy_ud = ud;
}

static void fold_ascii(char *s)
{
    for (; *s != '\0'; s++) {
        if (*s >= 'A' && *s <= 'Z') { *s = (char)(*s - 'A' + 'a'); }
    }
}

static void trim_fqdn(char *name)
{
    size_t len = strlen(name);
    if (len > 0 && name[len - 1] == '.') {
        name[len - 1] = '\0';
    }
}

/* Only the queried name and its CNAME chain: other answer records are not trusted. */
#define CHAIN_MAX 12

typedef struct {
    char names[CHAIN_MAX][1100];
    size_t n;
} chain_t;

static bool chain_has(const chain_t *c, const char *name)
{
    for (size_t i = 0; i < c->n; i++) {
        if (strcmp(c->names[i], name) == 0) { return true; }
    }
    return false;
}

static void chain_add(chain_t *c, const char *name)
{
    if (c->n >= CHAIN_MAX || chain_has(c, name)) { return; }
    snprintf(c->names[c->n], sizeof(c->names[0]), "%s", name);
    c->n++;
}

static void chain_build(chain_t *c, const firc_dns_msg_t *msg, const char *qname)
{
    c->n = 0;
    if (qname == NULL || qname[0] == '\0') { return; }
    chain_add(c, qname);

    /* Repeated passes: the answer section need not be in chain order. */
    for (size_t pass = 0; pass < CHAIN_MAX; pass++) {
        size_t before = c->n;
        for (size_t i = 0; i < msg->n_answers; i++) {
            const firc_dns_rr_t *rr = &msg->answers[i];
            if (rr->rtype != FIRC_DNS_TYPE_CNAME) { continue; }
            char owner[1100], target[1100];
            if (firc_dns_name_to_string(rr->name, rr->name_len, owner, sizeof(owner), NULL) != FIRC_OK) {
                continue;
            }
            trim_fqdn(owner);
            fold_ascii(owner);
            if (!chain_has(c, owner)) { continue; }
            if (firc_dns_name_to_string(rr->rdata, rr->rdata_len, target, sizeof(target), NULL) !=
                FIRC_OK) {
                continue;
            }
            trim_fqdn(target);
            fold_ascii(target);
            chain_add(c, target);
        }
        if (c->n == before) { break; }
    }
}

static void tell_unmatched(const firc_dns_pipeline_t *p, const firc_dns_msg_t *msg, const firc_ip_t *client,
                           const char *qname, const chain_t *chain)
{
    if (p->unmatched_fn == NULL || client == NULL || qname == NULL) { return; }
    firc_ip_t reals[16];
    size_t n = 0;
    for (size_t i = 0; i < msg->n_answers && n < sizeof(reals) / sizeof(reals[0]); i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        if (rr->rtype != FIRC_DNS_TYPE_A && rr->rtype != FIRC_DNS_TYPE_AAAA) { continue; }
        uint8_t len = rr->rtype == FIRC_DNS_TYPE_A ? 4 : 16;
        if (rr->rdata_len != len) { continue; }
        char rr_owner[1100];
        if (firc_dns_name_to_string(rr->name, rr->name_len, rr_owner, sizeof(rr_owner), NULL) != FIRC_OK) {
            continue;
        }
        trim_fqdn(rr_owner);
        fold_ascii(rr_owner);
        if (!chain_has(chain, rr_owner)) { continue; }
        memcpy(reals[n].b, rr->rdata, len);
        reals[n].len = len;
        n++;
    }
    if (n > 0) { p->unmatched_fn(p->unmatched_ud, client, qname, reals, n); }
}

static bool covered(const firc_dns_pipeline_t *p, const firc_ip_t *client)
{
    if (client == NULL || p->snapshot == NULL || p->snapshot->n_groups == 0) { return false; }
    if (p->someone_covers_all) { return true; }
    for (size_t i = 0; i < p->snapshot->n_groups; i++) {
        if (firc_devsel_allows(p->snapshot->groups[i]->devices, client, p->policy_fn, p->device_fn,
                               p->policy_ud)) {
            return true;
        }
    }
    return false;
}

static void journal_begin(firc_event_t *ev, int64_t now, const firc_ip_t *client, const char *qname,
                          const firc_dns_msg_t *msg, uint32_t rcode, firc_dns_resolver_t resolver)
{
    memset(ev, 0, sizeof(*ev));
    ev->kind = FIRC_EVENT_DNS;
    ev->at = now;
    ev->u.dns.client = *client;
    memcpy(ev->u.dns.name, qname, strnlen(qname, sizeof(ev->u.dns.name) - 1));
    ev->u.dns.qtype = msg->n_questions > 0 ? msg->questions[0].qtype : 0;
    ev->u.dns.rcode = (uint8_t)rcode;
    ev->u.dns.resolver = (uint8_t)resolver;
}

static void journal_group(firc_event_t *ev, const firc_group_snapshot_t *g)
{
    if (ev == NULL || g == NULL) { return; }
    firc_id_format(g->id, ev->u.dns.group_id);
    const char *gn = g->name ? g->name : "";
    snprintf(ev->u.dns.group_name, sizeof(ev->u.dns.group_name), "%.*s",
             firc_utf8_clamp(gn, (int)sizeof(ev->u.dns.group_name) - 1), gn);
}

/* The recall gets only rcode-0 A/AAAA answers for owned names; others evict useful pairs. */
static void journal_put(firc_dns_pipeline_t *p, firc_event_t *ev, firc_dns_decision_t decision)
{
    if (ev == NULL) { return; }
    ev->u.dns.decision = (uint8_t)decision;
    firc_event_put(ev);
    const uint16_t qt = ev->u.dns.qtype;
    if (ev->u.dns.rcode != 0 || ev->u.dns.group_id[0] == '\0' ||
        (qt != FIRC_DNS_TYPE_A && qt != FIRC_DNS_TYPE_AAAA)) {
        return;
    }
    firc_recall_answer(p->recall, &ev->u.dns.client, ev->u.dns.name,
                       qt == FIRC_DNS_TYPE_A ? FIRC_RECALL_V4 : FIRC_RECALL_V6, (uint8_t)decision,
                       ev->u.dns.fake.len ? &ev->u.dns.fake : NULL, ev->at);
}

static void note_blocked(firc_dns_pipeline_t *p, const firc_dns_msg_t *msg, int64_t now, const firc_ip_t *client,
                         const char *qname, const firc_group_snapshot_t *owner, firc_block_answer_t answer)
{
    static const firc_ip_t nobody = {{0}, 0};
    firc_event_t ev;
    journal_begin(&ev, now, client != NULL ? client : &nobody, qname, msg, firc_dns_block_rcode(answer),
                  FIRC_DNS_RESOLVER_LOCAL);
    journal_group(&ev, owner);
    ev.u.dns.decision = (uint8_t)FIRC_DNS_BLOCKED;
    firc_event_put(&ev);
    const uint16_t qt = ev.u.dns.qtype;
    if (client == NULL || (qt != FIRC_DNS_TYPE_A && qt != FIRC_DNS_TYPE_AAAA)) { return; }
    firc_recall_answer(p->recall, client, ev.u.dns.name, qt == FIRC_DNS_TYPE_A ? FIRC_RECALL_V4 : FIRC_RECALL_V6,
                       (uint8_t)FIRC_DNS_BLOCKED, NULL, now);
}

typedef struct {
    firc_ip_t v[64];
    size_t n;
    bool had4, had6;
    uint32_t ttl;
} reals_t;

static void chain_reals(const firc_dns_msg_t *msg, const chain_t *chain, uint32_t clamp, reals_t *r)
{
    r->n = 0;
    r->had4 = r->had6 = false;
    r->ttl = clamp;
    for (size_t i = 0; i < msg->n_answers; i++) {
        const firc_dns_rr_t *rr = &msg->answers[i];
        if (rr->rtype != FIRC_DNS_TYPE_A && rr->rtype != FIRC_DNS_TYPE_AAAA) { continue; }
        char rr_owner[1100];
        if (firc_dns_name_to_string(rr->name, rr->name_len, rr_owner, sizeof(rr_owner), NULL) != FIRC_OK) {
            continue;
        }
        trim_fqdn(rr_owner);
        fold_ascii(rr_owner);
        if (!chain_has(chain, rr_owner)) { continue; }
        uint8_t len = rr->rtype == FIRC_DNS_TYPE_A ? 4 : 16;
        if (rr->rdata_len != len) { continue; }
        if (len == 4) { r->had4 = true; } else { r->had6 = true; }
        if (rr->ttl < r->ttl) { r->ttl = rr->ttl; }
        if (r->n < sizeof(r->v) / sizeof(r->v[0])) {
            memcpy(r->v[r->n].b, rr->rdata, len);
            r->v[r->n].len = len;
            r->n++;
        }
    }
}

static firc_dns_verdict_t answer_fakes(firc_dns_pipeline_t *p, firc_dns_msg_t *msg, int64_t now,
                                       const firc_group_snapshot_t *owner, const char *qname, const reals_t *r,
                                       bool cross, bool *pool_changed, firc_event_t *ev)
{
    char group_id[FIRC_ID_STR_LEN];
    firc_id_format(owner->id, group_id);

    uint64_t gen_before = firc_fakeip_gen(p->pool);
    firc_ip_t fake4 = {{0}, 0}, fake6 = {{0}, 0};
    firc_err_t issue = firc_fakeip_get(p->pool, qname, group_id, now, &fake4, &fake6);
    if (issue == FIRC_ERR_INVAL) {
        firc_lograte_say(&p->said_refused, 60000, FIRC_LOG_WARN, NULL, "pool refused to issue for %s: caller bug", qname);
        return FIRC_DNS_DROP; /* never the real answer in place of a rewrite */
    }
    /* LIMIT/NOMEM answer with the blackhole, never the real address. */

    if (ev != NULL) {
        size_t k = r->n < FIRC_EVENT_REALS ? r->n : FIRC_EVENT_REALS;
        memcpy(ev->u.dns.reals, r->v, k * sizeof(r->v[0]));
        ev->u.dns.n_reals = (uint8_t)k;
    }
    if (issue == FIRC_OK && cross) { firc_fakeip_clear_real(p->pool, qname, group_id, 4); }
    if (issue == FIRC_OK && r->n > 0) {
        firc_err_t e = firc_fakeip_set_reals(p->pool, qname, group_id, r->v, r->n);
        if (e != FIRC_OK) {
            firc_lograte_say(&p->said_reals, 60000, FIRC_LOG_WARN, NULL, "recording real addresses for %s: %s", qname,
                             firc_err_str(e));
        }
        /* Every real address, not only the event's four. */
        for (size_t i = 0; i < r->n; i++) {
            firc_recall_real(p->recall, &r->v[i], qname, group_id, now);
        }
    }

    /* Set whatever happens to the answer below: the pool has already changed. */
    if (pool_changed != NULL) { *pool_changed = firc_fakeip_gen(p->pool) != gen_before; }

    bool want4 = r->had4 || cross, want6 = r->had6 && !cross;
    if (!cross && p->v6_fn != NULL && !p->v6_fn(p->v6_ud, owner->id)) {
        want6 = false;
        (void)firc_dns_msg_strip_aaaa(msg);
    }
    firc_err_t e = firc_dns_msg_collapse(msg, want4 ? &fake4 : NULL, want6 ? &fake6 : NULL, r->ttl);
    if (e != FIRC_OK) {
        /* Neither the real answer nor a broken one may go out. */
        firc_lograte_say(&p->said_collapse, 60000, FIRC_LOG_WARN, NULL, "collapsing the answer for %s: %s", qname,
                         firc_err_str(e));
        return FIRC_DNS_DROP;
    }
    if (ev != NULL) {
        uint16_t qt = msg->questions[0].qtype;
        if (qt == FIRC_DNS_TYPE_A && want4) { ev->u.dns.fake = fake4; }
        if (qt == FIRC_DNS_TYPE_AAAA && want6) { ev->u.dns.fake = fake6; }
    }
    journal_put(p, ev, issue == FIRC_OK ? FIRC_DNS_ISSUED : FIRC_DNS_POOL_REFUSED);

    bool waits = issue == FIRC_OK &&
                 ((want4 && firc_fakeip_needs_commit(p->pool, &fake4)) ||
                  (want6 && firc_fakeip_needs_commit(p->pool, &fake6)));
    return waits ? FIRC_DNS_HOLD : FIRC_DNS_REWRITTEN;
}

static firc_dns_verdict_t rewrite(firc_dns_pipeline_t *p, firc_dns_msg_t *msg, int64_t now,
                                  const firc_group_snapshot_t *owner, const char *qname,
                                  const chain_t *chain, bool *pool_changed, firc_event_t *ev)
{
    reals_t r;
    chain_reals(msg, chain, p->clamp_secs, &r);
    if (msg->questions[0].qtype == FIRC_DNS_TYPE_A && !r.had4 && p->cross_fn != NULL &&
        p->cross_fn(p->cross_ud, owner->id)) {
        return FIRC_DNS_ASK_AAAA;
    }
    return answer_fakes(p, msg, now, owner, qname, &r, false, pool_changed, ev);
}

static bool usable_v6(const firc_dns_pipeline_t *p, const reals_t *r)
{
    size_t n6 = 0;
    for (size_t i = 0; i < r->n; i++) {
        if (r->v[i].len != 16) { continue; }
        if (firc_resolver_ip_is_sink(&r->v[i]) || firc_fakeip_overlaps(p->pool, &r->v[i], 128)) { return false; }
        n6++;
    }
    return n6 > 0;
}

static firc_dns_verdict_t cross_nodata(firc_dns_pipeline_t *p, int64_t now, const firc_group_snapshot_t *owner,
                                       const char *qname, bool *pool_changed, firc_event_t *ev)
{
    char group_id[FIRC_ID_STR_LEN];
    firc_id_format(owner->id, group_id);
    uint64_t gen_before = firc_fakeip_gen(p->pool);
    firc_ip_t fake4 = {{0}, 0}, fake6 = {{0}, 0};
    firc_err_t issue = firc_fakeip_get(p->pool, qname, group_id, now, &fake4, &fake6);
    if (issue == FIRC_OK) { firc_fakeip_clear_real(p->pool, qname, group_id, 4); }
    if (pool_changed != NULL) { *pool_changed = firc_fakeip_gen(p->pool) != gen_before; }
    if (issue != FIRC_ERR_INVAL) { journal_put(p, ev, issue == FIRC_OK ? FIRC_DNS_ISSUED : FIRC_DNS_POOL_REFUSED); }
    return FIRC_DNS_PASS;
}

firc_dns_verdict_t firc_dns_pipeline_cross_answer(firc_dns_pipeline_t *p, firc_dns_msg_t *a_msg,
                                                  const firc_dns_msg_t *aaaa_msg, int64_t now,
                                                  const firc_ip_t *client, firc_dns_resolver_t resolver,
                                                  bool *pool_changed)
{
    if (pool_changed != NULL) { *pool_changed = false; }
    char qname[FIRC_DNS_MAX_NAME * 4 + 2];
    if (p == NULL || p->pool == NULL || a_msg == NULL || a_msg->n_questions == 0 ||
        a_msg->questions[0].qtype != FIRC_DNS_TYPE_A || !firc_dns_msg_queried_name(a_msg, qname, sizeof(qname))) {
        return FIRC_DNS_PASS;
    }
    const firc_group_snapshot_t *owner =
        firc_ruleset_snapshot_owner(p->snapshot, qname, client, p->policy_fn, p->device_fn, p->policy_ud, NULL);
    if (owner == NULL || owner->block >= 0) { return FIRC_DNS_PASS; }

    firc_event_t ev_store;
    firc_event_t *ev = covered(p, client) ? &ev_store : NULL;
    if (ev != NULL) {
        journal_begin(ev, now, client, qname, a_msg, 0, resolver);
        journal_group(ev, owner);
    }
    if (p->cross_fn == NULL || !p->cross_fn(p->cross_ud, owner->id)) {
        return cross_nodata(p, now, owner, qname, pool_changed, ev);
    }
    reals_t r = {0};
    if (aaaa_msg != NULL && (aaaa_msg->flags & 0x000fu) == 0) {
        chain_t chain;
        chain_build(&chain, aaaa_msg, qname);
        chain_reals(aaaa_msg, &chain, p->clamp_secs, &r);
    }
    if (!usable_v6(p, &r)) { return cross_nodata(p, now, owner, qname, pool_changed, ev); }
    size_t kept = 0;
    for (size_t i = 0; i < r.n; i++) {
        if (r.v[i].len == 16) { r.v[kept++] = r.v[i]; }
    }
    r.n = kept;
    r.had4 = false;
    return answer_fakes(p, a_msg, now, owner, qname, &r, true, pool_changed, ev);
}

static uint64_t outage_after(const firc_dns_pipeline_t *p)
{
    if (p->last_healthy_ms == 0) { return FIRC_DNS_OUTAGE_ERROR_AFTER_MS; }
    uint64_t first = p->outage.count != 0 ? p->outage.first_ms : firc_logrep_now_ms();
    uint64_t quiet_until = p->last_healthy_ms + FIRC_DNS_OUTAGE_ERROR_AFTER_MS;
    return quiet_until > first + FIRC_DNS_OUTAGE_ERROR_AFTER_MS ? quiet_until - first : FIRC_DNS_OUTAGE_ERROR_AFTER_MS;
}

firc_dns_verdict_t firc_dns_pipeline_handle_message_from(firc_dns_pipeline_t *p, firc_dns_msg_t *msg,
                                                         int64_t now, const firc_ip_t *client,
                                                         firc_dns_resolver_t resolver, bool *pool_changed)
{
    if (pool_changed != NULL) { *pool_changed = false; }
    uint32_t rcode = msg->flags & 0x000fu;
    /* NXDOMAIN/NOTIMP stay at DEBUG: at WARN they wrap the firmware's syslog ring in minutes. */
    bool common = resolver != FIRC_DNS_RESOLVER_GROUP && resolver != FIRC_DNS_RESOLVER_CACHE;
    bool failing = rcode == 2u || rcode == 5u;
    if (common && failing) {
        if (p->failing_count == 0) { p->failing_rcode = rcode; }
        p->failing_count++;
        p->healthy_count = 0;
        if (p->failing_count >= FIRC_DNS_OUTAGE_RUN) {
            p->outage_said = true;
            firc_logrep_fail_for_quiet(&p->outage, outage_after(p), NULL,
                                 "upstream is answering rcode=%u and has for %llu responses; names it "
                                 "refuses are not routed",
                                 p->failing_rcode, (unsigned long long)p->failing_count);
        }
    } else if (common && p->failing_count > 0) {
        p->healthy_count++;
        p->last_healthy_ms = firc_logrep_now_ms();
        if (p->healthy_count >= FIRC_DNS_OUTAGE_RUN) {
            if (p->outage_said) {
                FIRC_INFO("upstream is answering again, after %llu failed response(s) starting "
                          "with rcode=%u",
                          (unsigned long long)p->failing_count, p->failing_rcode);
            }
            p->failing_count = 0;
            p->healthy_count = 0;
            p->failing_rcode = 0;
            p->outage_said = false;
            p->last_healthy_ms = 0;
            firc_logrep_reset(&p->outage);
        }
    }
    char qname[FIRC_DNS_MAX_NAME * 4 + 2];
    const firc_group_snapshot_t *owner = NULL, *matched = NULL;
    if (firc_dns_msg_queried_name(msg, qname, sizeof(qname))) {
        owner = firc_ruleset_snapshot_owner(p->snapshot, qname, client, p->policy_fn, p->device_fn, p->policy_ud,
                                            &matched);
    } else {
        qname[0] = '\0';
    }
    if (owner != NULL && owner->block >= 0 && firc_dns_blockable(msg)) {
        firc_block_answer_t answer = (firc_block_answer_t)owner->block;
        if (firc_dns_msg_block(msg, answer, p->clamp_secs) != FIRC_OK) { return FIRC_DNS_DROP; }
        note_blocked(p, msg, now, client, qname, owner, answer);
        return FIRC_DNS_REWRITTEN;
    }

    const bool journalled = covered(p, client);
    firc_event_t ev_store;
    firc_event_t *ev = journalled ? &ev_store : NULL;
    if (rcode != 0) {
        if (ev != NULL) {
            journal_begin(ev, now, client, qname, msg, rcode, resolver);
            journal_group(ev, owner);
            journal_put(p, ev, FIRC_DNS_PASSED);
        }
        return FIRC_DNS_PASS;
    }

    if (ev != NULL) {
        journal_begin(ev, now, client, qname, msg, rcode, resolver);
        journal_group(ev, owner != NULL ? owner : matched);
    }

    chain_t chain;
    chain_build(&chain, msg, qname);

    bool addr_question = msg->n_questions > 0 &&
        (msg->questions[0].qtype == FIRC_DNS_TYPE_A || msg->questions[0].qtype == FIRC_DNS_TYPE_AAAA);

    if (matched == NULL) {
        if (addr_question) { tell_unmatched(p, msg, client, qname, &chain); }
        if (p->snapshot != NULL && p->snapshot->provisional && p->pool != NULL && addr_question) {
            /* RETIMED, not REWRITTEN: the address is real, so AAAA stripping must still apply. */
            uint32_t clamp = p->clamp_secs;
            if (p->unmatched_ttl_secs > 0 && p->unmatched_ttl_secs < clamp) {
                clamp = p->unmatched_ttl_secs;
            }
            journal_put(p, ev, FIRC_DNS_NO_MATCH);
            if (firc_dns_msg_clamp_ttl(msg, clamp)) { return FIRC_DNS_RETIMED; }
            return FIRC_DNS_PASS;
        }
        journal_put(p, ev, FIRC_DNS_NO_MATCH);
        if (p->unmatched_ttl_secs > 0 && addr_question) {
            if (firc_dns_msg_clamp_ttl(msg, p->unmatched_ttl_secs)) { return FIRC_DNS_RETIMED; }
        }
        return FIRC_DNS_PASS;
    }
    if (p->pool == NULL || msg->n_questions == 0) {
        journal_put(p, ev, FIRC_DNS_PASSED);
        return FIRC_DNS_PASS;
    }
    if (owner == NULL) {
        journal_put(p, ev, FIRC_DNS_NOT_COVERED);
        if (addr_question) { tell_unmatched(p, msg, client, qname, &chain); }
        if (p->unmatched_ttl_secs > 0 && addr_question) {
            if (firc_dns_msg_clamp_ttl(msg, p->unmatched_ttl_secs)) { return FIRC_DNS_RETIMED; }
        }
        return FIRC_DNS_PASS;
    }
    switch (msg->questions[0].qtype) {
    case FIRC_DNS_TYPE_A:
    case FIRC_DNS_TYPE_AAAA:
        return rewrite(p, msg, now, owner, qname, &chain, pool_changed, ev);
    case 64: /* SVCB */
    case 65: /* HTTPS */
        if (firc_dns_msg_collapse(msg, NULL, NULL, 0) != FIRC_OK) { return FIRC_DNS_DROP; }
        journal_put(p, ev, FIRC_DNS_PASSED);
        return FIRC_DNS_REWRITTEN;
    default:
        journal_put(p, ev, FIRC_DNS_PASSED);
        return FIRC_DNS_PASS;
    }
}

firc_dns_verdict_t firc_dns_pipeline_handle_message(firc_dns_pipeline_t *p, firc_dns_msg_t *msg,
                                                    int64_t now, const firc_ip_t *client,
                                                    bool *pool_changed)
{
    return firc_dns_pipeline_handle_message_from(p, msg, now, client, FIRC_DNS_RESOLVER_UPSTREAM,
                                                 pool_changed);
}

firc_recall_t *firc_dns_pipeline_recall(firc_dns_pipeline_t *p)
{
    return p != NULL ? p->recall : NULL;
}

bool firc_dns_pipeline_covers(const firc_dns_pipeline_t *p, const firc_ip_t *client)
{
    return p != NULL && covered(p, client);
}

void firc_dns_pipeline_note_blocked(firc_dns_pipeline_t *p, const firc_dns_msg_t *query, const firc_ip_t *client,
                                    firc_block_answer_t answer)
{
    if (p == NULL || query == NULL) { return; }
    char qname[FIRC_DNS_MAX_NAME * 4 + 2];
    if (!firc_dns_msg_queried_name(query, qname, sizeof(qname))) { qname[0] = '\0'; }
    const firc_group_snapshot_t *owner = qname[0] != '\0' && p->snapshot != NULL
        ? firc_ruleset_snapshot_owner(p->snapshot, qname, client, p->policy_fn, p->device_fn, p->policy_ud, NULL)
        : NULL;
    note_blocked(p, query, (int64_t)time(NULL), client, qname, owner, answer);
}

const firc_group_snapshot_t *firc_dns_pipeline_owner(firc_dns_pipeline_t *p, const firc_dns_msg_t *query,
                                                     const firc_ip_t *client)
{
    if (p == NULL || p->snapshot == NULL || query == NULL) { return NULL; }
    char qname[FIRC_DNS_MAX_NAME * 4 + 2];
    if (!firc_dns_msg_queried_name(query, qname, sizeof(qname))) { return NULL; }
    return firc_ruleset_snapshot_owner(p->snapshot, qname, client, p->policy_fn, p->device_fn, p->policy_ud, NULL);
}
