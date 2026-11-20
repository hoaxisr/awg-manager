#ifndef FIRC_XTABLES_ABI_H
#define FIRC_XTABLES_ABI_H

#include <stddef.h>
#include <stdint.h>

#define XT_ALIGN8(n) (((n) + 7u) & ~7u)
#define XT_NAME_LEN 29
#define XT_TABLE_LEN 32
#define XT_CHAIN_LEN 32
#define XT_ERRORNAME_LEN 30
#define XT_NUMHOOKS 5

#define XT_SO_SET_REPLACE 64
#define XT_SO_SET_ADD_COUNTERS 65
#define XT_SO_GET_INFO 64
#define XT_SO_GET_ENTRIES 65
#define XT_SO_GET_REVISION_MATCH4 66
#define XT_SO_GET_REVISION_TARGET4 67
#define XT_SO_GET_REVISION_MATCH6 68
#define XT_SO_GET_REVISION_TARGET6 69
#define XT_SOL_IP 0
#define XT_SOL_IPV6 41

#define XT_VERDICT_DROP (-1)
#define XT_VERDICT_ACCEPT (-2)
#define XT_VERDICT_RETURN (-5)

#define XT_IPT_F_GOTO 0x02u
#define XT_IP6T_F_PROTO 0x01u
#define XT_IP6T_F_GOTO 0x04u
#define XT_INV_VIA_IN 0x01u
#define XT_INV_VIA_OUT 0x02u
#define XT_INV_DSTIP 0x10u

#define XT_NAT_MAP_IPS 0x1u
#define XT_NAT_PROTO_SPECIFIED 0x2u

#define XT_MATCH_HDR_LEN 32u
#define XT_STANDARD_TARGET_LEN 40u
#define XT_ERROR_TARGET_LEN 64u
#define XT_REPLACE_HDR_LEN 96u
#define XT_GET_ENTRIES_HDR_LEN 40u
#define XT_COUNTERS_INFO_HDR_LEN 40u
#define XT_GETINFO_LEN 84u

typedef struct firc_xt_counters {
    uint64_t pcnt, bcnt;
} xt_counters_t;

typedef struct firc_xt_ipt_ip {
    uint8_t src[4], dst[4], smsk[4], dmsk[4];
    char iniface[16], outiface[16];
    uint8_t iniface_mask[16], outiface_mask[16];
    uint16_t proto;
    uint8_t flags, invflags;
} xt_ipt_ip_t;

typedef struct firc_xt_ipt_entry {
    xt_ipt_ip_t ip;
    uint32_t nfcache;
    uint16_t target_offset, next_offset;
    uint32_t comefrom;
    _Alignas(8) xt_counters_t counters;
} xt_ipt_entry_t;

typedef struct firc_xt_ip6t_ip6 {
    _Alignas(4) uint8_t src[16];
    uint8_t dst[16], smsk[16], dmsk[16];
    char iniface[16], outiface[16];
    uint8_t iniface_mask[16], outiface_mask[16];
    uint16_t proto;
    uint8_t tos, flags, invflags;
} xt_ip6t_ip6_t;

typedef struct firc_xt_ip6t_entry {
    xt_ip6t_ip6_t ipv6;
    uint32_t nfcache;
    uint16_t target_offset, next_offset;
    uint32_t comefrom;
    _Alignas(8) xt_counters_t counters;
} xt_ip6t_entry_t;

typedef struct firc_xt_ext_hdr {
    uint16_t size;
    char name[XT_NAME_LEN];
    uint8_t revision;
} xt_ext_hdr_t;

typedef struct firc_xt_standard_target {
    xt_ext_hdr_t hdr;
    int32_t verdict;
} xt_standard_target_t;

typedef struct firc_xt_error_target {
    xt_ext_hdr_t hdr;
    char errorname[XT_ERRORNAME_LEN];
} xt_error_target_t;

