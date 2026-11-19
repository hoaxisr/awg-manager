#ifndef FIRC_TAPREPORT_H
#define FIRC_TAPREPORT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/fakeip.h"

typedef struct firc_tap_report firc_tap_report_t;

/* max_flows distinct flows are remembered for dedup; 0 takes a default; NULL on allocation failure. */
firc_tap_report_t *firc_tap_report_new(size_t max_flows);
void firc_tap_report_free(firc_tap_report_t *r);

void firc_tap_report_saw_packet(firc_tap_report_t *r);
void firc_tap_report_saw_no_name(firc_tap_report_t *r);
void firc_tap_report_saw_loss(firc_tap_report_t *r);
void firc_tap_report_saw_rules_gone(firc_tap_report_t *r);
/* Checked first: nothing else matters once an interface the capture watches does not exist. */
void firc_tap_report_saw_iface_gone(firc_tap_report_t *r);
void firc_tap_report_saw_iface_there(firc_tap_report_t *r);
void firc_tap_report_saw_unparsed(firc_tap_report_t *r);
void firc_tap_report_saw_stranger(firc_tap_report_t *r);

/* One bypass event per (client, name) per this many seconds. */
#define FIRC_TAP_DEDUP_SECS 60

/* Past max_flows or on OOM this always answers true, just unfolded. */
bool firc_tap_report_admit(firc_tap_report_t *r, const firc_ip_t *client, const char *name,
                           int64_t now, uint32_t *repeats_out);

/* Counts a second in which socket `which` (0 new, 1 hello) read FIRC_TAP_LIMIT_PER_SEC+ packets. */
void firc_tap_report_saw_at(firc_tap_report_t *r, int which, int64_t sec);

void firc_tap_report_saw_cold_recall(firc_tap_report_t *r);

/* The bypasses reported so far: the leak count a summary opens with. */
uint64_t firc_tap_report_printed(const firc_tap_report_t *r);

/* Most sentences one summary hands over. */
#define FIRC_TAP_SENTENCES_MAX 13

/* Longest sentence firc_tap_report_sentences hands over, NUL excluded. */
#define FIRC_TAP_SENTENCE_MAX 239

/* final=false omits the capture's conclusions, which mid-run would misread as a verdict. */
void firc_tap_report_sentences(const firc_tap_report_t *r, bool final,
                               void (*fn)(const char *sentence, void *ud), void *ud);

#endif /* FIRC_TAPREPORT_H */
