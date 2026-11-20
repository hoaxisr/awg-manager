#ifndef FIRC_DNSWIRE_H
#define FIRC_DNSWIRE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/err.h"
#include "firc/fakeip_addr.h"
#include "firc/models.h"

#define FIRC_DNS_MAX_MSG 65535
#define FIRC_DNS_MAX_NAME 255      /* wire octets incl. root */
#define FIRC_DNS_HEADER_LEN 12

#define FIRC_DNS_TYPE_A 1
#define FIRC_DNS_TYPE_NS 2
#define FIRC_DNS_TYPE_CNAME 5
#define FIRC_DNS_TYPE_SOA 6
#define FIRC_DNS_TYPE_PTR 12
#define FIRC_DNS_TYPE_MX 15
#define FIRC_DNS_TYPE_AAAA 28
#define FIRC_DNS_TYPE_SRV 33
#define FIRC_DNS_TYPE_OPT 41

#define FIRC_DNS_FLAG_QR 0x8000
#define FIRC_DNS_FLAG_TC 0x0200
#define FIRC_DNS_FLAG_RD 0x0100
#define FIRC_DNS_FLAG_RA 0x0080
#define FIRC_DNS_RCODE_NXDOMAIN 3
#define FIRC_DNS_CLASS_IN 1
#define FIRC_DNS_EDNS_SIZE 1232

typedef struct firc_dns_question {
    uint8_t name[FIRC_DNS_MAX_NAME + 1]; /* decompressed wire form */
    size_t name_len;
    uint16_t qtype;
    uint16_t qclass;
} firc_dns_question_t;

typedef struct firc_dns_rr {
    uint8_t name[FIRC_DNS_MAX_NAME + 1];
    size_t name_len;
    uint16_t rtype;
    uint16_t rclass;
    uint32_t ttl;
    uint8_t *rdata; /* canonical (decompressed) rdata, malloc'd */
    size_t rdata_len;
} firc_dns_rr_t;

typedef struct firc_dns_msg {
    uint16_t id;
    uint16_t flags;
    firc_dns_question_t *questions;
    size_t n_questions;
    firc_dns_rr_t *answers;
    size_t n_answers;
    firc_dns_rr_t *authority;
    size_t n_authority;
    firc_dns_rr_t *additional;
    size_t n_additional;
} firc_dns_msg_t;

/* FIRC_ERR_PROTO on malformed input. */
firc_err_t firc_dns_msg_parse(const uint8_t *buf, size_t len,
                          firc_dns_msg_t **out);
void firc_dns_msg_free(firc_dns_msg_t *msg);

typedef enum firc_dns_verdict {
    FIRC_DNS_PASS = 0,   /* forward the upstream's own bytes */
    FIRC_DNS_REWRITTEN,  /* fake address in place; pack and send now */
    FIRC_DNS_HOLD,       /* rewritten; send after the next commit or at the deadline */
    FIRC_DNS_DROP,       /* answer nothing: the real answer must not go out */
    FIRC_DNS_RETIMED,    /* only a TTL came down; still real, so AAAA stripping applies */
    FIRC_DNS_ASK_AAAA,   /* an empty A for a cross-capable owner: ask AAAA, then firc_dns_pipeline_cross_answer */
} firc_dns_verdict_t;

/* Compresses owner names, never rdata. Caller frees *out. */
firc_err_t firc_dns_msg_pack(const firc_dns_msg_t *msg, uint8_t **out,
                         size_t *out_len);

#define FIRC_DNS_UDP_MIN_SIZE 512
/* 512 unless the query's OPT advertises more. */
uint16_t firc_dns_msg_udp_payload_size(const firc_dns_msg_t *msg);

/* Sets TC and drops every record but an OPT. */
void firc_dns_msg_truncate(firc_dns_msg_t *msg);

/* The question's name, lower-cased, no trailing dot. False for no question,
 * the root, an escaped name, or one that does not fit `out`. */
bool firc_dns_msg_queried_name(const firc_dns_msg_t *msg, char *out, size_t out_len);

/* Wire form of a dotted name, no trailing dot; false for an empty label, a label over 63 or a name over 255. */
bool firc_dns_name_from_text(const char *text, uint8_t *out, size_t *out_len);

/* Caps answer-section TTLs at max_ttl; true only when one came down. */
bool firc_dns_msg_clamp_ttl(firc_dns_msg_t *msg, uint32_t max_ttl);

/* Removes AAAA answers; true only when one was removed. */
bool firc_dns_msg_strip_aaaa(firc_dns_msg_t *msg);

bool firc_dns_ptr_query_addr(const firc_dns_msg_t *query, firc_ip_t *out);

firc_err_t firc_dns_make_ptr_response(const firc_dns_msg_t *query, const char *target, uint32_t ttl,
                                      uint8_t **out, size_t *out_len);

/* Whether msg is one QUERY-opcode question of class IN, the only kind a block group answers. */
bool firc_dns_blockable(const firc_dns_msg_t *msg);

/* The rcode a block answer carries: 0, NXDOMAIN or REFUSED. */
uint8_t firc_dns_block_rcode(firc_block_answer_t answer);

/* Turns msg into the block answer to its one question, in place; msg is unchanged on error. */
firc_err_t firc_dns_msg_block(firc_dns_msg_t *msg, firc_block_answer_t answer, uint32_t ttl);

/* The block answer to query, as firc_dns_msg_block makes it; caller frees *out. */
firc_err_t firc_dns_make_block_response(const firc_dns_msg_t *query, firc_block_answer_t answer, uint32_t ttl,
                                        uint8_t **out, size_t *out_len);

/* Escaped presentation form; buf_len 4*255+2 always fits. *out_written (nullable)
 * excludes the terminator. */
firc_err_t firc_dns_name_to_string(const uint8_t *name, size_t name_len,
                               char *buf, size_t buf_len, size_t *out_written);

#endif /* FIRC_DNSWIRE_H */
