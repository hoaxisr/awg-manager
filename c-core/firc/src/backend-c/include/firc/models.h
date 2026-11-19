#ifndef FIRC_MODELS_H
#define FIRC_MODELS_H

#include <stdbool.h>
#include <stdint.h>
#include <string.h>

#include "firc/duration.h"
#include "firc/err.h"
#include "firc/hash.h"
#include "firc/id.h"

#define FIRC_RULE_DOMAIN    "domain"
#define FIRC_RULE_NAMESPACE "namespace"
#define FIRC_RULE_WILDCARD  "wildcard"
#define FIRC_RULE_REGEX     "regex"
#define FIRC_RULE_SUBNET    "subnet"
#define FIRC_RULE_SUBNET6   "subnet6"

typedef struct firc_rule {
    firc_id_t id;
    char *type;
    char *rule;
    bool enable;
    char *proto; /* subnet/subnet6 only; NULL/"" = any protocol */
    char *ports; /* NULL/"" = all ports */
    char *comment;
    char *autolist;
    bool autolist_by_domain;
} firc_rule_t;

/* Which client devices a group applies to; empty = every device. */
typedef struct firc_devsel_spec {
    char **allow;
    size_t n_allow;
    char **deny;
    size_t n_deny;
} firc_devsel_spec_t;

/* forward-declared: full definition is with the rest of a list's machinery below */
typedef struct firc_group_list firc_group_list_t;

/* Where a group's names are resolved; tunnel is true unless the file says false. */
typedef struct firc_group_resolve {
    bool tunnel;
    char *server; /* NULL/"" means the firmware's own resolvers for this interface; else "addr[:port]" */
} firc_group_resolve_t;

#define FIRC_GROUP_IFACES_MAX 4

/* The interface value of a group that routes nothing and answers its names locally. */
#define FIRC_IFACE_BLOCK "block"

typedef enum { FIRC_BLOCK_NULL = 0, FIRC_BLOCK_NXDOMAIN, FIRC_BLOCK_REFUSED } firc_block_answer_t;

typedef struct firc_group {
    firc_id_t id;
    char *name;
    char *iface; /* yaml key: interface; entry 0 of the list */
    char *iface_rest[FIRC_GROUP_IFACES_MAX - 1];
    size_t n_iface_rest;
    bool enable;
    firc_rule_t **rules;
    size_t n_rules;
    firc_devsel_spec_t devices;
    firc_group_resolve_t resolve;
    firc_block_answer_t block;
    firc_group_list_t *list; /* NULL: a manual group */
} firc_group_t;

/* Entries in the group's interface list: 1 + the ones after `iface`. */
static inline size_t firc_group_n_ifaces(const firc_group_t *g) { return 1u + g->n_iface_rest; }
/* Entry i of the list; "" past its end or for an unset first entry. */
static inline const char *firc_group_iface_at(const firc_group_t *g, size_t i)
{
    if (i == 0) { return g->iface != NULL ? g->iface : ""; }
    return i - 1 < g->n_iface_rest ? g->iface_rest[i - 1] : "";
}
/* Whether g's interface is exactly block, alone. */
bool firc_group_is_block(const firc_group_t *g);
/* "null", "nxdomain" or "refused". */
const char *firc_block_answer_name(firc_block_answer_t a);
/* Reads one of those three names into *out; false for anything else. */
bool firc_block_answer_parse(const char *s, firc_block_answer_t *out);
/* Whether name is an entry of g's list. */
bool firc_group_has_iface(const firc_group_t *g, const char *name);
/* NULL when names is a valid list of 1..4; else why, with *bad the offending index. */
const char *firc_group_ifaces_check(const char *const *names, size_t n, size_t *bad);
/* Replaces the list with copies of names (1..4); FIRC_ERR_NOMEM leaves it as it was. */
firc_err_t firc_group_set_ifaces(firc_group_t *g, const char *const *names, size_t n);

void firc_devsel_spec_clear(firc_devsel_spec_t *s);
firc_err_t firc_devsel_spec_copy(firc_devsel_spec_t *dst, const firc_devsel_spec_t *src);

