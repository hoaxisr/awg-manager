#ifndef FIRC_XTABLES_INTERNAL_H
#define FIRC_XTABLES_INTERNAL_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <string.h>

#include "abi.h"
#include "firc/xtables.h"

#define FIRC_XT_NONE UINT32_MAX

typedef struct firc_xt_entry {
    uint8_t *bytes;
    uint32_t len;
    int32_t old_index;
    uint32_t old_off;
    uint32_t points_at;
    char jump[FIRC_XT_NAME_LEN];
    bool staged;
    bool owned;
    uint16_t set;
    uint16_t set_at;
} firc_xt_entry_t;

typedef struct firc_xt_chain {
    char name[FIRC_XT_NAME_LEN];
    int hook;
    firc_xt_entry_t head;
    firc_xt_entry_t *rules;
    size_t n_rules, cap_rules;
    firc_xt_entry_t tail;
} firc_xt_chain_t;

typedef struct firc_xt_tset {
    char name[FIRC_XT_NAME_LEN];
    uint16_t index;
    bool known;
} firc_xt_tset_t;

typedef struct firc_xt_table {
    firc_ipt_proto_t fam;
    uint32_t valid_hooks;
    firc_xt_chain_t *chains;
    size_t n_chains, cap_chains;
    firc_xt_entry_t end;
    uint32_t n_read;
    uint8_t mac_rev;
    firc_xt_tset_t *sets;
    size_t n_sets, cap_sets;
} firc_xt_table_t;

static inline uint32_t firc_xt_ehdr(firc_ipt_proto_t fam) {
    return fam == FIRC_IPT_PROTO_IPV6 ? (uint32_t)sizeof(xt_ip6t_entry_t) : (uint32_t)sizeof(xt_ipt_entry_t);
}
static inline uint32_t firc_xt_at_toff(firc_ipt_proto_t fam) {
    return fam == FIRC_IPT_PROTO_IPV6 ? (uint32_t)offsetof(xt_ip6t_entry_t, target_offset)
                                      : (uint32_t)offsetof(xt_ipt_entry_t, target_offset);
}
static inline uint32_t firc_xt_at_next(firc_ipt_proto_t fam) { return firc_xt_at_toff(fam) + 2u; }
static inline uint32_t firc_xt_at_comefrom(firc_ipt_proto_t fam) { return firc_xt_at_toff(fam) + 4u; }
static inline uint32_t firc_xt_at_counters(firc_ipt_proto_t fam) {
    return fam == FIRC_IPT_PROTO_IPV6 ? (uint32_t)offsetof(xt_ip6t_entry_t, counters)
                                      : (uint32_t)offsetof(xt_ipt_entry_t, counters);
}
static inline uint16_t firc_xt_rd16(const uint8_t *p) { uint16_t v; memcpy(&v, p, sizeof(v)); return v; }
static inline uint32_t firc_xt_rd32(const uint8_t *p) { uint32_t v; memcpy(&v, p, sizeof(v)); return v; }
static inline void firc_xt_wr16(uint8_t *p, uint16_t v) { memcpy(p, &v, sizeof(v)); }
static inline void firc_xt_wr32(uint8_t *p, uint32_t v) { memcpy(p, &v, sizeof(v)); }

firc_err_t firc_xt_parse(firc_ipt_proto_t fam, const firc_xt_info_t *info, uint8_t *blob, firc_xt_table_t *out,
                         const char **why);
firc_err_t firc_xt_serialise(const firc_xt_table_t *t, uint8_t **blob, firc_xt_info_t *info, int32_t **old_index,
                             char *why, size_t why_len);
firc_err_t firc_xt_serialise_room(const firc_xt_table_t *t, size_t room, uint8_t **blob, firc_xt_info_t *info,
                                  int32_t **old_index, char *why, size_t why_len);
void firc_xt_table_clear(firc_xt_table_t *t);
void firc_xt_entry_clear(firc_xt_entry_t *e);
firc_xt_chain_t *firc_xt_find_chain(firc_xt_table_t *t, const char *name);
firc_err_t firc_xt_add_user_chain(firc_xt_table_t *t, const char *name, firc_xt_chain_t **out);
firc_err_t firc_xt_drop_chain(firc_xt_table_t *t, firc_xt_chain_t *c);
uint16_t firc_xt_table_set(firc_xt_table_t *t, const char *name);

typedef enum firc_xt_ext {
    FIRC_XT_EXT_DNAT = 1u << 0,
    FIRC_XT_EXT_MASQUERADE = 1u << 1,
    FIRC_XT_EXT_MARK = 1u << 2,
    FIRC_XT_EXT_TCP = 1u << 3,
    FIRC_XT_EXT_UDP = 1u << 4,
    FIRC_XT_EXT_REJECT = 1u << 5,
    FIRC_XT_EXT_MARK_TARGET = 1u << 6,
    FIRC_XT_EXT_CONNMARK = 1u << 7,
    FIRC_XT_EXT_CONNTRACK = 1u << 8,
    FIRC_XT_EXT_MULTIPORT = 1u << 9,
    FIRC_XT_EXT_MAC = 1u << 10,
    FIRC_XT_EXT_LIMIT = 1u << 11,
    FIRC_XT_EXT_LENGTH = 1u << 12,
    FIRC_XT_EXT_CONNBYTES = 1u << 13,
    FIRC_XT_EXT_STRING = 1u << 14,
    FIRC_XT_EXT_NFLOG = 1u << 15,
    FIRC_XT_EXT_CONNMARK_MATCH = 1u << 16,
    FIRC_XT_EXT_REDIRECT = 1u << 17,
    FIRC_XT_EXT_ADDRTYPE = 1u << 18,
    FIRC_XT_EXT_SET = 1u << 19,
    FIRC_XT_EXT_TCPMSS = 1u << 20,
} firc_xt_ext_t;

#define FIRC_XT_EXT_V4_ONLY (FIRC_XT_EXT_REDIRECT | FIRC_XT_EXT_ADDRTYPE)

typedef struct firc_xt_ext_desc {
    uint32_t bit;
    const char *name;
    bool target;
    uint8_t rev4, rev6;
    bool capture;
} firc_xt_ext_desc_t;

extern const firc_xt_ext_desc_t firc_xt_exts[];
extern const size_t firc_xt_n_exts;

firc_err_t firc_xt_encode(firc_ipt_proto_t fam, uint8_t mac_rev, const firc_ipt_rule_t *rule, firc_xt_entry_t *out,
                          uint32_t *exts, char set[FIRC_XT_NAME_LEN]);

bool firc_xt_entry_same(firc_ipt_proto_t fam, const firc_xt_entry_t *a, const firc_xt_entry_t *b);
firc_err_t firc_xt_merge(firc_xt_table_t *t, const firc_xt_stage_t *stage, uint32_t *exts, char *why, size_t why_len);
firc_err_t firc_xt_acl_judge(firc_xt_table_t *t, const char *in_chain, const char *out_chain, firc_xt_acl_t *out);
bool firc_xt_blob_equal(const firc_xt_info_t *ai, const uint8_t *a, const firc_xt_info_t *bi, const uint8_t *b);
firc_err_t firc_xt_carry_counters(const int32_t *old_index, uint32_t n_new, const firc_xt_counter_t *old,
                                  uint32_t n_old, firc_xt_counter_t **out);

int firc_xt_sock_lock(int *fd, unsigned wait_ms);
void firc_xt_sock_unlock(int *fd);

#endif
