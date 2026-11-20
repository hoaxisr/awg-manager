#ifndef FIRC_XTABLES_H
#define FIRC_XTABLES_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "firc/cancel.h"
#include "firc/err.h"
#include "firc/iptables.h"

#define FIRC_XT_NAME_LEN 32
#define FIRC_XT_NUMHOOKS 5
#define FIRC_XT_LOCK_WAIT_MS 200u
#define FIRC_XT_HEADROOM 96u

typedef struct firc_xt_info {
    uint32_t valid_hooks;
    uint32_t hook_entry[FIRC_XT_NUMHOOKS];
    uint32_t underflow[FIRC_XT_NUMHOOKS];
    uint32_t num_entries;
    uint32_t size;
} firc_xt_info_t;

typedef struct firc_xt_counter {
    uint64_t pcnt, bcnt;
} firc_xt_counter_t;

/* The kernel boundary: each returns 0 or an errno. The blob of get_entries and replace has FIRC_XT_HEADROOM
 * bytes before it that the call may overwrite. */
typedef struct firc_xt_kernel_ops {
    int (*get_info)(void *ud, firc_ipt_proto_t fam, const char *table, firc_xt_info_t *out);
    int (*get_entries)(void *ud, firc_ipt_proto_t fam, const char *table, uint8_t *blob, uint32_t size);
    int (*replace)(void *ud, firc_ipt_proto_t fam, const char *table, const firc_xt_info_t *info,
                   uint8_t *blob, uint32_t num_counters, firc_xt_counter_t *old_counters);
    int (*add_counters)(void *ud, firc_ipt_proto_t fam, const char *table,
                        const firc_xt_counter_t *counters, uint32_t n);
    int (*get_revision)(void *ud, firc_ipt_proto_t fam, bool target, const char *name, uint8_t revision);
    int (*lock)(void *ud, unsigned wait_ms);
    void (*unlock)(void *ud);
    void (*destroy)(void *ud);
    int (*set_index)(void *ud, const char *name, uint16_t *index);
} firc_xt_kernel_ops_t;

typedef enum firc_xt_stage_kind {
    FIRC_XT_STAGE_OVERRIDE = 0,
    FIRC_XT_STAGE_PATCH,
} firc_xt_stage_kind_t;

typedef struct firc_xt_jump {
    uint32_t entry;
    char chain[FIRC_XT_NAME_LEN];
} firc_xt_jump_t;

typedef struct firc_xt_setref {
    uint32_t entry;
    uint32_t at;
    char name[FIRC_XT_NAME_LEN];
} firc_xt_setref_t;

/* One chain's rules for fam back to back; a jump's verdict is 0, its chain in jumps; a set match's index is 0, its set in sets.
 * mac_rev, set before the first rule, is the mac match revision written: firc_xt_mac_rev of the handle;
 * the first entry fixes it in first_mac_rev, and a later rule under another mac_rev is refused. */
typedef struct firc_xt_block {
    firc_ipt_proto_t fam;
    uint8_t *bytes;
    size_t len, cap;
    uint32_t n;
    uint32_t exts;
    firc_xt_jump_t *jumps;
    size_t n_jumps, cap_jumps;
    firc_xt_setref_t *sets;
    size_t n_sets, cap_sets;
    uint8_t mac_rev, first_mac_rev;
} firc_xt_block_t;

void firc_xt_block_init(firc_xt_block_t *b);
void firc_xt_block_free(firc_xt_block_t *b);
/* Encodes rule for fam at the end; FIRC_ERR_INVAL when it is outside what firc writes or fam is not the block's. */
firc_err_t firc_xt_block_add_rule(firc_xt_block_t *b, firc_ipt_proto_t fam, const firc_ipt_rule_t *rule);
/* -d fake/32 (/128) -m mark --mark handled/handled -j DNAT --to-destination real, built from bytes; fake and real are 4 or 16 bytes as fam says.
 * FIRC_ERR_INVAL when fam is unknown or not the block's. */
firc_err_t firc_xt_block_add_dnat(firc_xt_block_t *b, firc_ipt_proto_t fam, const uint8_t *fake, const uint8_t *real);
bool firc_xt_block_equal(const firc_xt_block_t *a, const firc_xt_block_t *b);

typedef enum firc_xt_role {
    FIRC_XT_ROLE_OTHER = 0,
    FIRC_XT_ROLE_MARKS,
    FIRC_XT_ROLE_REWRITES,
} firc_xt_role_t;

/* addr and mask are the entry's destination, 16 bytes each (a v4 one in the first 4); zero when it has none or inverts it. */
typedef void (*firc_xt_role_fn)(void *ud, uint32_t index, firc_xt_role_t role, const uint8_t *addr, const uint8_t *mask);
/* Calls fn for every entry of b in order: MARKS when its only condition is a destination and its target is MARK or a jump,
 * REWRITES when it is DNAT to an address, whatever its conditions. FIRC_ERR_INVAL, and no call, for an unsound block or another family.
 * MARKS mean something only for a mangle block, REWRITES only for a nat one. */
