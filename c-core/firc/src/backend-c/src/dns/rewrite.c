#include "firc/dnsrewrite.h"

#include <stdlib.h>
#include <string.h>

#define TYPE_SVCB 64
#define TYPE_HTTPS 65

static bool is_hint(uint16_t t) {
    return t == TYPE_SVCB || t == TYPE_HTTPS;
}

static void drop_rrs(firc_dns_rr_t *rrs, size_t *n, bool (*drop)(uint16_t)) {
    size_t kept = 0;
    for (size_t i = 0; i < *n; i++) {
        if (drop(rrs[i].rtype)) {
            free(rrs[i].rdata);
            continue;
        }
        if (kept != i) { rrs[kept] = rrs[i]; }
        kept++;
    }
    *n = kept;
}

static bool drop_from_answer(uint16_t t) {
    return t == FIRC_DNS_TYPE_A || t == FIRC_DNS_TYPE_AAAA || t == FIRC_DNS_TYPE_CNAME || is_hint(t);
}

static void set_address_rr(firc_dns_rr_t *rr, const firc_dns_question_t *q, const firc_ip_t *ip,
                           uint8_t *rdata, uint32_t ttl) {
    memset(rr, 0, sizeof(*rr));
    memcpy(rr->name, q->name, q->name_len);
    rr->name_len = q->name_len;
    rr->rtype = ip->len == 4 ? FIRC_DNS_TYPE_A : FIRC_DNS_TYPE_AAAA;
    rr->rclass = q->qclass;
    rr->ttl = ttl;
    memcpy(rdata, ip->b, ip->len);
    rr->rdata = rdata;
    rr->rdata_len = ip->len;
}

firc_err_t firc_dns_msg_collapse(firc_dns_msg_t *msg, const firc_ip_t *fake4, size_t n4,
                                 const firc_ip_t *fake6, size_t n6, uint32_t ttl) {
    if (msg == NULL || msg->n_questions == 0 || msg->questions == NULL) { return FIRC_ERR_INVAL; }
    if ((n4 > 0 && fake4 == NULL) || (n6 > 0 && fake6 == NULL)) { return FIRC_ERR_INVAL; }
    for (size_t i = 0; i < n4; i++) {
        if (fake4[i].len != 4) { return FIRC_ERR_INVAL; }
    }
    for (size_t i = 0; i < n6; i++) {
        if (fake6[i].len != 16) { return FIRC_ERR_INVAL; }
    }
    if (n4 == 0 && n6 == 0) {
        for (size_t i = 0; i < msg->n_answers; i++) {
            uint16_t t = msg->answers[i].rtype;
            if (t == FIRC_DNS_TYPE_A || t == FIRC_DNS_TYPE_AAAA) { return FIRC_ERR_INVAL; }
        }
    }

    /* Everything that can fail is allocated before anything is touched. */
    size_t n_new = n4 + n6;
    firc_dns_rr_t *grown = NULL;
    uint8_t **rds = NULL;
    if (n_new > 0) {
        grown = realloc(msg->answers, (msg->n_answers + n_new) * sizeof(*grown));
        if (grown == NULL) { return FIRC_ERR_NOMEM; }
        msg->answers = grown;
        rds = calloc(n_new, sizeof(*rds));
        if (rds == NULL) { return FIRC_ERR_NOMEM; }
    }
    for (size_t i = 0; i < n_new; i++) {
        rds[i] = malloc(i < n4 ? 4 : 16);
        if (rds[i] == NULL) {
            for (size_t j = 0; j < i; j++) { free(rds[j]); }
            free(rds);
            return FIRC_ERR_NOMEM;
        }
    }

    drop_rrs(msg->answers, &msg->n_answers, drop_from_answer);
    drop_rrs(msg->authority, &msg->n_authority, is_hint);
    drop_rrs(msg->additional, &msg->n_additional, is_hint);

    const firc_dns_question_t *q = &msg->questions[0];
    for (size_t i = 0; i < n_new; i++) {
        set_address_rr(&msg->answers[msg->n_answers++], q, i < n4 ? &fake4[i] : &fake6[i - n4], rds[i], ttl);
    }
    free(rds);
    return FIRC_OK;
}