typedef struct firc_xt_getinfo {
    char name[XT_TABLE_LEN];
    uint32_t valid_hooks;
    uint32_t hook_entry[XT_NUMHOOKS];
    uint32_t underflow[XT_NUMHOOKS];
    uint32_t num_entries;
    uint32_t size;
} xt_getinfo_t;

typedef struct firc_xt_get_entries_hdr {
    char name[XT_TABLE_LEN];
    uint32_t size;
} xt_get_entries_hdr_t;

typedef struct firc_xt_replace_hdr {
    char name[XT_TABLE_LEN];
    uint32_t valid_hooks;
    uint32_t num_entries;
    uint32_t size;
    uint32_t hook_entry[XT_NUMHOOKS];
    uint32_t underflow[XT_NUMHOOKS];
    uint32_t num_counters;
    xt_counters_t *counters;
} xt_replace_hdr_t;

typedef struct firc_xt_counters_info_hdr {
    char name[XT_TABLE_LEN];
    uint32_t num_counters;
} xt_counters_info_hdr_t;

typedef struct firc_xt_get_revision {
    char name[XT_NAME_LEN];
    uint8_t revision;
} xt_get_revision_t;

typedef struct firc_xt_mark_mtinfo1 {
    uint32_t mark, mask;
    uint8_t invert;
} xt_mark_mtinfo1_t;

typedef struct firc_xt_tcp {
    uint16_t spts[2], dpts[2];
    uint8_t option, flg_mask, flg_cmp, invflags;
} xt_tcp_t;

typedef struct firc_xt_udp {
    uint16_t spts[2], dpts[2];
    uint8_t invflags;
} xt_udp_t;

typedef struct firc_xt_nat_ipv4_range {
    uint32_t flags;
    uint8_t min_ip[4], max_ip[4];
    uint16_t min_port, max_port;
} xt_nat_ipv4_range_t;

typedef struct firc_xt_nat_ipv4_compat {
    uint32_t rangesize;
    xt_nat_ipv4_range_t range[1];
} xt_nat_ipv4_compat_t;

typedef struct firc_xt_nat_range {
    uint32_t flags;
    _Alignas(4) uint8_t min_addr[16];
    uint8_t max_addr[16];
    uint16_t min_proto, max_proto;
} xt_nat_range_t;

#define XT_REJECT_V4_HOST_UNREACH 1u
#define XT_REJECT_V4_TCP_RESET 7u
#define XT_REJECT_V6_ADDR_UNREACH 3u
#define XT_REJECT_V6_TCP_RESET 6u
#define XT_CONNMARK_MODE_SAVE 1u
#define XT_CT_STATE 0x0001u
#define XT_CT_ORIGDST 0x0008u
#define XT_CT_DIRECTION 0x1000u
#define XT_CT_STATE_INVALID 0x01u
#define XT_CT_STATE_ESTABLISHED 0x02u
#define XT_CT_STATE_RELATED 0x04u
#define XT_CT_STATE_NEW 0x08u
#define XT_MULTI_PORTS 15
#define XT_MULTIPORT_DESTINATION 1u
#define XT_LIMIT_SCALE 10000u
#define XT_LIMIT_USERSPACE 8u
#define XT_STRING_USERSPACE 152u
#define XT_STRING_MAX_PATTERN 128u
#define XT_CONNBYTES_PKTS 0u
#define XT_CONNBYTES_BYTES 1u
#define XT_CONNBYTES_AVGPKT 2u
#define XT_CONNBYTES_DIR_ORIGINAL 0u
#define XT_CONNBYTES_DIR_REPLY 1u
#define XT_CONNBYTES_DIR_BOTH 2u
#define XT_TCP_FIN 0x01u
#define XT_TCP_SYN 0x02u
#define XT_TCP_RST 0x04u
#define XT_TCP_PSH 0x08u
#define XT_TCP_ACK 0x10u
#define XT_TCP_URG 0x20u
#define XT_ADDRTYPE_LOCAL 0x0004u
#define XT_ADDRTYPE_INVERT_DEST 0x2u