firc_err_t firc_xt_block_roles(const firc_xt_block_t *b, firc_ipt_proto_t fam, firc_xt_role_fn fn, void *ud);
/* Appends to out, which must be empty, the entries of in whose keep[index] is true, their jumps and sets with them. */
firc_err_t firc_xt_block_keep(const firc_xt_block_t *in, firc_ipt_proto_t fam, const bool *keep, firc_xt_block_t *out);

typedef struct firc_xt_patch_op {
    firc_ipt_option_t option;
    int rule_num;
    firc_ipt_rule_t *rule;
} firc_xt_patch_op_t;

/* One registered chain as the engine staged it; rules (or block, when set) for an override, ops for a patch. */
typedef struct firc_xt_stage_chain {
    const char *name;
    firc_xt_stage_kind_t kind;
    firc_ipt_rule_t *const *rules;
    size_t n_rules;
    const firc_xt_patch_op_t *ops;
    size_t n_ops;
    /* The merged table borrows its bytes until cleared: it must outlive serialise and the write, unappended. */
    const firc_xt_block_t *block;
} firc_xt_stage_chain_t;

/* A table's staging; sweep_prefix non-NULL also drops every unstaged chain and jump of ours.
 * relocate_jumps, with a sweep_prefix, first cuts every jump of ours from the built-in chains, so each staged one goes back at its staged position. */
typedef struct firc_xt_stage {
    const firc_xt_stage_chain_t *chains;
    size_t n_chains;
    const char *sweep_prefix;
    bool relocate_jumps;
} firc_xt_stage_t;

/* A handle over ops; ops->destroy(ud) runs at firc_xt_free. NULL when ops is NULL or memory runs out. */
firc_xt_t *firc_xt_new(firc_ipt_proto_t fam, const firc_xt_kernel_ops_t *ops, void *ud);
void firc_xt_free(firc_xt_t *xt);
firc_ipt_proto_t firc_xt_proto(const firc_xt_t *xt);

/* Asks every revision a pass writes; each one missing or unaskable is said at ERR once and fails only the tables needing it. FIRC_ERR_IO if any could not be asked, else FIRC_ERR_NOSYS if any is missing. */
firc_err_t firc_xt_probe(firc_xt_t *xt);

/* The same for the capture's extensions (limit, length, connbytes, string, NFLOG); call after firc_tap_load_modules. */
firc_err_t firc_xt_probe_capture(firc_xt_t *xt);

/* The mac match revision the probe confirmed: 1 when the kernel has it (Keenetic), else 0. */
uint8_t firc_xt_mac_rev(const firc_xt_t *xt);

/* False when the probe marked the extension named name (a target or a match, as the kernel spells it) missing,
 * or firc never writes it in the handle's family. */
bool firc_xt_has(const firc_xt_t *xt, const char *name);

/* A table the kernel refuses is written to dir/refused-<family>-<table>.bin, one file per table, overwritten; NULL turns it off. */
firc_err_t firc_xt_set_dump_dir(firc_xt_t *xt, const char *dir);

/* Reads table, merges stage, replaces the table when the result differs. AGAIN and CANCELED mean build it again. */
firc_err_t firc_xt_commit(firc_xt_t *xt, const char *table, const firc_xt_stage_t *stage, firc_cancel_t *cancel);

int firc_xt_refusal(const firc_xt_t *xt);

/* The shape the last firc_xt_commit of table left in the kernel; all zero when it failed. */
firc_xt_info_t firc_xt_left_shape(const firc_xt_t *xt, const char *table);

/* One GET_INFO: true when table has the shape want, false when it moved or cannot be read. */
bool firc_xt_shape_is(firc_xt_t *xt, const char *table, const firc_xt_info_t *want);

#define FIRC_XT_ACL_MAX 32

typedef struct firc_xt_acl {
    char iface[FIRC_XT_ACL_MAX][16];
    size_t n;
    bool all;
} firc_xt_acl_t;

/* Lists, once each in rule order, in_chain's -i then out_chain's -o interfaces whose rule (a jump or goto followed one
 * level) can meet anything but ACCEPT or RETURN; such a rule without an exact name sets all. A missing chain adds none.
 * AGAIN when the table moved while read, LIMIT past FIRC_XT_ACL_MAX, INVAL on a v6 handle. */
firc_err_t firc_xt_acl_denying(firc_xt_t *xt, const char *table, const char *in_chain, const char *out_chain,
                               firc_xt_acl_t *out);

/* The kernel through a raw socket of the family; NULL with errno set when the socket cannot be opened. */
firc_xt_t *firc_xt_real_new(firc_ipt_proto_t fam);

#endif
