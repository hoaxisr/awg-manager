#ifndef FIRC_RECALL_H
#define FIRC_RECALL_H

/* Loop thread only. Every call accepts a NULL recall and does nothing. */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip_addr.h"
#include "firc/id.h"

#define FIRC_RECALL_ADDRS 8192
#define FIRC_RECALL_PAIRS 8192
#define FIRC_RECALL_HORIZON 1800 /* seconds */

typedef struct firc_recall firc_recall_t;

typedef struct firc_recall_answer {
    int64_t at;
    uint8_t decision; /* firc_dns_decision_t */
    firc_ip_t fake;   /* len 0: none */
} firc_recall_answer_t;

/* NULL when a bound is 0 or the allocation fails. */
firc_recall_t *firc_recall_new(size_t max_addrs, size_t max_pairs, int64_t horizon);
void firc_recall_free(firc_recall_t *r);

/* Records `real` behind `name`; when full, the least recently used is evicted. */
void firc_recall_real(firc_recall_t *r, const firc_ip_t *real, const char *name,
                      const char group_id[FIRC_ID_STR_LEN], int64_t now);
bool firc_recall_by_real(const firc_recall_t *r, const firc_ip_t *real, char *name_out,
                         size_t name_cap, char group_out[FIRC_ID_STR_LEN]);

/* Answer families; any other is not recorded. */
#define FIRC_RECALL_V4 4u
#define FIRC_RECALL_V6 6u

/* `fake` NULL or of the other family is kept as none. */
void firc_recall_answer(firc_recall_t *r, const firc_ip_t *client, const char *name,
                        unsigned family, uint8_t decision, const firc_ip_t *fake, int64_t now);
/* False when none inside the horizon. */
bool firc_recall_last_answer(const firc_recall_t *r, const firc_ip_t *client, const char *name,
                             unsigned family, int64_t now, firc_recall_answer_t *out);
size_t firc_recall_real_count(const firc_recall_t *r);

/* Longest occupied run in either index. Tests only. */
size_t firc_recall_probe_max_for_test(const firc_recall_t *r);

#endif /* FIRC_RECALL_H */
