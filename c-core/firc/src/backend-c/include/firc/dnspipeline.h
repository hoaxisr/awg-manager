#ifndef FIRC_DNSPIPELINE_H
#define FIRC_DNSPIPELINE_H

#include <stdbool.h>
#include <stdint.h>

#include "firc/dnswire.h"
#include "firc/events.h"
#include "firc/fakeip.h"
#include "firc/recall.h"
#include "firc/rulesnap.h"

typedef struct firc_dns_pipeline firc_dns_pipeline_t;

firc_dns_pipeline_t *firc_dns_pipeline_create(void);
void firc_dns_pipeline_destroy(firc_dns_pipeline_t *p);

/* Takes ownership of snap and frees the previous one. Loop thread only. */
void firc_dns_pipeline_set_snapshot(firc_dns_pipeline_t *p,
                                  firc_ruleset_snapshot_t *snap);
/* Borrowed or NULL; keep it only via firc_ruleset_snapshot_ref. Loop thread. */
firc_ruleset_snapshot_t *firc_dns_pipeline_snapshot(firc_dns_pipeline_t *p);

/* Borrowed; NULL turns the rewrite off. Loop thread only. */
void firc_dns_pipeline_set_pool(firc_dns_pipeline_t *p, firc_fakeip_t *pool, uint32_t clamp_secs);

/* Whether a group's table routes IPv6; an owner it says no for gets AAAA NODATA. NULL: every group does. */
typedef bool (*firc_dns_v6_fn)(void *ud, firc_id_t group_id);
void firc_dns_pipeline_set_v6_fn(firc_dns_pipeline_t *p, firc_dns_v6_fn fn, void *ud);

/* Whether an owner's empty A answer is followed by an AAAA question answered with its v4 fake. NULL: none is. */
typedef bool (*firc_dns_cross_fn)(void *ud, firc_id_t group_id);
void firc_dns_pipeline_set_cross_fn(firc_dns_pipeline_t *p, firc_dns_cross_fn fn, void *ud);

typedef void (*firc_dns_unmatched_fn)(void *ud, const firc_ip_t *client, const char *name, const firc_ip_t *reals,
                                      size_t n_reals);
void firc_dns_pipeline_set_unmatched_fn(firc_dns_pipeline_t *p, firc_dns_unmatched_fn fn, void *ud);

/* TTL ceiling for a real (unmatched) A/AAAA answer; 0 = off. Loop thread. */
void firc_dns_pipeline_set_unmatched_ttl(firc_dns_pipeline_t *p, uint32_t secs);

/* NULL client is admitted; *pool_changed (nullable) set means ask for a commit. */
firc_dns_verdict_t firc_dns_pipeline_handle_message(firc_dns_pipeline_t *p, firc_dns_msg_t *msg,
                                                    int64_t now, const firc_ip_t *client,
                                                    bool *pool_changed);

firc_dns_verdict_t firc_dns_pipeline_handle_message_from(firc_dns_pipeline_t *p, firc_dns_msg_t *msg,
                                                         int64_t now, const firc_ip_t *client,
                                                         firc_dns_resolver_t resolver, bool *pool_changed);

/* Answers a_msg, the A answer that got FIRC_DNS_ASK_AAAA, from aaaa_msg (NULL: the follow-up failed): REWRITTEN or
 * HOLD with the v4 fake when it carries a v6 real, else PASS with a_msg unchanged. Loop thread. */
firc_dns_verdict_t firc_dns_pipeline_cross_answer(firc_dns_pipeline_t *p, firc_dns_msg_t *a_msg,
                                                  const firc_dns_msg_t *aaaa_msg, int64_t now,
                                                  const firc_ip_t *client, firc_dns_resolver_t resolver,
                                                  bool *pool_changed);

/* Without one a "policy:" selector entry matches no device. */
void firc_dns_pipeline_set_policy_resolver(firc_dns_pipeline_t *p, firc_devsel_policy_fn fn,
                                           firc_devsel_device_fn device, void *ud);

/* Owned by the pipeline; NULL when its allocation failed. Loop thread. */
firc_recall_t *firc_dns_pipeline_recall(firc_dns_pipeline_t *p);
/* Whether some group's selector admits `client`; NULL is not covered. */
bool firc_dns_pipeline_covers(const firc_dns_pipeline_t *p, const firc_ip_t *client);

/* Journals a block answer the proxy gave to query, client NULL included, and feeds the recall. Loop thread. */
void firc_dns_pipeline_note_blocked(firc_dns_pipeline_t *p, const firc_dns_msg_t *query, const firc_ip_t *client,
                                    firc_block_answer_t answer);

/* The group owning the queried name for client (first matching and admitting), or NULL;
 * valid until the next set_snapshot. */
const firc_group_snapshot_t *firc_dns_pipeline_owner(firc_dns_pipeline_t *p, const firc_dns_msg_t *query,
                                                     const firc_ip_t *client);

#endif /* FIRC_DNSPIPELINE_H */