/* Checks and canonicalises every entry of ds in place; FIRC_ERR_INVAL names the bad entry via in_allow, entry, why. */
firc_err_t firc_devsel_spec_check_canon(firc_devsel_spec_t *ds, bool *in_allow, const char **entry,
                                        const char **why);

/* True if now takes a device away from was (an entry joined deny, left allow, or allow went non-empty). */
bool firc_devsel_spec_narrows(const firc_devsel_spec_t *was, const firc_devsel_spec_t *now);

/* One of a list's rules; text lives in the arena below at off. type/list_type index six interned names. */
#define FIRC_SUB_RULE_HAS_SPEC 0x01u
typedef struct firc_sub_rule_ref {
    uint32_t off;
    firc_id_t id;
    uint8_t type;
    bool enable;
    uint8_t flags;
    uint8_t list_type;
} firc_sub_rule_ref_t;
_Static_assert(sizeof(firc_sub_rule_ref_t) == 12, "a list rule's reference is twelve bytes");

/* one text arena plus one ref per rule; the matcher borrows text+off, so the arena never moves once built */
typedef struct firc_sub_rules {
    char *text;
    size_t text_len;
    size_t text_cap;
    firc_sub_rule_ref_t *v;
    size_t n;
    size_t cap;
} firc_sub_rules_t;

/* What changed about a fetched rule; keyed by rule text + list type + proto/ports, never by id. */
typedef struct firc_sub_override {
    char *rule;      /* the key; never NULL, never empty */
    char *list_type; /* the key's type as the list gave it; NULL: any type of this text */
    char *proto;     /* the key's spec; NULL: none */
    char *ports;
    char *type;      /* NULL: the list's own type stands */
    bool enable;
    bool has_enable; /* false: no opinion about enable */
} firc_sub_override_t;

/* Override key: text + list type + proto + ports; "" and NULL compare equal. */
typedef struct firc_sub_rule_key {
    const char *text;
    const char *list_type;
    const char *proto;
    const char *ports;
} firc_sub_rule_key_t;

/* Which half of a job is running, when one is. */
typedef enum firc_sub_stage {
    FIRC_SUB_STAGE_FETCH,
    FIRC_SUB_STAGE_PARSE,
} firc_sub_stage_t;

/* Where a list is between asking for a body and having one; runtime only, never serialized. */
typedef enum firc_sub_sync_state {
    FIRC_SUB_SYNC_IDLE,
    FIRC_SUB_SYNC_QUEUED,
    FIRC_SUB_SYNC_FETCHING,
    FIRC_SUB_SYNC_ERROR,
} firc_sub_sync_state_t;

/* "idle", "queued", "fetching", "error" -- what the API answers with. */
const char *firc_sub_sync_state_name(firc_sub_sync_state_t s);

/* A group's list: what it's fetched from, what was changed, and (runtime only) the sync state. */
typedef struct firc_group_list {
    char *url;
    uint32_t interval;
    uint32_t last_update;
    uint32_t last_check; /* runtime only, never serialized */
    /* SHA-256 of the last accepted body, so an unchanged fetch is not reparsed; runtime only */
    uint8_t body_hash[FIRC_SHA256_DIGEST_LEN];
    bool has_body_hash;
    uint8_t failed_fetches;
    firc_sub_rules_t rules;
    firc_sub_override_t **overrides;
    size_t n_overrides;

    /* loop thread only; sync_seq is the latest enqueued job's seq, any other is stale and dropped */
    firc_sub_sync_state_t sync_state;
    char sync_error[256];
    uint64_t sync_seq;
    struct {
        firc_sub_stage_t stage;
        size_t bytes, total, lines;
        bool applying; /* the loop's own half: refresh, compare, rebuild */
    } sync_progress;
} firc_group_list_t;

firc_group_list_t *firc_group_list_new(void); /* url = "" */
void firc_group_list_free(firc_group_list_t *l);