#define XT_SO_IP_SET 83
#define XT_IP_SET_OP_VERSION 0x00000100u
#define XT_IP_SET_OP_GET_BYNAME 0x00000006u
#define XT_IPSET_MAXNAMELEN 32
#define XT_IPSET_INVALID_ID 65535u

typedef struct firc_xt_set_info {
    uint16_t index;
    uint8_t dim, flags;
} xt_set_info_t;

typedef struct firc_xt_ip_set_req_version {
    uint32_t op, version;
} xt_ip_set_req_version_t;

typedef struct firc_xt_ip_set_req_get_set {
    uint32_t op, version;
    union {
        char name[XT_IPSET_MAXNAMELEN];
        uint16_t index;
    } set;
} xt_ip_set_req_get_set_t;

typedef struct firc_xt_addrtype_info_v1 {
    uint16_t source, dest;
    uint32_t flags;
} xt_addrtype_info_v1_t;

typedef struct firc_xt_reject_info {
    uint32_t with;
} xt_reject_info_t;

typedef struct firc_xt_mark_tginfo2 {
    uint32_t mark, mask;
} xt_mark_tginfo2_t;

typedef struct firc_xt_connmark_mtinfo1 {
    uint32_t mark, mask;
    uint8_t invert;
} xt_connmark_mtinfo1_t;

typedef struct firc_xt_connmark_tginfo1 {
    uint32_t ctmark, ctmask, nfmask;
    uint8_t mode;
} xt_connmark_tginfo1_t;

typedef struct firc_xt_conntrack_mtinfo3 {
    _Alignas(4) uint8_t origsrc_addr[16];
    uint8_t origsrc_mask[16], origdst_addr[16], origdst_mask[16];
    uint8_t replsrc_addr[16], replsrc_mask[16], repldst_addr[16], repldst_mask[16];
    uint32_t expires_min, expires_max;
    uint16_t l4proto, origsrc_port, origdst_port, replsrc_port, repldst_port;
    uint16_t match_flags, invert_flags, state_mask, status_mask;
    uint16_t origsrc_port_high, origdst_port_high, replsrc_port_high, repldst_port_high;
} xt_conntrack_mtinfo3_t;

typedef struct firc_xt_multiport_v1 {
    uint8_t flags, count;
    uint16_t ports[XT_MULTI_PORTS];
    uint8_t pflags[XT_MULTI_PORTS];
    uint8_t invert;
} xt_multiport_v1_t;

typedef struct firc_xt_mac_info {
    uint8_t srcaddr[6];
    int32_t invert;
} xt_mac_info_t;

typedef struct firc_xt_rateinfo {
    uint32_t avg, burst;
    unsigned long prev;
    uint32_t credit, credit_cap, cost;
    void *master;
} xt_rateinfo_t;

typedef struct firc_xt_length_info {
    uint16_t min, max;
    uint8_t invert;
} xt_length_info_t;

typedef struct firc_xt_connbytes_info {
    _Alignas(8) uint64_t from;
    _Alignas(8) uint64_t to;
    uint8_t what, direction;
} xt_connbytes_info_t;

typedef struct firc_xt_string_info {
    uint16_t from_offset, to_offset;
    char algo[16];
    char pattern[XT_STRING_MAX_PATTERN];
    uint8_t patlen;
    uint8_t flags;
    _Alignas(8) void *config;
} xt_string_info_t;

typedef struct firc_xt_nflog_info {
    uint32_t len;
    uint16_t group, threshold, flags, pad;
    char prefix[64];
} xt_nflog_info_t;

#define XT_TCPMSS_CLAMP_PMTU 0xffff

typedef struct firc_xt_tcpmss_info {
    uint16_t mss;
} xt_tcpmss_info_t;

