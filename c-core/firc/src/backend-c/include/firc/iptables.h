#ifndef FIRC_IPTABLES_H
#define FIRC_IPTABLES_H

#include <stddef.h>
#include <stdbool.h>
#include <stdint.h>

#include "firc/cancel.h"
#include "firc/err.h"

typedef enum firc_ipt_proto {
    FIRC_IPT_PROTO_IPV4 = 0,
    FIRC_IPT_PROTO_IPV6 = 1,
} firc_ipt_proto_t;

typedef struct firc_xt firc_xt_t;
struct firc_xt_acl;

/* argv-style rule parts, each owned. */
typedef struct firc_ipt_rule {
    char **parts;
    size_t n_parts;
} firc_ipt_rule_t;

firc_ipt_rule_t *firc_ipt_rule_new(const char *const *args, size_t n_args);
firc_ipt_rule_t *firc_ipt_rule_clone(const firc_ipt_rule_t *r);
void firc_ipt_rule_free(firc_ipt_rule_t *r);
bool firc_ipt_rule_equal(const firc_ipt_rule_t *a, const firc_ipt_rule_t *b);
char *firc_ipt_rule_string(const firc_ipt_rule_t *r);

typedef enum firc_ipt_option {
    FIRC_IPT_OP_APPEND = 0,
    FIRC_IPT_OP_INSERT,
} firc_ipt_option_t;

typedef struct firc_ipt firc_ipt_t;

/* Takes xt on success. Every chain of ours is named chain_prefix...; not thread-safe: one thread per engine. */
firc_ipt_t *firc_ipt_new(firc_xt_t *xt, const char *chain_prefix);
void firc_ipt_free(firc_ipt_t *ipt);
firc_ipt_proto_t firc_ipt_proto(const firc_ipt_t *ipt);
const char *firc_ipt_prefix(const firc_ipt_t *ipt);

/* Borrowed token, NULL detaches; checked before each table's write, a raised one makes commit return FIRC_ERR_CANCELED. */
void firc_ipt_set_cancel(firc_ipt_t *ipt, firc_cancel_t *cancel);

/* Drops every registration of the commit being staged; what was last written is kept. */
void firc_ipt_discard(firc_ipt_t *ipt);

/* Per table, what the commits since the last call did ("mangle 12 ms, nat skipped"); then empties it. */
void firc_ipt_take_report(firc_ipt_t *ipt, char *out, size_t cap);

/* table is filter, mangle or nat. A patch is for a built-in chain, an override for a chain named with the prefix.
 * A patch registered again keeps what it staged; an override registered again starts empty. */
firc_err_t firc_ipt_register_chain_patch(firc_ipt_t *ipt, const char *table, const char *chain);
firc_err_t firc_ipt_register_chain_override(firc_ipt_t *ipt, const char *table, const char *chain);

/* FIRC_ERR_STATE for an unregistered chain; FIRC_ERR_INVAL, said once, for a rule outside what firc writes. */
firc_err_t firc_ipt_append(firc_ipt_t *ipt, const char *table, const char *chain, const char *const *args, size_t n_args);
/* Patches only. */
firc_err_t firc_ipt_insert(firc_ipt_t *ipt, const char *table, const char *chain, int rule_num, const char *const *args,
                           size_t n_args);
/* Overrides only: -d fake/32 (/128) -m mark --mark handled/handled -j DNAT --to-destination real, from bytes
 * (4 or 16 as the family). */
firc_err_t firc_ipt_append_dnat(firc_ipt_t *ipt, const char *table, const char *chain, const uint8_t *fake,
                                const uint8_t *real);

/* Writes every registered table, skipping one staged as last written; drops each chain and jump of ours not staged. */
firc_err_t firc_ipt_commit(firc_ipt_t *ipt);
/* The same without the skip, and with every jump of ours in a built-in chain put back at its staged place. */
firc_err_t firc_ipt_commit_full(firc_ipt_t *ipt);
int firc_ipt_refusal(const firc_ipt_t *ipt);
/* The table the last commit failed to write, "filter", "mangle" or "nat"; NULL when it wrote all or never ran. */
const char *firc_ipt_refused_table(const firc_ipt_t *ipt);
/* Whether a table firc wrote no longer has the shape firc left. Reads the kernel; NULL-safe. */
bool firc_ipt_tables_moved(firc_ipt_t *ipt);

/* Writes chain (unjumped) into table and takes it away again, outside the registrations; FIRC_ERR_IO when refused. */
firc_err_t firc_ipt_probe_chain(firc_ipt_t *ipt, const char *table, const char *chain, firc_ipt_rule_t *const *rules,
                                size_t n);
/* firc_xt_has on the engine's handle; false for a NULL engine. */
bool firc_ipt_has(const firc_ipt_t *ipt, const char *name);
/* firc_xt_acl_denying on the engine's handle; INVAL for a NULL engine. */
firc_err_t firc_ipt_acl_denying(firc_ipt_t *ipt, const char *table, const char *in_chain, const char *out_chain,
                                struct firc_xt_acl *out);
/* firc_xt_probe_capture on the engine's handle. */
firc_err_t firc_ipt_probe_capture(firc_ipt_t *ipt);

#endif /* FIRC_IPTABLES_H */