typedef struct firc_app_config {
    struct {
        bool enabled;
        /* no auth.enabled: the WebUI always requires login; an old file with the key just ignores it */
        struct {
            char *address;
            uint16_t port;
        } host;
    } http_web;
    struct {
        struct {
            char *address;
            uint16_t port;
        } host, upstream;
        bool disable_remap53;
        bool disable_drop_aaaa;
        bool disable_stats;
        uint64_t max_idle_conns;
        uint64_t max_concurrent;
        firc_duration_t timeout;
        /* ceiling on a REAL answer's TTL for an unmatched/not-covered name; 0 = off */
        firc_duration_t unmatched_ttl;
    } dns_proxy;
    struct {
        struct {
            char *chain_prefix;
        } iptables;
        bool disable_ipv4;
        bool disable_ipv6;
        uint32_t start_mark_table_index;
    } netfilter;
    struct {
        struct {
            char *pool; /* CIDR text, e.g. "198.18.0.0/15"; empty v6 means use the generated prefix */
            uint8_t chunk;
        } v4, v6;
        firc_duration_t ttl_clamp;   /* ceiling on the TTL we hand out */
        /* 32-bit: a larger value truncates to 0 on 32-bit targets, meaning "default" not "more" */
        uint32_t max_names;
    } fakeip;

    char **link;
    size_t n_link;
    struct {
        bool check;
    } update;
    struct {
        char *url;
        char *wan_url; /* "" means url */
        firc_duration_t interval;
        firc_duration_t timeout;
    } check;
    struct {
        bool enabled;
        char *group;
        uint32_t fail_threshold;
        firc_duration_t fail_time;
        uint32_t retrans_threshold;
        char **exclude;
        size_t n_exclude;
        bool recheck;
        firc_duration_t recheck_interval;
        char *check_iface;
        char *mode;
    } autolist;
} firc_app_config_t;

/* Whole-application state produced by config load (defaults + overlay). */
typedef struct firc_config {
    firc_app_config_t app;
    firc_group_t **groups;
    size_t n_groups;
    bool groups_present; /* was the `groups` key present in YAML */
} firc_config_t;

firc_rule_t *firc_rule_new(void);
void firc_rule_free(firc_rule_t *r);
/* A copy of every field of r with strings of its own; *out is untouched on failure. */
firc_err_t firc_rule_dup(const firc_rule_t *r, firc_rule_t **out);
#define FIRC_RULE_COMMENT_MAX 256
/* NULL when s (NULL is none) can be a rule's comment once trimmed of spaces; else why not. */
const char *firc_rule_comment_why(const char *s);
/* Sets r's comment from s trimmed of spaces (none when empty); FIRC_ERR_INVAL with *why as firc_rule_comment_why. */
firc_err_t firc_rule_set_comment(firc_rule_t *r, const char *s, const char **why);

firc_group_t *firc_group_new(void);
void firc_group_free(firc_group_t *g);
firc_err_t firc_group_add_rule(firc_group_t *g, firc_rule_t *r); /* takes ownership */
/* Fills the zeroed out with g's rule pointers in order, the one at `at` replaced by put (dropped when put is
 * NULL; at SIZE_MAX touches none), then append when not NULL. Shares the pointers; FIRC_ERR_NOMEM frees out's array only. */
firc_err_t firc_group_rules_with(const firc_group_t *g, size_t at, firc_rule_t *put, firc_rule_t *append,
                                 firc_group_t *out);
/* FIRC_ERR_EXIST when two of g's rules share an id (the zero id included); FIRC_ERR_NOMEM. */
firc_err_t firc_group_check_rule_ids(const firc_group_t *g);

/* The one interned copy of a rule type's name; pointers compare equal with ==. NULL folds to "". */
const char *firc_rule_type_intern(const char *type);
/* The same names by index: 0 is "", 1..6 the six types. */
uint8_t firc_rule_type_index(const char *type);
const char *firc_rule_type_name(uint8_t index);

void firc_sub_rules_init(firc_sub_rules_t *rs);
void firc_sub_rules_free(firc_sub_rules_t *rs); /* the members; `rs` itself is the caller's */
/* Appends one rule; `text` is copied into the arena. */
firc_err_t firc_sub_rules_push(firc_sub_rules_t *rs, const char *text, const char *type, bool enable,
                               firc_id_t id);
/* Like firc_sub_rules_push, but proto/ports (NULL/"" both mean none) are stored after the text's NUL. */
firc_err_t firc_sub_rules_push_spec(firc_sub_rules_t *rs, const char *text, const char *type, bool enable,
                                    firc_id_t id, const char *proto, const char *ports);