_Static_assert(sizeof(xt_ipt_ip_t) == 84, "ipt_ip is 84 bytes");
_Static_assert(offsetof(xt_ipt_ip_t, proto) == 80, "ipt_ip.proto at 80");
_Static_assert(sizeof(xt_ipt_entry_t) == 112, "ipt_entry is 112 bytes");
_Static_assert(offsetof(xt_ipt_entry_t, nfcache) == 84, "ipt_entry.nfcache at 84");
_Static_assert(offsetof(xt_ipt_entry_t, target_offset) == 88, "ipt_entry.target_offset at 88");
_Static_assert(offsetof(xt_ipt_entry_t, next_offset) == 90, "ipt_entry.next_offset at 90");
_Static_assert(offsetof(xt_ipt_entry_t, comefrom) == 92, "ipt_entry.comefrom at 92");
_Static_assert(offsetof(xt_ipt_entry_t, counters) == 96, "ipt_entry.counters at 96");
_Static_assert(sizeof(xt_ip6t_ip6_t) == 136, "ip6t_ip6 is 136 bytes");
_Static_assert(offsetof(xt_ip6t_ip6_t, proto) == 128, "ip6t_ip6.proto at 128");
_Static_assert(offsetof(xt_ip6t_ip6_t, flags) == 131, "ip6t_ip6.flags at 131");
_Static_assert(sizeof(xt_ip6t_entry_t) == 168, "ip6t_entry is 168 bytes");
_Static_assert(offsetof(xt_ip6t_entry_t, target_offset) == 140, "ip6t_entry.target_offset at 140");
_Static_assert(offsetof(xt_ip6t_entry_t, next_offset) == 142, "ip6t_entry.next_offset at 142");
_Static_assert(offsetof(xt_ip6t_entry_t, comefrom) == 144, "ip6t_entry.comefrom at 144");
_Static_assert(offsetof(xt_ip6t_entry_t, counters) == 152, "ip6t_entry.counters at 152");
_Static_assert(sizeof(xt_counters_t) == 16, "xt_counters is 16 bytes");
_Static_assert(sizeof(xt_ext_hdr_t) == 32, "xt_entry_match/target header is 32 bytes");
_Static_assert(XT_ALIGN8(sizeof(xt_standard_target_t)) == XT_STANDARD_TARGET_LEN, "standard target is 40");
_Static_assert(XT_ALIGN8(sizeof(xt_error_target_t)) == XT_ERROR_TARGET_LEN, "error target is 64");
_Static_assert(sizeof(xt_getinfo_t) == XT_GETINFO_LEN, "ipt_getinfo is 84 bytes");
_Static_assert(offsetof(xt_get_entries_hdr_t, size) == 32, "ipt_get_entries.size at 32");
_Static_assert(offsetof(xt_replace_hdr_t, num_counters) == 84, "ipt_replace.num_counters at 84");
_Static_assert(offsetof(xt_replace_hdr_t, counters) == 88, "ipt_replace.counters at 88");
_Static_assert(sizeof(xt_replace_hdr_t) <= XT_REPLACE_HDR_LEN, "ipt_replace header fits in 96");
_Static_assert(offsetof(xt_counters_info_hdr_t, num_counters) == 32, "xt_counters_info.num_counters at 32");
_Static_assert(sizeof(xt_get_revision_t) == 30, "xt_get_revision is 30 bytes");
_Static_assert(sizeof(xt_mark_mtinfo1_t) == 12, "xt_mark_mtinfo1 is 12 bytes");
_Static_assert(sizeof(xt_tcp_t) == 12, "xt_tcp is 12 bytes");
_Static_assert(sizeof(xt_udp_t) == 10, "xt_udp is 10 bytes");
_Static_assert(sizeof(xt_nat_ipv4_compat_t) == 20, "nf_nat_ipv4_multi_range_compat is 20 bytes");
_Static_assert(sizeof(xt_nat_range_t) == 40, "nf_nat_range is 40 bytes");

