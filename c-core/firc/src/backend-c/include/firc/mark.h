#ifndef FIRC_MARK_H
#define FIRC_MARK_H

#include <stdbool.h>
#include <stdint.h>

#define FIRC_MARK_HANDLED 0x40000000u
#define FIRC_MARK_GROUP_MASK 0x00ff0000u
#define FIRC_MARK_GROUP_SHIFT 16u

/* What firc writes, and the only bits it may touch. */
#define FIRC_MARK_WRITE_MASK (FIRC_MARK_HANDLED | FIRC_MARK_GROUP_MASK)

/* Policy marks are matched under this, ignoring firc's own bits. */
#define FIRC_MARK_POLICY_MASK (~FIRC_MARK_WRITE_MASK & 0xffffffffu)

/* A group's devices chain is `<prefix><id>` + this. */
#define FIRC_DEVICES_CHAIN_SUFFIX "D"

/* A list group's egress sub-chain is `<prefix><id>` + this. */
#define FIRC_EGRESS_LIST_SUFFIX "E"

/* The egress dispatch chain, shared by every routed group, is `<prefix>` + this. */
#define FIRC_EGRESS_CHAIN_SUFFIX "EGRESS"

/* Field 0 means no group; past 255 a group is refused, never wrapped. */
#define FIRC_MARK_MAX_GROUPS 255u

static inline bool firc_mark_for_field(uint32_t field, uint32_t *out) {
    if (field == 0 || field > FIRC_MARK_MAX_GROUPS) { return false; }
    *out = FIRC_MARK_HANDLED | (field << FIRC_MARK_GROUP_SHIFT);
    return true;
}

/* Group field only: routing ignores the handled bit, the conntrack flush requires it. */
static inline uint32_t firc_mark_group_value(uint32_t field) {
    return (field << FIRC_MARK_GROUP_SHIFT) & FIRC_MARK_GROUP_MASK;
}

/* Ahead of the firmware's policy rules (100/101) and a source-ipset rule at 90. */
#define FIRC_RULE_PRIORITY 50u

/* Reply twin: FASTNAT routes DNAT replies by ct->mark; this sends them via main. */
#define FIRC_RULE_PRIORITY_REPLY 49u

/* Owner of a tunnel uplink's field in the shared allocator: this + the tunnel id. */
#define FIRC_MARK_TUNNEL_OWNER "tun:"

/* A tunnel's own sockets, ahead of every group rule. */
#define FIRC_RULE_PRIORITY_TUNNEL 48u

static inline uint32_t firc_rule_priority_for_field(uint32_t field) {
    (void)field;
    return FIRC_RULE_PRIORITY;
}

#endif /* FIRC_MARK_H */