/* Gives back the growth slack; called once the set is complete. */
void firc_sub_rules_shrink(firc_sub_rules_t *rs);
/* `dst` takes everything `src` had; `src` is left empty. */
void firc_sub_rules_move(firc_sub_rules_t *dst, firc_sub_rules_t *src);
firc_err_t firc_sub_rules_copy(firc_sub_rules_t *dst, const firc_sub_rules_t *src);
static inline const char *firc_sub_rules_text(const firc_sub_rules_t *rs, size_t i) {
    return rs->text + rs->v[i].off;
}
static inline const char *firc_sub_rules_type(const firc_sub_rules_t *rs, size_t i) {
    return firc_rule_type_name(rs->v[i].type);
}
static inline bool firc_sub_rules_enable(const firc_sub_rules_t *rs, size_t i) { return rs->v[i].enable; }
static inline firc_id_t firc_sub_rules_id(const firc_sub_rules_t *rs, size_t i) { return rs->v[i].id; }
static inline void firc_sub_rules_set_type(firc_sub_rules_t *rs, size_t i, const char *type) {
    rs->v[i].type = firc_rule_type_index(type);
}
/* The rule's proto, or NULL when it carries no spec. */
static inline const char *firc_sub_rules_proto(const firc_sub_rules_t *rs, size_t i) {
    if (!(rs->v[i].flags & FIRC_SUB_RULE_HAS_SPEC)) { return NULL; }
    const char *t = rs->text + rs->v[i].off;
    t += strlen(t) + 1;
    return t[0] != '\0' ? t : NULL;
}
/* The rule's ports, or NULL when it carries no spec. */
static inline const char *firc_sub_rules_ports(const firc_sub_rules_t *rs, size_t i) {
    if (!(rs->v[i].flags & FIRC_SUB_RULE_HAS_SPEC)) { return NULL; }
    const char *t = rs->text + rs->v[i].off;
    t += strlen(t) + 1;
    t += strlen(t) + 1;
    return t[0] != '\0' ? t : NULL;
}
/* The type the parser stored; never moved by firc_sub_rules_set_type. */
static inline const char *firc_sub_rules_list_type(const firc_sub_rules_t *rs, size_t i) {
    return firc_rule_type_name(rs->v[i].list_type);
}
/* The index of the rule with this id, or false. */
bool firc_sub_rules_find_id(const firc_sub_rules_t *rs, firc_id_t id, size_t *out_idx);

firc_sub_override_t *firc_sub_override_new(void);
void firc_sub_override_free(firc_sub_override_t *o);

/* Upserts the override for key; type NULL keeps, "" clears it; FIRC_ERR_INVAL if key->text is empty. */
firc_err_t firc_group_list_set_override(firc_group_list_t *l, const firc_sub_rule_key_t *key,
                                        const char *type, const bool *enable);

/* Finds the override reaching key: exact list_type match, or the text-only entry when stored list_type is NULL. */
const firc_sub_override_t *firc_group_list_find_override(const firc_group_list_t *l,
                                                         const firc_sub_rule_key_t *key);

/* Stamps l's overrides onto a freshly derived rule array, by text. */
void firc_sub_apply_overrides(const firc_group_list_t *l, firc_sub_rules_t *rules);

/* Initialize with DefaultAppConfig values (allocates strings). */
firc_err_t firc_app_config_init_defaults(firc_app_config_t *c);
void firc_app_config_clear(firc_app_config_t *c);

firc_err_t firc_config_init_defaults(firc_config_t *c);
void firc_config_clear(firc_config_t *c);
firc_err_t firc_config_add_group(firc_config_t *c, firc_group_t *g);
/* Frees c->groups[idx] and shifts the remaining pointers down (idx must be < c->n_groups). */
void firc_config_remove_group_by_index(firc_config_t *c, size_t idx);
/* Frees every group and empties c->groups (does not touch the app config). */
void firc_config_clear_groups(firc_config_t *c);

/* True when an enabled group names devices in its selector: only then is the router's device map worth waiting for. */
bool firc_config_selects_devices(const firc_config_t *c);

/* strdup that maps NULL input to NULL output and reports OOM. */
firc_err_t firc_strset(char **dst, const char *src);

#endif /* FIRC_MODELS_H */