_Static_assert(offsetof(xt_standard_target_t, verdict) == 32, "standard target verdict at 32");
_Static_assert(offsetof(xt_replace_hdr_t, valid_hooks) == 32, "ipt_replace.valid_hooks at 32");
_Static_assert(offsetof(xt_replace_hdr_t, hook_entry) == 44, "ipt_replace.hook_entry at 44");
_Static_assert(offsetof(xt_replace_hdr_t, underflow) == 64, "ipt_replace.underflow at 64");
_Static_assert(offsetof(xt_getinfo_t, hook_entry) == 36, "ipt_getinfo.hook_entry at 36");
_Static_assert(offsetof(xt_getinfo_t, underflow) == 56, "ipt_getinfo.underflow at 56");
_Static_assert(offsetof(xt_getinfo_t, size) == 80, "ipt_getinfo.size at 80");
_Static_assert(sizeof(xt_reject_info_t) == 4, "ipt_reject_info is 4 bytes");
_Static_assert(sizeof(xt_mark_tginfo2_t) == 8, "xt_mark_tginfo2 is 8 bytes");
_Static_assert(sizeof(xt_connmark_tginfo1_t) == 16, "xt_connmark_tginfo1 is 16 bytes");
_Static_assert(sizeof(xt_connmark_mtinfo1_t) == 12, "xt_connmark_mtinfo1 is 12 bytes");
_Static_assert(offsetof(xt_connmark_mtinfo1_t, invert) == 8, "connmark match invert at 8");
_Static_assert(sizeof(xt_conntrack_mtinfo3_t) == 164, "xt_conntrack_mtinfo3 is 164 bytes");
_Static_assert(offsetof(xt_conntrack_mtinfo3_t, match_flags) == 146, "conntrack match_flags at 146");
_Static_assert(offsetof(xt_conntrack_mtinfo3_t, state_mask) == 150, "conntrack state_mask at 150");
_Static_assert(sizeof(xt_multiport_v1_t) == 48, "xt_multiport_v1 is 48 bytes");
_Static_assert(offsetof(xt_multiport_v1_t, pflags) == 32, "multiport pflags at 32");
_Static_assert(sizeof(xt_mac_info_t) == 12, "xt_mac_info is 12 bytes");
_Static_assert(sizeof(xt_rateinfo_t) == (sizeof(void *) == 8 ? 40u : 28u), "xt_rateinfo is 28 (ILP32) or 40 (LP64) bytes");
_Static_assert(offsetof(xt_rateinfo_t, prev) == XT_LIMIT_USERSPACE, "limit's userspace part is 8 bytes");
_Static_assert(sizeof(xt_length_info_t) == 6, "xt_length_info is 6 bytes");
_Static_assert(sizeof(xt_connbytes_info_t) == 24, "xt_connbytes_info is 24 bytes");
_Static_assert(offsetof(xt_connbytes_info_t, what) == 16, "connbytes what at 16");
_Static_assert(sizeof(xt_string_info_t) == 160, "xt_string_info is 160 bytes on ILP32 and LP64");
_Static_assert(offsetof(xt_string_info_t, patlen) == 148, "string patlen at 148");
_Static_assert(offsetof(xt_string_info_t, config) == XT_STRING_USERSPACE, "string's userspace part is 152 bytes");
_Static_assert(sizeof(xt_nflog_info_t) == 76, "xt_nflog_info is 76 bytes");
_Static_assert(sizeof(xt_addrtype_info_v1_t) == 8, "xt_addrtype_info_v1 is 8 bytes");
_Static_assert(sizeof(xt_set_info_t) == 4, "xt_set_info is 4 bytes");
_Static_assert(sizeof(xt_ip_set_req_version_t) == 8, "ip_set_req_version is 8 bytes");
_Static_assert(sizeof(xt_ip_set_req_get_set_t) == 40, "ip_set_req_get_set is 40 bytes");
_Static_assert(offsetof(xt_ip_set_req_get_set_t, set) == 8, "ip_set_req_get_set.set at 8");

#endif
